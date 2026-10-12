package application

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/marmot1024/metricspire/internal/executionauth"
	"github.com/marmot1024/metricspire/internal/model"
)

// AnalysisJobs is a bounded single-process manager, not a durable queue. It
// is deliberately separate from the online query manager and its contracts.
type AnalysisJobs struct {
	service       *AnalysisService
	mu            sync.Mutex
	jobs          map[string]*analysisJob
	slots         chan struct{}
	capacity      int
	ttl           time.Duration
	closed        bool
	wait          sync.WaitGroup
	retainedBytes int
	resultBudget  int
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
	finishing                    bool
	output                       []byte
}

func NewAnalysisJobs(service *AnalysisService, concurrency, capacity int, ttl time.Duration, resultBytes ...int) (*AnalysisJobs, error) {
	if service == nil || concurrency < 1 || concurrency > 32 || capacity < concurrency || capacity > 1000 || ttl <= 0 || ttl > 24*time.Hour {
		return nil, errors.New("invalid analysis job manager budgets")
	}
	budget := 32 << 20
	if len(resultBytes) > 1 {
		return nil, errors.New("invalid analysis result byte budget")
	}
	if len(resultBytes) == 1 {
		budget = resultBytes[0]
	}
	if budget < 1 || budget > 256<<20 {
		return nil, errors.New("invalid analysis result byte budget")
	}
	return &AnalysisJobs{resultBudget: budget, service: service, jobs: map[string]*analysisJob{}, slots: make(chan struct{}, concurrency), capacity: capacity, ttl: ttl}, nil
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
			m.remove(key)
		}
	}
	if m.closed || len(m.jobs) >= m.capacity {
		m.mu.Unlock()
		cancel()
		return AnalysisJobSnapshot{}, &model.Problem{Code: "analysis_capacity", Message: "analysis job storage unavailable or full"}
	}
	m.jobs[id] = job
	m.wait.Add(1)
	snapshot := cloneResolverValue(job.snapshot)
	m.mu.Unlock()
	reserved = false
	go func() {
		defer m.wait.Done()
		defer func() { cancel(); <-m.slots }()
		output, err := m.service.executePlanned(worker, in, p, id, func(err error) error {
			m.mu.Lock()
			defer m.mu.Unlock()
			job.finishing = true
			if worker.Err() != nil {
				return worker.Err()
			}
			return err
		})
		var encoded []byte
		if err == nil {
			encoded, err = json.Marshal(output)
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		job.cancel = nil
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
			// Retain encoded results to bound stored bytes, not an unbounded
			// collection of decoded row maps. TTL is an upper retention limit.
			for m.retainedBytes+len(encoded) > m.resultBudget {
				oldest := ""
				for key, candidate := range m.jobs {
					if len(candidate.output) > 0 && (oldest == "" || candidate.snapshot.FinishedAt.Before(*m.jobs[oldest].snapshot.FinishedAt)) {
						oldest = key
					}
				}
				if oldest == "" {
					m.remove(id)
					return
				}
				m.remove(oldest)
			}
			job.output = encoded
			m.retainedBytes += len(encoded)
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
	return m.snapshot(j)
}

func (m *AnalysisJobs) Cancel(namespace string, rc model.RequestContext, id string) (AnalysisJobSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, err := m.owned(namespace, rc, id)
	if err != nil {
		return AnalysisJobSnapshot{}, err
	}
	if j.snapshot.Status == model.JobRunning && !j.finishing {
		j.cancel()
	}
	return m.snapshot(j)
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
	m.closed = true
	for _, j := range m.jobs {
		if j.snapshot.Status == model.JobRunning && !j.finishing {
			j.cancel()
		}
	}
	m.mu.Unlock()
	m.wait.Wait()
}

func (m *AnalysisJobs) remove(id string) {
	if j := m.jobs[id]; j != nil {
		m.retainedBytes -= len(j.output)
		delete(m.jobs, id)
	}
}

func (m *AnalysisJobs) snapshot(j *analysisJob) (AnalysisJobSnapshot, error) {
	snapshot := cloneResolverValue(j.snapshot)
	if len(j.output) > 0 {
		snapshot.Output = &AnalysisOutput{}
		decoder := json.NewDecoder(bytes.NewReader(j.output))
		decoder.UseNumber()
		if err := decoder.Decode(snapshot.Output); err != nil {
			return AnalysisJobSnapshot{}, err
		}
	}
	return snapshot, nil
}
