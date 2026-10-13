package application_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/audit"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/model"
)

type fastEngine struct {
	calls        atomic.Int32
	capabilities model.EngineCapabilities
}

func (e *fastEngine) Capabilities() model.EngineCapabilities { return e.capabilities }
func (e *fastEngine) Execute(context.Context, model.PhysicalPlan) (model.ExecutionSnapshot, error) {
	e.calls.Add(1)
	return model.ExecutionSnapshot{Result: &model.TypedResult{}}, nil
}

type fastFixture struct {
	repository *catalog.MemoryRepository
	management *catalog.Service
	source     model.SemanticSource
	policy     model.PolicySource
	binding    model.SourceBinding
	engine     *fastEngine
	recorder   *audit.MemoryRecorder
	queries    *application.QueryService
	catalog    *application.CatalogService
	input      application.QueryInput
	bindings   atomic.Int32
}

func newFastFixture(t *testing.T) *fastFixture {
	t.Helper()
	f := &fastFixture{repository: catalog.NewMemoryRepository(), engine: &fastEngine{}, recorder: audit.NewMemoryRecorder()}
	root := filepath.Join("..", "..", "examples", "orders")
	read(t, filepath.Join(root, "model.yaml"), &f.source)
	read(t, filepath.Join(root, "policy.yaml"), &f.policy)
	read(t, filepath.Join(root, "binding.json"), &f.binding)
	read(t, filepath.Join(root, "capabilities.json"), &f.engine.capabilities)
	read(t, filepath.Join(root, "query.json"), &f.input.Query)
	read(t, filepath.Join(root, "context.json"), &f.input.Context)
	f.input.Namespace, f.input.ModelName = "demo", f.source.Metadata.Name
	f.policy.ManifestFingerprint, f.binding.ManifestFingerprint = "", ""
	f.management, _ = catalog.NewService(f.repository)
	draft, err := f.management.SaveDraft(t.Context(), catalog.SaveDraftInput{Namespace: "demo", Source: f.source, Actor: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.management.Publish(t.Context(), "demo", f.source.Metadata.Name, draft.Revision, "owner", "initial"); err != nil {
		t.Fatal(err)
	}
	policies := application.PolicyResolverFunc(func(context.Context, application.QueryScope, catalog.Release) (model.PolicySource, error) {
		return f.policy, nil
	})
	bindings := application.BindingResolverFunc(func(context.Context, application.QueryScope, catalog.Release) (model.SourceBinding, error) {
		f.bindings.Add(1)
		return f.binding, nil
	})
	f.queries, err = application.NewQueryService(f.repository, policies, bindings, f.engine, f.recorder)
	if err != nil {
		t.Fatal(err)
	}
	f.catalog, err = application.NewCatalogService(f.repository, policies, bindings)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestPreparedQueryChecksActualPlanBeforeAuditAndEngine(t *testing.T) {
	for _, change := range []string{"none", "release", "policy", "binding", "query", "partial"} {
		t.Run(change, func(t *testing.T) {
			f := newFastFixture(t)
			plan, err := f.queries.PlanActive(t.Context(), f.input)
			if err != nil {
				t.Fatal(err)
			}
			expected := application.ExpectPlan(plan)
			f.input.ExpectedPlan = &expected
			switch change {
			case "release":
				f.source.Metadata.Version = "1.1.0"
				draft, err := f.management.SaveDraft(t.Context(), catalog.SaveDraftInput{Namespace: "demo", Source: f.source, Actor: "owner", ExpectedRevision: 1})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.management.Publish(t.Context(), "demo", f.source.Metadata.Name, draft.Revision, "owner", "new release"); err != nil {
					t.Fatal(err)
				}
			case "policy":
				f.policy.Metadata.Version = "1.1.0"
			case "binding":
				f.binding.Datasets[0].Resource.Table = "other_orders"
			case "query":
				f.input.Query.Limit++
			case "partial":
				expected.PhysicalFingerprint = ""
			}
			_, err = f.queries.ExecuteActive(t.Context(), f.input)
			if change == "none" {
				if err != nil || f.engine.calls.Load() != 1 || len(f.recorder.Events()) != 2 {
					t.Fatalf("unchanged plan rejected: %v", err)
				}
			} else {
				var problem *model.Problem
				if !errors.As(err, &problem) || problem.Code != "prepared_query_changed" || f.engine.calls.Load() != 0 || len(f.recorder.Events()) != 0 {
					t.Fatalf("changed plan executed: %v", err)
				}
			}
		})
	}
}

type gatedFastExecutor struct {
	service          *application.QueryService
	entered, proceed chan struct{}
}

func (g gatedFastExecutor) ExecuteActive(ctx context.Context, input application.QueryInput) (application.QueryOutput, error) {
	close(g.entered)
	select {
	case <-g.proceed:
		return g.service.ExecuteActive(ctx, input)
	case <-ctx.Done():
		return application.QueryOutput{}, ctx.Err()
	}
}

func TestPreparedQueryRejectsChangeAfterAsyncSubmission(t *testing.T) {
	f := newFastFixture(t)
	plan, err := f.queries.PlanActive(t.Context(), f.input)
	if err != nil {
		t.Fatal(err)
	}
	expected := application.ExpectPlan(plan)
	f.input.ExpectedPlan = &expected
	gate := gatedFastExecutor{f.queries, make(chan struct{}), make(chan struct{})}
	jobs, err := application.NewJobManager(t.Context(), gate, time.Second, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer jobs.Close()
	submitted, err := jobs.Submit(t.Context(), f.input)
	if err != nil {
		t.Fatal(err)
	}
	<-gate.entered
	f.binding.Datasets[0].Resource.Table = "changed_after_submit"
	close(gate.proceed)
	result := awaitManagedJob(t, jobs, f.input.Context.Tenant, f.input.Context.Principal, submitted.Job.ID)
	if result.Job.Status != model.JobFailed || result.Job.Error == nil || result.Job.Error.Code != "prepared_query_changed" || f.engine.calls.Load() != 0 || len(f.recorder.Events()) != 0 {
		t.Fatalf("async change executed: %#v", result)
	}
}

func TestSummarySearchRanksBeforeLimitAndSkipsBindings(t *testing.T) {
	f := newFastFixture(t)
	// The substring match precedes the exact match in the manifest.
	for i := range f.source.Spec.Metrics {
		if f.source.Spec.Metrics[i].Name != "refund_rate" {
			f.source.Spec.Metrics[i].Description += " refund_rate"
		}
	}
	draft, err := f.management.SaveDraft(t.Context(), catalog.SaveDraftInput{Namespace: "demo", Source: f.source, Actor: "owner", ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.management.Publish(t.Context(), "demo", f.source.Metadata.Name, draft.Revision, "owner", "search fixture"); err != nil {
		t.Fatal(err)
	}
	summaries, err := f.catalog.SearchActiveSummary(t.Context(), f.input.QueryScope, "refund_rate", 1)
	if err != nil || len(summaries) != 1 || summaries[0].Name != "refund_rate" || len(summaries[0].DimensionDetails) != 0 || f.bindings.Load() != 0 {
		t.Fatalf("summary = %#v, %v", summaries, err)
	}
	f.policy.Rules[0].Metrics = []string{"gross_revenue"}
	summaries, err = f.catalog.SearchActiveSummary(t.Context(), f.input.QueryScope, "refund_rate", 1)
	if err != nil || len(summaries) != 1 || summaries[0].Name != "gross_revenue" {
		t.Fatalf("unauthorized exact match displaced authorized candidate: %#v, %v", summaries, err)
	}
	details, err := f.catalog.SearchActiveCodes(t.Context(), f.input.QueryScope, []string{"refund_rate", "gross_revenue"})
	if err != nil || len(details) != 1 || details[0].Name != "gross_revenue" || len(details[0].DimensionDetails) == 0 {
		t.Fatalf("details ignored authorization: %#v, %v", details, err)
	}
}
