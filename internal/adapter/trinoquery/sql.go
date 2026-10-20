// Package trinoquery executes bounded analyses over reviewed single-table catalogs.
package trinoquery

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/exploration"
	"github.com/marmot1024/metricspire/internal/model"
)

const Trino = "trino"
const Presto = "presto"

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)
var decimal = regexp.MustCompile(`^-?[0-9]{1,38}(\.[0-9]{1,38})?$`)

func problem(code, message string) error { return &model.Problem{Code: code, Message: message} }
func quote(s string) string              { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func literal(s string, t model.DataType) (string, error) {
	switch t {
	case model.DataTypeString:
		if strings.ContainsRune(s, 0) {
			return "", problem("invalid_analysis", "NUL string is unsupported")
		}
		return "'" + strings.ReplaceAll(s, "'", "''") + "'", nil
	case model.DataTypeDate:
		if _, err := time.Parse(time.DateOnly, s); err != nil {
			return "", problem("invalid_analysis", "invalid date literal")
		}
		return "DATE '" + s + "'", nil
	case model.DataTypeInteger:
		if _, err := strconv.ParseInt(s, 10, 64); err != nil {
			return "", problem("invalid_analysis", "integer literal exceeds signed 64 bits")
		}
		return s, nil
	case model.DataTypeDecimal:
		if !decimal.MatchString(s) || len(strings.NewReplacer("-", "", ".", "").Replace(s)) > 38 {
			return "", problem("invalid_analysis", "invalid decimal literal")
		}
		return "DECIMAL '" + s + "'", nil
	case model.DataTypeBoolean:
		if s != "true" && s != "false" {
			return "", problem("invalid_analysis", "invalid boolean literal")
		}
		return strings.ToUpper(s), nil
	default:
		return "", problem("capability_missing", "timestamp expressions are not supported by this adapter")
	}
}
func ValidateCatalog(c exploration.Catalog, driver string) error {
	if err := exploration.ValidateCatalog(c); err != nil {
		return err
	}
	if c.EngineName() != driver || len(c.Relationships) != 0 {
		return problem("capability_missing", "Trino/Presto catalogs require the selected engine and a single-table source without relationships")
	}
	for _, d := range c.Datasets {
		for _, f := range d.Fields {
			if f.Type == model.DataTypeTimestamp {
				return problem("capability_missing", "timestamp fields need a reviewed timezone contract; use DATE-based sources")
			}
		}
	}
	return nil
}

type compiler struct {
	current string
	raw     map[string]string
	next    int
	types   map[string]model.DataType
}

func (b *compiler) relation() string { return fmt.Sprintf("(%s) AS a%d", b.current, b.next) }
func (b *compiler) append(s string)  { b.current = s; b.next++ }
func (b *compiler) expression(e exploration.Expression) (string, error) {
	if e.Op == "ref" {
		if b.raw != nil {
			v, ok := b.raw[e.Ref]
			if !ok {
				return "", problem("invalid_analysis", "unknown raw reference")
			}
			return v, nil
		}
		return quote(e.Ref), nil
	}
	if e.Op == "literal" {
		return literal(e.Value, e.Type)
	}
	args := make([]string, len(e.Args))
	for i, a := range e.Args {
		v, err := b.expression(a)
		if err != nil {
			return "", err
		}
		args[i] = v
	}
	ops := map[string]string{"add": "+", "subtract": "-", "multiply": "*", "eq": "=", "neq": "<>", "lt": "<", "lte": "<=", "gt": ">", "gte": ">="}
	if op, ok := ops[e.Op]; ok {
		return "(" + args[0] + " " + op + " " + args[1] + ")", nil
	}
	switch e.Op {
	case "divide":
		numerator := args[0]
		if typ, err := exploration.ExpressionType(e.Args[0], b.types, nil, true); err == nil && typ == model.DataTypeInteger {
			numerator = "CAST(" + numerator + " AS DECIMAL(38,18))"
		}
		return "(" + numerator + " / NULLIF(" + args[1] + ", 0))", nil
	case "and", "or":
		return "(" + strings.Join(args, " "+strings.ToUpper(e.Op)+" ") + ")", nil
	case "not":
		return "(NOT " + args[0] + ")", nil
	case "is_null":
		return "(" + args[0] + " IS NULL)", nil
	case "is_not_null":
		return "(" + args[0] + " IS NOT NULL)", nil
	case "weekday":
		return "(day_of_week(" + args[0] + ") - 1)", nil
	case "case":
		s := "CASE"
		for i := 0; i < len(args)-1; i += 2 {
			s += " WHEN " + args[i] + " THEN " + args[i+1]
		}
		return s + " ELSE " + args[len(args)-1] + " END", nil
	case "share":
		partition := ""
		keys := []string{}
		for _, key := range e.PartitionBy {
			keys = append(keys, quote(key))
		}
		if len(keys) > 0 {
			partition = "PARTITION BY " + strings.Join(keys, ", ")
		}
		numerator := args[0]
		if typ, err := exploration.ExpressionType(e.Args[0], b.types, nil, true); err == nil && typ == model.DataTypeInteger {
			numerator = "CAST(" + numerator + " AS DECIMAL(38,18))"
		}
		return "(" + numerator + " / NULLIF(SUM(" + args[0] + ") OVER (" + partition + "), 0))", nil
	}
	return "", problem("capability_missing", "unsupported analysis operation")
}
func (b *compiler) stage(groups []string, ms []exploration.Measure, cs []exploration.Calculation, tg *model.TimeGrouping) error {
	selects, groupSQL := []string{}, []string{}
	for _, g := range groups {
		col := quote(g)
		if tg != nil && tg.Dimension == g && tg.Granularity != model.GrainDay {
			col = "CAST(date_trunc('" + string(tg.Granularity) + "', " + col + ") AS DATE)"
		}
		groupSQL = append(groupSQL, col)
		selects = append(selects, col+" AS "+quote(g))
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
		if m.Op == "avg" && b.types[m.Field] == model.DataTypeInteger {
			field = "CAST(" + field + " AS DECIMAL(38,12))"
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
	nextTypes := map[string]model.DataType{}
	for _, g := range groups {
		nextTypes[g] = b.types[g]
	}
	for _, m := range ms {
		typ := b.types[m.Field]
		if m.Op == "avg" {
			typ = model.DataTypeDecimal
		}
		if m.Op == "count" || m.Op == "count_distinct" {
			typ = model.DataTypeInteger
		}
		nextTypes[m.Name] = typ
	}
	b.types = nextTypes
	for _, c := range cs {
		expr, err := b.expression(c.Expression)
		if err != nil {
			return err
		}
		b.append("SELECT *, " + expr + " AS " + quote(c.Name) + " FROM " + b.relation())
		typ, err := exploration.ExpressionType(c.Expression, b.types, groups, true)
		if err != nil {
			return err
		}
		b.types[c.Name] = typ
	}
	return nil
}
func Compile(p exploration.Plan, driver string) (string, error) {
	if err := exploration.Verify(p); err != nil {
		return "", err
	}
	if p.Base.Engine != driver || (driver != Trino && driver != Presto) {
		return "", problem("engine_mismatch", "analysis plan uses another engine")
	}
	if p.Source.Dataset == "" || len(p.Relations) != 0 {
		return "", problem("capability_missing", "this adapter requires an approved single-table source")
	}
	r := p.Base.Root.Resource
	if r.Kind != model.ResourceTable || r.URI != "" || !identifier.MatchString(r.Catalog) || !identifier.MatchString(r.Schema) || !identifier.MatchString(r.Table) {
		return "", problem("invalid_analysis", "a fully qualified reviewed table is required")
	}
	b := compiler{raw: map[string]string{}}
	selects := []string{}
	types := map[string]model.DataType{}
	b.types = types
	zones := map[string]string{}
	for _, d := range p.Base.Dimensions {
		if d.Entity != p.Base.Root.Entity || !identifier.MatchString(d.Column) {
			return "", problem("invalid_analysis", "unbound source column")
		}
		b.raw[d.Name] = quote(d.Column)
		types[d.Name] = d.DataType
		zones[d.Name] = d.CalendarTimezone
		selects = append(selects, quote(d.Column)+" AS "+quote(d.Name))
	}
	timeCol, ok := b.raw[p.Query.TimeRange.Dimension]
	if !ok {
		return "", problem("invalid_analysis", "unbound date range")
	}
	loc, err := time.LoadLocation(zones[p.Query.TimeRange.Dimension])
	if err != nil {
		return "", err
	}
	a, err := time.Parse(time.RFC3339Nano, p.Query.TimeRange.Start)
	if err != nil {
		return "", err
	}
	z, err := time.Parse(time.RFC3339Nano, p.Query.TimeRange.End)
	if err != nil {
		return "", err
	}
	start, _ := literal(a.In(loc).Format(time.DateOnly), model.DataTypeDate)
	end, _ := literal(z.In(loc).Format(time.DateOnly), model.DataTypeDate)
	predicates := []string{timeCol + " >= " + start, timeCol + " < " + end}
	for _, f := range p.Base.Filters {
		vals := []string{}
		for _, value := range f.Values {
			v, err := literal(value, types[f.Dimension])
			if err != nil {
				return "", err
			}
			vals = append(vals, v)
		}
		col, ok := b.raw[f.Dimension]
		if !ok {
			return "", problem("invalid_analysis", "unbound filter")
		}
		if f.Operator == model.FilterEqual {
			predicates = append(predicates, col+" = "+vals[0])
		} else if f.Operator == model.FilterIn {
			predicates = append(predicates, col+" IN ("+strings.Join(vals, ", ")+")")
		} else {
			return "", problem("invalid_analysis", "unsupported filter")
		}
	}
	for _, e := range []*exploration.Expression{p.Source.FixedFilter, p.Query.Where} {
		if e != nil {
			expr, err := b.expression(*e)
			if err != nil {
				return "", err
			}
			predicates = append(predicates, expr)
		}
	}
	b.current = "SELECT " + strings.Join(selects, ", ") + " FROM " + quote(r.Catalog) + "." + quote(r.Schema) + "." + quote(r.Table) + " WHERE " + strings.Join(predicates, " AND ")
	b.raw = nil
	if err := b.stage(p.Query.GroupBy, p.Query.Measures, p.Query.Calculations, p.Query.TimeGrouping); err != nil {
		return "", err
	}
	if s := p.Query.Summarize; s != nil {
		if err := b.stage(s.GroupBy, s.Measures, s.Calculations, nil); err != nil {
			return "", err
		}
	}
	outputs := []string{}
	for _, c := range p.Columns {
		outputs = append(outputs, quote(c.Name))
	}
	sql := "SELECT " + strings.Join(outputs, ", ") + " FROM " + b.relation()
	orders := []string{}
	for _, o := range p.Query.OrderBy {
		nulls := " NULLS FIRST"
		if o.Direction == model.SortDescending {
			nulls = " NULLS LAST"
		}
		orders = append(orders, quote(o.Field)+" "+strings.ToUpper(string(o.Direction))+nulls)
	}
	if len(orders) > 0 {
		sql += " ORDER BY " + strings.Join(orders, ", ")
	}
	sql += fmt.Sprintf(" LIMIT %d", p.Query.Limit)
	if len(sql) > 1<<20 {
		return "", problem("invalid_analysis", "compiled query exceeds text budget")
	}
	return sql, nil
}
