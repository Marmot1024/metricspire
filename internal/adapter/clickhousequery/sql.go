package clickhousequery

import (
	"fmt"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/planner"
)

type statement struct {
	sql        string
	params     map[string]string
	dimensions []model.PhysicalDimension
	fields     []model.PhysicalField
}

func compile(p model.PhysicalPlan, tenant string, types map[string]string) (statement, error) {
	bad := func() (statement, error) {
		return statement{}, problem("unsupported_online_query", "a bounded daily single-table ClickHouse plan with supported types is required")
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
	s := statement{params: map[string]string{}}
	selects, groups := []string{}, []string{}
	parameter := func(v, t string) string {
		name := fmt.Sprintf("p%d", len(s.params))
		s.params[name] = v
		return "{" + name + ":" + t + "}"
	}
	dims := map[string]model.PhysicalDimension{}
	for _, d := range p.Dimensions {
		if d.Resource != p.Root.Resource || d.Entity != p.Root.Entity || !identifier.MatchString(d.Column) {
			return bad()
		}
		actual := types[d.Column]
		base := unwrapNullable(actual)
		valid := false
		switch d.DataType {
		case model.DataTypeDate:
			valid = base == "Date"
		case model.DataTypeString:
			valid = base == "String" || base == "LowCardinality(String)"
		case model.DataTypeBoolean:
			valid = base == "Bool"
		case model.DataTypeInteger:
			valid = strings.HasPrefix(base, "Int") && supportedNumeric(base)
		}
		if !valid {
			return bad()
		}
		dims[d.Name] = d
		if d.Output {
			s.dimensions = append(s.dimensions, d)
			selects = append(selects, "toString(t0."+quote(d.Column)+")")
			groups = append(groups, "t0."+quote(d.Column))
		}
	}
	// Return only exact SUM leaves. Arbitrary-precision arithmetic in result.go
	// avoids Decimal128/256 intermediate overflow and float division in SQL.
	for _, f := range p.MetricFields {
		if f.Resource != p.Root.Resource || f.Entity != p.Root.Entity || !identifier.MatchString(f.Column) || !supportedNumeric(types[f.Column]) || (f.DataType != model.DataTypeDecimal && f.DataType != model.DataTypeInteger) {
			return bad()
		}
		if f.DataType == model.DataTypeInteger && strings.Contains(types[f.Column], "Decimal") {
			return bad()
		}
		s.fields = append(s.fields, f)
		selects = append(selects, "toString(sumOrNull(toDecimal128(t0."+quote(f.Column)+",6)))")
	}
	if len(s.fields) == 0 {
		return bad()
	}
	// Int64/UInt64/Decimal(P<=18,S<=6), widened to Decimal128(6), summed over
	// <=50M rows stay below 10^33 scaled units (<2^127). No silent overflow.
	selects = append(selects, "toString(count())", "min(t0._metricspire_batch_id)", "max(t0._metricspire_batch_id)", "toString(toUnixTimestamp64Milli(min(t0._metricspire_data_as_of)))", "toString(toUnixTimestamp64Milli(max(t0._metricspire_data_as_of)))", "min(t0._metricspire_data_contract)", "max(t0._metricspire_data_contract)")
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
	s.sql = "SELECT " + strings.Join(selects, ",") + " FROM " + quote(p.Root.Resource.Schema) + "." + quote(p.Root.Resource.Table) + " AS t0 WHERE " + strings.Join(where, " AND ")
	if len(groups) > 0 {
		s.sql += " GROUP BY " + strings.Join(groups, ",")
	}
	// Sorting happens after bounded materialization. Overflow is refused rather
	// than returning an arbitrary top-N subset or hiding incompatible groups.
	s.sql += fmt.Sprintf(" LIMIT %d", p.Limit+1)
	return s, nil
}
