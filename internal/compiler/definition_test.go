package compiler_test

import (
	"testing"

	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/model"
)

func syntheticDefinition(variant string) *model.MetricDefinition {
	return &model.MetricDefinition{Family: "orders", Variant: variant, Population: "All synthetic orders", Exclusions: []string{}, Numerator: "Reviewed synthetic aggregate", Denominator: "Not applicable unless a ratio", CountingUnit: "Order", TimeBasis: "Order creation date"}
}

func TestStructuredDefinitionsAreValidatedAndVersioned(t *testing.T) {
	source := loadSource(t)
	for i := range source.Spec.Metrics {
		source.Spec.Metrics[i].Definition = syntheticDefinition(source.Spec.Metrics[i].Name)
	}
	before, err := compiler.Compile(source)
	if err != nil {
		t.Fatal(err)
	}
	changed := clone(t, source)
	changed.Spec.Metrics[0].Definition.Exclusions = []string{"Synthetic test orders"}
	after, err := compiler.Compile(changed)
	if err != nil {
		t.Fatal(err)
	}
	if before.Fingerprint == after.Fingerprint {
		t.Fatal("definition change did not change version")
	}
	for _, tc := range []struct {
		name   string
		change func(*model.SemanticSource)
	}{
		{"missing population", func(s *model.SemanticSource) { s.Spec.Metrics[0].Definition.Population = "" }},
		{"implicit exclusions", func(s *model.SemanticSource) { s.Spec.Metrics[0].Definition.Exclusions = nil }},
		{"duplicate variant", func(s *model.SemanticSource) {
			s.Spec.Metrics[1].Definition = syntheticDefinition(s.Spec.Metrics[0].Name)
		}},
		{"derived bypass", func(s *model.SemanticSource) {
			for i := range s.Spec.Metrics {
				if s.Spec.Metrics[i].Kind == model.MetricDerived {
					s.Spec.Metrics[i].Definition = nil
				}
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := clone(t, source)
			tc.change(&candidate)
			if _, err := compiler.Compile(candidate); err == nil {
				t.Fatal("invalid definition accepted")
			}
		})
	}
}
