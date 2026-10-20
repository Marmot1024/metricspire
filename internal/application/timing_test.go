package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/audit"
	"github.com/marmot1024/metricspire/internal/model"
)

type timedRecorder struct{ fail bool }

func (r timedRecorder) Record(_ context.Context, _ audit.QueryEvent) error {
	time.Sleep(3 * time.Millisecond)
	if r.fail {
		return errors.New("unavailable")
	}
	return nil
}

func TestOnlineTimingsSeparateEngineAndDurableAudit(t *testing.T) {
	service, _, engine, _, scope, q := onlineFixture(t, timedRecorder{})
	engine.run = func(context.Context, model.PhysicalPlan) (model.ExecutionSnapshot, error) {
		time.Sleep(10 * time.Millisecond)
		return model.ExecutionSnapshot{Result: &model.TypedResult{}, DataSnapshot: &model.DataSnapshot{}}, nil
	}
	out, err := service.Execute(t.Context(), scope, q)
	if err != nil {
		t.Fatal(err)
	}
	v := out.Timings
	if v == nil || v.EngineMS < 9 || v.AuditMS < 5 || v.ServiceMS < v.EngineMS+v.AuditMS+v.PlanningMS {
		t.Fatalf("invalid decomposition: %+v", v)
	}
	service, _, engine, _, scope, q = onlineFixture(t, timedRecorder{fail: true})
	_, err = service.Execute(t.Context(), scope, q)
	if err == nil || engine.calls != 0 {
		t.Fatal("audit failure reached engine")
	}
}

func TestQueryTimingsReachJobsAndSnapshotsAreIndependent(t *testing.T) {
	f := newFastFixture(t)
	manager, err := application.NewJobManager(t.Context(), f.queries, time.Second, 10, func() string { return "timed_job" })
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	submitted, err := manager.Submit(t.Context(), f.input)
	if err != nil {
		t.Fatal(err)
	}
	out := awaitManagedJob(t, manager, f.input.Context.Tenant, f.input.Context.Principal, submitted.Job.ID)
	if out.Job.Status != model.JobSucceeded || out.Timings == nil || out.Timings.ServiceMS <= 0 {
		t.Fatalf("missing timings: %+v", out)
	}
	original := *out.Timings
	out.Timings.EngineMS = -1
	copy, err := manager.Get(f.input.Context.Tenant, f.input.Context.Principal, submitted.Job.ID)
	if err != nil || *copy.Timings != original {
		t.Fatal("caller changed stored timings")
	}
	f.input.Query.Metrics = nil
	failed, err := f.queries.ExecuteActive(t.Context(), f.input)
	if err == nil || failed.Timings == nil || failed.Timings.EngineMS != 0 {
		t.Fatal("planning failure lost timing or executed")
	}
}
