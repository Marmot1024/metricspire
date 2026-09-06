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
