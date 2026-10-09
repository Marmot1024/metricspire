package application

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/executionauth"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/planner"
)

var numericMetricCode = regexp.MustCompile(`^[0-9]+$`)

// OnlineQuery is deliberately separate from the existing name-based
// SemanticQuery contract. Identity, release and physical sources are trusted.
type OnlineQuery struct {
	MetricCodes  []string            `json:"metric_codes"`
	Dimensions   []string            `json:"dimensions,omitempty"`
	TimeRange    *model.TimeRange    `json:"time_range"`
	TimeGrouping *model.TimeGrouping `json:"time_grouping,omitempty"`
	Filters      []model.Filter      `json:"filters,omitempty"`
	OrderBy      []model.OrderBy     `json:"order_by,omitempty"`
	Limit        int                 `json:"limit,omitempty"`
}

type OnlineResult struct {
	ReleaseID           string              `json:"release_id"`
	ManifestFingerprint string              `json:"manifest_fingerprint"`
	Snapshot            *model.DataSnapshot `json:"data_snapshot"`
	Result              *model.TypedResult  `json:"result"`
}

type OnlineService struct {
	releases   ActiveReleaseLister
	queries    *QueryService
	tenant     string
	namespaces map[string]struct{}
	slots      chan struct{}
	timeout    time.Duration
}

func NewOnlineService(releases ActiveReleaseLister, queries *QueryService, tenant string, namespaces []string, concurrency int, timeout time.Duration) (*OnlineService, error) {
	if releases == nil || queries == nil || tenant == "" || len(namespaces) == 0 || concurrency < 1 || concurrency > 32 || timeout <= 0 || timeout > 10*time.Second {
		return nil, errors.New("online queries require a reviewed tenant, namespaces, bounded concurrency and timeout")
	}
	return &OnlineService{releases: releases, queries: queries, tenant: tenant, namespaces: namespaceSet(namespaces), slots: make(chan struct{}, concurrency), timeout: timeout}, nil
}

func (s *OnlineService) Execute(ctx context.Context, scope QueryScope, request OnlineQuery) (OnlineResult, error) {
	if scope.Context.Tenant != s.tenant || scope.Context.Principal == "" || scope.Context.RequestID == "" {
		return OnlineResult{}, &model.Problem{Code: "permission_denied", Message: "online scope is not authorized"}
	}
	if _, ok := s.namespaces[scope.Namespace]; !ok {
		return OnlineResult{}, &model.Problem{Code: "online_not_enabled", Message: "online queries are not enabled for this namespace"}
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return OnlineResult{}, &model.Problem{Code: "online_overloaded", Message: "online concurrency limit reached"}
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	// Online PostgreSQL reads never forward a Databricks execution token.
	ctx = executionauth.WithoutAccessToken(ctx)
	if len(request.MetricCodes) == 0 || len(request.MetricCodes) > planner.MaximumMetrics {
		return OnlineResult{}, &model.Problem{Code: "invalid_query", Path: "metric_codes", Message: "request between 1 and 32 numeric metric codes"}
	}
	seen := map[string]bool{}
	for _, code := range request.MetricCodes {
		if !numericMetricCode.MatchString(code) || seen[code] {
			return OnlineResult{}, &model.Problem{Code: "invalid_query", Path: "metric_codes", Message: "metric codes must be distinct numeric strings"}
		}
		seen[code] = true
	}
	if request.TimeRange == nil || request.Limit < 0 || request.Limit > 1000 {
		return OnlineResult{}, &model.Problem{Code: "invalid_query", Message: "online queries require a time range and a limit of at most 1000"}
	}
	releases, err := s.releases.ListActiveReleases(ctx, scope.Namespace)
	if err != nil {
		return OnlineResult{}, err
	}
	type reference struct {
		release catalog.Release
		name    string
	}
	index := map[string][]reference{}
	for _, release := range releases {
		// Online serving is certified-only. Trial datasets stay on analysis paths.
		if catalog.IsTrialRelease(release) {
			continue
		}
		for _, metric := range release.Manifest.Definitions.Metrics {
			if seen[metric.ExternalCode] {
				index[metric.ExternalCode] = append(index[metric.ExternalCode], reference{release, metric.Name})
			}
		}
	}
	var release catalog.Release
	names := make([]string, len(request.MetricCodes))
	codes := map[string]string{}
	for i, code := range request.MetricCodes {
		refs := index[code]
		if len(refs) != 1 {
			return OnlineResult{}, &model.Problem{Code: "metric_route_not_found", Path: "metric_codes", Message: "codes must resolve uniquely to one certified active model"}
		}
		if i > 0 && (release.Name != refs[0].release.Name || release.ID != refs[0].release.ID) {
			return OnlineResult{}, &model.Problem{Code: "cross_model_query", Path: "metric_codes", Message: "online output metrics must share one active model"}
		}
		release = refs[0].release
		names[i] = refs[0].name
		codes[names[i]] = code
	}
	scope.ModelName = release.Name
	limit := request.Limit
	if limit == 0 {
		limit = 1000
	}
	query := model.SemanticQuery{APIVersion: model.APIVersion, Kind: model.KindSemanticQuery, Metrics: names, GroupBy: request.Dimensions, TimeRange: request.TimeRange, TimeGrouping: request.TimeGrouping, Filters: request.Filters, Limit: limit, OrderBy: append([]model.OrderBy(nil), request.OrderBy...)}
	for i := range query.OrderBy {
		for name, code := range codes {
			if query.OrderBy[i].Field == code {
				query.OrderBy[i].Field = name
			}
		}
	}
	input := QueryInput{QueryScope: scope, Query: query}
	policySource, err := s.queries.policies.ResolvePolicy(ctx, scope, release)
	if err != nil {
		return OnlineResult{}, err
	}
	explained, err := explainRelease(input, release, policySource, false)
	if err != nil {
		return OnlineResult{}, err
	}
	// Resolve and execute the same immutable release; no second active-pointer read.
	planned, err := s.queries.planExplained(ctx, input, explained)
	if err != nil {
		return OnlineResult{}, err
	}
	if planned.Physical.TimeRange == nil {
		return OnlineResult{}, &model.Problem{Code: "invalid_query", Message: "online query requires a bounded time range"}
	}
	start, _ := time.Parse(time.RFC3339Nano, planned.Physical.TimeRange.Start)
	end, _ := time.Parse(time.RFC3339Nano, planned.Physical.TimeRange.End)
	if end.Sub(start) > 31*24*time.Hour {
		return OnlineResult{}, &model.Problem{Code: "budget_exceeded", Path: "time_range", Message: "online time range must not exceed 31 days"}
	}
	output, err := s.queries.executePlanned(ctx, ctx, input, planned)
	if err != nil {
		return OnlineResult{}, err
	}
	if output.Execution.Result == nil || output.Execution.DataSnapshot == nil {
		return OnlineResult{}, &model.Problem{Code: "online_data_unavailable", Message: "online engine did not provide a complete snapshot"}
	}
	result := *output.Execution.Result
	result.Columns = append([]model.ResultColumn(nil), result.Columns...)
	for i := range result.Columns {
		if code, ok := codes[result.Columns[i].Name]; ok {
			result.Columns[i].Name = code
		}
	}
	return OnlineResult{ReleaseID: release.ID, ManifestFingerprint: release.ManifestFingerprint, Snapshot: output.Execution.DataSnapshot, Result: &result}, nil
}
