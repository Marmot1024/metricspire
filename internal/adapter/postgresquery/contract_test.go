package postgresquery

import (
	"path/filepath"
	"testing"

	"github.com/marmot1024/metricspire/internal/canonical"
	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
)

func TestDataContractIgnoresPresentationButRetainsCalculationAndBinding(t *testing.T) {
	fixture := func() (model.SemanticSource, model.SourceBinding) {
		var source model.SemanticSource
		var binding model.SourceBinding
		for name, target := range map[string]any{"model.yaml": &source, "binding.yaml": &binding} {
			if err := contractio.ReadFile(filepath.Join("..", "..", "..", "examples", "online", name), target); err != nil {
				t.Fatal(err)
			}
		}
		return source, binding
	}
	fingerprint := func(source model.SemanticSource, binding model.SourceBinding) string {
		manifest, err := compiler.Compile(source)
		if err != nil {
			t.Fatal(err)
		}
		before, _ := canonical.Marshal(manifest)
		bindingBefore, _ := canonical.Marshal(binding)
		value, err := DataContractFingerprint(manifest, binding)
		if err != nil {
			t.Fatal(err)
		}
		after, _ := canonical.Marshal(manifest)
		bindingAfter, _ := canonical.Marshal(binding)
		if string(before) != string(after) || string(bindingBefore) != string(bindingAfter) {
			t.Fatal("trusted definitions mutated")
		}
		return value
	}
	source, binding := fixture()
	want := fingerprint(source, binding)
	for _, test := range []struct {
		name       string
		compatible bool
		change     func(*model.SemanticSource, *model.SourceBinding)
	}{
		{"wording", true, func(s *model.SemanticSource, b *model.SourceBinding) {
			s.Metadata.Version = "1.0.1"
			s.Spec.Datasets[0].Description = "new wording"
			s.Spec.Dimensions[0].Description = "new wording"
			s.Spec.Metrics[0].DisplayName = "new label"
			s.Spec.Metrics[0].Description = "new wording"
			s.Spec.Metrics[0].Owner = "another-owner"
			s.Spec.Metrics[0].Tags = []string{"another-tag"}
			s.Spec.Metrics[0].UsageExamples = []string{"another-example"}
			s.Spec.Metrics[0].Verification.Evidence = []string{"another-evidence"}
			b.Metadata.Version = "1.0.1"
		}},
		{"field order", true, func(s *model.SemanticSource, b *model.SourceBinding) {
			b.Datasets[0].Fields[0], b.Datasets[0].Fields[1] = b.Datasets[0].Fields[1], b.Datasets[0].Fields[0]
		}},
		{"unit", false, func(s *model.SemanticSource, b *model.SourceBinding) { s.Spec.Metrics[0].Unit = "another-unit" }},
		{"code", false, func(s *model.SemanticSource, b *model.SourceBinding) { s.Spec.Metrics[0].ExternalCode = "99999" }},
		{"formula", false, func(s *model.SemanticSource, b *model.SourceBinding) {
			s.Spec.Metrics[2].Expression.Args[0], s.Spec.Metrics[2].Expression.Args[1] = s.Spec.Metrics[2].Expression.Args[1], s.Spec.Metrics[2].Expression.Args[0]
		}},
		{"column", false, func(s *model.SemanticSource, b *model.SourceBinding) {
			b.Datasets[0].Fields[0].Column = "different_column"
		}},
		{"source", false, func(s *model.SemanticSource, b *model.SourceBinding) { b.Datasets[0].Resource.Table = "another_table" }},
		{"calendar", false, func(s *model.SemanticSource, b *model.SourceBinding) {
			b.Datasets[0].Fields[1].CalendarTimezone = "Asia/Shanghai"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, b := fixture()
			test.change(&s, &b)
			if got := fingerprint(s, b); (got == want) != test.compatible {
				t.Fatalf("compatibility = %v, want %v", got == want, test.compatible)
			}
		})
	}
	manifest, _ := compiler.Compile(source)
	manifest.Definitions.Metrics[0].Description = "tampered"
	if _, err := DataContractFingerprint(manifest, binding); err == nil {
		t.Fatal("tampered manifest accepted")
	}
}
