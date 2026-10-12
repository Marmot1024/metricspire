package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/exploration"
	"github.com/marmot1024/metricspire/internal/mcpbridge"
	"github.com/marmot1024/metricspire/internal/model"
)

type analysisMCPBackend struct{ *remoteMCPBackend }

func (b *analysisMCPBackend) ListNamespaces(ctx context.Context) ([]string, error) {
	namespaces, err := b.remoteMCPBackend.ListNamespaces(ctx)
	if err != nil {
		return nil, err
	}
	extra, err := b.server.deps.AnalysisCatalogs.Namespaces(ctx, b.requestContext())
	if err != nil {
		return nil, b.analysisSafeError(err)
	}
	for _, name := range extra {
		found := false
		for _, existing := range namespaces {
			found = found || existing == name
		}
		if !found {
			namespaces = append(namespaces, name)
		}
	}
	return namespaces, nil
}

func (s *Server) analysisRoutes() {
	if s.deps.Analysis == nil {
		return
	}
	p := APIPrefix + "/namespaces/{namespace}/analysis"
	s.mux.HandleFunc(p+"/sources", s.handleAnalysisSources)
	s.mux.HandleFunc(p+"/plan", s.handleAnalysisPlan)
	s.mux.HandleFunc(p+"/jobs", s.handleAnalysisSubmit)
	s.mux.HandleFunc(p+"/jobs/{job}", s.handleAnalysisGet)
	s.mux.HandleFunc(p+"/jobs/{job}/cancel", s.handleAnalysisCancel)
}

func (s *Server) analysisRequest(w http.ResponseWriter, r *http.Request, method string) (*analysisMCPBackend, bool) {
	if r.Method != method {
		s.methodNotAllowed(w, r, method)
		return nil, false
	}
	p, ok := s.authorize(w, r, PermissionQuery)
	if !ok {
		return nil, false
	}
	return &analysisMCPBackend{&remoteMCPBackend{server: s, request: r, principal: p}}, true
}
func (s *Server) handleAnalysisSources(w http.ResponseWriter, r *http.Request) {
	b, ok := s.analysisRequest(w, r, http.MethodGet)
	if !ok {
		return
	}
	v, err := b.DiscoverAnalysis(r.Context(), mcpbridge.AnalysisDiscoveryInput{Namespace: r.PathValue("namespace")})
	if err != nil {
		s.analysisError(w, r, err)
		return
	}
	_ = writeJSON(w, http.StatusOK, v)
}
func (s *Server) handleAnalysisPlan(w http.ResponseWriter, r *http.Request) {
	b, ok := s.analysisRequest(w, r, http.MethodPost)
	if !ok {
		return
	}
	var q exploration.Query
	if !s.decodeJSON(w, r, &q) {
		return
	}
	v, err := b.plan(r.Context(), mcpbridge.AnalysisInput{Namespace: r.PathValue("namespace"), Query: q})
	if err != nil {
		s.analysisError(w, r, err)
		return
	}
	_ = writeJSON(w, http.StatusOK, v)
}
func (s *Server) handleAnalysisSubmit(w http.ResponseWriter, r *http.Request) {
	b, ok := s.analysisRequest(w, r, http.MethodPost)
	if !ok {
		return
	}
	var q exploration.Query
	if !s.decodeJSON(w, r, &q) {
		return
	}
	v, err := b.submit(r.Context(), mcpbridge.AnalysisInput{Namespace: r.PathValue("namespace"), Query: q})
	if err != nil {
		s.analysisError(w, r, err)
		return
	}
	_ = writeJSON(w, http.StatusAccepted, v)
}
func (s *Server) handleAnalysisGet(w http.ResponseWriter, r *http.Request) {
	s.analysisJob(w, r, false)
}
func (s *Server) handleAnalysisCancel(w http.ResponseWriter, r *http.Request) {
	s.analysisJob(w, r, true)
}
func (s *Server) analysisJob(w http.ResponseWriter, r *http.Request, cancel bool) {
	method := http.MethodGet
	if cancel {
		method = http.MethodPost
	}
	b, ok := s.analysisRequest(w, r, method)
	if !ok {
		return
	}
	v, err := b.job(mcpbridge.AnalysisJobInput{Namespace: r.PathValue("namespace"), JobID: r.PathValue("job")}, cancel)
	if err != nil {
		s.analysisError(w, r, err)
		return
	}
	_ = writeJSON(w, http.StatusOK, v)
}

func (b *analysisMCPBackend) DiscoverAnalysis(ctx context.Context, in mcpbridge.AnalysisDiscoveryInput) ([]application.AnalysisSourceView, error) {
	ctx, cancel := context.WithTimeout(ctx, b.server.config.ControlTimeout)
	defer cancel()
	v, err := b.server.deps.AnalysisCatalogs.Discover(ctx, in.Namespace, b.requestContext())
	return v, b.analysisSafeError(err)
}
func (b *analysisMCPBackend) PlanAnalysis(ctx context.Context, in mcpbridge.AnalysisInput) (application.AnalysisPlanView, error) {
	v, err := b.plan(ctx, in)
	return v, b.analysisSafeError(err)
}
func (b *analysisMCPBackend) SubmitAnalysis(ctx context.Context, in mcpbridge.AnalysisInput) (json.RawMessage, error) {
	v, err := b.submit(ctx, in)
	if err != nil {
		return nil, b.analysisSafeError(err)
	}
	return b.encode(v)
}
func (b *analysisMCPBackend) GetAnalysis(_ context.Context, in mcpbridge.AnalysisJobInput) (json.RawMessage, error) {
	v, err := b.job(in, false)
	if err != nil {
		return nil, b.analysisSafeError(err)
	}
	return b.encode(v)
}
func (b *analysisMCPBackend) CancelAnalysis(_ context.Context, in mcpbridge.AnalysisJobInput) (json.RawMessage, error) {
	v, err := b.job(in, true)
	if err != nil {
		return nil, b.analysisSafeError(err)
	}
	return b.encode(v)
}

func (b *analysisMCPBackend) plan(ctx context.Context, in mcpbridge.AnalysisInput) (application.AnalysisPlanView, error) {
	ctx, cancel := context.WithTimeout(ctx, b.server.config.ControlTimeout)
	defer cancel()
	p, err := b.server.deps.Analysis.Plan(ctx, application.AnalysisInput{Namespace: in.Namespace, Context: b.requestContext(), Query: in.Query})
	if err != nil {
		return application.AnalysisPlanView{}, err
	}
	return application.PublicAnalysisPlan(p.Plan), nil
}
func (b *analysisMCPBackend) submit(ctx context.Context, in mcpbridge.AnalysisInput) (application.AnalysisJobView, error) {
	// The HTTP budget limits admission only. Accepted execution uses its own
	// configured source budget and remains alive after request disconnection.
	ctx, cancel := context.WithTimeout(ctx, b.server.config.ControlTimeout)
	defer cancel()
	if b.server.deps.ExecutionCredential != nil {
		var err error
		ctx, err = b.server.deps.ExecutionCredential.AddToContext(ctx, b.request, b.principal)
		if err != nil {
			return application.AnalysisJobView{}, err
		}
		if ctx == nil {
			return application.AnalysisJobView{}, errors.New("execution credential provider returned nil context")
		}
	}
	v, err := b.server.deps.AnalysisJobs.Submit(ctx, application.AnalysisInput{Namespace: in.Namespace, Context: b.requestContext(), Query: in.Query})
	return application.PublicAnalysisJob(v), err
}
func (b *analysisMCPBackend) job(in mcpbridge.AnalysisJobInput, cancel bool) (application.AnalysisJobView, error) {
	var v application.AnalysisJobSnapshot
	var err error
	if cancel {
		v, err = b.server.deps.AnalysisJobs.Cancel(in.Namespace, b.requestContext(), in.JobID)
	} else {
		v, err = b.server.deps.AnalysisJobs.Get(in.Namespace, b.requestContext(), in.JobID)
	}
	return application.PublicAnalysisJob(v), err
}

func (b *analysisMCPBackend) analysisSafeError(err error) error {
	if err == nil {
		return nil
	}
	var domain *model.Problem
	if errors.As(err, &domain) && domain.Code == "invalid_analysis" {
		return mcpbridge.SanitizedDomainError(domain.Code, domain.Path, domain.Message, requestID(b.request))
	}
	if errors.Is(err, application.ErrAnalysisJobNotFound) || errors.Is(err, application.ErrResolutionNotFound) {
		return mcpbridge.SanitizedAPIError("not_found", requestID(b.request))
	}
	return b.safeError(err)
}
func (s *Server) analysisError(w http.ResponseWriter, r *http.Request, err error) {
	var domain *model.Problem
	switch {
	case errors.Is(err, application.ErrAnalysisJobNotFound), errors.Is(err, application.ErrResolutionNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "Not found", "analysis source or job was not found", "")
	case errors.As(err, &domain) && domain.Code == "analysis_forbidden":
		s.writeProblem(w, r, http.StatusForbidden, domain.Code, "Permission denied", domain.Message, domain.Path)
	case errors.As(err, &domain) && domain.Code == "analysis_capacity":
		s.writeProblem(w, r, http.StatusTooManyRequests, domain.Code, "Analysis busy", domain.Message, domain.Path)
	default:
		s.handleError(w, r, err)
	}
}
