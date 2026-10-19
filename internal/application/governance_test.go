package application_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/model"
	"go.yaml.in/yaml/v3"
)

func TestGovernanceReviewShowsBindingChangesAndDependents(t *testing.T) {
	source := readGovernanceContract[model.SemanticSource](t, "model.yaml")
	binding := readGovernanceContract[model.SourceBinding](t, "binding.json")
	binding.ManifestFingerprint = ""
	manifest, err := compiler.Compile(source)
	if err != nil {
		t.Fatal(err)
	}
	active := &catalog.Release{ID: "release-1", SourceRevision: 1, ManifestFingerprint: manifest.Fingerprint, Manifest: manifest}

	for index := range source.Spec.Metrics {
		if source.Spec.Metrics[index].Name == "gross_revenue" {
			source.Spec.Metrics[index].Description = "Reviewed business wording."
		}
	}
	review, err := application.ReviewGovernance(source, active, &binding)
	if err != nil {
		t.Fatal(err)
	}
	if review.Binding.Status != application.BindingReady || review.ActiveRelease == nil || review.ActiveRelease.ID != active.ID {
		t.Fatalf("review status = %#v", review)
	}
	if len(review.MetricChanges) != 1 || review.MetricChanges[0].Code != "gross_revenue" ||
		len(review.MetricChanges[0].Dependents) != 1 || review.MetricChanges[0].Dependents[0] != "average_order_value" ||
		review.MetricChanges[0].Breaking {
		t.Fatalf("metric changes = %#v", review.MetricChanges)
	}
}

func TestGovernanceReviewFlagsBreakingAndIncompleteBinding(t *testing.T) {
	source := readGovernanceContract[model.SemanticSource](t, "model.yaml")
	binding := readGovernanceContract[model.SourceBinding](t, "binding.json")
	manifest, err := compiler.Compile(source)
	if err != nil {
		t.Fatal(err)
	}
	active := &catalog.Release{ID: "release-1", SourceRevision: 1, ManifestFingerprint: manifest.Fingerprint, Manifest: manifest}
	for index := range source.Spec.Metrics {
		if source.Spec.Metrics[index].Name == "gross_revenue" {
			source.Spec.Metrics[index].Unit = "usd"
		}
	}
	binding.Datasets[0].Fields = binding.Datasets[0].Fields[1:]
	review, err := application.ReviewGovernance(source, active, &binding)
	if err != nil {
		t.Fatal(err)
	}
	if review.Binding.Status != application.BindingAttention || len(review.Binding.Problems) == 0 {
		t.Fatalf("binding review = %#v", review.Binding)
	}
	if len(review.MetricChanges) != 1 || !review.MetricChanges[0].Breaking {
		t.Fatalf("metric changes = %#v", review.MetricChanges)
	}
}

func TestGovernanceReviewReportsPublicationIssuesBeforePublish(t *testing.T) {
	source := readGovernanceContract[model.SemanticSource](t, "model.yaml")
	source.Spec.Metrics[0].Verification.Status = model.VerificationUnverified
	source.Spec.Metrics[0].Verification.Evidence = nil
	review, err := application.ReviewGovernance(source, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(review.PublicationIssues) != 2 || review.Binding.Status != application.BindingMissing {
		t.Fatalf("publication review = %#v", review)
	}
}

func TestGovernanceReviewTreatsAssignedExternalCodeAsStable(t *testing.T) {
	for _, test := range []struct {
		name       string
		before     string
		after      string
		isBreaking bool
	}{
		{name: "first assignment", before: "", after: "1001", isBreaking: false},
		{name: "renumber", before: "1001", after: "1002", isBreaking: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := readGovernanceContract[model.SemanticSource](t, "model.yaml")
			source.Spec.Metrics[0].ExternalCode = test.before
			manifest, err := compiler.Compile(source)
			if err != nil {
				t.Fatal(err)
			}
			active := &catalog.Release{ID: "release-1", SourceRevision: 1, ManifestFingerprint: manifest.Fingerprint, Manifest: manifest}
			source.Spec.Metrics[0].ExternalCode = test.after
			review, err := application.ReviewGovernance(source, active, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(review.MetricChanges) != 1 || review.MetricChanges[0].Breaking != test.isBreaking {
				t.Fatalf("metric changes = %#v", review.MetricChanges)
			}
		})
	}
}

func readGovernanceContract[T any](t *testing.T, name string) T {
	t.Helper()
	path := filepath.Join("..", "..", "examples", "orders", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value T
	if filepath.Ext(path) == ".json" {
		err = json.Unmarshal(data, &value)
	} else {
		err = yaml.Unmarshal(data, &value)
	}
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestGovernanceReviewIncludesDefinitionAddChangeAndRemoval(t *testing.T) {
	for _, operation := range []string{"add", "change", "remove"} {
		t.Run(operation, func(t *testing.T) {
			source := readGovernanceContract[model.SemanticSource](t, "model.yaml")
			define := func() {
				for i := range source.Spec.Metrics {
					source.Spec.Metrics[i].Definition = &model.MetricDefinition{Family: "orders", Variant: source.Spec.Metrics[i].Name, Population: "Synthetic orders", Exclusions: []string{}, Numerator: "Reviewed aggregate", Denominator: "Reviewed formula", CountingUnit: "Order", TimeBasis: "Creation date"}
				}
			}
			if operation != "add" {
				define()
			}
			manifest, err := compiler.Compile(source)
			if err != nil {
				t.Fatal(err)
			}
			active := &catalog.Release{Manifest: manifest, ManifestFingerprint: manifest.Fingerprint}
			switch operation {
			case "add":
				define()
			case "change":
				source.Spec.Metrics[0].Definition.Population = "Different population"
			case "remove":
				for i := range source.Spec.Metrics {
					source.Spec.Metrics[i].Definition = nil
				}
			}
			review, err := application.ReviewGovernance(source, active, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(review.MetricChanges) == 0 {
				t.Fatal("definition-only change hidden from review")
			}
			for _, c := range review.MetricChanges {
				if len(c.Fields) != 1 || c.Fields[0] != "definition" || c.Breaking != (operation != "add") {
					t.Fatalf("wrong definition review: %+v", c)
				}
			}
		})
	}
}
