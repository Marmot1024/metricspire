// Package application contains transport-independent product use cases.
package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/marmot1024/metricspire/internal/audit"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/executionauth"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/planner"
)

const auditCompletionTimeout = 5 * time.Second

type ReleaseReader interface {
	GetDraft(context.Context, string, string) (catalog.Draft, error)
	GetActiveRelease(context.Context, string, string) (catalog.Release, error)
}

type QueryEngine interface {
	Capabilities() model.EngineCapabilities
	Execute(context.Context, model.PhysicalPlan) (model.ExecutionSnapshot, error)
}

// PolicyResolver and BindingResolver are trusted server-side dependencies.
// Public transports must never accept either value from a query request.
type PolicyResolver interface {
	ResolvePolicy(context.Context, QueryScope, catalog.Release) (model.PolicySource, error)
}

type BindingResolver interface {
	ResolveBinding(context.Context, QueryScope, catalog.Release) (model.SourceBinding, error)
}

type PolicyResolverFunc func(context.Context, QueryScope, catalog.Release) (model.PolicySource, error)

func (function PolicyResolverFunc) ResolvePolicy(ctx context.Context, scope QueryScope, release catalog.Release) (model.PolicySource, error) {
	return function(ctx, scope, release)
}

type BindingResolverFunc func(context.Context, QueryScope, catalog.Release) (model.SourceBinding, error)

func (function BindingResolverFunc) ResolveBinding(ctx context.Context, scope QueryScope, release catalog.Release) (model.SourceBinding, error) {
	return function(ctx, scope, release)
}

type QueryService struct {
	releases ReleaseReader
	policies PolicyResolver
	bindings BindingResolver
	engine   QueryEngine
	audit    audit.Recorder
	now      func() time.Time
}

type QueryScope struct {
	Namespace string
	ModelName string
	Context   model.RequestContext
}

type QueryInput struct {
	QueryScope
	Query model.SemanticQuery
	// JobID is assigned by the trusted server-side JobManager. Public
	// transports decode only SemanticQuery and therefore cannot forge it.
	JobID string
}

type ExplainOutput struct {
	Release catalog.Release   `json:"release"`
	Logical model.LogicalPlan `json:"logical_plan"`
}

type PlanOutput struct {
	Release  catalog.Release    `json:"release"`
	Logical  model.LogicalPlan  `json:"logical_plan"`
	Physical model.PhysicalPlan `json:"physical_plan"`
}

type QueryOutput struct {
	Release   catalog.Release         `json:"release"`
	Logical   model.LogicalPlan       `json:"logical_plan"`
	Physical  model.PhysicalPlan      `json:"physical_plan"`
	Execution model.ExecutionSnapshot `json:"execution"`
}

func NewQueryService(releases ReleaseReader, policies PolicyResolver, bindings BindingResolver, engine QueryEngine, recorder audit.Recorder) (*QueryService, error) {
	if releases == nil || policies == nil || bindings == nil || engine == nil || recorder == nil {
		return nil, errors.New("release reader, policy resolver, binding resolver, query engine, and audit recorder are required")
	}
	return &QueryService{releases: releases, policies: policies, bindings: bindings, engine: engine, audit: recorder, now: time.Now}, nil
}

// ExplainActive resolves the active release and policy on the server, then
// produces the authorized logical plan without resolving a physical binding or
// executing against an analytical engine.
func (s *QueryService) ExplainActive(ctx context.Context, input QueryInput) (ExplainOutput, error) {
	if input.Namespace == "" || input.ModelName == "" {
		return ExplainOutput{}, errors.New("namespace and model name are required")
	}
	release, err := s.releases.GetActiveRelease(ctx, input.Namespace, input.ModelName)
	if err != nil {
		return ExplainOutput{}, fmt.Errorf("get active release: %w", err)
	}
	policySource, err := s.policies.ResolvePolicy(ctx, input.QueryScope, release)
	if err != nil {
		return ExplainOutput{}, fmt.Errorf("resolve policy for active release: %w", err)
	}
	return explainRelease(input, release, policySource, false)
}

// ExplainDraft is a maintainer-only use case. The transport must require both
// model:manage and query:execute before calling it. It compiles the latest
// draft in memory and never activates or publishes a release.
func (s *QueryService) ExplainDraft(ctx context.Context, input QueryInput) (ExplainOutput, error) {
	if input.Namespace == "" || input.ModelName == "" {
		return ExplainOutput{}, errors.New("namespace and model name are required")
	}
	draft, err := s.releases.GetDraft(ctx, input.Namespace, input.ModelName)
	if err != nil {
		return ExplainOutput{}, fmt.Errorf("get draft: %w", err)
	}
	manifest, err := compiler.Compile(draft.Source)
	if err != nil {
		return ExplainOutput{}, fmt.Errorf("compile draft: %w", err)
	}
	release := catalog.Release{
		ID: "draft:r" + fmt.Sprint(draft.Revision), Namespace: draft.Namespace, Name: draft.Name,
		SourceRevision: draft.Revision, ManifestFingerprint: manifest.Fingerprint, Manifest: manifest,
		CreatedBy: draft.UpdatedBy, Note: "maintainer draft preview", CreatedAt: draft.UpdatedAt,
	}
	policySource := model.PolicySource{
		APIVersion: model.APIVersion, Kind: model.KindPolicySource,
		Metadata:            model.Metadata{Name: "draft_preview", Version: fmt.Sprint(draft.Revision)},
		ManifestFingerprint: manifest.Fingerprint, Tenant: input.Context.Tenant,
		Rules: []model.PolicyRule{{
			Name: "maintainer_preview", Effect: model.EffectAllow,
			Principals: []string{input.Context.Principal}, Metrics: []string{"*"}, Dimensions: []string{"*"},
		}},
	}
	return explainRelease(input, release, policySource, true)
}

func explainRelease(input QueryInput, release catalog.Release, policySource model.PolicySource, allowUnverified bool) (ExplainOutput, error) {
	bundle, err := compiler.CompilePolicy(policySource, release.Manifest)
	if err != nil {
		return ExplainOutput{}, fmt.Errorf("compile query policy: %w", err)
	}
	var logical model.LogicalPlan
	if allowUnverified {
		logical, err = planner.BuildLogicalPreview(release.Manifest, bundle, input.Context, input.Query)
	} else {
		logical, err = planner.BuildLogical(release.Manifest, bundle, input.Context, input.Query)
	}
	if err != nil {
		return ExplainOutput{}, fmt.Errorf("build logical plan: %w", err)
	}
	return ExplainOutput{Release: release, Logical: logical}, nil
}

// PlanActive adds the trusted physical binding but does not execute the plan.
func (s *QueryService) PlanActive(ctx context.Context, input QueryInput) (PlanOutput, error) {
	explained, err := s.ExplainActive(ctx, input)
	if err != nil {
		return PlanOutput{}, err
	}
	return s.planExplained(ctx, input, explained)
}

func (s *QueryService) PlanDraft(ctx context.Context, input QueryInput) (PlanOutput, error) {
	explained, err := s.ExplainDraft(ctx, input)
	if err != nil {
		return PlanOutput{}, err
	}
	return s.planExplained(ctx, input, explained)
}

func (s *QueryService) planExplained(ctx context.Context, input QueryInput, explained ExplainOutput) (PlanOutput, error) {
	binding, err := s.bindings.ResolveBinding(ctx, input.QueryScope, explained.Release)
	if err != nil {
		return PlanOutput{}, fmt.Errorf("resolve query binding: %w", err)
	}
	physical, err := planner.BuildPhysical(explained.Release.Manifest, explained.Logical, binding, s.engine.Capabilities())
	if err != nil {
		return PlanOutput{}, fmt.Errorf("build physical plan: %w", err)
	}
	return PlanOutput{Release: explained.Release, Logical: explained.Logical, Physical: physical}, nil
}

// ExecuteActive resolves the active release and all trusted server-side inputs.
// The caller cannot select an unpublished release, policy, binding, engine, or
// stale manifest fingerprint through SemanticQuery.
func (s *QueryService) ExecuteActive(ctx context.Context, input QueryInput) (QueryOutput, error) {
	// The user-authorized engine token must reach only the analytical engine.
	// Control-plane, policy, planning, and audit dependencies receive an
	// explicitly masked context that retains cancellation and deadlines.
	controlContext := executionauth.WithoutAccessToken(ctx)
	planned, err := s.PlanActive(controlContext, input)
	if err != nil {
		return QueryOutput{}, err
	}
	return s.executePlanned(ctx, controlContext, input, planned)
}

// ExecuteDraft runs a bounded preview against the latest draft without
// creating or activating a release. Callers must enforce maintainer access.
func (s *QueryService) ExecuteDraft(ctx context.Context, input QueryInput) (QueryOutput, error) {
	controlContext := executionauth.WithoutAccessToken(ctx)
	planned, err := s.PlanDraft(controlContext, input)
	if err != nil {
		return QueryOutput{}, err
	}
	return s.executePlanned(ctx, controlContext, input, planned)
}

func (s *QueryService) executePlanned(ctx, controlContext context.Context, input QueryInput, planned PlanOutput) (QueryOutput, error) {
	baseEvent := audit.QueryEvent{
		RequestID: input.Context.RequestID, Tenant: input.Context.Tenant, Principal: input.Context.Principal,
		Namespace: input.Namespace, ModelName: input.ModelName, ReleaseID: planned.Release.ID,
		ManifestFingerprint: planned.Release.ManifestFingerprint, LogicalFingerprint: planned.Logical.Fingerprint,
		PhysicalFingerprint: planned.Physical.Fingerprint, JobID: input.JobID,
	}
	started := baseEvent
	started.Kind = audit.EventQueryStarted
	started.OccurredAt = s.now().UTC()
	if err := s.audit.Record(controlContext, started); err != nil {
		return QueryOutput{}, fmt.Errorf("%w: record query start: %v", audit.ErrUnavailable, err)
	}
	execution, err := s.engine.Execute(ctx, planned.Physical)
	finished := baseEvent
	finished.OccurredAt = s.now().UTC()
	if execution.Result != nil {
		finished.RowCount = len(execution.Result.Rows)
		finished.Truncated = execution.Result.Truncated
	}
	if err == nil {
		finished.Kind = audit.EventQuerySucceeded
	} else if errors.Is(err, context.Canceled) || execution.Job.Status == model.JobCancelled {
		finished.Kind = audit.EventQueryCancelled
		finished.ErrorCode = errorCode(err)
	} else {
		finished.Kind = audit.EventQueryFailed
		finished.ErrorCode = errorCode(err)
	}
	auditContext, cancelAudit := context.WithTimeout(context.WithoutCancel(controlContext), auditCompletionTimeout)
	defer cancelAudit()
	if auditErr := s.audit.Record(auditContext, finished); auditErr != nil {
		return QueryOutput{Release: planned.Release, Logical: planned.Logical, Physical: planned.Physical, Execution: execution}, fmt.Errorf("%w: record query completion: %v", audit.ErrUnavailable, auditErr)
	}
	if err != nil {
		return QueryOutput{Release: planned.Release, Logical: planned.Logical, Physical: planned.Physical, Execution: execution}, err
	}
	return QueryOutput{Release: planned.Release, Logical: planned.Logical, Physical: planned.Physical, Execution: execution}, nil
}

func errorCode(err error) string {
	var problem *model.Problem
	if errors.As(err, &problem) {
		return safeErrorCode(problem.Code)
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "engine_error"
}

func safeErrorCode(value string) string {
	if len(value) < 1 || len(value) > 64 {
		return "engine_error"
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' {
			return "engine_error"
		}
	}
	return value
}
