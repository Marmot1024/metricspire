package application

import (
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/model"
)

const (
	BindingReady     = "ready"
	BindingAttention = "attention"
	BindingMissing   = "missing"

	MetricAdded   = "added"
	MetricChanged = "changed"
	MetricRemoved = "removed"
)

// GovernanceReview is a read model for the maintainer console. It contains no
// credentials and never changes a draft or release.
type GovernanceReview struct {
	CandidateManifest model.SemanticManifest `json:"candidate_manifest"`
	ActiveRelease     *GovernanceRelease     `json:"active_release,omitempty"`
	Binding           BindingReview          `json:"binding"`
	StructureChanged  bool                   `json:"structure_changed"`
	MetricChanges     []MetricChange         `json:"metric_changes"`
	PublicationIssues []string               `json:"publication_issues,omitempty"`
}

type GovernanceRelease struct {
	ID                  string                 `json:"id"`
	SourceRevision      int64                  `json:"source_revision"`
	ManifestFingerprint string                 `json:"manifest_fingerprint"`
	Manifest            model.SemanticManifest `json:"manifest"`
}

type BindingReview struct {
	Status   string               `json:"status"`
	Message  string               `json:"message"`
	Problems []string             `json:"problems,omitempty"`
	Binding  *model.SourceBinding `json:"binding,omitempty"`
}

type MetricChange struct {
	Code        string   `json:"code"`
	DisplayName string   `json:"display_name"`
	Kind        string   `json:"kind"`
	Fields      []string `json:"fields,omitempty"`
	Breaking    bool     `json:"breaking"`
	Dependents  []string `json:"dependents,omitempty"`
}

// ReviewGovernance compiles a candidate source, compares it with the active
// release, and checks whether the configured binding covers the semantic
// model. Publication remains authoritative and repeats its stronger checks.
func ReviewGovernance(source model.SemanticSource, active *catalog.Release, binding *model.SourceBinding) (GovernanceReview, error) {
	candidate, err := compiler.Compile(source)
	if err != nil {
		return GovernanceReview{}, err
	}
	review := GovernanceReview{
		CandidateManifest: candidate,
		Binding:           reviewBinding(candidate, binding),
		PublicationIssues: catalog.PublicationIssues(candidate),
	}
	if active == nil {
		review.StructureChanged = true
		for _, metric := range candidate.Definitions.Metrics {
			review.MetricChanges = append(review.MetricChanges, MetricChange{
				Code: metric.Name, DisplayName: metric.DisplayName, Kind: MetricAdded,
				Dependents: dependentMetrics(candidate.Definitions.Metrics, metric.Name),
			})
		}
		return review, nil
	}
	review.ActiveRelease = &GovernanceRelease{
		ID: active.ID, SourceRevision: active.SourceRevision,
		ManifestFingerprint: active.ManifestFingerprint, Manifest: active.Manifest,
	}
	review.StructureChanged = !sameModelStructure(active.Manifest.Definitions, candidate.Definitions)
	review.MetricChanges = compareMetrics(active.Manifest.Definitions.Metrics, candidate.Definitions.Metrics)
	return review, nil
}

func compareMetrics(active, candidate []model.Metric) []MetricChange {
	activeByCode := make(map[string]model.Metric, len(active))
	candidateByCode := make(map[string]model.Metric, len(candidate))
	codes := make(map[string]struct{}, len(active)+len(candidate))
	for _, metric := range active {
		activeByCode[metric.Name] = metric
		codes[metric.Name] = struct{}{}
	}
	for _, metric := range candidate {
		candidateByCode[metric.Name] = metric
		codes[metric.Name] = struct{}{}
	}
	orderedCodes := make([]string, 0, len(codes))
	for code := range codes {
		orderedCodes = append(orderedCodes, code)
	}
	sort.Strings(orderedCodes)

	changes := make([]MetricChange, 0)
	for _, code := range orderedCodes {
		before, existed := activeByCode[code]
		after, exists := candidateByCode[code]
		switch {
		case !existed:
			changes = append(changes, MetricChange{
				Code: code, DisplayName: after.DisplayName, Kind: MetricAdded,
				Dependents: dependentMetrics(candidate, code),
			})
		case !exists:
			changes = append(changes, MetricChange{
				Code: code, DisplayName: before.DisplayName, Kind: MetricRemoved, Breaking: true,
				Dependents: dependentMetrics(active, code),
			})
		default:
			fields, breaking := changedMetricFields(before, after)
			if len(fields) == 0 {
				continue
			}
			changes = append(changes, MetricChange{
				Code: code, DisplayName: after.DisplayName, Kind: MetricChanged,
				Fields: fields, Breaking: breaking, Dependents: dependentMetrics(candidate, code),
			})
		}
	}
	return changes
}

func changedMetricFields(before, after model.Metric) ([]string, bool) {
	fields := make([]string, 0, 10)
	add := func(name string, changed bool) {
		if changed {
			fields = append(fields, name)
		}
	}
	externalCodeChanged := before.ExternalCode != after.ExternalCode
	add("display_name", before.DisplayName != after.DisplayName)
	add("external_code", externalCodeChanged)
	add("description", before.Description != after.Description)
	add("owner", before.Owner != after.Owner)
	add("tags", !slices.Equal(before.Tags, after.Tags))
	add("usage_examples", !slices.Equal(before.UsageExamples, after.UsageExamples))
	add("deprecated", before.Deprecated != after.Deprecated)
	add("verification", !reflect.DeepEqual(before.Verification, after.Verification))

	breaking := before.ExternalCode != "" && externalCodeChanged
	addExecution := func(name string, changed bool) {
		add(name, changed)
		breaking = breaking || changed
	}
	addExecution("entity", before.Entity != after.Entity)
	addExecution("kind", before.Kind != after.Kind)
	addExecution("value_type", before.ValueType != after.ValueType)
	addExecution("unit", before.Unit != after.Unit)
	addExecution("expression", !reflect.DeepEqual(before.Expression, after.Expression))
	addExecution("allowed_dimensions", !slices.Equal(before.AllowedDimensions, after.AllowedDimensions))
	addExecution("time_dimension", before.TimeDimension != after.TimeDimension)
	if before.Deprecated && !after.Deprecated {
		breaking = true
	}
	return fields, breaking
}

func dependentMetrics(metrics []model.Metric, target string) []string {
	reverse := make(map[string][]string)
	for _, metric := range metrics {
		for _, dependency := range expressionMetricReferences(metric.Expression) {
			reverse[dependency] = append(reverse[dependency], metric.Name)
		}
	}
	seen := map[string]bool{target: true}
	queue := []string{target}
	result := make([]string, 0)
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, dependent := range reverse[current] {
			if seen[dependent] {
				continue
			}
			seen[dependent] = true
			result = append(result, dependent)
			queue = append(queue, dependent)
		}
	}
	sort.Strings(result)
	return result
}

func expressionMetricReferences(expression model.Expression) []string {
	values := make(map[string]struct{})
	var visit func(model.Expression)
	visit = func(current model.Expression) {
		if current.Op == model.OpMetric && strings.TrimSpace(current.Metric) != "" {
			values[current.Metric] = struct{}{}
		}
		for _, argument := range current.Args {
			visit(argument)
		}
	}
	visit(expression)
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sameModelStructure(before, after model.SemanticSpec) bool {
	return reflect.DeepEqual(before.Datasets, after.Datasets) &&
		reflect.DeepEqual(before.Entities, after.Entities) &&
		reflect.DeepEqual(before.Dimensions, after.Dimensions) &&
		reflect.DeepEqual(before.Relationships, after.Relationships)
}

func reviewBinding(manifest model.SemanticManifest, binding *model.SourceBinding) BindingReview {
	if binding == nil {
		return BindingReview{Status: BindingMissing, Message: "尚未配置数据源绑定。"}
	}
	copy := *binding
	result := BindingReview{Status: BindingReady, Message: "数据集和字段映射完整。", Binding: &copy}
	if binding.ManifestFingerprint != "" && binding.ManifestFingerprint != manifest.Fingerprint {
		result.Problems = append(result.Problems, "绑定指纹与当前候选指标定义不一致")
	}
	boundDatasets := make(map[string]model.DatasetBinding, len(binding.Datasets))
	for _, dataset := range binding.Datasets {
		boundDatasets[dataset.Name] = dataset
	}
	for _, dataset := range manifest.Definitions.Datasets {
		mapped, exists := boundDatasets[dataset.Name]
		if !exists {
			result.Problems = append(result.Problems, "缺少数据集映射："+dataset.Name)
			continue
		}
		boundFields := make(map[string]struct{}, len(mapped.Fields))
		for _, field := range mapped.Fields {
			if strings.TrimSpace(field.Column) != "" {
				boundFields[field.Name] = struct{}{}
			}
		}
		for _, field := range dataset.Fields {
			if _, exists := boundFields[field.Name]; !exists {
				result.Problems = append(result.Problems, "缺少字段映射："+dataset.Name+"."+field.Name)
			}
		}
	}
	if len(result.Problems) > 0 {
		result.Status = BindingAttention
		result.Message = "数据源绑定需要处理后才能稳定查询。"
	}
	return result
}
