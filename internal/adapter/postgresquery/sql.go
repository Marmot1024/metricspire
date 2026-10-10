// Package postgresquery reads reviewed, tenant-scoped aggregate snapshots.
// It is deliberately not a general PostgreSQL analytical adapter.
package postgresquery

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/planner"
)

const EngineName = "postgres_online"

var identifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
var finiteDecimal = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

func Capabilities() model.EngineCapabilities {
	return model.EngineCapabilities{Engine: EngineName, ExpressionOps: []model.ExpressionOp{model.OpSum, model.OpMetric, model.OpLiteral, model.OpAdd, model.OpSubtract, model.OpMultiply, model.OpDivide}, TimeGranularities: []model.TimeGranularity{model.GrainDay}}
}

type Statement struct {
	SQL     string
	Args    []any
	Columns []model.ResultColumn
	Types   []model.DataType
}

type sqlCompiler struct {
	plan    model.PhysicalPlan
	args    []any
	metrics map[string]model.PlannedMetric
	fields  map[string]model.PhysicalField
}

func compile(plan model.PhysicalPlan, tenant string) (Statement, error) {
	if err := planner.VerifyPhysical(plan); err != nil {
		return Statement{}, err
	}
	if plan.Engine != EngineName || len(plan.Joins) != 0 || plan.TimeRange == nil || plan.Limit < 1 || plan.Limit > 1000 {
		return Statement{}, reject("unsupported_online_query", "a bounded, single-table online plan is required")
	}
	r := plan.Root.Resource
	if r.Kind != model.ResourceTable || r.Catalog != "" || r.URI != "" || !identifier.MatchString(r.Schema) || !identifier.MatchString(r.Table) {
		return Statement{}, reject("unsupported_online_query", "an explicitly schema-qualified PostgreSQL table is required")
	}
	c := sqlCompiler{plan: plan, metrics: map[string]model.PlannedMetric{}, fields: map[string]model.PhysicalField{}}
	for _, m := range plan.Metrics {
		c.metrics[m.Name] = m
	}
	for _, f := range plan.MetricFields {
		if f.Entity != plan.Root.Entity || f.Resource != r || !identifier.MatchString(f.Column) {
			return Statement{}, reject("unsupported_online_query", "metric fields must belong to the reviewed root table")
		}
		c.fields[f.Entity+"."+f.Field] = f
	}
	selects := []string{}
	groups := []string{}
	columns := []model.ResultColumn{}
	types := []model.DataType{}
	for _, d := range plan.Dimensions {
		if d.Entity != plan.Root.Entity || d.Resource != r || !identifier.MatchString(d.Column) || !identifier.MatchString(d.Name) {
			return Statement{}, reject("unsupported_online_query", "dimensions must belong to the reviewed root table")
		}
		if d.Type == model.DimensionTime && d.DataType != model.DataTypeDate {
			return Statement{}, reject("unsupported_online_query", "the first online slice supports calendar DATE dimensions only")
		}
		if d.Output {
			col := "t0." + quote(d.Column)
			selects = append(selects, col+"::text AS "+quote(d.Name))
			groups = append(groups, col)
			columns = append(columns, resultColumn(d.Name, d.DataType))
			types = append(types, d.DataType)
		}
	}
	if plan.TimeGrouping != nil && plan.TimeGrouping.Granularity != model.GrainDay {
		return Statement{}, reject("unsupported_online_query", "only daily grouping is supported")
	}
	for _, m := range plan.Metrics {
		if !m.Output {
			continue
		}
		expr, err := c.metric(m.Name, map[string]bool{})
		if err != nil {
			return Statement{}, err
		}
		if m.ValueType != model.DataTypeInteger && m.ValueType != model.DataTypeDecimal {
			return Statement{}, reject("unsupported_online_query", "online metrics must be numeric")
		}
		cast := "numeric"
		if m.ValueType == model.DataTypeInteger {
			cast = "bigint"
		}
		selects = append(selects, "("+expr+")::"+cast+"::text AS "+quote(m.Name))
		columns = append(columns, resultColumn(m.Name, m.ValueType))
		types = append(types, m.ValueType)
	}
	if len(selects) == 0 {
		return Statement{}, reject("unsupported_online_query", "no result columns")
	}
	// Metadata is aggregated with the results in the same PostgreSQL snapshot.
	// The writer must atomically publish a complete batch, never incremental rows.
	selects = append(selects, `count(*)::text`, `count(t0._metricspire_batch_id)::text`, `count(t0._metricspire_data_as_of)::text`, `count(t0._metricspire_data_contract)::text`, `min(t0._metricspire_batch_id)::text`, `max(t0._metricspire_batch_id)::text`, `min(t0._metricspire_data_as_of)::text`, `max(t0._metricspire_data_as_of)::text`, `min(t0._metricspire_data_contract)::text`, `max(t0._metricspire_data_contract)::text`)
	d, ok := findDimension(plan, plan.TimeRange.Dimension)
	if !ok || d.DataType != model.DataTypeDate {
		return Statement{}, reject("unsupported_online_query", "a calendar DATE time range is required")
	}
	loc, err := time.LoadLocation(d.CalendarTimezone)
	if err != nil {
		return Statement{}, reject("unsupported_online_query", "invalid calendar timezone")
	}
	start, err := time.Parse(time.RFC3339Nano, plan.TimeRange.Start)
	if err != nil {
		return Statement{}, err
	}
	end, err := time.Parse(time.RFC3339Nano, plan.TimeRange.End)
	if err != nil {
		return Statement{}, err
	}
	if !end.After(start) || end.Sub(start) > 31*24*time.Hour {
		return Statement{}, reject("budget_exceeded", "online time range must be positive and at most 31 days")
	}
	for _, boundary := range []time.Time{start.In(loc), end.In(loc)} {
		if boundary.Hour() != 0 || boundary.Minute() != 0 || boundary.Second() != 0 || boundary.Nanosecond() != 0 {
			return Statement{}, reject("unsupported_online_query", "daily aggregates require whole calendar-day boundaries")
		}
	}
	predicates := []string{"t0._metricspire_tenant = " + c.parameter(tenant, model.DataTypeString), "t0." + quote(d.Column) + " >= " + c.parameter(start.In(loc).Format(time.DateOnly), model.DataTypeDate), "t0." + quote(d.Column) + " < " + c.parameter(end.In(loc).Format(time.DateOnly), model.DataTypeDate)}
	for _, filter := range plan.Filters {
		d, ok := findDimension(plan, filter.Dimension)
		if !ok {
			return Statement{}, reject("unsupported_online_query", "filter dimension is missing")
		}
		if len(filter.Values) == 0 || (filter.Operator == model.FilterEqual && len(filter.Values) != 1) || (filter.Operator != model.FilterEqual && filter.Operator != model.FilterIn) {
			return Statement{}, reject("unsupported_online_query", "unsupported filter")
		}
		values := []string{}
		for _, v := range filter.Values {
			values = append(values, c.parameter(v, d.DataType))
		}
		predicates = append(predicates, "t0."+quote(d.Column)+" IN ("+strings.Join(values, ",")+")")
	}
	sql := "SELECT " + strings.Join(selects, ", ") + " FROM " + quote(r.Schema) + "." + quote(r.Table) + " AS t0 WHERE " + strings.Join(predicates, " AND ")
	if len(groups) > 0 {
		sql += " GROUP BY " + strings.Join(groups, ",")
	}
	if len(plan.OrderBy) > 0 {
		orders := []string{}
		for _, order := range plan.OrderBy {
			found := false
			for i, col := range columns {
				if col.Name == order.Field {
					found = true
					cast := "text"
					switch types[i] {
					case model.DataTypeInteger, model.DataTypeDecimal:
						cast = "numeric"
					case model.DataTypeDate:
						cast = "date"
					}
					direction := strings.ToUpper(string(order.Direction))
					if direction != "ASC" && direction != "DESC" {
						return Statement{}, reject("unsupported_online_query", "unsupported sort direction")
					}
					orders = append(orders, "("+quote(col.Name)+")::"+cast+" "+direction)
				}
			}
			if !found {
				return Statement{}, reject("unsupported_online_query", "sort must refer to an output column")
			}
		}
		// Cast aliases in an outer query; ORDER BY expressions cannot resolve select aliases.
		sql = "SELECT * FROM (" + sql + ") AS online_result ORDER BY " + strings.Join(orders, ",")
	}
	// Fetch one extra row so incomplete results cannot be presented as complete.
	sql += fmt.Sprintf(" LIMIT %d", plan.Limit+1)
	return Statement{SQL: sql, Args: c.args, Columns: columns, Types: types}, nil
}

func (c *sqlCompiler) metric(name string, stack map[string]bool) (string, error) {
	if stack[name] {
		return "", reject("unsupported_online_query", "cyclic metric dependency")
	}
	m, ok := c.metrics[name]
	if !ok {
		return "", reject("unsupported_online_query", "metric dependency is missing")
	}
	stack[name] = true
	defer delete(stack, name)
	return c.expression(m.Expression, stack)
}

func (c *sqlCompiler) expression(e model.Expression, stack map[string]bool) (string, error) {
	if len(e.Filters) > 0 {
		return "", reject("unsupported_online_query", "filtered aggregates are outside the first online slice")
	}
	switch e.Op {
	case model.OpMetric:
		return c.metric(e.Metric, stack)
	case model.OpLiteral:
		return c.parameter(e.Value, model.DataTypeDecimal), nil
	case model.OpSum:
		f, ok := c.fields[e.Field]
		if !ok || (f.DataType != model.DataTypeInteger && f.DataType != model.DataTypeDecimal) {
			return "", reject("unsupported_online_query", "SUM requires a bound numeric field")
		}
		return "SUM(t0." + quote(f.Column) + "::numeric)", nil
	case model.OpAdd, model.OpSubtract, model.OpMultiply, model.OpDivide:
		if len(e.Args) != 2 {
			return "", reject("unsupported_online_query", "binary expression requires two arguments")
		}
		left, err := c.expression(e.Args[0], stack)
		if err != nil {
			return "", err
		}
		right, err := c.expression(e.Args[1], stack)
		if err != nil {
			return "", err
		}
		op := map[model.ExpressionOp]string{model.OpAdd: "+", model.OpSubtract: "-", model.OpMultiply: "*", model.OpDivide: "/"}[e.Op]
		if e.Op == model.OpDivide {
			right = "NULLIF(" + right + ",0)"
		}
		return "(" + left + op + right + ")", nil
	default:
		return "", reject("unsupported_online_query", "only additive aggregates and simple arithmetic are supported")
	}
}

func (c *sqlCompiler) parameter(value string, t model.DataType) string {
	c.args = append(c.args, value)
	cast := "text"
	switch t {
	case model.DataTypeInteger:
		cast = "bigint"
	case model.DataTypeDecimal:
		cast = "numeric"
	case model.DataTypeDate:
		cast = "date"
	case model.DataTypeBoolean:
		cast = "boolean"
	}
	return fmt.Sprintf("$%d::text::%s", len(c.args), cast)
}
func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func findDimension(p model.PhysicalPlan, name string) (model.PhysicalDimension, bool) {
	for _, d := range p.Dimensions {
		if d.Name == name {
			return d, true
		}
	}
	return model.PhysicalDimension{}, false
}
func resultColumn(name string, t model.DataType) model.ResultColumn {
	kind := map[model.DataType]string{model.DataTypeInteger: "LONG", model.DataTypeDecimal: "DECIMAL", model.DataTypeDate: "DATE", model.DataTypeBoolean: "BOOLEAN", model.DataTypeString: "STRING"}[t]
	return model.ResultColumn{Name: name, TypeName: kind, TypeText: kind}
}
func reject(code, message string) *model.Problem {
	return &model.Problem{Code: code, Message: message, Path: "online_query"}
}
