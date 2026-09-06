package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/marmot1024/metricspire/internal/audit"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/executionauth"
	"github.com/marmot1024/metricspire/internal/model"
)

var (
	ErrJobNotFound = errors.New("query job not found")
	ErrJobFinished = errors.New("query job is already finished")
)

type ActiveQueryExecutor interface {
	ExecuteActive(context.Context, QueryInput) (QueryOutput, error)
}

type QueryJobSnapshot struct {
	Job                  model.ExecutionJob  `json:"job"`
	ReleaseID            string              `json:"release_id,omitempty"`
	ManifestFingerprint  string              `json:"manifest_fingerprint,omitempty"`
	ResolvedTimeRange    *model.TimeRange    `json:"resolved_time_range,omitempty"`
	ResolvedTimeGrouping *model.TimeGrouping `json:"resolved_time_grouping,omitempty"`
	Result               *model.TypedResult  `json:"result,omitempty"`
}

type JobManager struct {
	mu       sync.RWMutex
	wait     sync.WaitGroup
	parent   context.Context
	executor ActiveQueryExecutor
	timeout  time.Duration
	maxJobs  int
	newID    func() string
	jobs     map[string]*managedJob
	order    []string
	closed   bool
}

type managedJob struct {
	tenant    string
	principal string
	snapshot  QueryJobSnapshot
	cancel    context.CancelFunc
}

func NewJobManager(parent context.Context, executor ActiveQueryExecutor, timeout time.Duration, maxJobs int, idGenerator func() string) (*JobManager, error) {
	if parent == nil || executor == nil {
		return nil, errors.New("job parent context and query executor are required")
	}
	if timeout <= 0 || maxJobs < 1 {
		return nil, errors.New("job timeout and retention limit must be positive")
	}
	if idGenerator == nil {
		idGenerator = newJobID
	}
	return &JobManager{
		parent: parent, executor: executor, timeout: timeout, maxJobs: maxJobs,
		newID: idGenerator, jobs: make(map[string]*managedJob),
	}, nil
}

func (manager *JobManager) Submit(submissionContext context.Context, input QueryInput) (QueryJobSnapshot, error) {
	if submissionContext == nil {
		return QueryJobSnapshot{}, errors.New("job submission context is required")
	}
	if input.Context.Tenant == "" || input.Context.Principal == "" || input.Context.RequestID == "" {
		return QueryJobSnapshot{}, errors.New("trusted request context is incomplete")
	}
	now := time.Now().UTC()
	id := manager.newID()
	jobParent := executionauth.PropagateAccessToken(manager.parent, submissionContext)
	ctx, cancel := context.WithTimeout(jobParent, manager.timeout)
	job := &managedJob{
		tenant: input.Context.Tenant, principal: input.Context.Principal, cancel: cancel,
		snapshot: QueryJobSnapshot{Job: model.ExecutionJob{ID: id, Status: model.JobPending, SubmittedAt: now}},
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		cancel()
		return QueryJobSnapshot{}, errors.New("query job manager is closed")
	}
	if _, exists := manager.jobs[id]; exists {
		manager.mu.Unlock()
		cancel()
		return QueryJobSnapshot{}, errors.New("job ID generator returned a duplicate")
	}
	manager.evictFinishedLocked()
	if len(manager.jobs) >= manager.maxJobs {
		manager.mu.Unlock()
		cancel()
		return QueryJobSnapshot{}, errors.New("query job capacity is exhausted")
	}
	manager.jobs[id] = job
	manager.order = append(manager.order, id)
	manager.wait.Add(1)
	input.JobID = id
	snapshot := cloneJob(job.snapshot)
	manager.mu.Unlock()
	go func() {
		defer manager.wait.Done()
		defer cancel()
		manager.run(ctx, id, input)
	}()
	return snapshot, nil
}

func (manager *JobManager) Get(tenant, principal, id string) (QueryJobSnapshot, error) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	job, exists := manager.jobs[id]
	if !exists || job.tenant != tenant || job.principal != principal {
		return QueryJobSnapshot{}, ErrJobNotFound
	}
	return cloneJob(job.snapshot), nil
}

func (manager *JobManager) Cancel(tenant, principal, id string) (QueryJobSnapshot, error) {
	manager.mu.Lock()
	job, exists := manager.jobs[id]
	if !exists || job.tenant != tenant || job.principal != principal {
		manager.mu.Unlock()
		return QueryJobSnapshot{}, ErrJobNotFound
	}
	if terminal(job.snapshot.Job.Status) {
		snapshot := cloneJob(job.snapshot)
		manager.mu.Unlock()
		return snapshot, ErrJobFinished
	}
	if job.cancel != nil {
		job.cancel()
	}
	snapshot := cloneJob(job.snapshot)
	manager.mu.Unlock()
	return snapshot, nil
}

func (manager *JobManager) Close() {
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		manager.wait.Wait()
		return
	}
	manager.closed = true
	for _, job := range manager.jobs {
		if job.cancel != nil {
			job.cancel()
		}
	}
	manager.mu.Unlock()
	manager.wait.Wait()
}

func (manager *JobManager) run(ctx context.Context, id string, input QueryInput) {
	started := time.Now().UTC()
	manager.mu.Lock()
	job, exists := manager.jobs[id]
	if !exists {
		manager.mu.Unlock()
		return
	}
	job.snapshot.Job.Status = model.JobRunning
	job.snapshot.Job.StartedAt = &started
	manager.mu.Unlock()

	output, err := manager.executor.ExecuteActive(ctx, input)
	finished := time.Now().UTC()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	job, exists = manager.jobs[id]
	if !exists {
		return
	}
	job.snapshot.Job.FinishedAt = &finished
	// Release the cancel closure once the job is terminal. In Apps user-
	// authorization mode it closes over the short-lived execution token.
	job.cancel = nil
	job.snapshot.Job.PhysicalFingerprint = output.Physical.Fingerprint
	job.snapshot.Job.RowLimit = int64(output.Physical.Limit)
	job.snapshot.ReleaseID = output.Release.ID
	job.snapshot.ManifestFingerprint = output.Release.ManifestFingerprint
	if output.Logical.TimeRange != nil {
		value := *output.Logical.TimeRange
		job.snapshot.ResolvedTimeRange = &value
	}
	if output.Logical.TimeGrouping != nil {
		value := *output.Logical.TimeGrouping
		job.snapshot.ResolvedTimeGrouping = &value
	}
	if err == nil {
		job.snapshot.Job.Status = model.JobSucceeded
		job.snapshot.Result = output.Execution.Result
		return
	}
	problem := jobProblem(err)
	job.snapshot.Job.Error = &problem
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || output.Execution.Job.Status == model.JobCancelled {
		job.snapshot.Job.Status = model.JobCancelled
		return
	}
	job.snapshot.Job.Status = model.JobFailed
}

func (manager *JobManager) evictFinishedLocked() {
	if len(manager.jobs) < manager.maxJobs {
		return
	}
	kept := manager.order[:0]
	for _, id := range manager.order {
		job := manager.jobs[id]
		if len(manager.jobs) >= manager.maxJobs && terminal(job.snapshot.Job.Status) {
			delete(manager.jobs, id)
			continue
		}
		kept = append(kept, id)
	}
	manager.order = kept
}

func terminal(status model.JobStatus) bool {
	return status == model.JobSucceeded || status == model.JobFailed || status == model.JobCancelled
}

func jobProblem(err error) model.Problem {
	var domain *model.Problem
	if errors.As(err, &domain) {
		return publicJobProblem(*domain)
	}
	switch {
	case errors.Is(err, audit.ErrUnavailable):
		return model.Problem{Code: "audit_unavailable", Message: "query audit is unavailable"}
	case errors.Is(err, catalog.ErrNotFound):
		return model.Problem{Code: "not_found", Message: "no active semantic model release was found"}
	case errors.Is(err, context.DeadlineExceeded):
		return model.Problem{Code: "timeout", Message: "query exceeded its server deadline"}
	case errors.Is(err, context.Canceled):
		return model.Problem{Code: "cancelled", Message: "query was cancelled"}
	default:
		return model.Problem{Code: "query_failed", Message: "query execution failed"}
	}
}

func publicJobProblem(problem model.Problem) model.Problem {
	code := problem.Code
	if code == "engine_http_error" {
		// The Databricks adapter constructs this message only from an HTTP
		// status and a strictly bounded uppercase error code. Preserve that
		// operational signal without exposing the upstream response body.
		return model.Problem{Code: code, Message: problem.Message}
	}
	if strings.HasPrefix(code, "engine_") || code == "invalid_engine_response" ||
		code == "result_truncated" || code == "result_unavailable" || code == "invalid_physical_plan" ||
		code == "invalid_identifier" || code == "unsupported_resource" || code == "metric_cycle" {
		message := "analytical query execution failed"
		if code == "engine_limit_exceeded" || code == "engine_response_too_large" || code == "result_truncated" {
			message = "analytical query exceeded a configured result limit"
		}
		return model.Problem{Code: code, Message: message}
	}
	return problem
}

func cloneJob(value QueryJobSnapshot) QueryJobSnapshot {
	result := value
	if value.ResolvedTimeRange != nil {
		timeRange := *value.ResolvedTimeRange
		result.ResolvedTimeRange = &timeRange
	}
	if value.ResolvedTimeGrouping != nil {
		grouping := *value.ResolvedTimeGrouping
		result.ResolvedTimeGrouping = &grouping
	}
	if value.Job.Error != nil {
		problem := *value.Job.Error
		result.Job.Error = &problem
	}
	if value.Result != nil {
		result.Result = &model.TypedResult{
			Columns: append([]model.ResultColumn(nil), value.Result.Columns...),
			Rows:    make([][]any, len(value.Result.Rows)), Truncated: value.Result.Truncated,
		}
		for index := range value.Result.Rows {
			result.Result.Rows[index] = append([]any(nil), value.Result.Rows[index]...)
		}
	}
	return result
}

func newJobID() string {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return fmt.Sprintf("job_%d", time.Now().UnixNano())
	}
	return "job_" + hex.EncodeToString(data)
}
