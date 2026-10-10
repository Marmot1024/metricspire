package postgresquery

import (
	"encoding/json"
	"sort"

	"github.com/marmot1024/metricspire/internal/canonical"
	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/model"
)

// DataContractFingerprint is stored by the aggregate producer on each batch.
// Descriptive releases can reuse the same data; calculation, grain, code, type
// and physical source changes cannot. It is deliberately conservative at model
// scope, not a per-query cache key or a replacement for immutable release IDs.
func DataContractFingerprint(manifest model.SemanticManifest, binding model.SourceBinding) (string, error) {
	if err := compiler.VerifyManifest(manifest); err != nil {
		return "", err
	}
	if binding.Engine != EngineName || binding.APIVersion != model.APIVersion || binding.Kind != model.KindSourceBinding || len(binding.Datasets) != 1 {
		return "", reject("unsupported_online_query", "online data contract requires one PostgreSQL binding")
	}
	// Clone before removing presentation metadata: releases are immutable and
	// may be used concurrently by other requests.
	encoded, err := json.Marshal(struct {
		Definitions model.SemanticSpec
		Binding     model.SourceBinding
	}{manifest.Definitions, binding})
	if err != nil {
		return "", err
	}
	var value struct {
		Definitions model.SemanticSpec
		Binding     model.SourceBinding
	}
	if err := json.Unmarshal(encoded, &value); err != nil {
		return "", err
	}
	for i := range value.Definitions.Datasets {
		value.Definitions.Datasets[i].Description = ""
	}
	for i := range value.Definitions.Dimensions {
		value.Definitions.Dimensions[i].Description = ""
	}
	for i := range value.Definitions.Metrics {
		m := &value.Definitions.Metrics[i]
		m.DisplayName, m.Description, m.Owner = "", "", ""
		m.Tags, m.UsageExamples = nil, nil
		m.Deprecated = false
		m.Verification = model.Verification{}
	}
	value.Binding.Metadata = model.Metadata{}
	value.Binding.ManifestFingerprint = ""
	for i := range value.Binding.Datasets {
		fields := value.Binding.Datasets[i].Fields
		sort.Slice(fields, func(a, b int) bool { return fields[a].Name < fields[b].Name })
	}
	return canonical.Fingerprint(struct {
		Version     string              `json:"version"`
		Model       string              `json:"model"`
		Definitions model.SemanticSpec  `json:"definitions"`
		Binding     model.SourceBinding `json:"binding"`
	}{"postgres-online-data-v1", manifest.Metadata.Name, value.Definitions, value.Binding})
}
