package exploration

// Include transport parameters added by trusted bindings, not just user filters.
// Keep this admission ceiling aligned with the warehouse statement contract.
func validateParameterBudget(p Plan) error {
	count := 2 // half-open root date range
	var visit func(Expression)
	visit = func(e Expression) {
		if e.Op == "literal" {
			count++
		}
		for _, a := range e.Args {
			visit(a)
		}
	}
	stage := func(ms []Measure, cs []Calculation) {
		for _, m := range ms {
			if m.Where != nil {
				visit(*m.Where)
			}
		}
		for _, c := range cs {
			visit(c.Expression)
		}
	}
	stage(p.Query.Measures, p.Query.Calculations)
	if s := p.Query.Summarize; s != nil {
		stage(s.Measures, s.Calculations)
	}
	for _, e := range []*Expression{p.Query.Where, p.Source.FixedFilter} {
		if e != nil {
			visit(*e)
		}
	}
	for _, f := range p.Base.Filters {
		count += len(f.Values)
	}
	for _, m := range p.Base.Metrics {
		for _, f := range m.Expression.Filters {
			count += len(f.Values)
		}
	}
	for _, r := range p.Relations {
		count++
		if r.Missing == "error" {
			count++
		}
		if r.Snapshot != nil {
			count += 2
		}
	}
	if count > 1000 {
		return invalid("query", "combined parameter budget exceeds 1000")
	}
	return nil
}
