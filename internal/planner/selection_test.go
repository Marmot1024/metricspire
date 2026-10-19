package planner_test

import (
	"errors"
	"testing"

	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/planner"
)

func TestMetricSelectionGuardsMeaningAfterAuthorization(t *testing.T) {
	v := loadFixtureWithSource(t, func(s *model.SemanticSource) {
		for i := range s.Spec.Metrics {
			s.Spec.Metrics[i].Definition = &model.MetricDefinition{Family: "orders", Variant: s.Spec.Metrics[i].Name, Population: "Synthetic orders", Exclusions: []string{}, Numerator: "Reviewed aggregate", Denominator: "Not applicable", CountingUnit: "Order", TimeBasis: "Creation date"}
		}
	})
	check := func(q model.SemanticQuery, rc model.RequestContext, want string) {
		t.Helper()
		_, err := planner.BuildLogical(v.manifest, v.bundle, rc, q)
		if want == "" {
			if err != nil {
				t.Fatal(err)
			}
			return
		}
		var p *model.Problem
		if !errors.As(err, &p) || p.Code != want {
			t.Fatalf("error=%v want=%s", err, want)
		}
	}
	check(v.query, v.context, "metric_selection_required")
	denied := v.context
	denied.Roles = nil
	check(v.query, denied, "permission_denied")
	v.query.MetricSelections = map[string]string{}
	for _, name := range v.query.Metrics {
		v.query.MetricSelections[name] = v.manifest.Fingerprint
	}
	check(v.query, v.context, "")
	v.query.MetricSelections[v.query.Metrics[0]] = "sha256:stale"
	check(v.query, v.context, "metric_selection_changed")
	v.query.MetricSelections[v.query.Metrics[0]] = v.manifest.Fingerprint
	v.query.MetricSelections["unrequested"] = v.manifest.Fingerprint
	check(v.query, v.context, "metric_selection_changed")
	delete(v.query.MetricSelections, "unrequested")
	delete(v.query.MetricSelections, v.query.Metrics[0])
	check(v.query, v.context, "metric_selection_required")
}
