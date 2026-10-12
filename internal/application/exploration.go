package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/audit"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/executionauth"
	"github.com/marmot1024/metricspire/internal/exploration"
	"github.com/marmot1024/metricspire/internal/model"
)

type AnalysisEngine interface {
	Execute(context.Context, exploration.Plan) (model.ExecutionSnapshot, error)
}

type AnalysisSourceBinding struct {
	ModelName string
	Source    exploration.Source
}

// ResolveAnalysisSource must enforce namespace/tenant membership and return
// trusted configuration. Query bodies contain only a logical source code.
type AnalysisSourceResolver interface {
	ResolveAnalysisSource(context.Context, string, model.RequestContext, string) (AnalysisSourceBinding, error)
}

type AnalysisSourceResolverFunc func(context.Context, string, model.RequestContext, string) (AnalysisSourceBinding, error)

func (f AnalysisSourceResolverFunc) ResolveAnalysisSource(ctx context.Context, ns string, rc model.RequestContext, source string) (AnalysisSourceBinding, error) {
	return f(ctx, ns, rc, source)
}

type AnalysisInput struct {
	Namespace string
	Context   model.RequestContext
	Query     exploration.Query
}

type AnalysisPlanOutput struct {
	Release catalog.Release  `json:"release"`
	Plan    exploration.Plan `json:"plan"`
}

type AnalysisOutput struct {
	AnalysisPlanOutput
	Execution model.ExecutionSnapshot `json:"execution"`
}

type AnalysisService struct {
	queries *QueryService
	sources AnalysisSourceResolver
	engine  AnalysisEngine
	audit   audit.Recorder
}

func NewAnalysisService(queries *QueryService, sources AnalysisSourceResolver, engine AnalysisEngine, recorder audit.Recorder) (*AnalysisService, error) {
	_, direct := sources.(AnalysisCatalogResolver)
	if queries == nil && !direct || sources == nil || engine == nil || recorder == nil {
		return nil, errors.New("query planner, trusted analysis sources, analysis engine and audit are required")
	}
	return &AnalysisService{queries: queries, sources: sources, engine: engine, audit: recorder}, nil
}

func (s *AnalysisService) Plan(ctx context.Context, in AnalysisInput) (AnalysisPlanOutput, error) {
	started := time.Now()
	ctx = executionauth.WithoutAccessToken(ctx)
	// Bound even source resolution before its narrower budget is known.
	ctx, cancelResolution := context.WithTimeout(ctx, 15*time.Minute)
	defer cancelResolution()
	if in.Namespace == "" {
		return AnalysisPlanOutput{}, errors.New("namespace is required")
	}
	var binding AnalysisSourceBinding
	var semantic *exploration.Catalog
	var err error
	if resolver, ok := s.sources.(AnalysisCatalogResolver); ok {
		var c exploration.Catalog
		c, err = resolver.ResolveAnalysisCatalog(ctx, in.Namespace, in.Context, in.Query.Source)
		if err == nil {
			semantic = &c
			binding.Source, _ = exploration.SourceIn(c, in.Query.Source)
			binding.ModelName = c.Metadata.Name
		}
	} else {
		binding, err = s.sources.ResolveAnalysisSource(ctx, in.Namespace, in.Context, in.Query.Source)
	}
	if err != nil {
		return AnalysisPlanOutput{}, fmt.Errorf("resolve approved analysis source: %w", err)
	}
	if binding.ModelName == "" {
		return AnalysisPlanOutput{}, errors.New("analysis source model is required")
	}
	if err = exploration.ValidateRequest(in.Query, binding.Source, in.Context); err != nil {
		return AnalysisPlanOutput{}, err
	}
	ctx, cancel := context.WithDeadline(ctx, started.Add(binding.Source.Timeout))
	defer cancel()
	if err := ctx.Err(); err != nil {
		return AnalysisPlanOutput{}, err
	}
	if binding.Source.Dataset != "" {
		if semantic == nil || semantic.Namespace != in.Namespace || semantic.Tenant != in.Context.Tenant {
			return AnalysisPlanOutput{}, errors.New("external semantic catalog resolver is required")
		}
		plan, err := exploration.PlanCatalog(*semantic, in.Query, in.Context)
		if err != nil {
			return AnalysisPlanOutput{}, err
		}
		if err = ctx.Err(); err != nil {
			return AnalysisPlanOutput{}, err
		}
		return AnalysisPlanOutput{Plan: plan}, nil
	}
	groups := exploration.References(in.Query)
	if in.Query.TimeGrouping == nil {
		// A range dimension is already authorized and physically bound by the
		// shared planner; it need not become a grouped output just to read it.
		filtered := []string{}
		for _, g := range groups {
			if g != in.Query.TimeRange.Dimension {
				filtered = append(filtered, g)
			}
		}
		groups = filtered
	}
	base, err := s.queries.PlanActive(ctx, QueryInput{
		QueryScope: QueryScope{Namespace: in.Namespace, ModelName: binding.ModelName, Context: in.Context},
		Query:      model.SemanticQuery{APIVersion: model.APIVersion, Kind: model.KindSemanticQuery, Metrics: []string{binding.Source.AnchorMetric}, GroupBy: groups, TimeRange: in.Query.TimeRange, TimeGrouping: in.Query.TimeGrouping, Filters: in.Query.Filters, Limit: in.Query.Limit},
	})
	if err != nil {
		return AnalysisPlanOutput{}, err
	}
	// Shared planning normalizes timestamps/timezone/filters. Pin the same
	// authorized values instead of compiling a second interpretation.
	in.Query.TimeRange = base.Physical.TimeRange
	in.Query.TimeGrouping = base.Physical.TimeGrouping
	in.Query.Filters = base.Physical.Filters
	p, err := exploration.Prepare(in.Query, binding.Source, base.Physical, in.Context)
	if err != nil {
		return AnalysisPlanOutput{}, err
	}
	if err = ctx.Err(); err != nil {
		return AnalysisPlanOutput{}, err
	}
	return AnalysisPlanOutput{Release: base.Release, Plan: p}, nil
}

func (s *AnalysisService) Execute(ctx context.Context, in AnalysisInput) (AnalysisOutput, error) {
	started := time.Now()
	p, err := s.Plan(ctx, in)
	if err != nil {
		return AnalysisOutput{}, err
	}
	ctx, cancel := context.WithDeadline(ctx, started.Add(p.Plan.Source.Timeout))
	defer cancel()
	return s.executePlanned(ctx, in, p, "")
}

// Private execution accepts only plans produced by this service. Public
// adapters must never deserialize an AnalysisPlanOutput to execute it.
func (s *AnalysisService) executePlanned(ctx context.Context, in AnalysisInput, p AnalysisPlanOutput, jobID string) (AnalysisOutput, error) {
	if err := ctx.Err(); err != nil {
		return AnalysisOutput{}, err
	}
	if err := exploration.Verify(p.Plan); err != nil {
		return AnalysisOutput{}, err
	}
	control := executionauth.WithoutAccessToken(ctx)
	event := audit.QueryEvent{RequestID: in.Context.RequestID, Tenant: in.Context.Tenant, Principal: in.Context.Principal, Namespace: in.Namespace, ModelName: p.Release.Name, ReleaseID: p.Release.ID, ManifestFingerprint: p.Plan.Base.ManifestFingerprint, LogicalFingerprint: p.Plan.Base.LogicalFingerprint, PhysicalFingerprint: p.Plan.Fingerprint, JobID: jobID, Kind: audit.EventQueryStarted, OccurredAt: time.Now()}
	if semantic := p.Plan.Semantics; semantic != nil {
		event.ModelName = semantic.Name
		event.ReleaseID = "external:" + semantic.Version
	}
	if err := s.audit.Record(control, event); err != nil {
		return AnalysisOutput{}, fmt.Errorf("%w: start analysis", audit.ErrUnavailable)
	}
	execution, err := s.engine.Execute(ctx, p.Plan)
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = validateAnalysisResult(p.Plan, execution)
	}
	event.Kind = audit.EventQuerySucceeded
	event.OccurredAt = time.Now()
	if err != nil {
		event.Kind = audit.EventQueryFailed
		event.ErrorCode = errorCode(err)
		if errors.Is(err, context.Canceled) {
			event.Kind = audit.EventQueryCancelled
		}
	}
	if err == nil && execution.Result != nil {
		event.RowCount = len(execution.Result.Rows)
		event.Truncated = execution.Result.Truncated
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(control), auditCompletionTimeout)
	defer cancel()
	if e := s.audit.Record(auditCtx, event); e != nil {
		return AnalysisOutput{}, fmt.Errorf("%w: complete analysis", audit.ErrUnavailable)
	}
	if err != nil {
		return AnalysisOutput{}, err
	}
	return AnalysisOutput{AnalysisPlanOutput: p, Execution: execution}, nil
}

func validateAnalysisResult(p exploration.Plan, result model.ExecutionSnapshot) error {
	if result.Job.Status != model.JobSucceeded || result.Result == nil || result.Result.Truncated || len(result.Result.Rows) > p.Query.Limit || result.Job.PhysicalFingerprint != p.Fingerprint {
		return errors.New("analysis result is incomplete or does not match its plan")
	}
	if len(result.Result.Columns) != len(p.Columns) {
		return errors.New("analysis result schema mismatch")
	}
	for i, c := range p.Columns {
		actual := result.Result.Columns[i]
		if actual.Name != c.Name || !analysisResultType(c.Type, actual.TypeName) {
			return errors.New("analysis result column mismatch")
		}
	}
	for _, row := range result.Result.Rows {
		if len(row) != len(p.Columns) {
			return errors.New("analysis result row width mismatch")
		}
	}
	return nil
}

func analysisResultType(expected model.DataType, actual string) bool {
	actual = strings.ToUpper(actual)
	switch expected {
	case model.DataTypeString:
		return actual == "STRING" || actual == "VARCHAR" || actual == "CHAR"
	case model.DataTypeInteger:
		return actual == "LONG" || actual == "BIGINT" || actual == "INT" || actual == "INTEGER" || actual == "SHORT" || actual == "SMALLINT" || actual == "BYTE" || actual == "TINYINT"
	case model.DataTypeDecimal:
		return actual == "DECIMAL" || actual == "DOUBLE" || actual == "FLOAT" || analysisResultType(model.DataTypeInteger, actual)
	case model.DataTypeBoolean:
		return actual == "BOOLEAN"
	case model.DataTypeDate:
		return actual == "DATE"
	case model.DataTypeTimestamp:
		return actual == "TIMESTAMP" || actual == "TIMESTAMP_NTZ"
	}
	return false
}
