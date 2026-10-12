package application_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/audit"
	"github.com/marmot1024/metricspire/internal/exploration"
	"github.com/marmot1024/metricspire/internal/model"
)

func awaitAnalysis(t *testing.T, jobs *application.AnalysisJobs, in application.AnalysisInput, id string) application.AnalysisJobSnapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s, err := jobs.Get(in.Namespace, in.Context, id)
		if err != nil {
			t.Fatal(err)
		}
		if s.Status != model.JobRunning {
			return s
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("job did not finish")
	return application.AnalysisJobSnapshot{}
}

func TestAnalysisCancellationBeforeEngineIsAudited(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	memory := audit.NewMemoryRecorder()
	recorder := audit.RecorderFunc(func(ctx context.Context, e audit.QueryEvent) error {
		if e.Kind == audit.EventQueryStarted {
			close(entered)
			<-release
		}
		return memory.Record(ctx, e)
	})
	service, in := analysisServiceFixture(t, analysisEngineFunc(func(_ context.Context, p exploration.Plan) (model.ExecutionSnapshot, error) {
		calls.Add(1)
		return analysisSuccess(p), nil
	}), recorder, false, time.Minute)
	jobs, _ := application.NewAnalysisJobs(service, 1, 2, time.Minute)
	defer jobs.Close()
	job, err := jobs.Submit(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	snapshot, err := jobs.Cancel(in.Namespace, in.Context, job.ID)
	close(release)
	if err != nil || snapshot.Status != model.JobRunning || snapshot.FinishedAt != nil {
		t.Fatal("cancel prematurely finalized job")
	}
	snapshot = awaitAnalysis(t, jobs, in, job.ID)
	if snapshot.Status != model.JobCancelled || calls.Load() != 0 {
		t.Fatal("cancelled job executed or lost status")
	}
	events := memory.Events()
	if len(events) != 2 || events[1].Kind != audit.EventQueryCancelled || events[1].JobID != job.ID {
		t.Fatal("accepted cancellation lost its audit")
	}
}

func TestAnalysisTerminalAuditSealsOutcomeAndCloseWaits(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	recorder := audit.RecorderFunc(func(_ context.Context, e audit.QueryEvent) error {
		if e.Kind == audit.EventQuerySucceeded {
			close(entered)
			<-release
		}
		return nil
	})
	service, in := analysisServiceFixture(t, analysisEngineFunc(func(_ context.Context, p exploration.Plan) (model.ExecutionSnapshot, error) {
		return analysisSuccess(p), nil
	}), recorder, false, time.Minute)
	jobs, _ := application.NewAnalysisJobs(service, 1, 2, time.Minute)
	job, err := jobs.Submit(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	snapshot, err := jobs.Cancel(in.Namespace, in.Context, job.ID)
	if err != nil || snapshot.Status != model.JobRunning {
		close(release)
		jobs.Close()
		t.Fatal("late cancel changed audited outcome")
	}
	closed := make(chan struct{})
	go func() { jobs.Close(); close(closed) }()
	select {
	case <-closed:
		close(release)
		t.Fatal("close returned before terminal audit")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close failed to drain worker")
	}
	snapshot = awaitAnalysis(t, jobs, in, job.ID)
	if snapshot.Status != model.JobSucceeded || snapshot.Output == nil {
		t.Fatal("late cancellation contradicted success audit")
	}
	if _, err := jobs.Submit(context.Background(), in); err == nil {
		t.Fatal("closed manager accepted work")
	}
}

func TestAnalysisAggregateResultRetentionBudget(t *testing.T) {
	service, in := analysisServiceFixture(t, analysisEngineFunc(func(_ context.Context, p exploration.Plan) (model.ExecutionSnapshot, error) {
		return analysisSuccess(p), nil
	}), audit.NewMemoryRecorder(), false, time.Minute)
	out, err := service.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(out)
	jobs, err := application.NewAnalysisJobs(service, 1, 4, time.Minute, 2*len(data)+16)
	if err != nil {
		t.Fatal(err)
	}
	defer jobs.Close()
	ids := []string{}
	for range 3 {
		j, err := jobs.Submit(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		if s := awaitAnalysis(t, jobs, in, j.ID); s.Status != model.JobSucceeded {
			t.Fatal(s.Status)
		}
		ids = append(ids, j.ID)
	}
	if _, err := jobs.Get(in.Namespace, in.Context, ids[0]); !errors.Is(err, application.ErrAnalysisJobNotFound) {
		t.Fatal("old result retained beyond total byte budget")
	}
	for _, id := range ids[1:] {
		if _, err := jobs.Get(in.Namespace, in.Context, id); err != nil {
			t.Fatal("new result unnecessarily evicted")
		}
	}
}

func TestAnalysisRetainsLargeIntegerExactly(t *testing.T) {
	service, in := analysisServiceFixture(t, analysisEngineFunc(func(_ context.Context, p exploration.Plan) (model.ExecutionSnapshot, error) {
		out := analysisSuccess(p)
		out.Result.Rows = [][]any{make([]any, len(p.Columns))}
		for i, c := range p.Columns {
			if c.Type == model.DataTypeInteger {
				out.Result.Rows[0][i] = int64(9007199254740993)
			}
		}
		return out, nil
	}), audit.NewMemoryRecorder(), false, time.Minute)
	jobs, _ := application.NewAnalysisJobs(service, 1, 2, time.Minute)
	defer jobs.Close()
	job, err := jobs.Submit(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := awaitAnalysis(t, jobs, in, job.ID)
	encoded, err := json.Marshal(application.PublicAnalysisJob(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte("9007199254740993")) || bytes.Contains(encoded, []byte("9007199254740992")) {
		t.Fatal("integer result rounded during storage or public projection")
	}
}

func TestAnalysisNonFiniteResultsFailBeforeSuccessAudit(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		recorder := audit.NewMemoryRecorder()
		service, in := analysisServiceFixture(t, analysisEngineFunc(func(_ context.Context, p exploration.Plan) (model.ExecutionSnapshot, error) {
			out := analysisSuccess(p)
			out.Result.Rows = [][]any{make([]any, len(p.Columns))}
			for i, c := range p.Columns {
				if c.Type == model.DataTypeDecimal {
					out.Result.Rows[0][i] = value
				}
			}
			return out, nil
		}), recorder, false, time.Minute)
		jobs, _ := application.NewAnalysisJobs(service, 1, 2, time.Minute)
		job, err := jobs.Submit(context.Background(), in)
		if err != nil {
			jobs.Close()
			t.Fatal(err)
		}
		snapshot := awaitAnalysis(t, jobs, in, job.ID)
		jobs.Close()
		events := recorder.Events()
		if snapshot.Status != model.JobFailed || snapshot.Output != nil || len(events) != 2 || events[1].Kind != audit.EventQueryFailed || events[1].ErrorCode != snapshot.ErrorCode {
			t.Fatal("unserializable result disagrees with terminal audit")
		}
	}
}
