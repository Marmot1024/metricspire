package httpapi

import (
	"context"
	"errors"
	"time"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/mcpbridge"
	"github.com/marmot1024/metricspire/internal/model"
)

func (b *remoteMCPBackend) GetMetricDetails(ctx context.Context, in mcpbridge.MetricDetailsInput) ([]mcpbridge.MetricDetail, error) {
	ctx, cancel := context.WithTimeout(ctx, b.server.config.ControlTimeout)
	defer cancel()
	catalogSearch, err := b.metricCatalog(in.Route)
	if err != nil {
		return nil, b.safeError(err)
	}
	entries, err := catalogSearch.SearchActiveCodes(ctx, application.QueryScope{Namespace: in.Namespace, Context: b.requestContext()}, in.Metrics)
	if err != nil {
		return nil, b.safeError(err)
	}
	byName := make(map[string][]application.MetricCatalogEntry)
	for _, m := range entries {
		byName[m.Name] = append(byName[m.Name], m)
	}
	out := make([]mcpbridge.MetricDetail, 0, len(in.Metrics))
	for _, name := range in.Metrics {
		matches := byName[name]
		if len(matches) != 1 {
			return nil, b.safeError(&model.Problem{Code: "metric_route_not_found", Path: "metrics", Message: "selected codes must each resolve to exactly one authorized active definition"})
		}
		out = append(out, mcpbridge.DetailMetric(matches[0]))
	}
	return out, nil
}

func (b *remoteMCPBackend) DiscoverQueryRoutes(ctx context.Context, in mcpbridge.AnalysisDiscoveryInput) (mcpbridge.QueryRoutes, error) {
	ctx, cancel := context.WithTimeout(ctx, b.server.config.ControlTimeout)
	defer cancel()
	entries, err := b.SearchMetrics(ctx, mcpbridge.SearchInput{Namespace: in.Namespace, Limit: 1})
	if err != nil {
		return mcpbridge.QueryRoutes{}, err
	}
	result := mcpbridge.QueryRoutes{
		Namespace:        in.Namespace,
		PublishedMetrics: mcpbridge.QueryRoute{Available: len(entries) > 0 && b.server.deps.Queries != nil, Workflow: []string{"search_metrics", "get_metric_details", "prepare_query", "run_query"}},
		OnlineMetrics:    mcpbridge.QueryRoute{Workflow: []string{}},
		Exploration:      mcpbridge.QueryRoute{Workflow: []string{"discover_analysis", "plan_analysis", "submit_analysis", "get_analysis", "cancel_analysis"}},
		SelectionRules: []string{
			"Prefer online_metrics for a matching certified definition with an external_code when that serving route is available; use query_online_metrics with numeric codes and code-to-manifest_fingerprint selections. Do not fall back on serving failures.",
			"Use published_metrics when a maintained definition, population, denominator and time grain answer the question.",
			"Use exploration for a requested temporary formula, cohort or counting grain only when available and approved sources/fields support its meaning; discover_analysis first.",
			"Resolve population, exclusions, denominator and counting grain before execution. Roles and usage history rank suggestions, never silently choose a different definition. Confirm no suitable authorized metric before exploration; empty search is not proof of absence.",
			"Permission, connection and timeout failures never authorize switching query categories.",
			"Availability is current-user catalog access, not business certification or proof that a data query succeeds.",
		},
	}

	if online, ok := b.server.deps.Online.(interface {
		Available(application.QueryScope) bool
	}); ok && b.server.deps.OnlineCatalogSearch != nil && online.Available(application.QueryScope{Namespace: in.Namespace, Context: b.requestContext()}) {
		onlineEntries, err := b.SearchMetrics(ctx, mcpbridge.SearchInput{Namespace: in.Namespace, Route: "online_metrics", Limit: 1})
		if err != nil {
			return mcpbridge.QueryRoutes{}, err
		}
		result.OnlineMetrics.Available = len(onlineEntries) > 0
		if result.OnlineMetrics.Available {
			result.OnlineMetrics.Workflow = []string{"search_metrics (route=online_metrics)", "get_metric_details (route=online_metrics)", "query_online_metrics"}
		}
	}

	if b.server.deps.Queries == nil {
		result.PublishedMetrics.Workflow = []string{}
	}
	if b.server.deps.AnalysisCatalogs != nil {
		namespaces, err := b.server.deps.AnalysisCatalogs.Namespaces(ctx, b.requestContext())
		if err != nil {
			return mcpbridge.QueryRoutes{}, b.safeError(err)
		}
		for _, namespace := range namespaces {
			if namespace == in.Namespace {
				result.Exploration.Available = true
			}
		}
	}
	result.NextStep = "Use an available route only after confirming semantic fit. For exploration keep namespace and discover query.source with discover_analysis."
	if !result.PublishedMetrics.Available && !result.OnlineMetrics.Available && !result.Exploration.Available {
		result.NextStep = "No query route returned for this namespace. Verify the canonical business domain with list_namespaces; a source name belongs in query.source, not namespace. This does not prove data absence."
	}
	return result, nil
}

func (b *remoteMCPBackend) preparePlan(ctx context.Context, in mcpbridge.QueryInput) (application.QueryInput, application.PlanOutput, error) {
	input, err := b.activeQueryInput(ctx, in)
	if err != nil {
		return input, application.PlanOutput{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, b.server.config.QueryTimeout)
	defer cancel()
	plan, err := b.server.deps.Queries.PlanActive(ctx, input)
	return input, plan, b.safeError(err)
}

func (b *remoteMCPBackend) PrepareQuery(ctx context.Context, in mcpbridge.QueryInput) (mcpbridge.PreparedQuery, error) {
	started := time.Now()
	_, plan, err := b.preparePlan(ctx, in)
	if err != nil {
		return mcpbridge.PreparedQuery{}, err
	}
	return preparedQuery(in, plan, time.Since(started).Milliseconds()), nil
}

func preparedQuery(in mcpbridge.QueryInput, plan application.PlanOutput, elapsed int64) mcpbridge.PreparedQuery {
	in.Query.Limit = plan.Logical.Limit
	in.Query.TimeRange, in.Query.TimeGrouping = plan.Logical.TimeRange, plan.Logical.TimeGrouping
	metrics := make([]mcpbridge.MetricSummary, 0, len(in.Query.Metrics))
	for _, logical := range plan.Logical.Metrics {
		if !logical.Output {
			continue
		}
		for _, m := range plan.Release.Manifest.Definitions.Metrics {
			if m.Name == logical.Name {
				metrics = append(metrics, mcpbridge.DetailMetric(application.MetricCatalogEntry{Definition: m.Definition, Name: m.Name, ExternalCode: m.ExternalCode, DisplayName: m.DisplayName, Description: m.Description, ValueType: m.ValueType, Unit: m.Unit, VerificationStatus: m.Verification.Status, ReleaseChannel: plan.Release.Channel, Deprecated: m.Deprecated, TimeDimension: m.TimeDimension}).MetricSummary)
			}
		}
	}
	return mcpbridge.PreparedQuery{Namespace: in.Namespace, Query: in.Query, ExpectedPlan: application.ExpectPlan(plan), Metrics: metrics, Engine: plan.Physical.Engine, ElapsedMS: elapsed, NextStep: "Preparation validates a plan; it does not resolve user intent. If population/exclusion choices remain unresolved or the requested rule is undocumented, ask the user and STOP before run_query. Do not execute a generic total and add a caveat. Otherwise review meaning/time/grain and send query and expected_plan unchanged to run_query."}
}

func (b *remoteMCPBackend) RunQuery(ctx context.Context, in mcpbridge.RunQueryInput) (mcpbridge.RunQueryOutput, error) {
	started := time.Now()
	input, plan, err := b.preparePlan(ctx, mcpbridge.QueryInput{Namespace: in.Namespace, Query: in.Query})
	if err != nil {
		return mcpbridge.RunQueryOutput{}, err
	}
	if in.ExpectedPlan != application.ExpectPlan(plan) {
		return mcpbridge.RunQueryOutput{}, b.safeError(&model.Problem{Code: "prepared_query_changed", Path: "query", Message: "query, release, policy or binding changed; prepare again"})
	}
	// This guard is checked again on the plan actually executed by the async
	// job. A pre-submit check alone cannot close that race.
	input.ExpectedPlan = &in.ExpectedPlan
	admissionContext, cancelAdmission := context.WithTimeout(ctx, b.server.config.QueryTimeout)
	defer cancelAdmission()
	executionContext := admissionContext
	if b.server.deps.ExecutionCredential != nil {
		executionContext, err = b.server.deps.ExecutionCredential.AddToContext(admissionContext, b.request, b.principal)
		if err != nil {
			return mcpbridge.RunQueryOutput{}, b.safeError(err)
		}
		if executionContext == nil {
			return mcpbridge.RunQueryOutput{}, b.safeError(errors.New("nil execution context"))
		}
	}
	if admissionContext.Err() != nil {
		return mcpbridge.RunQueryOutput{}, b.safeError(admissionContext.Err())
	}
	snapshot, err := b.server.deps.Jobs.Submit(executionContext, input)
	if err != nil {
		return mcpbridge.RunQueryOutput{}, b.safeError(err)
	}
	cancelAdmission()
	result := mcpbridge.RunQueryOutput{QueryJobSnapshot: snapshot, Metrics: preparedQuery(mcpbridge.QueryInput{Namespace: in.Namespace, Query: in.Query}, plan, 0).Metrics}
	wait := in.WaitSeconds
	if wait == 0 {
		wait = 10
	}
	waitContext, cancel := context.WithTimeout(ctx, time.Duration(wait)*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if result.Job.Status == model.JobSucceeded || result.Job.Status == model.JobFailed || result.Job.Status == model.JobCancelled {
			break
		}
		select {
		case <-waitContext.Done():
			// Waiting does not own the accepted job's lifetime. Even cancellation
			// returns its ID when the transport can still carry a response.
			result.WaitError = "wait_timeout"
			if ctx.Err() != nil {
				result.WaitError = "request_cancelled"
			}
		case <-ticker.C:
			next, err := b.server.deps.Jobs.Get(b.principal.Tenant, b.principal.Subject, result.Job.ID)
			if err != nil {
				result.WaitError = "status_unavailable"
			} else {
				result.QueryJobSnapshot = next
			}
		}
		if result.WaitError != "" {
			break
		}
	}
	result.ElapsedMS = time.Since(started).Milliseconds()
	switch result.Job.Status {
	case model.JobSucceeded:
		result.NextStep = "Answer from the typed result and actual time/release evidence; retain catalog verification status. Polling must not resubmit this accepted job. A later explicit refresh is a new authorized query; this result does not prove later freshness or access."
	case model.JobFailed, model.JobCancelled:
		result.NextStep = "Report this terminal status and error. Do not retry submission or switch query categories automatically."
	default:
		result.NextStep = "Keep this job ID; use get_query or cancel_query. Do not call run_query again for the accepted job."
	}
	return result, nil
}
