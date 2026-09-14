package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/audit"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/governance"
	"github.com/marmot1024/metricspire/internal/mcpbridge"
	"github.com/marmot1024/metricspire/internal/model"
)

type remoteMCPBackendKey struct{}

type remoteMCPBackend struct {
	server    *Server
	request   *http.Request
	principal Principal
}

func (server *Server) handleMCP(response http.ResponseWriter, request *http.Request) {
	principal, ok := server.authorize(response, request, PermissionQuery)
	if !ok {
		return
	}
	backend := &remoteMCPBackend{server: server, request: request, principal: principal}
	ctx := context.WithValue(request.Context(), remoteMCPBackendKey{}, mcpbridge.Backend(backend))
	server.mcp.ServeHTTP(response, request.WithContext(ctx))
}

func (backend *remoteMCPBackend) requestContext() model.RequestContext {
	return model.RequestContext{
		Tenant: backend.principal.Tenant, Principal: backend.principal.Subject,
		Roles: append([]string(nil), backend.principal.Roles...), RequestID: requestID(backend.request),
	}
}

func (backend *remoteMCPBackend) ListNamespaces(context.Context) ([]string, error) {
	return configuredUINamespaces(backend.server.config.UIModels), nil
}

func (backend *remoteMCPBackend) SearchMetrics(ctx context.Context, input mcpbridge.SearchInput) ([]application.MetricCatalogEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, backend.server.config.ControlTimeout)
	defer cancel()
	result, err := backend.server.deps.CatalogSearch.SearchActive(ctx, application.QueryScope{
		Namespace: input.Namespace, Context: backend.requestContext(),
	}, input.Search, input.Limit)
	return result, backend.safeError(err)
}

func (backend *remoteMCPBackend) ExplainQuery(ctx context.Context, input mcpbridge.QueryInput) (application.ExplainOutput, error) {
	query, err := backend.activeQueryInput(ctx, input)
	if err != nil {
		return application.ExplainOutput{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, backend.server.config.QueryTimeout)
	defer cancel()
	result, err := backend.server.deps.Queries.ExplainActive(ctx, query)
	return result, backend.safeError(err)
}

func (backend *remoteMCPBackend) PlanQuery(ctx context.Context, input mcpbridge.QueryInput) (application.PlanOutput, error) {
	query, err := backend.activeQueryInput(ctx, input)
	if err != nil {
		return application.PlanOutput{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, backend.server.config.QueryTimeout)
	defer cancel()
	result, err := backend.server.deps.Queries.PlanActive(ctx, query)
	return result, backend.safeError(err)
}

func (backend *remoteMCPBackend) SubmitQuery(ctx context.Context, input mcpbridge.QueryInput) (json.RawMessage, error) {
	query, err := backend.activeQueryInput(ctx, input)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, backend.server.config.QueryTimeout)
	defer cancel()
	executionContext := ctx
	if backend.server.deps.ExecutionCredential != nil {
		executionContext, err = backend.server.deps.ExecutionCredential.AddToContext(ctx, backend.request, backend.principal)
		if err != nil {
			return nil, backend.safeError(err)
		}
		if executionContext == nil {
			return nil, backend.safeError(errors.New("execution credential provider returned a nil context"))
		}
	}
	result, err := backend.server.deps.Jobs.Submit(executionContext, query)
	if err != nil {
		return nil, backend.safeError(err)
	}
	return backend.encode(result)
}

func (backend *remoteMCPBackend) GetQuery(_ context.Context, input mcpbridge.JobInput) (json.RawMessage, error) {
	result, err := backend.server.deps.Jobs.Get(backend.principal.Tenant, backend.principal.Subject, input.JobID)
	if err != nil {
		return nil, backend.safeError(err)
	}
	return backend.encode(result)
}

func (backend *remoteMCPBackend) CancelQuery(_ context.Context, input mcpbridge.JobInput) (json.RawMessage, error) {
	result, err := backend.server.deps.Jobs.Cancel(backend.principal.Tenant, backend.principal.Subject, input.JobID)
	if err != nil && !errors.Is(err, application.ErrJobFinished) {
		return nil, backend.safeError(err)
	}
	return backend.encode(result)
}

func (backend *remoteMCPBackend) activeQueryInput(ctx context.Context, input mcpbridge.QueryInput) (application.QueryInput, error) {
	ctx, cancel := context.WithTimeout(ctx, backend.server.config.ControlTimeout)
	defer cancel()
	scope := application.QueryScope{Namespace: input.Namespace, Context: backend.requestContext()}
	modelName, err := backend.server.deps.CatalogSearch.ResolveActiveModel(ctx, scope, input.Query.Metrics)
	if err != nil {
		return application.QueryInput{}, backend.safeError(err)
	}
	scope.ModelName = modelName
	return application.QueryInput{QueryScope: scope, Query: input.Query}, nil
}

func (backend *remoteMCPBackend) encode(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, backend.safeError(err)
	}
	return encoded, nil
}

func (backend *remoteMCPBackend) safeError(err error) error {
	if err == nil {
		return nil
	}
	code := "internal_error"
	var domain *model.Problem
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		code = "timeout"
	case errors.Is(err, context.Canceled):
		code = "cancelled"
	case errors.Is(err, audit.ErrUnavailable):
		code = "audit_unavailable"
	case errors.Is(err, ErrUnauthenticated):
		code = "unauthenticated"
	case errors.Is(err, catalog.ErrNotFound), errors.Is(err, governance.ErrNotFound):
		code = "not_found"
	case errors.Is(err, application.ErrJobNotFound):
		code = "job_not_found"
	case errors.Is(err, catalog.ErrConflict), errors.Is(err, governance.ErrConflict):
		code = "conflict"
	case errors.As(err, &domain):
		code = domain.Code
	default:
		// Do not log the raw error: an authentication or engine adapter error may
		// contain a transient credential or an upstream response body.
		backend.server.logger.Error("MCP operation failed", "request_id", requestID(backend.request), "code", code)
	}
	return mcpbridge.SanitizedAPIError(code, requestID(backend.request))
}
