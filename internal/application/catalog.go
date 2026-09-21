package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/planner"
	"github.com/marmot1024/metricspire/internal/policy"
)

type ActiveReleaseLister interface {
	ListActiveReleases(context.Context, string) ([]catalog.Release, error)
}

// ResolveActiveModel keeps the public query boundary metric-first. It returns
// one internal model only when every requested metric exists in, and is
// authorized from, exactly one active release. Callers never choose the model.
func (service *CatalogService) ResolveActiveModel(ctx context.Context, scope QueryScope, metrics []string) (string, error) {
	if strings.TrimSpace(scope.Namespace) == "" || strings.TrimSpace(scope.Context.Tenant) == "" || strings.TrimSpace(scope.Context.Principal) == "" {
		return "", errors.New("metric route scope is incomplete")
	}
	if len(metrics) == 0 {
		return "", &model.Problem{Code: "invalid_query", Path: "metrics", Message: "at least one metric is required"}
	}
	if len(metrics) > planner.MaximumMetrics {
		return "", &model.Problem{Code: "budget_exceeded", Path: "metrics", Message: fmt.Sprintf("must contain at most %d metrics", planner.MaximumMetrics)}
	}
	releases, err := service.releases.ListActiveReleases(ctx, scope.Namespace)
	if err != nil {
		return "", err
	}
	candidates := make([]string, 0, 1)
	for _, release := range releases {
		if catalog.IsTrialRelease(release) && !service.trialNamespaceEnabled(scope.Namespace) {
			continue
		}
		available := make(map[string]struct{}, len(release.Manifest.Definitions.Metrics))
		for _, metric := range release.Manifest.Definitions.Metrics {
			available[metric.Name] = struct{}{}
		}
		containsAll := true
		for _, metric := range metrics {
			if _, exists := available[metric]; !exists {
				containsAll = false
				break
			}
		}
		if !containsAll {
			continue
		}
		modelScope := scope
		modelScope.ModelName = release.Name
		policySource, err := service.policies.ResolvePolicy(ctx, modelScope, release)
		if errors.Is(err, ErrResolutionNotFound) {
			continue
		}
		if err != nil {
			return "", err
		}
		bundle, err := compiler.CompilePolicy(policySource, release.Manifest)
		if err != nil {
			return "", err
		}
		if _, err := policy.Authorize(scope.Context, bundle, release.ManifestFingerprint, model.SemanticQuery{Metrics: metrics}); err != nil {
			var problem *model.Problem
			if errors.As(err, &problem) && problem.Code == "permission_denied" {
				continue
			}
			return "", err
		}
		candidates = append(candidates, release.Name)
	}
	switch len(candidates) {
	case 1:
		return candidates[0], nil
	case 0:
		return "", &model.Problem{Code: "metric_route_not_found", Path: "metrics", Message: "the requested metrics do not resolve to one authorized active model"}
	default:
		return "", &model.Problem{Code: "metric_route_ambiguous", Path: "metrics", Message: fmt.Sprintf("the requested metrics resolve to %d active models; metric codes must be unique within a namespace", len(candidates))}
	}
}

type MetricCatalogEntry struct {
	Namespace           string                   `json:"namespace"`
	ModelName           string                   `json:"semantic_model_name"`
	ReleaseID           string                   `json:"release_id"`
	ReleaseChannel      catalog.ReleaseChannel   `json:"release_channel"`
	ManifestFingerprint string                   `json:"manifest_fingerprint"`
	Name                string                   `json:"name"`
	ExternalCode        string                   `json:"external_code,omitempty"`
	DisplayName         string                   `json:"display_name"`
	Description         string                   `json:"description"`
	Owner               string                   `json:"owner"`
	VerificationStatus  model.VerificationStatus `json:"verification_status"`
	Tags                []string                 `json:"tags,omitempty"`
	UsageExamples       []string                 `json:"usage_examples,omitempty"`
	Deprecated          bool                     `json:"deprecated"`
	Entity              string                   `json:"entity"`
	Kind                model.MetricKind         `json:"metric_kind"`
	ValueType           model.DataType           `json:"value_type"`
	Unit                string                   `json:"unit,omitempty"`
	Expression          model.Expression         `json:"expression"`
	AllowedDimensions   []string                 `json:"allowed_dimensions"`
	DimensionDetails    []MetricDimensionEntry   `json:"dimension_details,omitempty"`
	TimeDimension       string                   `json:"time_dimension,omitempty"`
	TimeGranularities   []model.TimeGranularity  `json:"time_granularities,omitempty"`
}

type MetricDimensionEntry struct {
	Name              string                  `json:"name"`
	Description       string                  `json:"description,omitempty"`
	Type              model.DimensionType     `json:"type"`
	DataType          model.DataType          `json:"data_type"`
	TimeGranularities []model.TimeGranularity `json:"time_granularities,omitempty"`
	CalendarTimezone  string                  `json:"calendar_timezone,omitempty"`
}

type CatalogService struct {
	releases        ActiveReleaseLister
	policies        PolicyResolver
	bindings        BindingResolver
	trialNamespaces map[string]struct{}
}

type CatalogServiceOption func(*CatalogService)

func WithTrialCatalogReleaseNamespaces(namespaces ...string) CatalogServiceOption {
	return func(service *CatalogService) {
		service.trialNamespaces = namespaceSet(namespaces)
	}
}

func NewCatalogService(releases ActiveReleaseLister, policies PolicyResolver, bindings BindingResolver, options ...CatalogServiceOption) (*CatalogService, error) {
	if releases == nil || policies == nil || bindings == nil {
		return nil, errors.New("active release lister, policy resolver, and binding resolver are required")
	}
	service := &CatalogService{releases: releases, policies: policies, bindings: bindings}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service, nil
}

func namespaceSet(namespaces []string) map[string]struct{} {
	result := make(map[string]struct{}, len(namespaces))
	for _, namespace := range namespaces {
		if namespace = strings.TrimSpace(namespace); namespace != "" {
			result[namespace] = struct{}{}
		}
	}
	return result
}

func (service *CatalogService) trialNamespaceEnabled(namespace string) bool {
	_, enabled := service.trialNamespaces[namespace]
	return enabled
}

// SearchActive returns only metrics in active releases that the trusted
// principal is authorized to query. Physical bindings are never exposed.
func (service *CatalogService) SearchActive(ctx context.Context, scope QueryScope, search string, limit int) ([]MetricCatalogEntry, error) {
	if strings.TrimSpace(scope.Namespace) == "" || strings.TrimSpace(scope.Context.Tenant) == "" || strings.TrimSpace(scope.Context.Principal) == "" {
		return nil, errors.New("catalog search scope is incomplete")
	}
	if limit < 1 || limit > 100 {
		return nil, errors.New("catalog search limit must be between 1 and 100")
	}
	releases, err := service.releases.ListActiveReleases(ctx, scope.Namespace)
	if err != nil {
		return nil, err
	}
	query := strings.ToLower(strings.TrimSpace(search))
	results := make([]MetricCatalogEntry, 0)
	for _, release := range releases {
		if catalog.IsTrialRelease(release) && !service.trialNamespaceEnabled(scope.Namespace) {
			continue
		}
		modelScope := scope
		modelScope.ModelName = release.Name
		policySource, err := service.policies.ResolvePolicy(ctx, modelScope, release)
		if errors.Is(err, ErrResolutionNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		bundle, err := compiler.CompilePolicy(policySource, release.Manifest)
		if err != nil {
			return nil, err
		}
		resolvedBinding, err := service.bindings.ResolveBinding(ctx, modelScope, release)
		if err != nil {
			return nil, err
		}
		for _, metric := range release.Manifest.Definitions.Metrics {
			candidate := model.SemanticQuery{Metrics: []string{metric.Name}}
			if _, err := policy.Authorize(scope.Context, bundle, release.ManifestFingerprint, candidate); err != nil {
				var problem *model.Problem
				if errors.As(err, &problem) && problem.Code == "permission_denied" {
					continue
				}
				return nil, err
			}
			if !matchesMetricCatalogQuery(query, metric) {
				continue
			}
			authorizedDimensions, err := catalogDimensions(scope.Context, bundle, release.ManifestFingerprint, metric)
			if err != nil {
				return nil, err
			}
			dimensionDetails := catalogDimensionDetails(release.Manifest, resolvedBinding, authorizedDimensions)
			var granularities []model.TimeGranularity
			for _, dimension := range release.Manifest.Definitions.Dimensions {
				if dimension.Name == metric.TimeDimension {
					granularities = append([]model.TimeGranularity(nil), dimension.TimeGranularities...)
					break
				}
			}
			results = append(results, MetricCatalogEntry{
				Namespace: release.Namespace, ModelName: release.Name, ReleaseID: release.ID,
				ReleaseChannel: release.Channel, ManifestFingerprint: release.ManifestFingerprint, Name: metric.Name,
				ExternalCode: metric.ExternalCode,
				DisplayName:  metric.DisplayName, Description: metric.Description, Owner: metric.Owner,
				VerificationStatus: metric.Verification.Status,
				Tags:               append([]string(nil), metric.Tags...), UsageExamples: append([]string(nil), metric.UsageExamples...), Deprecated: metric.Deprecated,
				Entity: metric.Entity, Kind: metric.Kind, ValueType: metric.ValueType, Unit: metric.Unit,
				Expression:        metric.Expression,
				AllowedDimensions: authorizedDimensions, DimensionDetails: dimensionDetails, TimeDimension: metric.TimeDimension,
				TimeGranularities: granularities,
			})
			if len(results) == limit {
				return results, nil
			}
		}
	}
	return results, nil
}

func catalogDimensionDetails(manifest model.SemanticManifest, binding model.SourceBinding, names []string) []MetricDimensionEntry {
	dimensions := make(map[string]model.Dimension, len(manifest.Definitions.Dimensions))
	entities := make(map[string]model.Entity, len(manifest.Definitions.Entities))
	datasets := make(map[string]model.Dataset, len(manifest.Definitions.Datasets))
	for _, dimension := range manifest.Definitions.Dimensions {
		dimensions[dimension.Name] = dimension
	}
	for _, entity := range manifest.Definitions.Entities {
		entities[entity.Name] = entity
	}
	for _, dataset := range manifest.Definitions.Datasets {
		datasets[dataset.Name] = dataset
	}
	result := make([]MetricDimensionEntry, 0, len(names))
	for _, name := range names {
		dimension := dimensions[name]
		entity := entities[dimension.Entity]
		dataType := model.DataType("")
		for _, field := range datasets[entity.Dataset].Fields {
			if field.Name == dimension.Field {
				dataType = field.DataType
				break
			}
		}
		entry := MetricDimensionEntry{
			Name: name, Description: dimension.Description, Type: dimension.Type, DataType: dataType,
			TimeGranularities: append([]model.TimeGranularity(nil), dimension.TimeGranularities...),
		}
		for _, dataset := range binding.Datasets {
			if dataset.Name != entity.Dataset {
				continue
			}
			for _, field := range dataset.Fields {
				if field.Name == dimension.Field {
					entry.CalendarTimezone = field.CalendarTimezone
					break
				}
			}
		}
		result = append(result, entry)
	}
	return result
}

func catalogDimensions(context model.RequestContext, bundle model.PolicyBundle, manifestFingerprint string, metric model.Metric) ([]string, error) {
	result := make([]string, 0, len(metric.AllowedDimensions))
	for _, dimension := range metric.AllowedDimensions {
		candidate := model.SemanticQuery{Metrics: []string{metric.Name}, GroupBy: []string{dimension}}
		if _, err := policy.Authorize(context, bundle, manifestFingerprint, candidate); err != nil {
			var problem *model.Problem
			if errors.As(err, &problem) && problem.Code == "permission_denied" {
				continue
			}
			return nil, err
		}
		result = append(result, dimension)
	}
	return result, nil
}

func matchesMetricCatalogQuery(query string, metric model.Metric) bool {
	if query == "" {
		return true
	}
	values := []string{metric.ExternalCode, metric.Name, metric.DisplayName, metric.Description, metric.Owner, strings.Join(metric.Tags, " ")}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}
