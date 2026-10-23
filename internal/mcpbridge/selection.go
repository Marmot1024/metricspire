package mcpbridge

import (
	"reflect"
	"sort"
	"strings"
)

// Search is lexical retrieval, not a semantic-match decision. In particular,
// one returned candidate and a full page must not be mistaken for certainty.
type selectionGuidance struct {
	CandidateCount int    `json:"candidate_count"`
	AtLimit        bool   `json:"at_limit"`
	NextStep       string `json:"next_step"`
}

func searchSelection(count, limit int) selectionGuidance {
	next := "Review candidate definitions, population/exclusions, denominator and grain. Rank is not semantic fit. Execute an unambiguous match. If a material population/exclusion choice is unresolved or a requested rule is undocumented, ask and stop before execution; a generic total with a caveat does not resolve that choice."
	if count == 0 {
		next = "No authorized lexical match in this search. Refine terms/aliases and confirm the unmet definition with the user before approved exploration; do not infer data absence."
	}
	return selectionGuidance{count, limit > 0 && count >= limit, next}
}

type metricComparison struct {
	DifferingFields              []string `json:"differing_fields"`
	MissingDefinitions           []string `json:"missing_definitions"`
	MissingStructuredDefinitions []string `json:"missing_structured_definitions"`
	HasScopeAlternatives         bool     `json:"has_scope_alternatives"`
	NextStep                     string   `json:"next_step"`
}

// Compare only published, authorized metadata. Never derive an exclusion,
// denominator or role default from names, popularity, or SQL expressions.
func compareMetrics(metrics []MetricDetail) metricComparison {
	out := metricComparison{DifferingFields: []string{}, MissingDefinitions: []string{}, NextStep: "Compare the listed fields in metrics. Differences may be intentional across requested metrics; ask only if they leave the requested meaning ambiguous. If a material population/exclusion choice is unresolved or a requested rule is undocumented, ask and stop before execution. A verified metric or generic total plus a caveat cannot resolve that choice. Missing definitions require clarification, not inference. Role/history ranking cannot change scope. After confirming no suitable metric, discover and plan approved exploration."}
	fields := []struct {
		name  string
		value func(MetricDetail) any
	}{
		{"definition.population", func(m MetricDetail) any {
			if m.Definition == nil {
				return nil
			}
			return m.Definition.Population
		}},
		{"definition.exclusions", func(m MetricDetail) any {
			if m.Definition == nil {
				return nil
			}
			return m.Definition.Exclusions
		}},
		{"definition.numerator", func(m MetricDetail) any {
			if m.Definition == nil {
				return nil
			}
			return m.Definition.Numerator
		}},
		{"definition.denominator", func(m MetricDetail) any {
			if m.Definition == nil {
				return nil
			}
			return m.Definition.Denominator
		}},
		{"definition.counting_unit", func(m MetricDetail) any {
			if m.Definition == nil {
				return nil
			}
			return m.Definition.CountingUnit
		}},
		{"definition.time_basis", func(m MetricDetail) any {
			if m.Definition == nil {
				return nil
			}
			return m.Definition.TimeBasis
		}},
		{"description", func(m MetricDetail) any { return m.Description }},
		{"unit", func(m MetricDetail) any { return m.Unit }},
		{"value_type", func(m MetricDetail) any { return m.ValueType }},
		{"time_dimension", func(m MetricDetail) any { return m.TimeDimension }},
		{"allowed_dimensions", func(m MetricDetail) any { v := append([]string{}, m.AllowedDimensions...); sort.Strings(v); return v }},
		{"dimension_details", func(m MetricDetail) any {
			v := append(m.DimensionDetails[:0:0], m.DimensionDetails...)
			sort.Slice(v, func(i, j int) bool { return v[i].Name < v[j].Name })
			return v
		}},
		{"verification_status", func(m MetricDetail) any { return m.VerificationStatus }},
		{"release_channel", func(m MetricDetail) any { return m.ReleaseChannel }},
	}
	for _, m := range metrics {
		if m.Definition == nil {
			out.MissingStructuredDefinitions = append(out.MissingStructuredDefinitions, m.Name)
		}
		if strings.TrimSpace(m.Description) == "" {
			out.MissingDefinitions = append(out.MissingDefinitions, m.Name)
		}
	}
	if len(metrics) > 1 {
		for _, field := range fields {
			first := field.value(metrics[0])
			for _, m := range metrics[1:] {
				if !reflect.DeepEqual(first, field.value(m)) {
					out.DifferingFields = append(out.DifferingFields, field.name)
					break
				}
			}
		}
	}
	// Same-family alternatives need a scope decision, not a popularity default.
	// This is metadata evidence, not a claim about what the user has confirmed.
	for i, a := range metrics {
		for _, b := range metrics[i+1:] {
			if a.Definition == nil || b.Definition == nil || a.Definition.Family == "" || a.Definition.Family != b.Definition.Family {
				continue
			}
			x, y := a.Definition, b.Definition
			if x.Population != y.Population || !reflect.DeepEqual(x.Exclusions, y.Exclusions) || x.Denominator != y.Denominator || x.CountingUnit != y.CountingUnit || x.TimeBasis != y.TimeBasis {
				out.HasScopeAlternatives = true
			}
		}
	}
	if out.HasScopeAlternatives {
		out.NextStep = "Same-family scope alternatives are present. If the user has not specified the differing scope, present the choices and STOP before prepare or execution. Words such as total/sum do not mean include test accounts or choose the broadest population. If the request explicitly resolves the difference, select that matching definition. " + out.NextStep
	}
	return out
}
