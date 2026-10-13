package mcpbridge

import (
	"context"
	"errors"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// FastBackend is optional: compatibility bridges keep the legacy tool set.
type FastBackend interface {
	GetMetricDetails(context.Context, MetricDetailsInput) ([]MetricDetail, error)
	DiscoverQueryRoutes(context.Context, AnalysisDiscoveryInput) (QueryRoutes, error)
	PrepareQuery(context.Context, QueryInput) (PreparedQuery, error)
	RunQuery(context.Context, RunQueryInput) (RunQueryOutput, error)
}

type MetricSummary struct {
	Name               string                   `json:"name"`
	ExternalCode       string                   `json:"external_code,omitempty"`
	DisplayName        string                   `json:"display_name"`
	Description        string                   `json:"description"`
	ValueType          model.DataType           `json:"value_type"`
	Unit               string                   `json:"unit,omitempty"`
	VerificationStatus model.VerificationStatus `json:"verification_status"`
	ReleaseChannel     catalog.ReleaseChannel   `json:"release_channel"`
	Deprecated         bool                     `json:"deprecated"`
	TimeDimension      string                   `json:"time_dimension,omitempty"`
}

type MetricDetail struct {
	MetricSummary
	ReleaseID           string                             `json:"release_id"`
	ManifestFingerprint string                             `json:"manifest_fingerprint"`
	AllowedDimensions   []string                           `json:"allowed_dimensions"`
	DimensionDetails    []application.MetricDimensionEntry `json:"dimension_details"`
}

func summarizeMetric(m application.MetricCatalogEntry) MetricSummary {
	return MetricSummary{Name: m.Name, ExternalCode: m.ExternalCode, DisplayName: m.DisplayName, Description: m.Description, ValueType: m.ValueType, Unit: m.Unit, VerificationStatus: m.VerificationStatus, ReleaseChannel: m.ReleaseChannel, Deprecated: m.Deprecated, TimeDimension: m.TimeDimension}
}

func DetailMetric(m application.MetricCatalogEntry) MetricDetail {
	return MetricDetail{summarizeMetric(m), m.ReleaseID, m.ManifestFingerprint, m.AllowedDimensions, m.DimensionDetails}
}

type MetricDetailsInput struct {
	Namespace string   `json:"namespace"`
	Metrics   []string `json:"metrics" jsonschema:"1 to 20 exact metric names from search_metrics; not external codes or physical names"`
}

type QueryRoute struct {
	Available bool     `json:"available"`
	Workflow  []string `json:"workflow"`
}

type QueryRoutes struct {
	Namespace        string     `json:"namespace"`
	PublishedMetrics QueryRoute `json:"published_metrics"`
	Exploration      QueryRoute `json:"exploration"`
	SelectionRules   []string   `json:"selection_rules"`
}

type PreparedQuery struct {
	Namespace    string                      `json:"namespace"`
	Query        model.SemanticQuery         `json:"query"`
	ExpectedPlan application.PlanExpectation `json:"expected_plan"`
	Metrics      []MetricSummary             `json:"metrics"`
	Engine       string                      `json:"engine"`
	ElapsedMS    int64                       `json:"elapsed_ms"`
	NextStep     string                      `json:"next_step"`
}

type RunQueryInput struct {
	Namespace    string                      `json:"namespace"`
	Query        model.SemanticQuery         `json:"query" jsonschema:"The normalized query returned by prepare_query"`
	ExpectedPlan application.PlanExpectation `json:"expected_plan" jsonschema:"Copy all four fingerprints/IDs from prepare_query; equality guards only, never authority to select a release"`
	WaitSeconds  int                         `json:"wait_seconds,omitempty" jsonschema:"Server waits up to 0-15 seconds; omitted or 0 uses 10 seconds. Pending work keeps its job ID; never resubmit it."`
}

type RunQueryOutput struct {
	application.QueryJobSnapshot
	Metrics   []MetricSummary `json:"metrics"`
	NextStep  string          `json:"next_step"`
	ElapsedMS int64           `json:"elapsed_ms"`
	WaitError string          `json:"wait_error,omitempty"`
}

func addFastTools(s *mcp.Server, b FastBackend) {
	add(s, "get_metric_details", "Get definitions and authorized dimension types, grains and calendar timezones for 1-20 selected exact metric names in one call. No query, expressions or physical mappings. Use before prepare_query.", true, func(ctx context.Context, in MetricDetailsInput) (any, error) {
		if _, err := namespacePath(in.Namespace); err != nil {
			return nil, err
		}
		if len(in.Metrics) < 1 || len(in.Metrics) > 20 {
			return nil, errors.New("metrics must contain 1 to 20 exact names")
		}
		seen := make(map[string]bool)
		for _, name := range in.Metrics {
			if name == "" || seen[name] {
				return nil, errors.New("metric names must be nonempty and unique")
			}
			seen[name] = true
		}
		out, err := b.GetMetricDetails(ctx, in)
		return map[string]any{"metrics": out}, err
	})
	add(s, "discover_query_routes", "Discover this user's available query categories for a namespace. Choose published_metrics when its maintained definition and grain answer the question; choose exploration for a requested temporary formula/cohort/counting grain only after discovering approved source semantics. No automatic fallback on empty search, permission or timeout. No data query.", true, func(ctx context.Context, in AnalysisDiscoveryInput) (any, error) {
		if _, err := namespacePath(in.Namespace); err != nil {
			return nil, err
		}
		return b.DiscoverQueryRoutes(ctx, in)
	})
	add(s, "prepare_query", "Preferred published-metric preflight: validate and return business definitions, normalized query and execution fingerprints in one call. No data query. Review meaning/time/grain, then pass query and expected_plan unchanged to run_query. No separate explain_query or plan_query needed.", true, func(ctx context.Context, in QueryInput) (any, error) {
		if _, err := namespacePath(in.Namespace); err != nil {
			return nil, err
		}
		return b.PrepareQuery(ctx, in)
	})
	add(s, "run_query", "Execute the prepared published-metric query once and wait briefly for its result. Requires user-authorized query scope. Current authorization and all fingerprints are rechecked before engine execution. Return results or the same pending job; use get_query/cancel_query, never run_query again for that job. No exploration fallback; costs may apply.", false, func(ctx context.Context, in RunQueryInput) (any, error) {
		if _, err := namespacePath(in.Namespace); err != nil {
			return nil, err
		}
		if in.WaitSeconds < 0 || in.WaitSeconds > 15 {
			return nil, errors.New("wait_seconds must be between 0 and 15")
		}
		p := in.ExpectedPlan
		if p.ReleaseID == "" || p.ManifestFingerprint == "" || p.LogicalFingerprint == "" || p.PhysicalFingerprint == "" {
			return nil, errors.New("expected_plan must contain all four values from prepare_query")
		}
		return b.RunQuery(ctx, in)
	})
}
