package mcpbridge

import (
	"reflect"
	"strings"
	"testing"

	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/model"
)

func TestMetricComparisonKeepsPopulationVariantsExplicit(t *testing.T) {
	a := MetricDetail{MetricSummary: MetricSummary{Name: "orders_all", Description: "Orders including test accounts", Unit: "orders", VerificationStatus: model.VerificationUnverified, ReleaseChannel: catalog.ReleaseChannelTrial}, AllowedDimensions: []string{"date", "region"}}
	b := a
	b.Name = "orders_production"
	b.Description = "Orders excluding test accounts"
	b.AllowedDimensions = []string{"region", "date"}
	got := compareMetrics([]MetricDetail{a, b})
	if !reflect.DeepEqual(got.DifferingFields, []string{"description"}) {
		t.Fatalf("lost population difference or treated ordering as meaning: %+v", got)
	}
	if a.AllowedDimensions[0] != "date" {
		t.Fatal("comparison mutated input")
	}
	b.Description = ""
	got = compareMetrics([]MetricDetail{a, b})
	if !reflect.DeepEqual(got.MissingDefinitions, []string{"orders_production"}) {
		t.Fatal("missing definition not surfaced")
	}
	if !strings.Contains(got.NextStep, "Role/history ranking cannot change scope") {
		t.Fatal("ranking/meaning boundary missing")
	}
}

func TestSearchDoesNotInferUniquenessOrAbsence(t *testing.T) {
	if got := searchSelection(0, 5); !strings.Contains(got.NextStep, "confirm") || got.AtLimit {
		t.Fatalf("empty retrieval interpreted as absence: %+v", got)
	}
	if got := searchSelection(1, 1); !got.AtLimit || !strings.Contains(got.NextStep, "Rank is not semantic fit") {
		t.Fatalf("limited result interpreted as unique semantic match: %+v", got)
	}
}
