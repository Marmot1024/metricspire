package application_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/adapter/databricks"
	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/audit"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/executionauth"
	"github.com/marmot1024/metricspire/internal/exploration"
	"github.com/marmot1024/metricspire/internal/model"
)

type analysisEngineFunc func(context.Context, exploration.Plan) (model.ExecutionSnapshot, error)

func (f analysisEngineFunc) Execute(ctx context.Context, p exploration.Plan) (model.ExecutionSnapshot, error) {
	return f(ctx, p)
}

type analysisPlanningEngine struct{}

func (analysisPlanningEngine) Capabilities() model.EngineCapabilities {
	return databricks.Capabilities()
}
func (analysisPlanningEngine) Execute(context.Context, model.PhysicalPlan) (model.ExecutionSnapshot, error) {
	panic("analysis must never execute through the metric query engine")
}

func analysisServiceFixture(t *testing.T, engine application.AnalysisEngine, recorder audit.Recorder, deny bool, timeout time.Duration) (*application.AnalysisService, application.AnalysisInput) {
	t.Helper()
	root := filepath.Join("..", "..", "testdata", "exploration")
	var source model.SemanticSource
	var binding model.SourceBinding
	var q exploration.Query
	read(t, filepath.Join(root, "model.json"), &source)
	read(t, filepath.Join(root, "binding.json"), &binding)
	read(t, filepath.Join(root, "query.json"), &q)
	repository := catalog.NewMemoryRepository()
	cs, _ := catalog.NewService(repository)
	draft, err := cs.SaveDraft(context.Background(), catalog.SaveDraftInput{Namespace: "demo", Source: source, Actor: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cs.Publish(context.Background(), "demo", source.Metadata.Name, draft.Revision, "owner", "neutral fixture"); err != nil {
		t.Fatal(err)
	}
	policy := model.PolicySource{APIVersion: model.APIVersion, Kind: model.KindPolicySource, Metadata: model.Metadata{Name: "demo", Version: "1"}, Tenant: "demo", Rules: []model.PolicyRule{{Name: "read", Effect: model.EffectAllow, Roles: []string{"analyst"}, Metrics: []string{"*"}, Dimensions: []string{"*"}}}}
	if deny {
		policy.Rules = append(policy.Rules, model.PolicyRule{Name: "private_person", Effect: model.EffectDeny, Roles: []string{"analyst"}, Metrics: []string{"*"}, Dimensions: []string{"person"}})
	}
	policyResolver := application.PolicyResolverFunc(func(ctx context.Context, _ application.QueryScope, _ catalog.Release) (model.PolicySource, error) {
		assertNoExecutionToken(t, ctx, "analysis policy")
		return policy, nil
	})
	binding.ManifestFingerprint = ""
	bindingResolver := application.BindingResolverFunc(func(ctx context.Context, _ application.QueryScope, _ catalog.Release) (model.SourceBinding, error) {
		assertNoExecutionToken(t, ctx, "analysis binding")
		return binding, nil
	})
	queries, err := application.NewQueryService(repository, policyResolver, bindingResolver, analysisPlanningEngine{}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	sources := application.AnalysisSourceResolverFunc(func(ctx context.Context, ns string, rc model.RequestContext, code string) (application.AnalysisSourceBinding, error) {
		assertNoExecutionToken(t, ctx, "analysis source resolver")
		if ns != "demo" || rc.Tenant != "demo" || code != "activity" {
			return application.AnalysisSourceBinding{}, application.ErrResolutionNotFound
		}
		return application.AnalysisSourceBinding{ModelName: source.Metadata.Name, Source: exploration.Source{Name: "activity", AnchorMetric: "active_events", Dimensions: []string{"person", "day", "tier"}, Roles: []string{"analyst"}, MaxWindowDays: 28, MaxRows: 100, Timeout: timeout}}, nil
	})
	service, err := application.NewAnalysisService(queries, sources, engine, recorder)
	if err != nil {
		t.Fatal(err)
	}
	in := application.AnalysisInput{Namespace: "demo", Context: model.RequestContext{Tenant: "demo", Principal: "reader", Roles: []string{"analyst"}, RequestID: "analysis-fixture"}, Query: q}
	return service, in
}

func analysisSuccess(p exploration.Plan) model.ExecutionSnapshot {
	columns := []model.ResultColumn{}
	for _, c := range p.Columns {
		typeName := map[model.DataType]string{model.DataTypeString: "STRING", model.DataTypeDate: "DATE", model.DataTypeInteger: "BIGINT", model.DataTypeDecimal: "DOUBLE", model.DataTypeBoolean: "BOOLEAN", model.DataTypeTimestamp: "TIMESTAMP"}[c.Type]
		columns = append(columns, model.ResultColumn{Name: c.Name, TypeName: typeName, TypeText: typeName})
	}
	return model.ExecutionSnapshot{Job: model.ExecutionJob{Status: model.JobSucceeded, PhysicalFingerprint: p.Fingerprint}, Result: &model.TypedResult{Columns: columns, Rows: [][]any{}}}
}

func TestAnalysisAuthorizationTokenIsolationAndFailClosedAudit(t *testing.T) {
	ctx, err := executionauth.WithAccessToken(context.Background(), "already-authorized-token")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	recorder := audit.NewMemoryRecorder()
	engine := analysisEngineFunc(func(ctx context.Context, p exploration.Plan) (model.ExecutionSnapshot, error) {
		calls.Add(1)
		if token, ok := executionauth.AccessToken(ctx); !ok || token != "already-authorized-token" {
			t.Error("existing token did not reach analysis engine")
		}
		return analysisSuccess(p), nil
	})
	service, in := analysisServiceFixture(t, engine, tokenCheckingRecorder{t: t, recorder: recorder}, false, time.Minute)
	p, err := service.Plan(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("planning executed data request")
	}
	output, err := service.Execute(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if output.Plan.Fingerprint != p.Plan.Fingerprint || calls.Load() != 1 || len(recorder.Events()) != 2 {
		t.Fatal("lost pinned plan, engine invocation or audit")
	}
	recorder.SetError(errors.New("offline"))
	if _, err = service.Execute(ctx, in); !errors.Is(err, audit.ErrUnavailable) {
		t.Fatalf("audit admission error: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("executed without audit admission")
	}
	denied, in := analysisServiceFixture(t, engine, audit.NewMemoryRecorder(), true, time.Minute)
	if _, err = denied.Execute(ctx, in); err == nil {
		t.Fatal("dimension deny bypassed")
	}
	if calls.Load() != 1 {
		t.Fatal("denial invoked engine")
	}
	in.Context.Roles = nil
	if _, err = service.Plan(ctx, in); err == nil {
		t.Fatal("source role gate bypassed")
	}
}

func TestAnalysisWithholdsIncompleteAndUnauditedResults(t *testing.T) {
	for _, mode := range []string{"truncated", "wrong_fingerprint", "wrong_schema", "wrong_type", "audit_completion", "late_success"} {
		t.Run(mode, func(t *testing.T) {
			recorder := audit.NewMemoryRecorder()
			engine := analysisEngineFunc(func(ctx context.Context, p exploration.Plan) (model.ExecutionSnapshot, error) {
				out := analysisSuccess(p)
				switch mode {
				case "truncated":
					out.Result.Truncated = true
				case "wrong_fingerprint":
					out.Job.PhysicalFingerprint = "other"
				case "wrong_schema":
					out.Result.Columns[0].Name = "other"
				case "wrong_type":
					out.Result.Columns[0].TypeName = "BINARY"
				case "audit_completion":
					recorder.SetError(errors.New("offline"))
				case "late_success":
					<-ctx.Done()
				}
				return out, nil
			})
			timeout := time.Minute
			if mode == "late_success" {
				timeout = 20 * time.Millisecond
			}
			service, in := analysisServiceFixture(t, engine, recorder, false, timeout)
			out, err := service.Execute(context.Background(), in)
			if err == nil || out.Execution.Result != nil {
				t.Fatal("invalid result released")
			}
		})
	}
}

func TestAnalysisAsyncOwnershipBudgetsAndCancellation(t *testing.T) {
	entered := make(chan struct{})
	stopped := make(chan struct{})
	engine := analysisEngineFunc(func(ctx context.Context, p exploration.Plan) (model.ExecutionSnapshot, error) {
		close(entered)
		<-ctx.Done()
		close(stopped)
		return model.ExecutionSnapshot{}, ctx.Err()
	})
	service, in := analysisServiceFixture(t, engine, audit.NewMemoryRecorder(), false, time.Minute)
	jobs, err := application.NewAnalysisJobs(service, 1, 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer jobs.Close()
	ctx, cancel := context.WithCancel(context.Background())
	job, err := jobs.Submit(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	cancel() // Disconnecting the submit request must not cancel accepted work.
	select {
	case <-stopped:
		t.Fatal("request cancellation stopped accepted job")
	default:
	}
	if _, err = jobs.Submit(context.Background(), in); err == nil {
		t.Fatal("concurrency budget bypassed")
	}
	other := in.Context
	other.Principal = "other"
	if _, err = jobs.Get(in.Namespace, other, job.ID); err == nil {
		t.Fatal("other principal read job")
	}
	if _, err = jobs.Cancel(in.Namespace, other, job.ID); err == nil {
		t.Fatal("other principal cancelled job")
	}
	other = in.Context
	other.Tenant = "other"
	if _, err = jobs.Get(in.Namespace, other, job.ID); err == nil {
		t.Fatal("cross-tenant job read")
	}
	if _, err = jobs.Get("other", in.Context, job.ID); err == nil {
		t.Fatal("cross-namespace job read")
	}
	snapshot, err := jobs.Cancel(in.Namespace, in.Context, job.ID)
	if err != nil || snapshot.Status != model.JobCancelled || snapshot.Output != nil {
		t.Fatal("cancel failed")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not reach engine")
	}
}

func TestAnalysisAsyncCompletesWithImmutableOwnerSnapshot(t *testing.T) {
	service, in := analysisServiceFixture(t, analysisEngineFunc(func(ctx context.Context, p exploration.Plan) (model.ExecutionSnapshot, error) {
		if token, ok := executionauth.AccessToken(ctx); !ok || token != "async-fixture-token" {
			t.Error("accepted worker lost existing execution token")
		}
		return analysisSuccess(p), nil
	}), audit.NewMemoryRecorder(), false, time.Minute)
	jobs, _ := application.NewAnalysisJobs(service, 1, 2, time.Minute)
	defer jobs.Close()
	ctx, err := executionauth.WithAccessToken(context.Background(), "async-fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	job, err := jobs.Submit(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		snapshot, err := jobs.Get(in.Namespace, in.Context, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Status == model.JobSucceeded {
			if snapshot.Output == nil || snapshot.Output.Plan.Fingerprint != job.Fingerprint {
				t.Fatal("async output lost plan")
			}
			snapshot.Output.Plan.Query.Source = "mutated"
			again, _ := jobs.Get(in.Namespace, in.Context, job.ID)
			if again.Output.Plan.Query.Source != "activity" {
				t.Fatal("caller mutated stored job")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not complete: %#v", snapshot)
		}
		time.Sleep(time.Millisecond)
	}
}
