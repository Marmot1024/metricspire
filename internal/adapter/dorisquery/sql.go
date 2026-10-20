package dorisquery

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/planner"
)

const queryHint = "/*+ SET_VAR(query_timeout=2,exec_mem_limit=268435456,parallel_pipeline_task_num=2,enable_sql_cache=false) */"

type statement struct {
	sql        string
	args       []any
	dimensions []model.PhysicalDimension
	fields     []model.PhysicalField
}

func compile(p model.PhysicalPlan, tenant string, types map[string]string) (statement, error) {
	bad := func() (statement, error) {
		return statement{}, problem("unsupported_online_query", "a bounded daily single-table Doris plan with supported types is required")
	}
	if err := planner.VerifyPhysical(p); err != nil {
		return statement{}, err
	}
	if p.Engine != EngineName || len(p.Joins) != 0 || p.TimeRange == nil || p.Limit < 1 || p.Limit > 1000 || !validResource(p.Root.Resource) {
		return bad()
	}
	if p.TimeGrouping != nil && p.TimeGrouping.Granularity != model.GrainDay {
		return bad()
	}
	s := statement{}
	selects, groups := []string{}, []string{}
	var parameterErr error
	parameter := func(v, typ string) string {
		switch typ {
		case "Date":
			if _, err := time.Parse(time.DateOnly, v); err != nil {
				parameterErr = err
			}
			s.args = append(s.args, v)
			return "CAST(? AS DATE)"
		case "Int64":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				parameterErr = err
			}
			s.args = append(s.args, n)
			return "CAST(? AS BIGINT)"
		case "Bool":
			n, err := strconv.ParseBool(v)
			if err != nil {
				parameterErr = err
			}
			s.args = append(s.args, n)
			return "CAST(? AS BOOLEAN)"
		default:
			s.args = append(s.args, v)
			return "?"
		}
	}

	dims := map[string]model.PhysicalDimension{}
	for _, d := range p.Dimensions {
		if d.Resource != p.Root.Resource || d.Entity != p.Root.Entity || !identifier.MatchString(d.Column) {
			return bad()
		}
		actual := types[d.Column]
		base := actual
		valid := false
		switch d.DataType {
		case model.DataTypeDate:
			valid = base == "date"
		case model.DataTypeString:
			valid = strings.HasPrefix(base, "varchar(") || strings.HasPrefix(base, "char(")
		case model.DataTypeBoolean:
			valid = base == "boolean" || base == "tinyint"
		case model.DataTypeInteger:
			valid = supportedNumeric(base) && !strings.HasPrefix(base, "decimal")
		}
		if !valid {
			return bad()
		}
		dims[d.Name] = d
		if d.Output {
			s.dimensions = append(s.dimensions, d)
			selects = append(selects, "CAST(t0."+quote(d.Column)+" AS STRING)")
			groups = append(groups, "t0."+quote(d.Column))
		}
	}
	// Return only exact SUM leaves. Arbitrary-precision arithmetic in result.go
	// avoids Decimal128/256 intermediate overflow and float division in SQL.
	for _, f := range p.MetricFields {
		if f.Resource != p.Root.Resource || f.Entity != p.Root.Entity || !identifier.MatchString(f.Column) || !supportedNumeric(types[f.Column]) || (f.DataType != model.DataTypeDecimal && f.DataType != model.DataTypeInteger) {
			return bad()
		}
		if f.DataType == model.DataTypeInteger && strings.Contains(types[f.Column], "decimal") {
			return bad()
		}
		s.fields = append(s.fields, f)
		selects = append(selects, "CAST(SUM(CAST(t0."+quote(f.Column)+" AS DECIMAL(38,6))) AS STRING)")
	}
	if len(s.fields) == 0 {
		return bad()
	}
	// Inputs are bounded exact integers/DECIMAL(<=18,<=6), widened before SUM.
	selects = append(selects, "CAST(COUNT(*) AS STRING)", "min(t0._metricspire_batch_id)", "max(t0._metricspire_batch_id)", "CAST(CAST(UNIX_TIMESTAMP(min(t0._metricspire_data_as_of))*1000 AS BIGINT) AS STRING)", "CAST(CAST(UNIX_TIMESTAMP(max(t0._metricspire_data_as_of))*1000 AS BIGINT) AS STRING)", "min(t0._metricspire_data_contract)", "max(t0._metricspire_data_contract)")

	d, ok := dims[p.TimeRange.Dimension]
	if !ok || d.DataType != model.DataTypeDate {
		return bad()
	}
	loc, err := time.LoadLocation(d.CalendarTimezone)
	if err != nil {
		return bad()
	}
	start, err := time.Parse(time.RFC3339Nano, p.TimeRange.Start)
	if err != nil {
		return bad()
	}
	end, err := time.Parse(time.RFC3339Nano, p.TimeRange.End)
	if err != nil {
		return bad()
	}
	if !end.After(start) || end.Sub(start) > 31*24*time.Hour {
		return statement{}, problem("budget_exceeded", "online time range must be positive and at most 31 days")
	}
	for _, t := range []time.Time{start.In(loc), end.In(loc)} {
		if t.Hour() != 0 || t.Minute() != 0 || t.Second() != 0 || t.Nanosecond() != 0 {
			return bad()
		}
	}
	where := []string{"t0._metricspire_tenant=" + parameter(tenant, "String"), "t0." + quote(d.Column) + ">=" + parameter(start.In(loc).Format(time.DateOnly), "Date"), "t0." + quote(d.Column) + "<" + parameter(end.In(loc).Format(time.DateOnly), "Date")}
	for _, f := range p.Filters {
		dim, ok := dims[f.Dimension]
		if !ok || len(f.Values) == 0 || (f.Operator == model.FilterEqual && len(f.Values) != 1) || (f.Operator != model.FilterEqual && f.Operator != model.FilterIn) {
			return bad()
		}
		typ := map[model.DataType]string{model.DataTypeDate: "Date", model.DataTypeString: "String", model.DataTypeInteger: "Int64", model.DataTypeBoolean: "Bool"}[dim.DataType]
		if typ == "" {
			return bad()
		}
		values := []string{}
		for _, v := range f.Values {
			values = append(values, parameter(v, typ))
		}
		where = append(where, "t0."+quote(dim.Column)+" IN ("+strings.Join(values, ",")+")")
	}
	s.sql = "SELECT " + queryHint + " " + strings.Join(selects, ",") + " FROM " + quote(p.Root.Resource.Schema) + "." + quote(p.Root.Resource.Table) + " AS t0 WHERE " + strings.Join(where, " AND ")
	if len(groups) > 0 {
		s.sql += " GROUP BY " + strings.Join(groups, ",")
	}
	// Sorting happens after bounded materialization. Overflow is refused rather
	// than returning an arbitrary top-N subset or hiding incompatible groups.
	s.sql += fmt.Sprintf(" LIMIT %d", p.Limit+1)
	if parameterErr != nil {
		return bad()
	}
	return s, nil
}
