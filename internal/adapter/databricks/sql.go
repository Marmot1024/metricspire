// Package databricks compiles physical plans to Databricks SQL and executes
// them through the Statement Execution API.
package databricks

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/planner"
)

const EngineName = "databricks_sql"

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)

type Parameter struct {
	Name  string  `json:"name"`
	Value *string `json:"value"`
	Type  string  `json:"type"`
}

type Statement struct {
	SQL                 string      `json:"statement"`
	Parameters          []Parameter `json:"parameters,omitempty"`
	PhysicalFingerprint string      `json:"physical_fingerprint"`
	RowLimit            int64       `json:"row_limit"`
}

func Capabilities() model.EngineCapabilities {
	return model.EngineCapabilities{
		Engine: EngineName,
		ExpressionOps: []model.ExpressionOp{
			model.OpAdd, model.OpAverage, model.OpCount, model.OpCountDistinct,
			model.OpDivide, model.OpLiteral, model.OpMaximum, model.OpMetric,
			model.OpMinimum, model.OpMultiply, model.OpSubtract, model.OpSum,
		},
		MetricFilterOperators: []model.MetricFilterOperator{
			model.MetricFilterEqual, model.MetricFilterIn, model.MetricFilterIsNotNull,
			model.MetricFilterIsNull, model.MetricFilterNotEqual, model.MetricFilterNotIn,
		},
		JoinCardinalities: []model.Cardinality{model.CardinalityManyToOne},
		TimeGranularities: []model.TimeGranularity{model.GrainDay, model.GrainMonth, model.GrainWeek},
		MaxJoins:          8,
	}
}

func Compile(plan model.PhysicalPlan) (Statement, error) {
	if err := planner.VerifyPhysical(plan); err != nil {
		return Statement{}, err
	}
	if plan.Engine != EngineName {
		return Statement{}, problem("engine_mismatch", "physical_plan.engine", "expected %q", EngineName)
	}
	if plan.Root.Entity == "" {
		return Statement{}, problem("invalid_physical_plan", "physical_plan.root.entity", "root entity is required")
	}
	root, err := renderTable(plan.Root.Resource)
	if err != nil {
		return Statement{}, err
	}
	aliases := map[string]string{plan.Root.Entity: "t0"}
	joins := make([]string, 0, len(plan.Joins))
	for i, join := range plan.Joins {
		fromAlias, exists := aliases[join.FromEntity]
		if !exists {
			return Statement{}, problem("invalid_physical_plan", "physical_plan.joins", "join %q starts from an unavailable entity", join.Name)
		}
		if _, exists := aliases[join.ToEntity]; exists {
			return Statement{}, problem("invalid_physical_plan", "physical_plan.joins", "entity %q is joined more than once", join.ToEntity)
		}
		toTable, err := renderTable(join.ToResource)
		if err != nil {
			return Statement{}, err
		}
		toAlias := fmt.Sprintf("t%d", i+1)
		aliases[join.ToEntity] = toAlias
		joins = append(joins, fmt.Sprintf(
			"LEFT JOIN %s AS %s ON %s.%s = %s.%s",
			toTable, toAlias, fromAlias, quote(join.FromColumn), toAlias, quote(join.ToColumn),
		))
	}

	compiler := newExpressionCompiler(plan, aliases)
	selects := make([]string, 0)
	groups := make([]string, 0)
	for _, dimension := range plan.Dimensions {
		if !dimension.Output {
			continue
		}
		expression, err := compiler.dimensionExpression(dimension, plan.TimeGrouping)
		if err != nil {
			return Statement{}, err
		}
		selects = append(selects, fmt.Sprintf("%s AS %s", expression, quote(dimension.Name)))
		groups = append(groups, expression)
	}
	for _, metric := range plan.Metrics {
		if !metric.Output {
			continue
		}
		expression, err := compiler.metric(metric.Name, nil)
		if err != nil {
			return Statement{}, err
		}
		selects = append(selects, fmt.Sprintf("%s AS %s", expression, quote(metric.Name)))
	}
	if len(selects) == 0 {
		return Statement{}, problem("invalid_physical_plan", "physical_plan", "plan has no output columns")
	}

	predicates := make([]string, 0, len(plan.Filters)+2)
	if plan.TimeRange != nil {
		dimension, ok := findDimension(plan.Dimensions, plan.TimeRange.Dimension)
		if !ok {
			return Statement{}, problem("invalid_physical_plan", "physical_plan.time_range", "time dimension is missing")
		}
		column, err := compiler.dimensionColumn(dimension)
		if err != nil {
			return Statement{}, err
		}
		start := compiler.parameter("time_start", plan.TimeRange.Start, model.DataTypeTimestamp)
		end := compiler.parameter("time_end", plan.TimeRange.End, model.DataTypeTimestamp)
		predicates = append(predicates, fmt.Sprintf("%s >= :%s AND %s < :%s", column, start, column, end))
	}
	for _, filter := range plan.Filters {
		dimension, ok := findDimension(plan.Dimensions, filter.Dimension)
		if !ok {
			return Statement{}, problem("invalid_physical_plan", "physical_plan.filters", "dimension %q is missing", filter.Dimension)
		}
		column, err := compiler.dimensionColumn(dimension)
		if err != nil {
			return Statement{}, err
		}
		predicate, err := compiler.queryFilter(column, dimension.DataType, filter)
		if err != nil {
			return Statement{}, err
		}
		predicates = append(predicates, predicate)
	}

	var sql strings.Builder
	sql.WriteString("SELECT\n  ")
	sql.WriteString(strings.Join(selects, ",\n  "))
	sql.WriteString("\nFROM ")
	sql.WriteString(root)
	sql.WriteString(" AS t0")
	for _, join := range joins {
		sql.WriteString("\n")
		sql.WriteString(join)
	}
	if len(predicates) != 0 {
		sql.WriteString("\nWHERE ")
		sql.WriteString(strings.Join(predicates, "\n  AND "))
	}
	if len(groups) != 0 {
		sql.WriteString("\nGROUP BY ")
		sql.WriteString(strings.Join(groups, ", "))
	}
	if len(plan.OrderBy) != 0 {
		orders := make([]string, 0, len(plan.OrderBy))
		for _, order := range plan.OrderBy {
			orders = append(orders, fmt.Sprintf("%s %s", quote(order.Field), strings.ToUpper(string(order.Direction))))
		}
		sql.WriteString("\nORDER BY ")
		sql.WriteString(strings.Join(orders, ", "))
	}
	sql.WriteString(fmt.Sprintf("\nLIMIT %d", plan.Limit))
	statement := Statement{
		SQL: sql.String(), Parameters: compiler.parameters,
		PhysicalFingerprint: plan.Fingerprint, RowLimit: int64(plan.Limit),
	}
	if err := validateStatement(statement); err != nil {
		return Statement{}, err
	}
	return statement, nil
}

type expressionCompiler struct {
	aliases    map[string]string
	fields     map[string]model.PhysicalField
	dimensions map[string]model.PhysicalDimension
	metrics    map[string]model.PlannedMetric
	parameters []Parameter
	next       int
}

func newExpressionCompiler(plan model.PhysicalPlan, aliases map[string]string) *expressionCompiler {
	result := &expressionCompiler{
		aliases: aliases, fields: make(map[string]model.PhysicalField),
		dimensions: make(map[string]model.PhysicalDimension), metrics: make(map[string]model.PlannedMetric),
	}
	for _, field := range plan.MetricFields {
		result.fields[field.Entity+"."+field.Field] = field
	}
	for _, dimension := range plan.Dimensions {
		result.dimensions[dimension.Name] = dimension
	}
	for _, metric := range plan.Metrics {
		result.metrics[metric.Name] = metric
	}
	return result
}

func (c *expressionCompiler) metric(name string, stack []string) (string, error) {
	for _, current := range stack {
		if current == name {
			return "", problem("metric_cycle", "physical_plan.metrics", "cycle reached metric %q", name)
		}
	}
	metric, exists := c.metrics[name]
	if !exists {
		return "", problem("invalid_physical_plan", "physical_plan.metrics", "metric %q is missing", name)
	}
	return c.expression(metric.Expression, append(stack, name))
}

func (c *expressionCompiler) expression(expression model.Expression, stack []string) (string, error) {
	switch expression.Op {
	case model.OpMetric:
		return c.metric(expression.Metric, stack)
	case model.OpLiteral:
		name := c.parameter("literal", expression.Value, model.DataTypeDecimal)
		return ":" + name, nil
	case model.OpAdd, model.OpSubtract, model.OpMultiply, model.OpDivide:
		if len(expression.Args) != 2 {
			return "", problem("invalid_physical_plan", "physical_plan.metrics", "%s requires two arguments", expression.Op)
		}
		left, err := c.expression(expression.Args[0], stack)
		if err != nil {
			return "", err
		}
		right, err := c.expression(expression.Args[1], stack)
		if err != nil {
			return "", err
		}
		operator := map[model.ExpressionOp]string{
			model.OpAdd: "+", model.OpSubtract: "-", model.OpMultiply: "*", model.OpDivide: "/",
		}[expression.Op]
		if expression.Op == model.OpDivide {
			right = "NULLIF(" + right + ", 0)"
		}
		return "(" + left + " " + operator + " " + right + ")", nil
	case model.OpSum, model.OpCount, model.OpCountDistinct, model.OpAverage, model.OpMinimum, model.OpMaximum:
		return c.aggregate(expression)
	default:
		return "", problem("invalid_physical_plan", "physical_plan.metrics", "unsupported expression operation %q", expression.Op)
	}
}

func (c *expressionCompiler) aggregate(expression model.Expression) (string, error) {
	field, exists := c.fields[expression.Field]
	if !exists {
		return "", problem("invalid_physical_plan", "physical_plan.metric_fields", "field %q is missing", expression.Field)
	}
	column, err := c.fieldColumn(field)
	if err != nil {
		return "", err
	}
	value := column
	if len(expression.Filters) != 0 {
		predicates := make([]string, 0, len(expression.Filters))
		for _, filter := range expression.Filters {
			filterField, exists := c.fields[filter.Field]
			if !exists {
				return "", problem("invalid_physical_plan", "physical_plan.metric_fields", "filter field %q is missing", filter.Field)
			}
			filterColumn, err := c.fieldColumn(filterField)
			if err != nil {
				return "", err
			}
			predicate, err := c.metricFilter(filterColumn, filterField.DataType, filter)
			if err != nil {
				return "", err
			}
			predicates = append(predicates, predicate)
		}
		value = fmt.Sprintf("CASE WHEN %s THEN %s ELSE NULL END", strings.Join(predicates, " AND "), column)
	}
	switch expression.Op {
	case model.OpSum:
		return "SUM(" + value + ")", nil
	case model.OpCount:
		return "COUNT(" + value + ")", nil
	case model.OpCountDistinct:
		return "COUNT(DISTINCT " + value + ")", nil
	case model.OpAverage:
		return "AVG(" + value + ")", nil
	case model.OpMinimum:
		return "MIN(" + value + ")", nil
	case model.OpMaximum:
		return "MAX(" + value + ")", nil
	default:
		return "", problem("invalid_physical_plan", "physical_plan.metrics", "unsupported aggregate %q", expression.Op)
	}
}

func (c *expressionCompiler) queryFilter(column string, dataType model.DataType, filter model.Filter) (string, error) {
	values := make([]string, 0, len(filter.Values))
	for _, value := range filter.Values {
		values = append(values, ":"+c.parameter("filter", value, dataType))
	}
	switch filter.Operator {
	case model.FilterEqual:
		if len(values) != 1 {
			return "", problem("invalid_physical_plan", "physical_plan.filters", "eq requires one value")
		}
		return column + " = " + values[0], nil
	case model.FilterIn:
		if len(values) == 0 {
			return "", problem("invalid_physical_plan", "physical_plan.filters", "in requires values")
		}
		return column + " IN (" + strings.Join(values, ", ") + ")", nil
	default:
		return "", problem("invalid_physical_plan", "physical_plan.filters", "unsupported operator %q", filter.Operator)
	}
}

func (c *expressionCompiler) metricFilter(column string, dataType model.DataType, filter model.MetricFilter) (string, error) {
	values := make([]string, 0, len(filter.Values))
	for _, value := range filter.Values {
		values = append(values, ":"+c.parameter("metric_filter", value, dataType))
	}
	switch filter.Operator {
	case model.MetricFilterEqual:
		if len(values) != 1 {
			return "", problem("invalid_physical_plan", "physical_plan.metric_filters", "eq requires one value")
		}
		return column + " = " + values[0], nil
	case model.MetricFilterNotEqual:
		if len(values) != 1 {
			return "", problem("invalid_physical_plan", "physical_plan.metric_filters", "neq requires one value")
		}
		return column + " <> " + values[0], nil
	case model.MetricFilterIn:
		if len(values) == 0 {
			return "", problem("invalid_physical_plan", "physical_plan.metric_filters", "in requires values")
		}
		return column + " IN (" + strings.Join(values, ", ") + ")", nil
	case model.MetricFilterNotIn:
		if len(values) == 0 {
			return "", problem("invalid_physical_plan", "physical_plan.metric_filters", "not_in requires values")
		}
		return column + " NOT IN (" + strings.Join(values, ", ") + ")", nil
	case model.MetricFilterIsNull:
		return column + " IS NULL", nil
	case model.MetricFilterIsNotNull:
		return column + " IS NOT NULL", nil
	default:
		return "", problem("invalid_physical_plan", "physical_plan.metric_filters", "unsupported operator %q", filter.Operator)
	}
}

func (c *expressionCompiler) dimensionExpression(dimension model.PhysicalDimension, grouping *model.TimeGrouping) (string, error) {
	column, err := c.dimensionColumn(dimension)
	if err != nil {
		return "", err
	}
	if dimension.Type != model.DimensionTime || grouping == nil || grouping.Dimension != dimension.Name {
		return column, nil
	}
	local := column
	if dimension.DataType == model.DataTypeTimestamp {
		timezone := c.parameter("timezone", grouping.Timezone, model.DataTypeString)
		local = fmt.Sprintf("from_utc_timestamp(%s, :%s)", column, timezone)
	}
	grain := strings.ToUpper(string(grouping.Granularity))
	if grouping.Granularity == model.GrainWeek && grouping.WeekStart == model.WeekStartSunday {
		return fmt.Sprintf("date_trunc('WEEK', %s + INTERVAL 1 DAY) - INTERVAL 1 DAY", local), nil
	}
	return fmt.Sprintf("date_trunc('%s', %s)", grain, local), nil
}

func (c *expressionCompiler) dimensionColumn(dimension model.PhysicalDimension) (string, error) {
	alias, exists := c.aliases[dimension.Entity]
	if !exists {
		return "", problem("invalid_physical_plan", "physical_plan.dimensions", "entity %q has no SQL alias", dimension.Entity)
	}
	return alias + "." + quote(dimension.Column), nil
}

func (c *expressionCompiler) fieldColumn(field model.PhysicalField) (string, error) {
	alias, exists := c.aliases[field.Entity]
	if !exists {
		return "", problem("invalid_physical_plan", "physical_plan.metric_fields", "entity %q has no SQL alias", field.Entity)
	}
	return alias + "." + quote(field.Column), nil
}

func (c *expressionCompiler) parameter(prefix, value string, dataType model.DataType) string {
	c.next++
	name := fmt.Sprintf("%s_%d", prefix, c.next)
	copy := value
	c.parameters = append(c.parameters, Parameter{Name: name, Value: &copy, Type: parameterType(dataType)})
	return name
}

func parameterType(dataType model.DataType) string {
	switch dataType {
	case model.DataTypeInteger:
		return "BIGINT"
	case model.DataTypeDecimal:
		return "DECIMAL(38,18)"
	case model.DataTypeBoolean:
		return "BOOLEAN"
	case model.DataTypeDate:
		return "DATE"
	case model.DataTypeTimestamp:
		return "TIMESTAMP"
	default:
		return "STRING"
	}
}

func findDimension(dimensions []model.PhysicalDimension, name string) (model.PhysicalDimension, bool) {
	for _, dimension := range dimensions {
		if dimension.Name == name {
			return dimension, true
		}
	}
	return model.PhysicalDimension{}, false
}

func renderTable(resource model.ResourceRef) (string, error) {
	if resource.Kind != model.ResourceTable || resource.Table == "" || resource.URI != "" {
		return "", problem("unsupported_resource", "physical_plan.resource", "Databricks SQL v0.1 requires a table resource")
	}
	parts := make([]string, 0, 3)
	for _, value := range []string{resource.Catalog, resource.Schema, resource.Table} {
		if value == "" {
			continue
		}
		if !identifierPattern.MatchString(value) {
			return "", problem("invalid_identifier", "physical_plan.resource", "%q is not a safe identifier", value)
		}
		parts = append(parts, quote(value))
	}
	return strings.Join(parts, "."), nil
}

func quote(value string) string { return "`" + value + "`" }

func problem(code, path, format string, arguments ...any) error {
	return &model.Problem{Code: code, Path: path, Message: fmt.Sprintf(format, arguments...)}
}
