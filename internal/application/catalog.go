package application

import (
	"context"
	"errors"
	"strings"

	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/policy"
)

type ActiveReleaseLister interface {
	ListActiveReleases(context.Context, string) ([]catalog.Release, error)
}

type MetricCatalogEntry struct {
	Namespace           string         `json:"namespace"`
	ModelName           string         `json:"model_name"`
	ReleaseID           string         `json:"release_id"`
	ManifestFingerprint string         `json:"manifest_fingerprint"`
	Name                string         `json:"name"`
	DisplayName         string         `json:"display_name"`
	Description         string         `json:"description"`
	Owner               string         `json:"owner"`
	Tags                []string       `json:"tags,omitempty"`
	Deprecated          bool           `json:"deprecated"`
	ValueType           model.DataType `json:"value_type"`
	Unit                string         `json:"unit,omitempty"`
	AllowedDimensions   []string       `json:"allowed_dimensions"`
}

type CatalogService struct {
	releases ActiveReleaseLister
	policies PolicyResolver
}

func NewCatalogService(releases ActiveReleaseLister, policies PolicyResolver) (*CatalogService, error) {
	if releases == nil || policies == nil {
		return nil, errors.New("active release lister and policy resolver are required")
	}
	return &CatalogService{releases: releases, policies: policies}, nil
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
		for _, metric := range release.Manifest.Definitions.Metrics {
			candidate := model.SemanticQuery{Metrics: []string{metric.Name}}
			if _, err := policy.Authorize(scope.Context, bundle, release.ManifestFingerprint, candidate); err != nil {
				var problem *model.Problem
				if errors.As(err, &problem) && problem.Code == "permission_denied" {
					continue
				}
				return nil, err
			}
			if !matchesMetricCatalogQuery(query, release.Name, metric) {
				continue
			}
			authorizedDimensions, err := catalogDimensions(scope.Context, bundle, release.ManifestFingerprint, metric)
			if err != nil {
				return nil, err
			}
			results = append(results, MetricCatalogEntry{
				Namespace: release.Namespace, ModelName: release.Name, ReleaseID: release.ID,
				ManifestFingerprint: release.ManifestFingerprint, Name: metric.Name,
				DisplayName: metric.DisplayName, Description: metric.Description, Owner: metric.Owner,
				Tags: append([]string(nil), metric.Tags...), Deprecated: metric.Deprecated,
				ValueType: metric.ValueType, Unit: metric.Unit,
				AllowedDimensions: authorizedDimensions,
			})
			if len(results) == limit {
				return results, nil
			}
		}
	}
	return results, nil
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

func matchesMetricCatalogQuery(query, modelName string, metric model.Metric) bool {
	if query == "" {
		return true
	}
	values := []string{modelName, metric.Name, metric.DisplayName, metric.Description, metric.Owner, strings.Join(metric.Tags, " ")}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}
