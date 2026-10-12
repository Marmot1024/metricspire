package databricks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/exploration"
	"github.com/marmot1024/metricspire/internal/model"
)

// AnalysisEngine uses the existing client and its credential/cancellation
// mechanisms. Constructing this engine never logs in or changes credentials.
type AnalysisEngine struct{ client *Client }

func NewAnalysisEngine(client *Client) (*AnalysisEngine, error) {
	if client == nil {
		return nil, errors.New("Databricks client is required")
	}
	return &AnalysisEngine{client: client}, nil
}

func (e *AnalysisEngine) Execute(ctx context.Context, p exploration.Plan) (model.ExecutionSnapshot, error) {
	statement, err := CompileAnalysis(p)
	if err != nil {
		return model.ExecutionSnapshot{}, err
	}
	snapshot, err := e.client.Execute(ctx, statement)
	// Only known server-generated guard markers get a domain error. Raw engine
	// messages may contain SQL, bindings or credentials and are never exposed.
	if err != nil {
		var engine *model.Problem
		if errors.As(err, &engine) && strings.Contains(engine.Message, "[USER_RAISED_EXCEPTION]") {
			for _, code := range []string{"analysis_duplicate_match", "analysis_missing_match"} {
				if strings.Contains(engine.Message, code) {
					return model.ExecutionSnapshot{}, problem(code, "relationships", "approved relationship failed its match contract")
				}
			}
		}
	}
	return snapshot, err
}

func CompileAnalysis(p exploration.Plan) (Statement, error) {
	if err := exploration.Verify(p); err != nil {
		return Statement{}, err
	}
	if p.Base.Engine != EngineName {
		return Statement{}, problem("engine_mismatch", "analysis.base.engine", "expected %q", EngineName)
	}
	root, err := renderTable(p.Base.Root.Resource)
	if err != nil {
		return Statement{}, err
	}
	aliases := map[string]string{p.Base.Root.Entity: "t0"}
	for i, r := range p.Relations {
		aliases[r.To] = fmt.Sprintf("t%d", i+1)
	}
	ec := newExpressionCompiler(p.Base, aliases)
	dimensions := map[string]model.PhysicalDimension{}
	for _, d := range p.Base.Dimensions {
		dimensions[d.Name] = d
	}
	selects := []string{}
	for _, dimension := range p.Base.Dimensions {
		col, err := ec.dimensionColumn(dimension)
		if err != nil {
			return Statement{}, err
		}
		selects = append(selects, col+" AS "+quote(dimension.Name))
	}
	predicates := []string{}
	if p.Source.Dataset == "" {
		anchor := p.Base.Metrics[0].Expression
		field, ok := ec.fields[anchor.Field]
		if !ok {
			return Statement{}, problem("invalid_analysis", "source.anchor", "anchor field is missing")
		}
		anchorColumn, err := ec.fieldColumn(field)
		if err != nil {
			return Statement{}, err
		}
		predicates = append(predicates, anchorColumn+" IS NOT NULL")
		for _, f := range anchor.Filters {
			field, ok := ec.fields[f.Field]
			if !ok {
				return Statement{}, problem("invalid_analysis", "source.anchor", "anchor filter field is missing")
			}
			col, err := ec.fieldColumn(field)
			if err != nil {
				return Statement{}, err
			}
			predicate, err := ec.metricFilter(col, field.DataType, f)
			if err != nil {
				return Statement{}, err
			}
			predicates = append(predicates, predicate)
		}
	}
	d := dimensions[p.Query.TimeRange.Dimension]
	loc, err := time.LoadLocation(d.CalendarTimezone)
	if err != nil {
		return Statement{}, err
	}
	a, err := time.Parse(time.RFC3339Nano, p.Query.TimeRange.Start)
	if err != nil {
		return Statement{}, err
	}
	b, err := time.Parse(time.RFC3339Nano, p.Query.TimeRange.End)
	if err != nil {
		return Statement{}, err
	}
	timeCol, err := ec.dimensionColumn(d)
	if err != nil {
		return Statement{}, err
	}
	start := ec.parameter("time_start", a.In(loc).Format(time.DateOnly), model.DataTypeDate)
	end := ec.parameter("time_end", b.In(loc).Format(time.DateOnly), model.DataTypeDate)
	predicates = append(predicates, fmt.Sprintf("%s >= :%s AND %s < :%s", timeCol, start, timeCol, end))
	joins, guards, err := compileAnalysisJoins(p, ec, a.In(loc), b.In(loc))
	if err != nil {
		return Statement{}, err
	}
	joinedPredicates := []string{}
	for _, f := range p.Base.Filters {
		d := dimensions[f.Dimension]
		col, err := ec.dimensionColumn(d)
		if err != nil {
			return Statement{}, err
		}
		predicate, err := ec.queryFilter(col, d.DataType, f)
		if err != nil {
			return Statement{}, err
		}
		if d.Entity == p.Base.Root.Entity {
			predicates = append(predicates, predicate)
		} else {
			joinedPredicates = append(joinedPredicates, predicate)
		}
	}
	builder := analysisSQL{parameters: ec, rawRefs: map[string]string{}}
	for name, dimension := range dimensions {
		col, err := ec.dimensionColumn(dimension)
		if err != nil {
			return Statement{}, err
		}
		builder.rawRefs[name] = col
	}
	var joined func(exploration.Expression) bool
	joined = func(e exploration.Expression) bool {
		if e.Op == "ref" && dimensions[e.Ref].Entity != p.Base.Root.Entity {
			return true
		}
		for _, a := range e.Args {
			if joined(a) {
				return true
			}
		}
		return false
	}
	var addCondition func(exploration.Expression) error
	addCondition = func(e exploration.Expression) error {
		// Only AND can be split without changing meaning. A mixed OR/CASE
		// must wait for verified associations before inspecting joined values.
		if e.Op == "and" {
			for _, a := range e.Args {
				if err := addCondition(a); err != nil {
					return err
				}
			}
			return nil
		}
		sql, err := builder.expression(e)
		if err != nil {
			return err
		}
		if joined(e) {
			joinedPredicates = append(joinedPredicates, "("+sql+")")
		} else {
			predicates = append(predicates, "("+sql+")")
		}
		return nil
	}
	for _, condition := range []*exploration.Expression{p.Source.FixedFilter, p.Query.Where} {
		if condition != nil {
			if err := addCondition(*condition); err != nil {
				return Statement{}, err
			}
		}
	}
	population := strings.Join(predicates, " AND ")
	if len(guards) > 0 {
		afterMatch := "TRUE"
		if len(joinedPredicates) > 0 {
			afterMatch = strings.Join(joinedPredicates, " AND ")
		}
		// Root filters exclude rows first. Joined predicates must not inspect
		// MAX-collapsed values until every cardinality guard has succeeded.
		population = "CASE WHEN " + population + " THEN CASE WHEN " + strings.Join(guards, " AND ") + " THEN " + afterMatch + " ELSE FALSE END ELSE FALSE END"
	}
	builder.rawRefs = nil
	builder.current = "SELECT " + strings.Join(selects, ", ") + " FROM " + root + " AS t0 " + strings.Join(joins, " ") + " WHERE " + population
	if err = builder.stage(p.Query.GroupBy, p.Query.Measures, p.Query.Calculations, p.Query.TimeGrouping); err != nil {
		return Statement{}, err
	}
	if summary := p.Query.Summarize; summary != nil {
		if err = builder.stage(summary.GroupBy, summary.Measures, summary.Calculations, nil); err != nil {
			return Statement{}, err
		}
	}
	outputs := []string{}
	for _, col := range p.Columns {
		outputs = append(outputs, quote(col.Name))
	}
	sql := "SELECT " + strings.Join(outputs, ", ") + " FROM " + builder.relation()
	if len(p.Query.OrderBy) > 0 {
		orders := []string{}
		for _, o := range p.Query.OrderBy {
			orders = append(orders, quote(o.Field)+" "+strings.ToUpper(string(o.Direction)))
		}
		sql += "\nORDER BY " + strings.Join(orders, ", ")
	}
	// The only limit is after both aggregation stages and window denominators.
	sql += fmt.Sprintf("\nLIMIT %d", p.Query.Limit)
	statement := Statement{SQL: sql, Parameters: ec.parameters, PhysicalFingerprint: p.Fingerprint, RowLimit: int64(p.Query.Limit)}
	if err = validateStatement(statement); err != nil {
		return Statement{}, err
	}
	return statement, nil
}

type analysisSQL struct {
	rawRefs    map[string]string
	parameters *expressionCompiler
	next       int
	current    string
}

func (b *analysisSQL) append(sql string) {
	b.current = sql
	b.next++
}

func (b *analysisSQL) relation() string { return fmt.Sprintf("(%s) AS a%d", b.current, b.next) }

func (b *analysisSQL) stage(groups []string, ms []exploration.Measure, cs []exploration.Calculation, tg *model.TimeGrouping) error {
	selects := []string{}
	groupSQL := []string{}
	for _, g := range groups {
		s := quote(g)
		if tg != nil && tg.Dimension == g && tg.Granularity != model.GrainDay {
			s = "trunc(" + s + ", '" + strings.ToUpper(string(tg.Granularity)) + "')"
		}
		groupSQL = append(groupSQL, s)
		selects = append(selects, s+" AS "+quote(g))
	}
	for _, m := range ms {
		field := quote(m.Field)
		if m.Field == "" {
			field = "1"
		}
		if m.Where != nil {
			cond, err := b.expression(*m.Where)
			if err != nil {
				return err
			}
			field = "CASE WHEN " + cond + " THEN " + field + " ELSE NULL END"
		}
		op := strings.ToUpper(m.Op)
		if m.Op == "count_distinct" {
			op = "COUNT"
			field = "DISTINCT " + field
		}
		selects = append(selects, op+"("+field+") AS "+quote(m.Name))
	}
	sql := "SELECT " + strings.Join(selects, ", ") + " FROM " + b.relation()
	if len(groupSQL) > 0 {
		sql += " GROUP BY " + strings.Join(groupSQL, ", ")
	}
	b.append(sql)
	for _, c := range cs {
		expr, err := b.expression(c.Expression)
		if err != nil {
			return err
		}
		b.append("SELECT *, " + expr + " AS " + quote(c.Name) + " FROM " + b.relation())
	}
	return nil
}

func (b *analysisSQL) expression(e exploration.Expression) (string, error) {
	if e.Op == "ref" {
		if b.rawRefs != nil {
			return b.rawRefs[e.Ref], nil
		}
		return quote(e.Ref), nil
	}
	if e.Op == "literal" {
		return ":" + b.parameters.parameter("analysis", e.Value, e.Type), nil
	}
	args := make([]string, len(e.Args))
	for i, a := range e.Args {
		s, err := b.expression(a)
		if err != nil {
			return "", err
		}
		args[i] = s
	}
	operators := map[string]string{"add": "+", "subtract": "-", "multiply": "*", "eq": "=", "neq": "<>", "lt": "<", "lte": "<=", "gt": ">", "gte": ">="}
	if op, ok := operators[e.Op]; ok {
		return "(" + args[0] + " " + op + " " + args[1] + ")", nil
	}
	switch e.Op {
	case "divide":
		return "(CAST(" + args[0] + " AS DECIMAL(38,18)) * 1.0 / NULLIF(" + args[1] + ", 0))", nil
	case "and", "or":
		return "(" + strings.Join(args, " "+strings.ToUpper(e.Op)+" ") + ")", nil
	case "not":
		return "(NOT " + args[0] + ")", nil
	case "is_null":
		return "(" + args[0] + " IS NULL)", nil
	case "is_not_null":
		return "(" + args[0] + " IS NOT NULL)", nil
	case "weekday":
		return "weekday(" + args[0] + ")", nil
	case "case":
		s := "CASE"
		for i := 0; i < len(args)-1; i += 2 {
			s += " WHEN " + args[i] + " THEN " + args[i+1]
		}
		return s + " ELSE " + args[len(args)-1] + " END", nil
	case "share":
		partition := ""
		if len(e.PartitionBy) > 0 {
			keys := []string{}
			for _, k := range e.PartitionBy {
				keys = append(keys, quote(k))
			}
			partition = "PARTITION BY " + strings.Join(keys, ", ")
		}
		return "(CAST(" + args[0] + " AS DECIMAL(38,18)) * 1.0 / NULLIF(SUM(" + args[0] + ") OVER (" + partition + "), 0))", nil
	}
	return "", problem("invalid_analysis", "expression", "unsupported operation")
}
