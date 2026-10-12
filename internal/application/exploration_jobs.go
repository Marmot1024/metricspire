package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/marmot1024/metricspire/internal/executionauth"
	"github.com/marmot1024/metricspire/internal/model"
)

// AnalysisJobs is a bounded single-process manager, not a durable queue. It
// is deliberately separate from the online query manager and its contracts.
type AnalysisJobs struct {
	service  *AnalysisService
	mu       sync.Mutex
	jobs     map[string]*analysisJob
	slots    chan struct{}
	capacity int
	ttl      time.Duration
	closed   bool
}

var ErrAnalysisJobNotFound = errors.New("analysis job not found")

type AnalysisJobSnapshot struct {
	ID          string          `json:"id"`
	Status      model.JobStatus `json:"status"`
	Fingerprint string          `json:"fingerprint"`
	SubmittedAt time.Time       `json:"submitted_at"`
	FinishedAt  *time.Time      `json:"finished_at,omitempty"`
	ErrorCode   string          `json:"error_code,omitempty"`
	Output      *AnalysisOutput `json:"output,omitempty"`
}

type analysisJob struct {
	snapshot                     AnalysisJobSnapshot
	namespace, tenant, principal string
	cancel                       context.CancelFunc
}

func NewAnalysisJobs(service *AnalysisService, concurrency, capacity int, ttl time.Duration) (*AnalysisJobs, error) {
	if service == nil || concurrency < 1 || concurrency > 32 || capacity < concurrency || capacity > 1000 || ttl <= 0 || ttl > 24*time.Hour {
		return nil, errors.New("invalid analysis job manager budgets")
	}
	return &AnalysisJobs{service: service, jobs: map[string]*analysisJob{}, slots: make(chan struct{}, concurrency), capacity: capacity, ttl: ttl}, nil
}

func (m *AnalysisJobs) Submit(ctx context.Context, in AnalysisInput) (AnalysisJobSnapshot, error) {
	start := time.Now()
	select {
	case m.slots <- struct{}{}:
	default:
		return AnalysisJobSnapshot{}, &model.Problem{Code: "analysis_capacity", Message: "analysis concurrency budget exceeded"}
	}
	reserved := true
	defer func() {
		if reserved {
			<-m.slots
		}
	}()
	// Authorize and pin the active release before returning a job ID.
	p, err := m.service.Plan(ctx, in)
	if err != nil {
		return AnalysisJobSnapshot{}, err
	}
	if err = ctx.Err(); err != nil {
		return AnalysisJobSnapshot{}, err
	}
	var bytes [16]byte
	if _, err = rand.Read(bytes[:]); err != nil {
		return AnalysisJobSnapshot{}, err
	}
	id := hex.EncodeToString(bytes[:])
	worker, cancel := context.WithDeadline(context.Background(), start.Add(p.Plan.Source.Timeout))
	worker = executionauth.PropagateAccessToken(worker, ctx)
	// Detach all caller-owned values. Plan has already been deep-copied.
	in = cloneResolverValue(in)
	job := &analysisJob{snapshot: AnalysisJobSnapshot{ID: id, Status: model.JobRunning, Fingerprint: p.Plan.Fingerprint, SubmittedAt: start}, namespace: in.Namespace, tenant: in.Context.Tenant, principal: in.Context.Principal, cancel: cancel}
	m.mu.Lock()
	for key, j := range m.jobs {
		if j.snapshot.FinishedAt != nil && time.Since(*j.snapshot.FinishedAt) >= m.ttl {
			delete(m.jobs, key)
		}
	}
	if m.closed || len(m.jobs) >= m.capacity {
		m.mu.Unlock()
		cancel()
		return AnalysisJobSnapshot{}, &model.Problem{Code: "analysis_capacity", Message: "analysis job storage unavailable or full"}
	}
	m.jobs[id] = job
	snapshot := cloneResolverValue(job.snapshot)
	m.mu.Unlock()
	reserved = false
	go func() {
		defer func() { cancel(); <-m.slots }()
		output, err := m.service.executePlanned(worker, in, p, id)
		m.mu.Lock()
		defer m.mu.Unlock()
		if job.snapshot.Status == model.JobCancelled {
			return
		}
		now := time.Now()
		job.snapshot.FinishedAt = &now
		if err != nil {
			job.snapshot.Status = model.JobFailed
			job.snapshot.ErrorCode = errorCode(err)
			if errors.Is(err, context.Canceled) {
				job.snapshot.Status = model.JobCancelled
			}
		} else {
			job.snapshot.Status = model.JobSucceeded
			job.snapshot.Output = &output
		}
	}()
	return snapshot, nil
}

// The caller context is trusted transport identity. Errors intentionally do
// not distinguish missing, expired, and another principal's jobs.
func (m *AnalysisJobs) Get(namespace string, rc model.RequestContext, id string) (AnalysisJobSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, err := m.owned(namespace, rc, id)
	if err != nil {
		return AnalysisJobSnapshot{}, err
	}
	return cloneResolverValue(j.snapshot), nil
}

func (m *AnalysisJobs) Cancel(namespace string, rc model.RequestContext, id string) (AnalysisJobSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, err := m.owned(namespace, rc, id)
	if err != nil {
		return AnalysisJobSnapshot{}, err
	}
	if j.snapshot.Status == model.JobRunning {
		j.cancel()
		now := time.Now()
		j.snapshot.Status = model.JobCancelled
		j.snapshot.FinishedAt = &now
		j.snapshot.ErrorCode = "cancelled"
		j.snapshot.Output = nil
	}
	return cloneResolverValue(j.snapshot), nil
}

func (m *AnalysisJobs) owned(namespace string, rc model.RequestContext, id string) (*analysisJob, error) {
	j, ok := m.jobs[id]
	if !ok || rc.Tenant == "" || rc.Principal == "" || j.namespace != namespace || j.tenant != rc.Tenant || j.principal != rc.Principal || j.snapshot.FinishedAt != nil && time.Since(*j.snapshot.FinishedAt) >= m.ttl {
		return nil, ErrAnalysisJobNotFound
	}
	return j, nil
}

func (m *AnalysisJobs) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for _, j := range m.jobs {
		if j.snapshot.Status == model.JobRunning {
			j.cancel()
		}
	}
}
