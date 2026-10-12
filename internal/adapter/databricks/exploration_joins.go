package databricks

import (
	"slices"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/exploration"
	"github.com/marmot1024/metricspire/internal/model"
)

// Target rows are grouped by their complete match key before joining. This
// prevents fanout even if SQL optimization prunes an error guard. More than
// one match is rejected, never resolved by picking an arbitrary label.
func compileAnalysisJoins(p exploration.Plan, ec *expressionCompiler, start, end time.Time) ([]string, []string, error) {
	joins := []string{}
	guards := []string{}
	for _, r := range p.Relations {
		table, err := renderTable(r.Resource)
		if err != nil {
			return nil, nil, err
		}
		fromAlias, fromOK := ec.aliases[r.From]
		alias, toOK := ec.aliases[r.To]
		if !fromOK || !toOK {
			return nil, nil, problem("invalid_analysis", "relations", "unbound relation entity")
		}
		keys := []string{}
		conditions := []string{}
		columns := map[string]bool{}
		for _, k := range r.Keys {
			keys = append(keys, k.To)
			columns[k.To] = true
			conditions = append(conditions, fromAlias+"."+quote(k.From)+" = "+alias+"."+quote(k.To))
		}
		where := ""
		if s := r.Snapshot; s != nil {
			keys = append(keys, s.To)
			columns[s.To] = true
			date := fromAlias + "." + quote(s.From)
			switch s.Anchor {
			case "week_start":
				date = "trunc(" + date + ", 'WEEK')"
			case "week_end":
				date = "date_add(trunc(" + date + ", 'WEEK'), 6)"
			}
			conditions = append(conditions, date+" = "+alias+"."+quote(s.To))
			min, max := start, end
			if s.Anchor != "same_day" {
				min = min.AddDate(0, 0, -6)
				max = max.AddDate(0, 0, 6)
			}
			first := ec.parameter("snapshot_start", min.Format(time.DateOnly), model.DataTypeDate)
			last := ec.parameter("snapshot_end", max.Format(time.DateOnly), model.DataTypeDate)
			where = " WHERE " + quote(s.To) + " >= :" + first + " AND " + quote(s.To) + " < :" + last
			if s.Anchor == "week_start" {
				where += " AND weekday(" + quote(s.To) + ") = 0"
			}
			if s.Anchor == "week_end" {
				where += " AND weekday(" + quote(s.To) + ") = 6"
			}
		}
		for _, d := range p.Base.Dimensions {
			if d.Entity == r.To {
				columns[d.Column] = true
			}
		}
		// A joined dataset can supply keys to the next approved relation.
		for _, child := range p.Relations {
			if child.From == r.To {
				for _, k := range child.Keys {
					columns[k.From] = true
				}
				if child.Snapshot != nil {
					columns[child.Snapshot.From] = true
				}
			}
		}
		ordered := []string{}
		for c := range columns {
			ordered = append(ordered, c)
		}
		slices.Sort(ordered)
		selects := []string{}
		group := []string{}
		for _, c := range ordered {
			if c == "_analysis_matches" {
				return nil, nil, problem("invalid_analysis", "relations", "reserved join column")
			}
			if slices.Contains(keys, c) {
				selects = append(selects, quote(c))
				group = append(group, quote(c))
			} else {
				selects = append(selects, "MAX("+quote(c)+") AS "+quote(c))
			}
		}
		selects = append(selects, "COUNT(*) AS `_analysis_matches`")
		target := "(SELECT " + strings.Join(selects, ", ") + " FROM " + table + where + " GROUP BY " + strings.Join(group, ", ") + ")"
		joins = append(joins, "LEFT JOIN "+target+" AS "+alias+" ON "+strings.Join(conditions, " AND "))
		duplicate := ec.parameter("join_error", "analysis_duplicate_match", model.DataTypeString)
		guard := "CASE WHEN " + alias + ".`_analysis_matches` > 1 THEN raise_error(:" + duplicate + ")"
		if r.Missing == "error" {
			missing := ec.parameter("join_error", "analysis_missing_match", model.DataTypeString)
			guard += " WHEN " + alias + ".`_analysis_matches` IS NULL THEN raise_error(:" + missing + ")"
		}
		guard += " ELSE TRUE END"
		guards = append(guards, "("+guard+")")
	}
	return joins, guards, nil
}
