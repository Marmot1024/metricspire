package planner

import "github.com/marmot1024/metricspire/internal/model"

// Selections are equality guards, never release selectors or authorization.
// Called only after authorization; no hidden candidate metadata is returned.
func validateMetricSelections(manifest model.SemanticManifest, query model.SemanticQuery, index manifestIndex) error {
	outputs := make(map[string]bool, len(query.Metrics))
	for _, name := range query.Metrics {
		outputs[name] = true
	}
	for name, fingerprint := range query.MetricSelections {
		if !outputs[name] || fingerprint != manifest.Fingerprint {
			return problem("metric_selection_changed", "metric_selections", "selected metric or definition version differs; review authorized metric details and choose again")
		}
	}
	for _, name := range query.Metrics {
		if metric, ok := index.metrics[name]; ok && metric.Definition != nil && query.MetricSelections[name] == "" {
			return problem("metric_selection_required", "metric_selections", "review structured population, exclusions, denominator, counting unit and time basis, then explicitly select each requested definition version")
		}
	}
	return nil
}
