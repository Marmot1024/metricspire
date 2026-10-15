package mcpbridge

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/exploration"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type AnalysisInput struct {
	Namespace string            `json:"namespace" jsonschema:"Business domain; use discover_analysis to find approved sources and attributes"`
	Query     exploration.Query `json:"query" jsonschema:"AnalysisQuery uses logical source and field codes, never metric codes or SQL. time_range is mandatory RFC3339 half-open range with declared business timezone. Grouping defines the counting unit. Row/measure where expressions are boolean. calculations are typed ordered expressions; summarize adds one aggregate stage; share is final-stage only. Honor discovered snapshot/missing semantics."`
}
type AnalysisDiscoveryInput struct {
	Namespace string `json:"namespace"`
}
type AnalysisJobInput struct {
	Namespace string `json:"namespace"`
	JobID     string `json:"job_id"`
}

// Optional interface keeps ordinary metric-only backends compatible.
type AnalysisBackend interface {
	DiscoverAnalysis(context.Context, AnalysisDiscoveryInput) ([]application.AnalysisSourceView, error)
	PlanAnalysis(context.Context, AnalysisInput) (application.AnalysisPlanView, error)
	SubmitAnalysis(context.Context, AnalysisInput) (json.RawMessage, error)
	GetAnalysis(context.Context, AnalysisJobInput) (json.RawMessage, error)
	CancelAnalysis(context.Context, AnalysisJobInput) (json.RawMessage, error)
}

func addAnalysisTools(s *mcp.Server, b AnalysisBackend) {
	add(s, "discover_analysis", "Discover approved sources, fields, calendars, associations and budgets. internal_only fields cannot be output as values. Use after confirming metric coverage is unsuitable; no data query.", true, func(ctx context.Context, in AnalysisDiscoveryInput) (any, error) {
		if _, err := namespacePath(in.Namespace); err != nil {
			return nil, err
		}
		v, err := b.DiscoverAnalysis(ctx, in)
		return map[string]any{"sources": v}, err
	})
	add(s, "plan_analysis", "Validate temporary formula, population and counting stages against approved semantics. Inspect normalized plan and gaps before submission. No execution or metric registration.", true, func(ctx context.Context, in AnalysisInput) (any, error) {
		if _, err := namespacePath(in.Namespace); err != nil {
			return nil, err
		}
		return b.PlanAnalysis(ctx, in)
	})
	add(s, "submit_analysis", "Execute the reviewed analysis once; costs may apply. Retain job_id for get_analysis/cancel_analysis. Never retry uncertain submissions blindly; no permission/timeout fallback.", false, func(ctx context.Context, in AnalysisInput) (any, error) {
		if _, err := namespacePath(in.Namespace); err != nil {
			return nil, err
		}
		return b.SubmitAnalysis(ctx, in)
	})
	add(s, "get_analysis", "Read your analysis job, pinned semantic evidence and exact typed result. Keep verification and counting grain visible.", true, func(ctx context.Context, in AnalysisJobInput) (any, error) {
		if err := validAnalysisJob(in); err != nil {
			return nil, err
		}
		return b.GetAnalysis(ctx, in)
	})
	add(s, "cancel_analysis", "Request cancellation of your analysis job and warehouse work; finished outcomes remain unchanged.", false, func(ctx context.Context, in AnalysisJobInput) (any, error) {
		if err := validAnalysisJob(in); err != nil {
			return nil, err
		}
		return b.CancelAnalysis(ctx, in)
	})
}

func validAnalysisJob(in AnalysisJobInput) error {
	if _, err := namespacePath(in.Namespace); err != nil {
		return err
	}
	if !identifier.MatchString(in.JobID) {
		return errors.New("invalid job_id")
	}
	return nil
}
func (a *api) DiscoverAnalysis(ctx context.Context, in AnalysisDiscoveryInput) ([]application.AnalysisSourceView, error) {
	path, err := namespacePath(in.Namespace)
	if err != nil {
		return nil, err
	}
	var out []application.AnalysisSourceView
	err = a.request(ctx, "GET", path+"/analysis/sources", nil, &out)
	return out, err
}
func (a *api) PlanAnalysis(ctx context.Context, in AnalysisInput) (application.AnalysisPlanView, error) {
	path, err := namespacePath(in.Namespace)
	if err != nil {
		return application.AnalysisPlanView{}, err
	}
	var out application.AnalysisPlanView
	err = a.request(ctx, "POST", path+"/analysis/plan", in.Query, &out)
	return out, err
}
func (a *api) SubmitAnalysis(ctx context.Context, in AnalysisInput) (json.RawMessage, error) {
	path, err := namespacePath(in.Namespace)
	if err != nil {
		return nil, err
	}
	var out json.RawMessage
	err = a.request(ctx, "POST", path+"/analysis/jobs", in.Query, &out)
	return out, err
}
func (a *api) GetAnalysis(ctx context.Context, in AnalysisJobInput) (json.RawMessage, error) {
	return a.analysisJob(ctx, in, false)
}
func (a *api) CancelAnalysis(ctx context.Context, in AnalysisJobInput) (json.RawMessage, error) {
	return a.analysisJob(ctx, in, true)
}
func (a *api) analysisJob(ctx context.Context, in AnalysisJobInput, cancel bool) (json.RawMessage, error) {
	if err := validAnalysisJob(in); err != nil {
		return nil, err
	}
	path, _ := namespacePath(in.Namespace)
	path += "/analysis/jobs/" + in.JobID
	method := "GET"
	if cancel {
		method = "POST"
		path += "/cancel"
	}
	var out json.RawMessage
	err := a.request(ctx, method, path, nil, &out)
	return out, err
}
