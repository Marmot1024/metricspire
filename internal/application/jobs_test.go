package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/executionauth"
	"github.com/marmot1024/metricspire/internal/model"
)

func TestJobManagerCancelsAndIsolatesJobsByPrincipal(t *testing.T) {
	executor := &blockingExecutor{started: make(chan struct{})}
	manager, err := application.NewJobManager(context.Background(), executor, time.Second, 10, func() string { return "job_cancel" })
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	input := trustedJobInput()
	submitted, err := manager.Submit(context.Background(), input)
	if err != nil || submitted.Job.Status != model.JobPending {
		t.Fatalf("Submit() = %#v, %v", submitted, err)
	}
	<-executor.started
	if executor.input.JobID != submitted.Job.ID {
		t.Fatalf("executor job ID = %q, want %q", executor.input.JobID, submitted.Job.ID)
	}
	if _, err := manager.Get("tenant", "someone-else", submitted.Job.ID); !errors.Is(err, application.ErrJobNotFound) {
		t.Fatalf("cross-principal Get() error = %v", err)
	}
	if _, err := manager.Cancel("tenant", "alice", submitted.Job.ID); err != nil {
		t.Fatal(err)
	}
	finished := awaitManagedJob(t, manager, "tenant", "alice", submitted.Job.ID)
	if finished.Job.Status != model.JobCancelled || finished.Job.Error == nil || finished.Job.Error.Code != "cancelled" {
		t.Fatalf("cancelled job = %#v", finished)
	}
}

func TestJobManagerEnforcesServerTimeout(t *testing.T) {
	executor := &blockingExecutor{started: make(chan struct{})}
	manager, err := application.NewJobManager(context.Background(), executor, 10*time.Millisecond, 10, func() string { return "job_timeout" })
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	submitted, err := manager.Submit(context.Background(), trustedJobInput())
	if err != nil {
		t.Fatal(err)
	}
	finished := awaitManagedJob(t, manager, "tenant", "alice", submitted.Job.ID)
	if finished.Job.Status != model.JobCancelled || finished.Job.Error == nil || finished.Job.Error.Code != "timeout" {
		t.Fatalf("timed out job = %#v", finished)
	}
}

func TestJobManagerCloseWaitsForCancellationAndRejectsNewJobs(t *testing.T) {
	executor := &blockingExecutor{started: make(chan struct{})}
	manager, err := application.NewJobManager(context.Background(), executor, time.Second, 10, func() string { return "job_close" })
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := manager.Submit(context.Background(), trustedJobInput())
	if err != nil {
		t.Fatal(err)
	}
	<-executor.started
	manager.Close()
	finished, err := manager.Get("tenant", "alice", submitted.Job.ID)
	if err != nil || finished.Job.Status != model.JobCancelled {
		t.Fatalf("job after Close = %#v, %v", finished, err)
	}
	if _, err := manager.Submit(context.Background(), trustedJobInput()); err == nil {
		t.Fatal("closed job manager accepted a new job")
	}
}

func TestJobManagerDoesNotExposeEngineErrorDetails(t *testing.T) {
	manager, err := application.NewJobManager(context.Background(), problemExecutor{}, time.Second, 10, func() string { return "job_problem" })
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	submitted, err := manager.Submit(context.Background(), trustedJobInput())
	if err != nil {
		t.Fatal(err)
	}
	finished := awaitManagedJob(t, manager, "tenant", "alice", submitted.Job.ID)
	if finished.Job.Error == nil || finished.Job.Error.Code != "engine_bad_request" ||
		strings.Contains(finished.Job.Error.Message, "secret_table") {
		t.Fatalf("public job error = %#v", finished.Job.Error)
	}
}

func TestJobManagerPropagatesOnlyRequestExecutionToken(t *testing.T) {
	executor := &tokenExecutor{observed: make(chan string, 1)}
	manager, err := application.NewJobManager(context.Background(), executor, time.Second, 10, func() string { return "job_token" })
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	submission := context.WithValue(context.Background(), unrelatedJobKey{}, "do-not-copy")
	submission, err = executionauth.WithAccessToken(submission, "short-lived-user-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Submit(submission, trustedJobInput()); err != nil {
		t.Fatal(err)
	}
	select {
	case observed := <-executor.observed:
		if observed != "short-lived-user-token" {
			t.Fatalf("execution token = %q", observed)
		}
	case <-time.After(time.Second):
		t.Fatal("executor did not observe request token")
	}
	if executor.unrelated != nil {
		t.Fatalf("unrelated request value leaked into job context: %#v", executor.unrelated)
	}
}

type blockingExecutor struct {
	started chan struct{}
	input   application.QueryInput
}

type problemExecutor struct{}

type tokenExecutor struct {
	observed  chan string
	unrelated any
}

type unrelatedJobKey struct{}

func (problemExecutor) ExecuteActive(context.Context, application.QueryInput) (application.QueryOutput, error) {
	return application.QueryOutput{}, &model.Problem{Code: "engine_bad_request", Message: "SELECT * FROM secret_table"}
}

func (executor *tokenExecutor) ExecuteActive(ctx context.Context, _ application.QueryInput) (application.QueryOutput, error) {
	token, _ := executionauth.AccessToken(ctx)
	executor.unrelated = ctx.Value(unrelatedJobKey{})
	executor.observed <- token
	return application.QueryOutput{}, nil
}

func (executor *blockingExecutor) ExecuteActive(ctx context.Context, input application.QueryInput) (application.QueryOutput, error) {
	executor.input = input
	close(executor.started)
	<-ctx.Done()
	return application.QueryOutput{Execution: model.ExecutionSnapshot{Job: model.ExecutionJob{Status: model.JobCancelled}}}, ctx.Err()
}

func trustedJobInput() application.QueryInput {
	return application.QueryInput{QueryScope: application.QueryScope{
		Namespace: "demo", ModelName: "commerce",
		Context: model.RequestContext{Tenant: "tenant", Principal: "alice", RequestID: "request"},
	}}
}

func awaitManagedJob(t *testing.T, manager *application.JobManager, tenant, principal, id string) application.QueryJobSnapshot {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		snapshot, err := manager.Get(tenant, principal, id)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Job.Status == model.JobSucceeded || snapshot.Job.Status == model.JobFailed || snapshot.Job.Status == model.JobCancelled {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not finish: %#v", snapshot)
		}
		time.Sleep(time.Millisecond)
	}
}
