package exploration

import "github.com/marmot1024/metricspire/internal/model"

// Internal-only fields may define a counting unit or predicate, but their
// values must not survive into the final result. Count operations produce
// public counts; arithmetic, extrema, aliases and buckets retain restrictions.
// This is an output restriction, not a statistical privacy guarantee.
func validateOutputAccess(q Query, internal []string) error {
	if len(internal) == 0 {
		return nil
	}
	restricted := map[string]bool{}
	for _, ref := range internal {
		restricted[ref] = true
	}
	restricted = stageOutputAccess(q.GroupBy, q.Measures, q.Calculations, restricted)
	if s := q.Summarize; s != nil {
		restricted = stageOutputAccess(s.GroupBy, s.Measures, s.Calculations, restricted)
	}
	for _, denied := range restricted {
		if denied {
			return &model.Problem{Code: "analysis_forbidden", Path: "output", Message: "internal-only attributes must not appear in final output; aggregate to counts or remove them in summarize"}
		}
	}
	return nil
}

func stageOutputAccess(groups []string, measures []Measure, calculations []Calculation, in map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, group := range groups {
		out[group] = in[group]
	}
	for _, m := range measures {
		out[m.Name] = in[m.Field] && m.Op != "count" && m.Op != "count_distinct"
	}
	var restricted func(Expression) bool
	restricted = func(e Expression) bool {
		if e.Op == "ref" && out[e.Ref] {
			return true
		}
		for _, ref := range e.PartitionBy {
			if out[ref] {
				return true
			}
		}
		for _, arg := range e.Args {
			if restricted(arg) {
				return true
			}
		}
		return false
	}
	for _, c := range calculations {
		out[c.Name] = restricted(c.Expression)
	}
	return out
}
