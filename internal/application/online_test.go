package application_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/adapter/postgresquery"
	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/audit"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
)

type onlineFakeEngine struct {
	calls int
	run   func(context.Context, model.PhysicalPlan) (model.ExecutionSnapshot, error)
}

func (e *onlineFakeEngine) Capabilities() model.EngineCapabilities {
	return postgresquery.Capabilities()
}
func (e *onlineFakeEngine) Execute(ctx context.Context, p model.PhysicalPlan) (model.ExecutionSnapshot, error) {
	e.calls++
	if e.run != nil {
		return e.run(ctx, p)
	}
	return model.ExecutionSnapshot{Result: &model.TypedResult{Columns: []model.ResultColumn{{Name: "revenue"}}, Rows: [][]any{{"150.00"}}}, DataSnapshot: &model.DataSnapshot{BatchID: "batch", DataAsOf: time.Now()}}, nil
}

func onlineFixture(t *testing.T, recorder audit.Recorder) (*application.OnlineService, *catalog.MemoryRepository, *onlineFakeEngine, model.SemanticSource, application.QueryScope, application.OnlineQuery) {
	t.Helper()
	root := filepath.Join("..", "..", "examples", "online")
	var source model.SemanticSource
	var policy model.PolicySource
	var binding model.SourceBinding
	var query application.OnlineQuery
	for path, target := range map[string]any{"model.yaml": &source, "policy.yaml": &policy, "binding.yaml": &binding, "query.json": &query} {
		if err := contractio.ReadFile(filepath.Join(root, path), target); err != nil {
			t.Fatal(err)
		}
	}
	repo := catalog.NewMemoryRepository()
	management, _ := catalog.NewService(repo)
	draft, err := management.SaveDraft(t.Context(), catalog.SaveDraftInput{Namespace: "demo", Source: source, Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = management.Publish(t.Context(), "demo", source.Metadata.Name, draft.Revision, "test", "fixture"); err != nil {
		t.Fatal(err)
	}
	policies, _ := application.NewConfiguredPolicyResolver([]application.PolicyConfiguration{{Namespace: "demo", ModelName: source.Metadata.Name, Tenant: "demo", Policy: policy}})
	bindings, _ := application.NewConfiguredBindingResolver([]application.BindingConfiguration{{Namespace: "demo", ModelName: source.Metadata.Name, Binding: binding}})
	engine := &onlineFakeEngine{}
	if recorder == nil {
		recorder = audit.NewMemoryRecorder()
	}
	queries, _ := application.NewQueryService(repo, policies, bindings, engine, recorder)
	service, err := application.NewOnlineService(repo, queries, "demo", []string{"demo"}, 1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	scope := application.QueryScope{Namespace: "demo", Context: model.RequestContext{Tenant: "demo", Principal: "analyst", Roles: []string{"analyst"}, RequestID: "request"}}
	return service, repo, engine, source, scope, query
}

func TestOnlineNumericCodesAndRequestBudgets(t *testing.T) {
	service, _, engine, _, scope, q := onlineFixture(t, nil)
	out, err := service.Execute(t.Context(), scope, q)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.Columns[0].Name != "10001" || out.ReleaseID == "" || out.Snapshot.BatchID != "batch" {
		t.Fatalf("unexpected result %#v", out)
	}
	for _, test := range []struct {
		name, code string
		change     func(*application.OnlineQuery, *application.QueryScope)
	}{
		{"english", "invalid_query", func(q *application.OnlineQuery, s *application.QueryScope) { q.MetricCodes = []string{"revenue"} }},
		{"duplicate", "invalid_query", func(q *application.OnlineQuery, s *application.QueryScope) {
			q.MetricCodes = []string{"10001", "10001"}
		}},
		{"missing", "metric_route_not_found", func(q *application.OnlineQuery, s *application.QueryScope) { q.MetricCodes = []string{"00000"} }},
		{"time required", "invalid_query", func(q *application.OnlineQuery, s *application.QueryScope) { q.TimeRange = nil }},
		{"rows", "invalid_query", func(q *application.OnlineQuery, s *application.QueryScope) { q.Limit = 1001 }},
		{"tenant", "permission_denied", func(q *application.OnlineQuery, s *application.QueryScope) { s.Context.Tenant = "other" }},
		{"policy", "permission_denied", func(q *application.OnlineQuery, s *application.QueryScope) { s.Context.Roles = nil }},
		{"namespace", "online_not_enabled", func(q *application.OnlineQuery, s *application.QueryScope) { s.Namespace = "other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			copy, s := q, scope
			test.change(&copy, &s)
			before := engine.calls
			_, err := service.Execute(t.Context(), s, copy)
			var problem *model.Problem
			if !errors.As(err, &problem) || problem.Code != test.code {
				t.Fatalf("error %v, want %s", err, test.code)
			}
			if before != engine.calls {
				t.Fatal("rejected request reached the engine")
			}
		})
	}
}

func TestOnlinePinsResolvedReleaseAndRejectsAmbiguousCode(t *testing.T) {
	service, repo, engine, source, scope, q := onlineFixture(t, nil)
	management, _ := catalog.NewService(repo)
	engine.run = func(ctx context.Context, plan model.PhysicalPlan) (model.ExecutionSnapshot, error) {
		active, _ := repo.GetActiveRelease(ctx, "demo", source.Metadata.Name)
		if active.ManifestFingerprint != plan.ManifestFingerprint {
			t.Fatal("wrong release")
		}
		return model.ExecutionSnapshot{Result: &model.TypedResult{}, DataSnapshot: &model.DataSnapshot{}}, nil
	}
	if _, err := service.Execute(t.Context(), scope, q); err != nil {
		t.Fatal(err)
	}
	source.Metadata.Name = "duplicate"
	draft, err := management.SaveDraft(t.Context(), catalog.SaveDraftInput{Namespace: "demo", Source: source, Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = management.Publish(t.Context(), "demo", source.Metadata.Name, draft.Revision, "test", "duplicate-code fixture"); err != nil {
		t.Fatal(err)
	}
	_, err = service.Execute(t.Context(), scope, q)
	var problem *model.Problem
	if !errors.As(err, &problem) || problem.Code != "metric_route_not_found" {
		t.Fatalf("duplicate namespace code not rejected: %v", err)
	}
}

func TestOnlineOverloadDoesNotQueueAndCancellationReleasesSlot(t *testing.T) {
	service, _, engine, _, scope, q := onlineFixture(t, nil)
	started := make(chan struct{})
	engine.run = func(ctx context.Context, p model.PhysicalPlan) (model.ExecutionSnapshot, error) {
		close(started)
		<-ctx.Done()
		return model.ExecutionSnapshot{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := service.Execute(ctx, scope, q); done <- err }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("query failed before engine execution: %v", err)
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("query did not reach engine")
	}
	_, err := service.Execute(t.Context(), scope, q)
	var p *model.Problem
	if !errors.As(err, &p) || p.Code != "online_overloaded" {
		t.Fatalf("overload %v", err)
	}
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel %v", err)
	}
	engine.run = nil
	if _, err = service.Execute(t.Context(), scope, q); err != nil {
		t.Fatalf("slot not released: %v", err)
	}
}

type unavailableOnlineAudit struct{}

func (unavailableOnlineAudit) Record(context.Context, audit.QueryEvent) error {
	return errors.New("unavailable")
}
func TestOnlineAuditFailurePreventsExecution(t *testing.T) {
	service, _, engine, _, scope, q := onlineFixture(t, unavailableOnlineAudit{})
	_, err := service.Execute(t.Context(), scope, q)
	if !errors.Is(err, audit.ErrUnavailable) || engine.calls != 0 {
		t.Fatalf("audit failure: %v calls=%d", err, engine.calls)
	}
}

type completionAudit struct {
	run func(context.Context) error
}

func (r completionAudit) Record(ctx context.Context, event audit.QueryEvent) error {
	if event.Kind == audit.EventQueryStarted {
		return nil
	}
	return r.run(ctx)
}

func TestOnlineCompletionAuditUsesRemainingRequestBudget(t *testing.T) {
	for _, ignoreCancellation := range []bool{false, true} {
		t.Run(map[bool]string{false: "honors cancellation", true: "late success"}[ignoreCancellation], func(t *testing.T) {
			service, _, engine, _, scope, q := onlineFixture(t, completionAudit{run: func(ctx context.Context) error {
				if ignoreCancellation {
					time.Sleep(100 * time.Millisecond)
					return nil
				}
				<-ctx.Done()
				return ctx.Err()
			}})
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer cancel()
			started := time.Now()
			out, err := service.Execute(ctx, scope, q)
			if !errors.Is(err, context.DeadlineExceeded) || out.Result != nil || engine.calls != 1 {
				t.Fatalf("result delivered after deadline: %v %#v calls=%d", err, out, engine.calls)
			}
			if !ignoreCancellation && time.Since(started) > 250*time.Millisecond {
				t.Fatal("completion audit extended request budget")
			}
		})
	}
}

func TestOnlineCompletionAuditFailureDoesNotDeliverResult(t *testing.T) {
	service, _, engine, _, scope, q := onlineFixture(t, completionAudit{run: func(context.Context) error { return errors.New("failed") }})
	out, err := service.Execute(t.Context(), scope, q)
	if !errors.Is(err, audit.ErrUnavailable) || out.Result != nil || engine.calls != 1 {
		t.Fatalf("result delivered without audit: %v %#v", err, out)
	}
}
