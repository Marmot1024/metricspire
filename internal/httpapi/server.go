package httpapi

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/audit"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/governance"
	"github.com/marmot1024/metricspire/internal/mcpbridge"
	"github.com/marmot1024/metricspire/internal/model"
)

var ErrUnauthenticated = errors.New("request is not authenticated")

//go:embed ui/*
var uiFiles embed.FS

type Server struct {
	config Config
	deps   Dependencies
	mux    *http.ServeMux
	mcp    http.Handler
	logger *slog.Logger
}

func NewServer(config Config, dependencies Dependencies, logger *slog.Logger) (*Server, error) {
	if dependencies.Authenticator == nil || dependencies.Readiness == nil || dependencies.Management == nil || dependencies.Catalog == nil || dependencies.CatalogSearch == nil || dependencies.Governance == nil || dependencies.Bindings == nil || dependencies.Queries == nil || dependencies.Jobs == nil {
		return nil, errors.New("authenticator, readiness checker, management service, catalog reader, catalog searcher, governance service, binding resolver, query service, and job service are required")
	}
	if config.MaxBodyBytes == 0 {
		config.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if config.ControlTimeout == 0 {
		config.ControlTimeout = DefaultControlTimeout
	}
	if config.QueryTimeout == 0 {
		config.QueryTimeout = DefaultQueryTimeout
	}
	if config.RequestID == nil {
		config.RequestID = newRequestID
	}
	if config.MaxBodyBytes < 1 || config.ControlTimeout <= 0 || config.QueryTimeout <= 0 {
		return nil, errors.New("HTTP body limit and timeouts must be positive")
	}
	if strings.TrimSpace(config.MCPVersion) == "" {
		config.MCPVersion = "dev"
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	server := &Server{config: config, deps: dependencies, mux: http.NewServeMux(), logger: logger}
	server.mcp = mcpbridge.NewStreamableHTTPHandler(config.MCPVersion, func(request *http.Request) mcpbridge.Backend {
		backend, _ := request.Context().Value(remoteMCPBackendKey{}).(mcpbridge.Backend)
		return backend
	})
	server.routes()
	return server, nil
}

func (server *Server) routes() {
	server.mux.HandleFunc("/health/live", server.handleLiveness)
	server.mux.HandleFunc("/health/ready", server.handleReadiness)
	server.mux.HandleFunc("/.well-known/oauth-protected-resource", server.handleMCPProtectedResourceMetadata)
	server.mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", server.handleMCPProtectedResourceMetadata)
	server.mux.HandleFunc("/.well-known/oauth-protected-resource/api/v1/mcp", server.handleMCPProtectedResourceMetadata)
	server.mux.HandleFunc("/mcp", server.handleMCP)
	server.mux.HandleFunc(APIPrefix+"/mcp", server.handleMCP)
	if server.deps.AuthEndpoints != nil {
		server.mux.Handle("/auth/", server.deps.AuthEndpoints)
	}
	server.mux.HandleFunc(APIPrefix+"/namespaces/{namespace}/models/{model}/draft", server.handleDraft)
	server.mux.HandleFunc(APIPrefix+"/namespaces/{namespace}/models/{model}/validate", server.handleValidate)
	server.mux.HandleFunc(APIPrefix+"/namespaces/{namespace}/models/{model}/review", server.handleReview)
	server.mux.HandleFunc(APIPrefix+"/namespaces/{namespace}/models/{model}/publish", server.handlePublish)
	server.mux.HandleFunc(APIPrefix+"/namespaces/{namespace}/models/{model}/releases", server.handleReleases)
	server.mux.HandleFunc(APIPrefix+"/namespaces/{namespace}/models/{model}/rollback", server.handleRollback)
	server.mux.HandleFunc(APIPrefix+"/namespaces/{namespace}/models/{model}/explain", server.handleExplain)
	server.mux.HandleFunc(APIPrefix+"/namespaces/{namespace}/models/{model}/plan", server.handlePlan)
	server.mux.HandleFunc(APIPrefix+"/namespaces/{namespace}/models/{model}/query", server.handleQuery)
	server.mux.HandleFunc(APIPrefix+"/namespaces/{namespace}/models/{model}/draft-explain", server.handleDraftExplain)
	server.mux.HandleFunc(APIPrefix+"/namespaces/{namespace}/models/{model}/draft-plan", server.handleDraftPlan)
	server.mux.HandleFunc(APIPrefix+"/namespaces/{namespace}/models/{model}/draft-query", server.handleDraftQuery)
	server.mux.HandleFunc(APIPrefix+"/namespaces/{namespace}/governance/metrics", server.handleGovernanceMetrics)
	server.mux.HandleFunc(APIPrefix+"/namespaces/{namespace}/governance/imports", server.handleGovernanceImports)
	server.mux.HandleFunc(APIPrefix+"/namespaces/{namespace}/governance/imports/{import}", server.handleGovernanceImport)
	server.mux.HandleFunc(APIPrefix+"/namespaces/{namespace}/governance/imports/{import}/rollback", server.handleGovernanceImportRollback)
	server.mux.HandleFunc(APIPrefix+"/namespaces/{namespace}/explain", server.handleMetricExplain)
	server.mux.HandleFunc(APIPrefix+"/namespaces/{namespace}/plan", server.handleMetricPlan)
	server.mux.HandleFunc(APIPrefix+"/namespaces/{namespace}/query", server.handleMetricQuery)
	server.mux.HandleFunc(APIPrefix+"/catalog/search", server.handleCatalogSearch)
	server.mux.HandleFunc(APIPrefix+"/ui/context", server.handleUIContext)
	server.mux.HandleFunc(APIPrefix+"/jobs/{job}", server.handleJob)
	server.mux.HandleFunc(APIPrefix+"/jobs/{job}/cancel", server.handleJobCancel)
	server.mux.HandleFunc("/assets/app.js", server.handleUIAsset)
	server.mux.HandleFunc("/assets/app.css", server.handleUIAsset)
	server.mux.HandleFunc("/", server.handleRoot)
}

func (server *Server) handleUIContext(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		server.methodNotAllowed(response, request, http.MethodGet)
		return
	}
	principal, ok := server.authenticate(response, request)
	if !ok {
		return
	}
	displayName := strings.TrimSpace(principal.DisplayName)
	if displayName == "" {
		displayName = principal.Subject
	}
	models := []UIModelRoute(nil)
	if principal.Has(PermissionManage) {
		models = append(models, server.config.UIModels...)
	}
	_ = writeJSON(response, http.StatusOK, UIContext{
		AuthenticationProfile: server.config.AuthenticationProfile,
		DisplayName:           displayName,
		Permissions:           append([]Permission(nil), principal.Permissions...),
		Namespaces:            configuredUINamespaces(server.config.UIModels),
		Models:                models,
	})
}

func configuredUINamespaces(routes []UIModelRoute) []string {
	result := make([]string, 0, len(routes))
	seen := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		if _, exists := seen[route.Namespace]; exists {
			continue
		}
		seen[route.Namespace] = struct{}{}
		result = append(result, route.Namespace)
	}
	return result
}

func (server *Server) handleLiveness(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		server.methodNotAllowed(response, request, http.MethodGet)
		return
	}
	_ = writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
}

func (server *Server) handleReadiness(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		server.methodNotAllowed(response, request, http.MethodGet)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), server.config.ControlTimeout)
	defer cancel()
	if err := server.deps.Readiness.Ready(ctx); err != nil {
		server.writeProblem(response, request, http.StatusServiceUnavailable, "not_ready", "Service unavailable", "the control plane is not ready", "")
		return
	}
	_ = writeJSON(response, http.StatusOK, map[string]string{"status": "ready"})
}

func (server *Server) handleCatalogSearch(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		server.methodNotAllowed(response, request, http.MethodGet)
		return
	}
	principal, ok := server.authorize(response, request, PermissionQuery)
	if !ok {
		return
	}
	namespace := strings.TrimSpace(request.URL.Query().Get("namespace"))
	if namespace == "" {
		server.writeProblem(response, request, http.StatusBadRequest, "invalid_request", "Invalid request", "namespace query parameter is required", "namespace")
		return
	}
	query := strings.ToLower(strings.TrimSpace(request.URL.Query().Get("q")))
	limit := 50
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			server.writeProblem(response, request, http.StatusBadRequest, "invalid_request", "Invalid request", "limit must be between 1 and 100", "limit")
			return
		}
		limit = parsed
	}
	server.withTimeout(response, request, server.config.ControlTimeout, func(ctx context.Context) error {
		results, err := server.deps.CatalogSearch.SearchActive(ctx, application.QueryScope{
			Namespace: namespace,
			Context: model.RequestContext{
				Tenant: principal.Tenant, Principal: principal.Subject, Roles: principal.Roles, RequestID: requestID(request),
			},
		}, query, limit)
		if err != nil {
			return err
		}
		return writeJSON(response, http.StatusOK, results)
	})
}

func (server *Server) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	setSecurityHeaders(response.Header())
	requestID := server.config.RequestID()
	response.Header().Set("X-Request-ID", requestID)
	request = request.WithContext(context.WithValue(request.Context(), requestIDKey{}, requestID))
	if !server.sameOrigin(request) {
		server.writeProblem(response, request, http.StatusForbidden, "cross_origin_request", "Cross-origin request rejected", "state-changing requests must originate from this MetricSpire deployment", "")
		return
	}
	server.mux.ServeHTTP(response, request)
}

func (server *Server) sameOrigin(request *http.Request) bool {
	switch request.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	allowed := strings.TrimSuffix(strings.TrimSpace(server.config.AllowedOrigin), "/")
	if allowed != "" {
		return strings.EqualFold(origin, allowed)
	}
	return strings.EqualFold(parsed.Host, request.Host)
}

func (server *Server) handleDraft(response http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		principal, ok := server.authorize(response, request, PermissionManage)
		if !ok {
			return
		}
		_ = principal
		server.withTimeout(response, request, server.config.ControlTimeout, func(ctx context.Context) error {
			draft, err := server.deps.Catalog.GetDraft(ctx, request.PathValue("namespace"), request.PathValue("model"))
			if err != nil {
				return err
			}
			return writeJSON(response, http.StatusOK, draft)
		})
	case http.MethodPut:
		principal, ok := server.authorize(response, request, PermissionManage)
		if !ok {
			return
		}
		var body SaveDraftRequest
		if !server.decodeJSON(response, request, &body) {
			return
		}
		namespace, name := request.PathValue("namespace"), request.PathValue("model")
		if body.Source.Metadata.Name != name {
			server.writeProblem(response, request, http.StatusUnprocessableEntity, "model_name_mismatch", "Model name mismatch", "source.metadata.name must match the model path", "source.metadata.name")
			return
		}
		server.withTimeout(response, request, server.config.ControlTimeout, func(ctx context.Context) error {
			draft, err := server.deps.Management.SaveDraft(ctx, catalog.SaveDraftInput{
				Namespace: namespace, Source: body.Source, Actor: principal.Subject, ExpectedRevision: body.ExpectedRevision,
			})
			if err != nil {
				return err
			}
			return writeJSON(response, http.StatusOK, draft)
		})
	default:
		server.methodNotAllowed(response, request, http.MethodGet+", "+http.MethodPut)
	}
}

func (server *Server) handleValidate(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		server.methodNotAllowed(response, request, http.MethodPost)
		return
	}
	if _, ok := server.authorize(response, request, PermissionManage); !ok {
		return
	}
	var body ValidateRequest
	if !server.decodeJSON(response, request, &body) {
		return
	}
	if body.Source.Metadata.Name != request.PathValue("model") {
		server.writeProblem(response, request, http.StatusUnprocessableEntity, "model_name_mismatch", "Model name mismatch", "source.metadata.name must match the model path", "source.metadata.name")
		return
	}
	server.withTimeout(response, request, server.config.ControlTimeout, func(context.Context) error {
		manifest, err := compiler.Compile(body.Source)
		if err != nil {
			return err
		}
		return writeJSON(response, http.StatusOK, manifest)
	})
}

func (server *Server) handleReview(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		server.methodNotAllowed(response, request, http.MethodPost)
		return
	}
	principal, ok := server.authorize(response, request, PermissionManage)
	if !ok {
		return
	}
	var body ReviewRequest
	if !server.decodeJSON(response, request, &body) {
		return
	}
	namespace, name := request.PathValue("namespace"), request.PathValue("model")
	if body.Source.Metadata.Name != name {
		server.writeProblem(response, request, http.StatusUnprocessableEntity, "model_name_mismatch", "Model name mismatch", "source.metadata.name must match the model path", "source.metadata.name")
		return
	}
	server.withTimeout(response, request, server.config.ControlTimeout, func(ctx context.Context) error {
		var active *catalog.Release
		current, err := server.deps.Catalog.GetActiveRelease(ctx, namespace, name)
		switch {
		case err == nil:
			active = &current
		case !errors.Is(err, catalog.ErrNotFound):
			return err
		}
		resolverRelease := catalog.Release{Namespace: namespace, Name: name}
		if active != nil {
			resolverRelease = *active
		}
		binding, err := server.deps.Bindings.ResolveBinding(ctx, application.QueryScope{
			Namespace: namespace, ModelName: name,
			Context: model.RequestContext{
				Tenant: principal.Tenant, Principal: principal.Subject, Roles: principal.Roles, RequestID: requestID(request),
			},
		}, resolverRelease)
		var bindingPointer *model.SourceBinding
		switch {
		case err == nil:
			bindingPointer = &binding
		case !errors.Is(err, application.ErrResolutionNotFound):
			return err
		}
		review, err := application.ReviewGovernance(body.Source, active, bindingPointer)
		if err != nil {
			return err
		}
		return writeJSON(response, http.StatusOK, review)
	})
}

func (server *Server) handlePublish(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		server.methodNotAllowed(response, request, http.MethodPost)
		return
	}
	principal, ok := server.authorize(response, request, PermissionManage)
	if !ok {
		return
	}
	var body PublishRequest
	if !server.decodeJSON(response, request, &body) {
		return
	}
	server.withTimeout(response, request, server.config.ControlTimeout, func(ctx context.Context) error {
		release, err := server.deps.Management.Publish(ctx, request.PathValue("namespace"), request.PathValue("model"), body.ExpectedRevision, principal.Subject, body.Note)
		if err != nil {
			return err
		}
		return writeJSON(response, http.StatusCreated, release)
	})
}

func (server *Server) handleReleases(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		server.methodNotAllowed(response, request, http.MethodGet)
		return
	}
	if _, ok := server.authorize(response, request, PermissionManage); !ok {
		return
	}
	server.withTimeout(response, request, server.config.ControlTimeout, func(ctx context.Context) error {
		namespace, name := request.PathValue("namespace"), request.PathValue("model")
		releases, err := server.deps.Catalog.ListReleases(ctx, namespace, name)
		if err != nil {
			return err
		}
		active, activeErr := server.deps.Catalog.GetActiveRelease(ctx, namespace, name)
		if activeErr != nil && !errors.Is(activeErr, catalog.ErrNotFound) {
			return activeErr
		}
		result := make([]ReleaseSummary, 0, len(releases))
		for _, release := range releases {
			result = append(result, ReleaseSummary{
				ID: release.ID, SourceRevision: release.SourceRevision, ManifestFingerprint: release.ManifestFingerprint,
				Active: release.ID == active.ID, CreatedBy: release.CreatedBy, Note: release.Note, CreatedAt: release.CreatedAt,
			})
		}
		return writeJSON(response, http.StatusOK, result)
	})
}

func (server *Server) handleRollback(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		server.methodNotAllowed(response, request, http.MethodPost)
		return
	}
	principal, ok := server.authorize(response, request, PermissionManage)
	if !ok {
		return
	}
	var body RollbackRequest
	if !server.decodeJSON(response, request, &body) {
		return
	}
	server.withTimeout(response, request, server.config.ControlTimeout, func(ctx context.Context) error {
		release, err := server.deps.Management.Rollback(ctx, request.PathValue("namespace"), request.PathValue("model"), body.ReleaseID, principal.Subject, body.Note)
		if err != nil {
			return err
		}
		return writeJSON(response, http.StatusOK, release)
	})
}

func (server *Server) handleExplain(response http.ResponseWriter, request *http.Request) {
	server.handleQueryOperation(response, request, "explain")
}

func (server *Server) handlePlan(response http.ResponseWriter, request *http.Request) {
	server.handleQueryOperation(response, request, "plan")
}

func (server *Server) handleQuery(response http.ResponseWriter, request *http.Request) {
	server.handleQueryOperation(response, request, "query")
}

func (server *Server) handleDraftExplain(response http.ResponseWriter, request *http.Request) {
	server.handleDraftQueryOperation(response, request, "explain")
}

func (server *Server) handleDraftPlan(response http.ResponseWriter, request *http.Request) {
	server.handleDraftQueryOperation(response, request, "plan")
}

func (server *Server) handleDraftQuery(response http.ResponseWriter, request *http.Request) {
	server.handleDraftQueryOperation(response, request, "query")
}

func (server *Server) handleGovernanceMetrics(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		server.methodNotAllowed(response, request, http.MethodGet)
		return
	}
	if _, ok := server.authorize(response, request, PermissionManage); !ok {
		return
	}
	limit := governance.MaxImportRecords
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > governance.MaxImportRecords {
			server.writeProblem(response, request, http.StatusBadRequest, "invalid_request", "Invalid request", "limit must be between 1 and 1000", "limit")
			return
		}
		limit = parsed
	}
	server.withTimeout(response, request, server.config.ControlTimeout, func(ctx context.Context) error {
		records, err := server.deps.Governance.List(ctx, request.PathValue("namespace"), request.URL.Query().Get("q"), limit)
		if err != nil {
			return err
		}
		return writeJSON(response, http.StatusOK, records)
	})
}

func (server *Server) handleGovernanceImports(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		server.methodNotAllowed(response, request, http.MethodPost)
		return
	}
	principal, ok := server.authorize(response, request, PermissionManage)
	if !ok {
		return
	}
	var body GovernanceImportRequest
	if !server.decodeJSON(response, request, &body) {
		return
	}
	server.withTimeout(response, request, server.config.ControlTimeout, func(ctx context.Context) error {
		batch, err := server.deps.Governance.Import(ctx, governance.ImportInput{
			Namespace: request.PathValue("namespace"), SourceFingerprint: body.SourceFingerprint,
			ExpectedPreviousImportID: body.ExpectedPreviousImportID, Records: body.Records, Actor: principal.Subject,
		})
		if err != nil {
			return err
		}
		return writeJSON(response, http.StatusCreated, batch)
	})
}

func (server *Server) handleGovernanceImport(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		server.methodNotAllowed(response, request, http.MethodGet)
		return
	}
	if _, ok := server.authorize(response, request, PermissionManage); !ok {
		return
	}
	server.withTimeout(response, request, server.config.ControlTimeout, func(ctx context.Context) error {
		batch, err := server.deps.Governance.GetImport(ctx, request.PathValue("namespace"), request.PathValue("import"))
		if err != nil {
			return err
		}
		return writeJSON(response, http.StatusOK, batch)
	})
}

func (server *Server) handleGovernanceImportRollback(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		server.methodNotAllowed(response, request, http.MethodPost)
		return
	}
	principal, ok := server.authorize(response, request, PermissionManage)
	if !ok {
		return
	}
	server.withTimeout(response, request, server.config.ControlTimeout, func(ctx context.Context) error {
		batch, err := server.deps.Governance.RollbackImport(ctx, request.PathValue("namespace"), request.PathValue("import"), principal.Subject)
		if err != nil {
			return err
		}
		return writeJSON(response, http.StatusOK, batch)
	})
}

func (server *Server) handleMetricExplain(response http.ResponseWriter, request *http.Request) {
	server.handleMetricQueryOperation(response, request, "explain")
}

func (server *Server) handleMetricPlan(response http.ResponseWriter, request *http.Request) {
	server.handleMetricQueryOperation(response, request, "plan")
}

func (server *Server) handleMetricQuery(response http.ResponseWriter, request *http.Request) {
	server.handleMetricQueryOperation(response, request, "query")
}

// handleMetricQueryOperation is the product-facing, metric-first query
// boundary. It resolves the internal active model from the requested metric
// codes. The older /models/{model} routes remain available for compatibility.
func (server *Server) handleMetricQueryOperation(response http.ResponseWriter, request *http.Request, operation string) {
	if request.Method != http.MethodPost {
		server.methodNotAllowed(response, request, http.MethodPost)
		return
	}
	principal, ok := server.authorize(response, request, PermissionQuery)
	if !ok {
		return
	}
	var query model.SemanticQuery
	if !server.decodeJSON(response, request, &query) {
		return
	}
	namespace := request.PathValue("namespace")
	ctx, cancel := context.WithTimeout(request.Context(), server.config.ControlTimeout)
	defer cancel()
	modelName, err := server.deps.CatalogSearch.ResolveActiveModel(ctx, application.QueryScope{
		Namespace: namespace,
		Context: model.RequestContext{
			Tenant: principal.Tenant, Principal: principal.Subject, Roles: principal.Roles, RequestID: requestID(request),
		},
	}, query.Metrics)
	if err != nil {
		server.handleError(response, request, err)
		return
	}
	server.executeQueryOperation(response, request, operation, principal, namespace, modelName, query)
}

func (server *Server) handleQueryOperation(response http.ResponseWriter, request *http.Request, operation string) {
	if request.Method != http.MethodPost {
		server.methodNotAllowed(response, request, http.MethodPost)
		return
	}
	principal, ok := server.authorize(response, request, PermissionQuery)
	if !ok {
		return
	}
	var query model.SemanticQuery
	if !server.decodeJSON(response, request, &query) {
		return
	}
	server.executeQueryOperation(response, request, operation, principal, request.PathValue("namespace"), request.PathValue("model"), query)
}

func (server *Server) handleDraftQueryOperation(response http.ResponseWriter, request *http.Request, operation string) {
	if request.Method != http.MethodPost {
		server.methodNotAllowed(response, request, http.MethodPost)
		return
	}
	principal, ok := server.authorize(response, request, PermissionManage)
	if !ok {
		return
	}
	if !principal.Has(PermissionQuery) {
		server.writeProblem(response, request, http.StatusForbidden, "permission_denied", "Permission denied", "draft preview requires both model management and query execution permission", "")
		return
	}
	var query model.SemanticQuery
	if !server.decodeJSON(response, request, &query) {
		return
	}
	input := application.QueryInput{
		QueryScope: application.QueryScope{
			Namespace: request.PathValue("namespace"), ModelName: request.PathValue("model"),
			Context: model.RequestContext{Tenant: principal.Tenant, Principal: principal.Subject, Roles: principal.Roles, RequestID: requestID(request)},
		},
		Query: query,
	}
	server.withTimeout(response, request, server.config.QueryTimeout, func(ctx context.Context) error {
		switch operation {
		case "explain":
			output, err := server.deps.Queries.ExplainDraft(ctx, input)
			if err != nil {
				return err
			}
			return writeJSON(response, http.StatusOK, output)
		case "plan":
			output, err := server.deps.Queries.PlanDraft(ctx, input)
			if err != nil {
				return err
			}
			return writeJSON(response, http.StatusOK, output)
		default:
			executionContext := ctx
			if server.deps.ExecutionCredential != nil {
				var err error
				executionContext, err = server.deps.ExecutionCredential.AddToContext(ctx, request, principal)
				if err != nil {
					return err
				}
				if executionContext == nil {
					return errors.New("execution credential provider returned a nil context")
				}
			}
			output, err := server.deps.Queries.ExecuteDraft(executionContext, input)
			if err != nil {
				return err
			}
			return writeJSON(response, http.StatusOK, output)
		}
	})
}

func (server *Server) executeQueryOperation(response http.ResponseWriter, request *http.Request, operation string, principal Principal, namespace, modelName string, query model.SemanticQuery) {
	input := application.QueryInput{
		QueryScope: application.QueryScope{
			Namespace: namespace, ModelName: modelName,
			Context: model.RequestContext{Tenant: principal.Tenant, Principal: principal.Subject, Roles: principal.Roles, RequestID: requestID(request)},
		},
		Query: query,
	}
	server.withTimeout(response, request, server.config.QueryTimeout, func(ctx context.Context) error {
		switch operation {
		case "explain":
			output, err := server.deps.Queries.ExplainActive(ctx, input)
			if err != nil {
				return err
			}
			return writeJSON(response, http.StatusOK, output)
		case "plan":
			output, err := server.deps.Queries.PlanActive(ctx, input)
			if err != nil {
				return err
			}
			return writeJSON(response, http.StatusOK, output)
		default:
			executionContext := ctx
			if server.deps.ExecutionCredential != nil {
				var err error
				executionContext, err = server.deps.ExecutionCredential.AddToContext(ctx, request, principal)
				if err != nil {
					return err
				}
				if executionContext == nil {
					return errors.New("execution credential provider returned a nil context")
				}
			}
			output, err := server.deps.Jobs.Submit(executionContext, input)
			if err != nil {
				return err
			}
			return writeJSON(response, http.StatusAccepted, output)
		}
	})
}

func (server *Server) handleJob(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		server.methodNotAllowed(response, request, http.MethodGet)
		return
	}
	principal, ok := server.authorize(response, request, PermissionQuery)
	if !ok {
		return
	}
	snapshot, err := server.deps.Jobs.Get(principal.Tenant, principal.Subject, request.PathValue("job"))
	if err != nil {
		server.handleError(response, request, err)
		return
	}
	_ = writeJSON(response, http.StatusOK, snapshot)
}

func (server *Server) handleJobCancel(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		server.methodNotAllowed(response, request, http.MethodPost)
		return
	}
	principal, ok := server.authorize(response, request, PermissionQuery)
	if !ok {
		return
	}
	snapshot, err := server.deps.Jobs.Cancel(principal.Tenant, principal.Subject, request.PathValue("job"))
	if err != nil && !errors.Is(err, application.ErrJobFinished) {
		server.handleError(response, request, err)
		return
	}
	_ = writeJSON(response, http.StatusAccepted, snapshot)
}

func (server *Server) authorize(response http.ResponseWriter, request *http.Request, permission Permission) (Principal, bool) {
	principal, ok := server.authenticate(response, request)
	if !ok {
		return Principal{}, false
	}
	if !principal.Has(permission) {
		server.writeProblem(response, request, http.StatusForbidden, "permission_denied", "Permission denied", "the authenticated principal lacks the required permission", "")
		return Principal{}, false
	}
	return principal, true
}

func (server *Server) authenticate(response http.ResponseWriter, request *http.Request) (Principal, bool) {
	principal, err := server.deps.Authenticator.Authenticate(request.Context(), request)
	if err != nil {
		var diagnostic interface{ SafeAuthenticationReason() string }
		if errors.As(err, &diagnostic) {
			server.logger.Warn("authentication rejected", "request_id", requestID(request), "reason", diagnostic.SafeAuthenticationReason())
		}
		server.setMCPAuthenticationChallenge(response, request)
		server.writeProblem(response, request, http.StatusUnauthorized, "unauthenticated", "Authentication required", "valid authentication is required", "")
		return Principal{}, false
	}
	if strings.TrimSpace(principal.Tenant) == "" || strings.TrimSpace(principal.Subject) == "" {
		server.logger.Error("authenticator returned incomplete principal", "request_id", requestID(request))
		server.writeProblem(response, request, http.StatusInternalServerError, "authentication_failed", "Authentication failed", "authentication provider returned an incomplete principal", "")
		return Principal{}, false
	}
	return principal, true
}

func (server *Server) handleMCPProtectedResourceMetadata(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		server.methodNotAllowed(response, request, http.MethodGet+", "+http.MethodHead)
		return
	}
	endpoint := "/mcp"
	if request.URL.Path == "/.well-known/oauth-protected-resource/api/v1/mcp" {
		endpoint = APIPrefix + "/mcp"
	}
	resource, authorizationServer, ok := server.mcpOAuthMetadata(endpoint)
	if !ok {
		server.handleNotFound(response, request)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	if request.Method == http.MethodHead {
		return
	}
	_ = json.NewEncoder(response).Encode(map[string]any{
		"resource":              resource,
		"authorization_servers": []string{authorizationServer},
	})
}

func (server *Server) setMCPAuthenticationChallenge(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/mcp" && request.URL.Path != APIPrefix+"/mcp" {
		return
	}
	resource, _, ok := server.mcpOAuthMetadata(request.URL.Path)
	if !ok {
		return
	}
	metadataURL := strings.TrimSuffix(resource, request.URL.Path) + "/.well-known/oauth-protected-resource" + request.URL.Path
	response.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+metadataURL+`"`)
}

func (server *Server) mcpOAuthMetadata(endpoint string) (string, string, bool) {
	origin := strings.TrimSuffix(strings.TrimSpace(server.config.AllowedOrigin), "/")
	authorizationServer := strings.TrimSpace(server.config.MCPAuthorizationServer)
	if origin == "" || authorizationServer == "" {
		return "", "", false
	}
	return origin + endpoint, authorizationServer, true
}

func (server *Server) decodeJSON(response http.ResponseWriter, request *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		server.writeProblem(response, request, http.StatusUnsupportedMediaType, "unsupported_media_type", "Unsupported media type", "Content-Type must be application/json", "")
		return false
	}
	request.Body = http.MaxBytesReader(response, request.Body, server.config.MaxBodyBytes)
	data, err := io.ReadAll(request.Body)
	if err != nil {
		var maximum *http.MaxBytesError
		if errors.As(err, &maximum) {
			server.writeProblem(response, request, http.StatusRequestEntityTooLarge, "request_too_large", "Request too large", fmt.Sprintf("request body exceeds %d bytes", maximum.Limit), "")
			return false
		}
		server.writeProblem(response, request, http.StatusBadRequest, "invalid_request", "Invalid request", "request body could not be read", "")
		return false
	}
	if len(data) == 0 {
		server.writeProblem(response, request, http.StatusBadRequest, "invalid_json", "Invalid JSON", "request body is empty", "")
		return false
	}
	if err := contractio.DecodeJSON(data, target); err != nil {
		server.writeProblem(response, request, http.StatusBadRequest, "invalid_json", "Invalid JSON", err.Error(), "")
		return false
	}
	return true
}

func (server *Server) withTimeout(response http.ResponseWriter, request *http.Request, timeout time.Duration, operation func(context.Context) error) {
	ctx, cancel := context.WithTimeout(request.Context(), timeout)
	defer cancel()
	if err := operation(ctx); err != nil {
		server.handleError(response, request, err)
	}
}

func (server *Server) handleError(response http.ResponseWriter, request *http.Request, err error) {
	var domain *model.Problem
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		server.writeProblem(response, request, http.StatusGatewayTimeout, "timeout", "Request timed out", "the operation exceeded its server deadline", "")
	case errors.Is(err, audit.ErrUnavailable):
		server.writeProblem(response, request, http.StatusServiceUnavailable, "audit_unavailable", "Audit unavailable", "the request was not completed because its audit record could not be durably preserved", "")
	case errors.Is(err, ErrUnauthenticated):
		server.writeProblem(response, request, http.StatusUnauthorized, "unauthenticated", "Authentication required", "valid authentication and execution authorization are required", "")
	case errors.Is(err, catalog.ErrNotFound):
		server.writeProblem(response, request, http.StatusNotFound, "not_found", "Not found", "the requested resource was not found", "")
	case errors.Is(err, governance.ErrNotFound):
		server.writeProblem(response, request, http.StatusNotFound, "not_found", "Not found", "the requested governance resource was not found", "")
	case errors.Is(err, application.ErrJobNotFound):
		server.writeProblem(response, request, http.StatusNotFound, "job_not_found", "Job not found", "the query job was not found", "")
	case errors.Is(err, catalog.ErrConflict):
		server.writeProblem(response, request, http.StatusConflict, "conflict", "Conflict", "the resource changed; refresh and retry", "")
	case errors.Is(err, governance.ErrConflict):
		server.writeProblem(response, request, http.StatusConflict, "conflict", "Conflict", "the governance import changed; refresh and retry", "")
	case errors.Is(err, governance.ErrInvalid):
		server.writeProblem(response, request, http.StatusUnprocessableEntity, "invalid_request", "Invalid governance request", "the governance request failed validation", "")
	case errors.Is(err, catalog.ErrNotPublishable):
		server.writeProblem(response, request, http.StatusUnprocessableEntity, "not_publishable", "Model is not publishable", err.Error(), "")
	case errors.As(err, &domain):
		status := http.StatusUnprocessableEntity
		if domain.Code == "permission_denied" {
			status = http.StatusForbidden
		}
		server.writeProblem(response, request, status, domain.Code, "Request rejected", domain.Message, domain.Path)
	default:
		server.logger.Error("HTTP operation failed", "request_id", requestID(request), "error", err)
		server.writeProblem(response, request, http.StatusInternalServerError, "internal_error", "Internal error", "the request could not be completed", "")
	}
}

func (server *Server) methodNotAllowed(response http.ResponseWriter, request *http.Request, allow string) {
	response.Header().Set("Allow", allow)
	server.writeProblem(response, request, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", "the HTTP method is not supported for this resource", "")
}

func (server *Server) handleNotFound(response http.ResponseWriter, request *http.Request) {
	server.writeProblem(response, request, http.StatusNotFound, "not_found", "Not found", "the requested resource does not exist", "")
}

func (server *Server) handleRoot(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/" {
		server.handleNotFound(response, request)
		return
	}
	if request.Method != http.MethodGet {
		server.methodNotAllowed(response, request, http.MethodGet)
		return
	}
	data, err := uiFiles.ReadFile("ui/index.html")
	if err != nil {
		server.handleError(response, request, err)
		return
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(response, string(data))
}

func (server *Server) handleUIAsset(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		server.methodNotAllowed(response, request, http.MethodGet)
		return
	}
	var name string
	var contentType string
	switch request.URL.Path {
	case "/assets/app.js":
		name = "ui/app.js"
		contentType = "text/javascript; charset=utf-8"
	case "/assets/app.css":
		name = "ui/app.css"
		contentType = "text/css; charset=utf-8"
	default:
		server.handleNotFound(response, request)
		return
	}
	data, err := uiFiles.ReadFile(name)
	if err != nil {
		server.handleNotFound(response, request)
		return
	}
	response.Header().Set("Content-Type", contentType)
	response.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(response, string(data))
}

func (server *Server) writeProblem(response http.ResponseWriter, request *http.Request, status int, code, title, detail, path string) {
	response.Header().Set("Content-Type", "application/problem+json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(Problem{
		Type: "https://metricspire.io/problems/" + code, Title: title, Status: status,
		Detail: detail, Code: code, Path: path, RequestID: requestID(request),
	})
}

func writeJSON(response http.ResponseWriter, status int, value any) error {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	return json.NewEncoder(response).Encode(value)
}

func setSecurityHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Security-Policy", "default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
}

type requestIDKey struct{}

func requestID(request *http.Request) string {
	value, _ := request.Context().Value(requestIDKey{}).(string)
	return value
}

// RequestID returns the server-generated request ID for transport adapters such
// as authentication endpoints.
func RequestID(request *http.Request) string { return requestID(request) }

func newRequestID() string {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return fmt.Sprintf("req_%d", time.Now().UnixNano())
	}
	return "req_" + hex.EncodeToString(data)
}
