// Package compiler turns reviewed sources into immutable runtime artifacts.
package compiler

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/canonical"
	"github.com/marmot1024/metricspire/internal/model"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var decimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

type catalog struct {
	datasets      map[string]model.Dataset
	entities      map[string]model.Entity
	dimensions    map[string]model.Dimension
	relationships map[string]model.Relationship
	metrics       map[string]model.Metric
}

func Compile(source model.SemanticSource) (model.SemanticManifest, error) {
	if source.APIVersion != model.APIVersion {
		return model.SemanticManifest{}, problem("invalid_api_version", "api_version", "must be %q", model.APIVersion)
	}
	if source.Kind != model.KindSemanticModel {
		return model.SemanticManifest{}, problem("invalid_kind", "kind", "must be %q", model.KindSemanticModel)
	}
	if err := validateMetadata(source.Metadata, "metadata"); err != nil {
		return model.SemanticManifest{}, err
	}

	spec, err := cloneSpec(source.Spec)
	if err != nil {
		return model.SemanticManifest{}, err
	}
	normalizeSpec(&spec)
	index, err := validateSpec(spec)
	if err != nil {
		return model.SemanticManifest{}, err
	}
	if err := validateMetricCycles(index.metrics); err != nil {
		return model.SemanticManifest{}, err
	}

	payload := struct {
		APIVersion  string             `json:"api_version"`
		Kind        string             `json:"kind"`
		Metadata    model.Metadata     `json:"metadata"`
		Definitions model.SemanticSpec `json:"definitions"`
	}{model.APIVersion, model.KindSemanticManifest, source.Metadata, spec}
	fingerprint, err := canonical.Fingerprint(payload)
	if err != nil {
		return model.SemanticManifest{}, fmt.Errorf("fingerprint manifest: %w", err)
	}
	return model.SemanticManifest{
		APIVersion:  model.APIVersion,
		Kind:        model.KindSemanticManifest,
		Metadata:    source.Metadata,
		Fingerprint: fingerprint,
		Definitions: spec,
	}, nil
}

// VerifyManifest detects accidental or unauthorized mutation after compilation.
// Publication trust still comes from the deployment channel; the fingerprint is
// a stable content identity, not a digital signature.
func VerifyManifest(manifest model.SemanticManifest) error {
	if manifest.APIVersion != model.APIVersion || manifest.Kind != model.KindSemanticManifest || manifest.Fingerprint == "" {
		return problem("invalid_manifest", "manifest", "manifest is not a compiled MetricSpire artifact")
	}
	payload := struct {
		APIVersion  string             `json:"api_version"`
		Kind        string             `json:"kind"`
		Metadata    model.Metadata     `json:"metadata"`
		Definitions model.SemanticSpec `json:"definitions"`
	}{manifest.APIVersion, manifest.Kind, manifest.Metadata, manifest.Definitions}
	fingerprint, err := canonical.Fingerprint(payload)
	if err != nil {
		return fmt.Errorf("fingerprint manifest: %w", err)
	}
	if fingerprint != manifest.Fingerprint {
		return problem("fingerprint_mismatch", "manifest.fingerprint", "manifest content does not match fingerprint")
	}
	return nil
}

func CompilePolicy(source model.PolicySource, manifest model.SemanticManifest) (model.PolicyBundle, error) {
	if err := VerifyManifest(manifest); err != nil {
		return model.PolicyBundle{}, err
	}
	if source.APIVersion != model.APIVersion {
		return model.PolicyBundle{}, problem("invalid_api_version", "api_version", "must be %q", model.APIVersion)
	}
	if source.Kind != model.KindPolicySource {
		return model.PolicyBundle{}, problem("invalid_kind", "kind", "must be %q", model.KindPolicySource)
	}
	if err := validateMetadata(source.Metadata, "metadata"); err != nil {
		return model.PolicyBundle{}, err
	}
	if source.ManifestFingerprint != "" && source.ManifestFingerprint != manifest.Fingerprint {
		return model.PolicyBundle{}, problem("manifest_mismatch", "manifest_fingerprint", "does not match compiled manifest")
	}
	if strings.TrimSpace(source.Tenant) == "" {
		return model.PolicyBundle{}, problem("required", "tenant", "must not be empty")
	}

	rules := append([]model.PolicyRule(nil), source.Rules...)
	for i := range rules {
		rule := &rules[i]
		path := fmt.Sprintf("rules[%d]", i)
		if err := validateName(rule.Name, path+".name"); err != nil {
			return model.PolicyBundle{}, err
		}
		if rule.Effect != model.EffectAllow && rule.Effect != model.EffectDeny {
			return model.PolicyBundle{}, problem("invalid_policy", path+".effect", "must be allow or deny")
		}
		if len(rule.Principals) == 0 && len(rule.Roles) == 0 {
			return model.PolicyBundle{}, problem("invalid_policy", path, "must select at least one principal or role")
		}
		if len(rule.Metrics) == 0 {
			return model.PolicyBundle{}, problem("invalid_policy", path+".metrics", "must not be empty")
		}
		if len(rule.Dimensions) == 0 {
			return model.PolicyBundle{}, problem("invalid_policy", path+".dimensions", "must not be empty")
		}
		for _, metric := range rule.Metrics {
			if metric != "*" && !containsMetric(manifest, metric) {
				return model.PolicyBundle{}, problem("unknown_reference", path+".metrics", "metric %q does not exist", metric)
			}
		}
		for _, dimension := range rule.Dimensions {
			if dimension != "*" && !containsDimension(manifest, dimension) {
				return model.PolicyBundle{}, problem("unknown_reference", path+".dimensions", "dimension %q does not exist", dimension)
			}
		}
		var err error
		if rule.Principals, err = normalizeSet(rule.Principals, path+".principals"); err != nil {
			return model.PolicyBundle{}, err
		}
		if rule.Roles, err = normalizeSet(rule.Roles, path+".roles"); err != nil {
			return model.PolicyBundle{}, err
		}
		if rule.Metrics, err = normalizeSet(rule.Metrics, path+".metrics"); err != nil {
			return model.PolicyBundle{}, err
		}
		if rule.Dimensions, err = normalizeSet(rule.Dimensions, path+".dimensions"); err != nil {
			return model.PolicyBundle{}, err
		}
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Name < rules[j].Name })
	for i := 1; i < len(rules); i++ {
		if rules[i-1].Name == rules[i].Name {
			return model.PolicyBundle{}, problem("duplicate_name", "rules", "duplicate rule %q", rules[i].Name)
		}
	}

	payload := struct {
		APIVersion          string             `json:"api_version"`
		Kind                string             `json:"kind"`
		Metadata            model.Metadata     `json:"metadata"`
		ManifestFingerprint string             `json:"manifest_fingerprint"`
		Tenant              string             `json:"tenant"`
		Rules               []model.PolicyRule `json:"rules"`
	}{model.APIVersion, model.KindPolicyBundle, source.Metadata, manifest.Fingerprint, source.Tenant, rules}
	fingerprint, err := canonical.Fingerprint(payload)
	if err != nil {
		return model.PolicyBundle{}, fmt.Errorf("fingerprint policy: %w", err)
	}
	return model.PolicyBundle{
		APIVersion:          model.APIVersion,
		Kind:                model.KindPolicyBundle,
		Metadata:            source.Metadata,
		Fingerprint:         fingerprint,
		ManifestFingerprint: manifest.Fingerprint,
		Tenant:              source.Tenant,
		Rules:               rules,
	}, nil
}

func VerifyPolicy(bundle model.PolicyBundle, manifest model.SemanticManifest) error {
	if err := VerifyManifest(manifest); err != nil {
		return err
	}
	if bundle.APIVersion != model.APIVersion || bundle.Kind != model.KindPolicyBundle || bundle.Fingerprint == "" {
		return problem("invalid_policy", "policy", "policy is not a compiled MetricSpire bundle")
	}
	if bundle.ManifestFingerprint != manifest.Fingerprint {
		return problem("manifest_mismatch", "policy.manifest_fingerprint", "policy does not match manifest")
	}
	payload := struct {
		APIVersion          string             `json:"api_version"`
		Kind                string             `json:"kind"`
		Metadata            model.Metadata     `json:"metadata"`
		ManifestFingerprint string             `json:"manifest_fingerprint"`
		Tenant              string             `json:"tenant"`
		Rules               []model.PolicyRule `json:"rules"`
	}{bundle.APIVersion, bundle.Kind, bundle.Metadata, bundle.ManifestFingerprint, bundle.Tenant, bundle.Rules}
	fingerprint, err := canonical.Fingerprint(payload)
	if err != nil {
		return fmt.Errorf("fingerprint policy: %w", err)
	}
	if fingerprint != bundle.Fingerprint {
		return problem("fingerprint_mismatch", "policy.fingerprint", "policy content does not match fingerprint")
	}
	return nil
}

func cloneSpec(spec model.SemanticSpec) (model.SemanticSpec, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return model.SemanticSpec{}, fmt.Errorf("clone semantic model: %w", err)
	}
	var result model.SemanticSpec
	if err := json.Unmarshal(data, &result); err != nil {
		return model.SemanticSpec{}, fmt.Errorf("clone semantic model: %w", err)
	}
	return result, nil
}

func normalizeSpec(spec *model.SemanticSpec) {
	for i := range spec.Datasets {
		sort.Slice(spec.Datasets[i].Fields, func(a, b int) bool {
			return spec.Datasets[i].Fields[a].Name < spec.Datasets[i].Fields[b].Name
		})
	}
	for i := range spec.Dimensions {
		slices.Sort(spec.Dimensions[i].TimeGranularities)
	}
	for i := range spec.Metrics {
		slices.Sort(spec.Metrics[i].Tags)
		slices.Sort(spec.Metrics[i].AllowedDimensions)
		slices.Sort(spec.Metrics[i].Verification.Evidence)
		slices.Sort(spec.Metrics[i].Verification.OpenQuestions)
		normalizeExpression(&spec.Metrics[i].Expression)
	}
	sort.Slice(spec.Datasets, func(i, j int) bool { return spec.Datasets[i].Name < spec.Datasets[j].Name })
	sort.Slice(spec.Entities, func(i, j int) bool { return spec.Entities[i].Name < spec.Entities[j].Name })
	sort.Slice(spec.Dimensions, func(i, j int) bool { return spec.Dimensions[i].Name < spec.Dimensions[j].Name })
	sort.Slice(spec.Relationships, func(i, j int) bool { return spec.Relationships[i].Name < spec.Relationships[j].Name })
	sort.Slice(spec.Metrics, func(i, j int) bool { return spec.Metrics[i].Name < spec.Metrics[j].Name })
}

func validateSpec(spec model.SemanticSpec) (catalog, error) {
	result := catalog{
		datasets:      make(map[string]model.Dataset),
		entities:      make(map[string]model.Entity),
		dimensions:    make(map[string]model.Dimension),
		relationships: make(map[string]model.Relationship),
		metrics:       make(map[string]model.Metric),
	}
	if len(spec.Datasets) == 0 || len(spec.Entities) == 0 || len(spec.Metrics) == 0 {
		return result, problem("required", "spec", "datasets, entities, and metrics must not be empty")
	}
	for i, dataset := range spec.Datasets {
		path := fmt.Sprintf("spec.datasets[%d]", i)
		if err := validateName(dataset.Name, path+".name"); err != nil {
			return result, err
		}
		if _, exists := result.datasets[dataset.Name]; exists {
			return result, problem("duplicate_name", path+".name", "dataset %q is duplicated", dataset.Name)
		}
		if len(dataset.Fields) == 0 {
			return result, problem("required", path+".fields", "must not be empty")
		}
		fields := make(map[string]struct{})
		for fieldIndex, field := range dataset.Fields {
			fieldPath := fmt.Sprintf("%s.fields[%d]", path, fieldIndex)
			if err := validateName(field.Name, fieldPath+".name"); err != nil {
				return result, err
			}
			if _, exists := fields[field.Name]; exists {
				return result, problem("duplicate_name", fieldPath+".name", "field %q is duplicated", field.Name)
			}
			if !validDataType(field.DataType) {
				return result, problem("invalid_type", fieldPath+".data_type", "unsupported data type %q", field.DataType)
			}
			fields[field.Name] = struct{}{}
		}
		result.datasets[dataset.Name] = dataset
	}

	for i, entity := range spec.Entities {
		path := fmt.Sprintf("spec.entities[%d]", i)
		if err := validateName(entity.Name, path+".name"); err != nil {
			return result, err
		}
		if _, exists := result.entities[entity.Name]; exists {
			return result, problem("duplicate_name", path+".name", "entity %q is duplicated", entity.Name)
		}
		dataset, exists := result.datasets[entity.Dataset]
		if !exists {
			return result, problem("unknown_reference", path+".dataset", "dataset %q does not exist", entity.Dataset)
		}
		if !datasetHasField(dataset, entity.Key) {
			return result, problem("unknown_reference", path+".key", "field %q does not exist in dataset %q", entity.Key, entity.Dataset)
		}
		result.entities[entity.Name] = entity
	}

	for i, dimension := range spec.Dimensions {
		path := fmt.Sprintf("spec.dimensions[%d]", i)
		if err := validateName(dimension.Name, path+".name"); err != nil {
			return result, err
		}
		if _, exists := result.dimensions[dimension.Name]; exists {
			return result, problem("duplicate_name", path+".name", "dimension %q is duplicated", dimension.Name)
		}
		entity, exists := result.entities[dimension.Entity]
		if !exists {
			return result, problem("unknown_reference", path+".entity", "entity %q does not exist", dimension.Entity)
		}
		field, exists := lookupField(result.datasets[entity.Dataset], dimension.Field)
		if !exists {
			return result, problem("unknown_reference", path+".field", "field %q does not exist for entity %q", dimension.Field, dimension.Entity)
		}
		switch dimension.Type {
		case model.DimensionCategorical:
			if len(dimension.TimeGranularities) != 0 {
				return result, problem("invalid_dimension", path+".time_granularities", "categorical dimensions cannot declare time granularities")
			}
		case model.DimensionTime:
			if field.DataType != model.DataTypeDate && field.DataType != model.DataTypeTimestamp {
				return result, problem("invalid_dimension", path+".field", "time dimension field must be date or timestamp")
			}
			if len(dimension.TimeGranularities) == 0 {
				return result, problem("required", path+".time_granularities", "time dimension must declare supported granularities")
			}
			if err := validateGranularities(dimension.TimeGranularities, path+".time_granularities"); err != nil {
				return result, err
			}
		default:
			return result, problem("invalid_dimension", path+".type", "unsupported dimension type %q", dimension.Type)
		}
		result.dimensions[dimension.Name] = dimension
	}

	for i, relationship := range spec.Relationships {
		path := fmt.Sprintf("spec.relationships[%d]", i)
		if err := validateName(relationship.Name, path+".name"); err != nil {
			return result, err
		}
		if _, exists := result.relationships[relationship.Name]; exists {
			return result, problem("duplicate_name", path+".name", "relationship %q is duplicated", relationship.Name)
		}
		from, fromExists := result.entities[relationship.FromEntity]
		to, toExists := result.entities[relationship.ToEntity]
		if !fromExists || !toExists {
			return result, problem("unknown_reference", path, "relationship entities must exist")
		}
		if relationship.FromEntity == relationship.ToEntity {
			return result, problem("invalid_relationship", path, "self relationships are not supported in v0.1")
		}
		if relationship.Cardinality != model.CardinalityManyToOne {
			return result, problem("unsafe_cardinality", path+".cardinality", "v0.1 only supports many_to_one")
		}
		if !datasetHasField(result.datasets[from.Dataset], relationship.FromField) {
			return result, problem("unknown_reference", path+".from_field", "field %q does not exist", relationship.FromField)
		}
		if !datasetHasField(result.datasets[to.Dataset], relationship.ToField) {
			return result, problem("unknown_reference", path+".to_field", "field %q does not exist", relationship.ToField)
		}
		result.relationships[relationship.Name] = relationship
	}

	for i, metric := range spec.Metrics {
		path := fmt.Sprintf("spec.metrics[%d]", i)
		if err := validateName(metric.Name, path+".name"); err != nil {
			return result, err
		}
		if _, exists := result.metrics[metric.Name]; exists {
			return result, problem("duplicate_name", path+".name", "metric %q is duplicated", metric.Name)
		}
		if _, exists := result.entities[metric.Entity]; !exists {
			return result, problem("unknown_reference", path+".entity", "entity %q does not exist", metric.Entity)
		}
		if metric.Kind != model.MetricAggregate && metric.Kind != model.MetricRatio && metric.Kind != model.MetricDerived {
			return result, problem("invalid_metric", path+".kind", "unsupported metric kind %q", metric.Kind)
		}
		if metric.ValueType != model.DataTypeInteger && metric.ValueType != model.DataTypeDecimal {
			return result, problem("invalid_metric", path+".value_type", "metric value type must be integer or decimal")
		}
		if metric.Verification.Status != model.VerificationUnverified && metric.Verification.Status != model.VerificationVerified {
			return result, problem("invalid_metric", path+".verification.status", "must be unverified or verified")
		}
		if err := rejectDuplicates(metric.Tags, path+".tags"); err != nil {
			return result, err
		}
		for tagIndex, tag := range metric.Tags {
			if strings.TrimSpace(tag) == "" {
				return result, problem("invalid_metric", fmt.Sprintf("%s.tags[%d]", path, tagIndex), "tag must not be empty")
			}
		}
		if err := rejectDuplicates(metric.AllowedDimensions, path+".allowed_dimensions"); err != nil {
			return result, err
		}
		result.metrics[metric.Name] = metric
	}

	for i, metric := range spec.Metrics {
		path := fmt.Sprintf("spec.metrics[%d]", i)
		for _, dimensionName := range metric.AllowedDimensions {
			dimension, exists := result.dimensions[dimensionName]
			if !exists {
				return result, problem("unknown_reference", path+".allowed_dimensions", "dimension %q does not exist", dimensionName)
			}
			if !entityReachable(metric.Entity, dimension.Entity, result.relationships) {
				return result, problem("unsafe_join", path+".allowed_dimensions", "dimension %q is not reachable through many_to_one relationships", dimensionName)
			}
		}
		if metric.TimeDimension != "" {
			dimension, exists := result.dimensions[metric.TimeDimension]
			if !exists || dimension.Type != model.DimensionTime {
				return result, problem("invalid_time", path+".time_dimension", "%q must reference a time dimension", metric.TimeDimension)
			}
			if !entityReachable(metric.Entity, dimension.Entity, result.relationships) {
				return result, problem("unsafe_join", path+".time_dimension", "time dimension is not reachable from metric entity")
			}
		}
		dependencies := make(map[string]struct{})
		if err := validateExpression(metric.Expression, metric, result, dependencies, path+".expression"); err != nil {
			return result, err
		}
		expressionType, err := inferExpressionType(metric.Expression, result)
		if err != nil {
			return result, problem("invalid_expression", path+".expression", "%v", err)
		}
		if expressionType != metric.ValueType {
			return result, problem(
				"invalid_metric", path+".value_type",
				"declares %q but expression produces %q", metric.ValueType, expressionType,
			)
		}
		switch metric.Kind {
		case model.MetricAggregate:
			if len(dependencies) != 0 {
				return result, problem("invalid_metric", path+".expression", "aggregate metrics cannot reference other metrics")
			}
			if !expressionContainsAggregate(metric.Expression) {
				return result, problem("invalid_metric", path+".expression", "aggregate metrics must contain a supported aggregate")
			}
		case model.MetricRatio:
			if metric.Expression.Op != model.OpDivide || len(dependencies) != 0 {
				return result, problem("invalid_metric", path+".expression", "ratio metrics must divide field-based expressions")
			}
			if !expressionContainsAggregate(metric.Expression.Args[0]) || !expressionContainsAggregate(metric.Expression.Args[1]) {
				return result, problem("invalid_metric", path+".expression", "ratio numerator and denominator must each contain an aggregate")
			}
		case model.MetricDerived:
			if len(dependencies) == 0 {
				return result, problem("invalid_metric", path+".expression", "derived metrics must reference at least one metric")
			}
			for dependencyName := range dependencies {
				dependency := result.metrics[dependencyName]
				for _, dimension := range metric.AllowedDimensions {
					if !slices.Contains(dependency.AllowedDimensions, dimension) {
						return result, problem("invalid_metric", path+".allowed_dimensions", "dimension %q is not valid for dependency %q", dimension, dependencyName)
					}
				}
				if metric.TimeDimension != dependency.TimeDimension {
					return result, problem("invalid_time", path+".time_dimension", "derived metric and dependency %q must use the same time dimension", dependencyName)
				}
			}
		}
	}
	return result, nil
}

func inferExpressionType(expression model.Expression, index catalog) (model.DataType, error) {
	switch expression.Op {
	case model.OpCount, model.OpCountDistinct:
		return model.DataTypeInteger, nil
	case model.OpAverage, model.OpDivide, model.OpLiteral:
		return model.DataTypeDecimal, nil
	case model.OpSum, model.OpMinimum, model.OpMaximum:
		entityName, fieldName, _ := strings.Cut(expression.Field, ".")
		entity, exists := index.entities[entityName]
		if !exists {
			return "", fmt.Errorf("expression field entity %q does not exist", entityName)
		}
		field, exists := lookupField(index.datasets[entity.Dataset], fieldName)
		if !exists {
			return "", fmt.Errorf("expression field %q does not exist", expression.Field)
		}
		return field.DataType, nil
	case model.OpMetric:
		metric, exists := index.metrics[expression.Metric]
		if !exists {
			return "", fmt.Errorf("metric %q does not exist", expression.Metric)
		}
		return metric.ValueType, nil
	case model.OpAdd, model.OpSubtract, model.OpMultiply:
		left, err := inferExpressionType(expression.Args[0], index)
		if err != nil {
			return "", err
		}
		right, err := inferExpressionType(expression.Args[1], index)
		if err != nil {
			return "", err
		}
		if left == model.DataTypeDecimal || right == model.DataTypeDecimal {
			return model.DataTypeDecimal, nil
		}
		return model.DataTypeInteger, nil
	default:
		return "", fmt.Errorf("unsupported operation %q", expression.Op)
	}
}

func expressionContainsAggregate(expression model.Expression) bool {
	switch expression.Op {
	case model.OpSum, model.OpCount, model.OpCountDistinct, model.OpAverage, model.OpMinimum, model.OpMaximum:
		return true
	}
	for _, argument := range expression.Args {
		if expressionContainsAggregate(argument) {
			return true
		}
	}
	return false
}

func validateExpression(expression model.Expression, metric model.Metric, index catalog, dependencies map[string]struct{}, path string) error {
	if expression.Op == "" {
		return problem("required", path+".op", "must not be empty")
	}
	switch expression.Op {
	case model.OpSum, model.OpCount, model.OpCountDistinct, model.OpAverage, model.OpMinimum, model.OpMaximum:
		if expression.Field == "" || expression.Metric != "" || expression.Value != "" || len(expression.Args) != 0 {
			return problem("invalid_expression", path, "%s requires a field and optional constrained filters", expression.Op)
		}
		entityName, fieldName, ok := strings.Cut(expression.Field, ".")
		if !ok || entityName != metric.Entity {
			return problem("invalid_expression", path+".field", "aggregate field must be qualified by metric entity %q", metric.Entity)
		}
		entity := index.entities[entityName]
		field, exists := lookupField(index.datasets[entity.Dataset], fieldName)
		if !exists {
			return problem("invalid_expression", path+".field", "aggregate field must exist")
		}
		if expression.Op != model.OpCount && expression.Op != model.OpCountDistinct && field.DataType != model.DataTypeInteger && field.DataType != model.DataTypeDecimal {
			return problem("invalid_expression", path+".field", "%s field must be numeric", expression.Op)
		}
		if (expression.Op == model.OpCount || expression.Op == model.OpCountDistinct) && metric.ValueType != model.DataTypeInteger {
			return problem("invalid_metric", path, "%s metric value_type must be integer", expression.Op)
		}
		if expression.Op == model.OpAverage && metric.ValueType != model.DataTypeDecimal {
			return problem("invalid_metric", path, "avg metric value_type must be decimal")
		}
		if err := validateMetricFilters(expression.Filters, metric, index, path+".filters"); err != nil {
			return err
		}
	case model.OpMetric:
		if expression.Metric == "" || expression.Field != "" || expression.Value != "" || len(expression.Args) != 0 || len(expression.Filters) != 0 {
			return problem("invalid_expression", path, "metric reference requires only metric")
		}
		dependency, exists := index.metrics[expression.Metric]
		if !exists {
			return problem("unknown_reference", path+".metric", "metric %q does not exist", expression.Metric)
		}
		if dependency.Entity != metric.Entity {
			return problem("invalid_expression", path+".metric", "cross-entity metric references are not supported in v0.1")
		}
		dependencies[expression.Metric] = struct{}{}
	case model.OpLiteral:
		if expression.Value == "" || expression.Field != "" || expression.Metric != "" || len(expression.Args) != 0 || len(expression.Filters) != 0 {
			return problem("invalid_expression", path, "literal requires only a decimal string value")
		}
		if !decimalPattern.MatchString(expression.Value) {
			return problem("invalid_expression", path+".value", "must be an exact decimal string")
		}
	case model.OpAdd, model.OpSubtract, model.OpMultiply, model.OpDivide:
		if expression.Field != "" || expression.Metric != "" || expression.Value != "" || len(expression.Args) != 2 || len(expression.Filters) != 0 {
			return problem("invalid_expression", path, "%s requires exactly two args", expression.Op)
		}
		for i, argument := range expression.Args {
			if err := validateExpression(argument, metric, index, dependencies, fmt.Sprintf("%s.args[%d]", path, i)); err != nil {
				return err
			}
		}
	default:
		return problem("invalid_expression", path+".op", "unsupported operation %q", expression.Op)
	}
	return nil
}

func normalizeExpression(expression *model.Expression) {
	for i := range expression.Args {
		normalizeExpression(&expression.Args[i])
	}
	for i := range expression.Filters {
		slices.Sort(expression.Filters[i].Values)
	}
	sort.Slice(expression.Filters, func(i, j int) bool {
		if expression.Filters[i].Field != expression.Filters[j].Field {
			return expression.Filters[i].Field < expression.Filters[j].Field
		}
		return expression.Filters[i].Operator < expression.Filters[j].Operator
	})
}

func validateMetricFilters(filters []model.MetricFilter, metric model.Metric, index catalog, path string) error {
	seen := make(map[string]struct{}, len(filters))
	for i, filter := range filters {
		filterPath := fmt.Sprintf("%s[%d]", path, i)
		entityName, fieldName, ok := strings.Cut(filter.Field, ".")
		if !ok || entityName != metric.Entity {
			return problem("invalid_expression", filterPath+".field", "filter field must be qualified by metric entity %q", metric.Entity)
		}
		entity := index.entities[entityName]
		field, exists := lookupField(index.datasets[entity.Dataset], fieldName)
		if !exists {
			return problem("unknown_reference", filterPath+".field", "field %q does not exist", filter.Field)
		}
		key := filter.Field + "\x00" + string(filter.Operator)
		if _, exists := seen[key]; exists {
			return problem("duplicate_value", filterPath, "filter %s %s is duplicated", filter.Field, filter.Operator)
		}
		seen[key] = struct{}{}
		switch filter.Operator {
		case model.MetricFilterEqual, model.MetricFilterNotEqual:
			if len(filter.Values) != 1 {
				return problem("invalid_expression", filterPath+".values", "%s requires one value", filter.Operator)
			}
		case model.MetricFilterIn, model.MetricFilterNotIn:
			if len(filter.Values) == 0 {
				return problem("invalid_expression", filterPath+".values", "%s requires at least one value", filter.Operator)
			}
		case model.MetricFilterIsNull, model.MetricFilterIsNotNull:
			if len(filter.Values) != 0 {
				return problem("invalid_expression", filterPath+".values", "%s does not accept values", filter.Operator)
			}
		default:
			return problem("invalid_expression", filterPath+".operator", "unsupported filter operator %q", filter.Operator)
		}
		if err := rejectDuplicates(filter.Values, filterPath+".values"); err != nil {
			return err
		}
		for valueIndex, value := range filter.Values {
			if err := validateScalar(value, field.DataType); err != nil {
				return problem("invalid_expression", fmt.Sprintf("%s.values[%d]", filterPath, valueIndex), "%v", err)
			}
		}
	}
	return nil
}

func validateScalar(value string, dataType model.DataType) error {
	switch dataType {
	case model.DataTypeString:
		if strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("string contains a control character")
		}
	case model.DataTypeInteger:
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return fmt.Errorf("%q is not an integer", value)
		}
	case model.DataTypeDecimal:
		if !decimalPattern.MatchString(value) {
			return fmt.Errorf("%q is not a decimal", value)
		}
	case model.DataTypeBoolean:
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("%q is not a boolean", value)
		}
	case model.DataTypeDate:
		if _, err := time.Parse(time.DateOnly, value); err != nil {
			return fmt.Errorf("%q is not an ISO date", value)
		}
	case model.DataTypeTimestamp:
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
			return fmt.Errorf("%q is not an RFC3339 timestamp", value)
		}
	default:
		return fmt.Errorf("unsupported data type %q", dataType)
	}
	return nil
}

func validateMetricCycles(metrics map[string]model.Metric) error {
	const (
		unseen = iota
		visiting
		done
	)
	state := make(map[string]int, len(metrics))
	stack := make([]string, 0, len(metrics))
	var visit func(string) error
	visit = func(name string) error {
		switch state[name] {
		case visiting:
			return problem("metric_cycle", "spec.metrics", "cycle detected: %s -> %s", strings.Join(stack, " -> "), name)
		case done:
			return nil
		}
		state[name] = visiting
		stack = append(stack, name)
		for _, dependency := range metricDependencies(metrics[name].Expression) {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = done
		return nil
	}
	names := make([]string, 0, len(metrics))
	for name := range metrics {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

func metricDependencies(expression model.Expression) []string {
	set := make(map[string]struct{})
	var walk func(model.Expression)
	walk = func(current model.Expression) {
		if current.Op == model.OpMetric {
			set[current.Metric] = struct{}{}
		}
		for _, argument := range current.Args {
			walk(argument)
		}
	}
	walk(expression)
	result := make([]string, 0, len(set))
	for name := range set {
		result = append(result, name)
	}
	slices.Sort(result)
	return result
}

func validateMetadata(metadata model.Metadata, path string) error {
	if err := validateName(metadata.Name, path+".name"); err != nil {
		return err
	}
	if strings.TrimSpace(metadata.Version) == "" {
		return problem("required", path+".version", "must not be empty")
	}
	return nil
}

func validateName(name, path string) error {
	if !namePattern.MatchString(name) {
		return problem("invalid_name", path, "%q must match %s", name, namePattern)
	}
	return nil
}

func validDataType(value model.DataType) bool {
	switch value {
	case model.DataTypeString, model.DataTypeInteger, model.DataTypeDecimal, model.DataTypeBoolean, model.DataTypeDate, model.DataTypeTimestamp:
		return true
	default:
		return false
	}
}

func validateGranularities(values []model.TimeGranularity, path string) error {
	for _, value := range values {
		if value != model.GrainDay && value != model.GrainWeek && value != model.GrainMonth {
			return problem("invalid_time", path, "unsupported granularity %q", value)
		}
	}
	return rejectDuplicates(values, path)
}

func datasetHasField(dataset model.Dataset, name string) bool {
	_, exists := lookupField(dataset, name)
	return exists
}

func lookupField(dataset model.Dataset, name string) (model.Field, bool) {
	for _, field := range dataset.Fields {
		if field.Name == name {
			return field, true
		}
	}
	return model.Field{}, false
}

func entityReachable(from, to string, relationships map[string]model.Relationship) bool {
	if from == to {
		return true
	}
	visited := map[string]bool{from: true}
	queue := []string{from}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		next := make([]string, 0)
		for _, relationship := range relationships {
			if relationship.FromEntity == current && !visited[relationship.ToEntity] {
				next = append(next, relationship.ToEntity)
			}
		}
		slices.Sort(next)
		for _, entity := range next {
			if entity == to {
				return true
			}
			visited[entity] = true
			queue = append(queue, entity)
		}
	}
	return false
}

func normalizeSet[T ~string](values []T, path string) ([]T, error) {
	result := append([]T(nil), values...)
	slices.Sort(result)
	if err := rejectDuplicates(result, path); err != nil {
		return nil, err
	}
	return result, nil
}

func rejectDuplicates[T comparable](values []T, path string) error {
	seen := make(map[T]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return problem("duplicate_value", path, "contains duplicate value %v", value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func containsMetric(manifest model.SemanticManifest, name string) bool {
	for _, metric := range manifest.Definitions.Metrics {
		if metric.Name == name {
			return true
		}
	}
	return false
}

func containsDimension(manifest model.SemanticManifest, name string) bool {
	for _, dimension := range manifest.Definitions.Dimensions {
		if dimension.Name == name {
			return true
		}
	}
	return false
}

func problem(code, path, format string, arguments ...any) error {
	return &model.Problem{Code: code, Path: path, Message: fmt.Sprintf(format, arguments...)}
}
