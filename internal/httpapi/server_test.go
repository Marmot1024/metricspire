package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/audit"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/httpapi"
	"github.com/marmot1024/metricspire/internal/model"
)

func TestHTTPBoundaryRejectsUnauthenticatedUnauthorizedAndUntrustedFields(t *testing.T) {
	handler, engine, _ := newTestServer(t, httpapi.Config{RequestID: func() string { return "req_contract" }})
	query := readQuery(t)

	response := performJSON(handler, http.MethodPost, queryPath("query"), "", query)
	assertProblem(t, response, http.StatusUnauthorized, "unauthenticated")
	if response.Header.Get("X-Content-Type-Options") != "nosniff" || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("security headers = %#v", response.Header)
	}

	response = performJSON(handler, http.MethodPost, queryPath("query"), "manage", query)
	assertProblem(t, response, http.StatusForbidden, "permission_denied")

	data, err := json.Marshal(query)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.TrimSuffix(data, []byte("}"))
	data = append(data, []byte(`,"principal":"forged","policy":{},"binding":{},"engine":"forged","manifest_fingerprint":"forged","sql":"SELECT secret"}`)...)
	request := httptest.NewRequest(http.MethodPost, queryPath("query"), bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer query")
	responseRecorder := httptest.NewRecorder()
	handler.ServeHTTP(responseRecorder, request)
	assertProblem(t, responseRecorder.Result(), http.StatusBadRequest, "invalid_json")
	if engine.executionCount() != 0 {
		t.Fatalf("engine executed %d times for rejected requests", engine.executionCount())
	}
}

func TestHTTPExplainPlanAndQueryUseTrustedServerInputs(t *testing.T) {
	handler, engine, observed := newTestServer(t, httpapi.Config{RequestID: func() string { return "req_trusted" }})
	query := readQuery(t)

	response := performJSON(handler, http.MethodPost, queryPath("explain"), "query", query)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("explain status = %d, body = %s", response.StatusCode, readBody(t, response))
	}
	if engine.executionCount() != 0 {
		t.Fatal("explain executed the analytical engine")
	}
	if observed.bindingCalls != 0 {
		t.Fatal("explain resolved a physical binding")
	}
	assertTrustedScope(t, observed.lastScope)

	response = performJSON(handler, http.MethodPost, queryPath("plan"), "query", query)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("plan status = %d, body = %s", response.StatusCode, readBody(t, response))
	}
	if engine.executionCount() != 0 {
		t.Fatal("plan executed the analytical engine")
	}
	if observed.bindingCalls != 1 {
		t.Fatalf("binding calls after plan = %d", observed.bindingCalls)
	}

	response = performJSON(handler, http.MethodPost, queryPath("query"), "query", query)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("query status = %d, body = %s", response.StatusCode, readBody(t, response))
	}
	var submitted application.QueryJobSnapshot
	decodeResponse(t, response, &submitted)
	output := awaitJob(t, handler, submitted.Job.ID, "query")
	if output.ReleaseID == "" || output.ManifestFingerprint == "" || output.Job.Status != model.JobSucceeded {
		t.Fatalf("query output = %#v", output)
	}
	if engine.executionCount() != 1 {
		t.Fatalf("engine execution count = %d", engine.executionCount())
	}
}

func TestHTTPJobCannotBeReadByAnotherPrincipal(t *testing.T) {
	handler, _, _ := newTestServer(t, httpapi.Config{RequestID: func() string { return "req_owner" }})
	response := performJSON(handler, http.MethodPost, queryPath("query"), "query", readQuery(t))
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("query status = %d", response.StatusCode)
	}
	var submitted application.QueryJobSnapshot
	decodeResponse(t, response, &submitted)

	request := httptest.NewRequest(http.MethodGet, httpapi.APIPrefix+"/jobs/"+submitted.Job.ID, nil)
	request.Header.Set("Authorization", "Bearer other-query")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	assertProblem(t, recorder.Result(), http.StatusNotFound, "job_not_found")
}

func TestHTTPJobCancellationReachesTheEngine(t *testing.T) {
	engine := &fakeEngine{block: true, started: make(chan struct{})}
	handler, _, _ := newTestServerWithEngine(t, httpapi.Config{RequestID: func() string { return "req_cancel" }}, engine, nil)
	response := performJSON(handler, http.MethodPost, queryPath("query"), "query", readQuery(t))
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("query status = %d", response.StatusCode)
	}
	var submitted application.QueryJobSnapshot
	decodeResponse(t, response, &submitted)
	<-engine.started

	request := httptest.NewRequest(http.MethodPost, httpapi.APIPrefix+"/jobs/"+submitted.Job.ID+"/cancel", nil)
	request.Header.Set("Authorization", "Bearer query")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Result().StatusCode != http.StatusAccepted {
		t.Fatalf("cancel status = %d", recorder.Result().StatusCode)
	}
	finished := awaitJob(t, handler, submitted.Job.ID, "query")
	if finished.Job.Status != model.JobCancelled || finished.Job.Error == nil || finished.Job.Error.Code != "cancelled" {
		t.Fatalf("cancelled job = %#v", finished)
	}
}

func TestHTTPAuditAdmissionFailureIsFailClosed(t *testing.T) {
	recorder := audit.NewMemoryRecorder()
	recorder.SetError(errors.New("audit unavailable"))
	engine := &fakeEngine{}
	handler, _, _ := newTestServerWithEngine(t, httpapi.Config{RequestID: func() string { return "req_audit" }}, engine, recorder)
	response := performJSON(handler, http.MethodPost, queryPath("query"), "query", readQuery(t))
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("query status = %d", response.StatusCode)
	}
	var submitted application.QueryJobSnapshot
	decodeResponse(t, response, &submitted)
	finished := awaitJob(t, handler, submitted.Job.ID, "query")
	if finished.Job.Status != model.JobFailed || finished.Job.Error == nil || finished.Job.Error.Code != "audit_unavailable" {
		t.Fatalf("audit-failed job = %#v", finished)
	}
	if engine.executionCount() != 0 {
		t.Fatalf("engine executed %d times despite audit admission failure", engine.executionCount())
	}
}

func TestHTTPQueryJobReportsUnpublishedModelAndBudgetRejection(t *testing.T) {
	t.Run("unpublished model", func(t *testing.T) {
		handler, engine, _ := newTestServer(t, httpapi.Config{RequestID: func() string { return "req_unpublished" }})
		path := httpapi.APIPrefix + "/namespaces/demo/models/missing/query"
		response := performJSON(handler, http.MethodPost, path, "query", readQuery(t))
		var submitted application.QueryJobSnapshot
		decodeResponse(t, response, &submitted)
		finished := awaitJob(t, handler, submitted.Job.ID, "query")
		if finished.Job.Status != model.JobFailed || finished.Job.Error == nil || finished.Job.Error.Code != "not_found" {
			t.Fatalf("unpublished job = %#v", finished)
		}
		if engine.executionCount() != 0 {
			t.Fatal("unpublished query executed the engine")
		}
	})

	t.Run("request budget", func(t *testing.T) {
		handler, engine, _ := newTestServer(t, httpapi.Config{RequestID: func() string { return "req_budget" }})
		query := readQuery(t)
		query.Metrics = make([]string, 33)
		response := performJSON(handler, http.MethodPost, queryPath("query"), "query", query)
		var submitted application.QueryJobSnapshot
		decodeResponse(t, response, &submitted)
		finished := awaitJob(t, handler, submitted.Job.ID, "query")
		if finished.Job.Status != model.JobFailed || finished.Job.Error == nil || finished.Job.Error.Code != "budget_exceeded" {
			t.Fatalf("budget-rejected job = %#v", finished)
		}
		if engine.executionCount() != 0 {
			t.Fatal("over-budget query executed the engine")
		}
	})
}

func TestHTTPQueryJobEnforcesTimeout(t *testing.T) {
	engine := &fakeEngine{block: true, started: make(chan struct{})}
	handler, _, _ := newTestServerWithEngine(t, httpapi.Config{
		RequestID: func() string { return "req_timeout" }, QueryTimeout: 10 * time.Millisecond,
	}, engine, nil)
	response := performJSON(handler, http.MethodPost, queryPath("query"), "query", readQuery(t))
	var submitted application.QueryJobSnapshot
	decodeResponse(t, response, &submitted)
	finished := awaitJob(t, handler, submitted.Job.ID, "query")
	if finished.Job.Status != model.JobCancelled || finished.Job.Error == nil || finished.Job.Error.Code != "timeout" {
		t.Fatalf("timed-out job = %#v", finished)
	}
}

func TestHTTPManagementUsesAuthenticatedActorAndReturnsReleaseSummaries(t *testing.T) {
	handler, _, _ := newTestServer(t, httpapi.Config{RequestID: func() string { return "req_manage" }})
	source := readSource(t)
	source.Metadata.Version = "1.1.0"

	response := performJSON(handler, http.MethodPut, modelPath("draft"), "query", httpapi.SaveDraftRequest{ExpectedRevision: 1, Source: source})
	assertProblem(t, response, http.StatusForbidden, "permission_denied")

	response = performJSON(handler, http.MethodPut, modelPath("draft"), "manage", httpapi.SaveDraftRequest{ExpectedRevision: 1, Source: source})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("save draft status = %d, body = %s", response.StatusCode, readBody(t, response))
	}
	var draft catalog.Draft
	decodeResponse(t, response, &draft)
	if draft.Revision != 2 || draft.UpdatedBy != "manager@example.com" {
		t.Fatalf("saved draft = %#v", draft)
	}
	request := httptest.NewRequest(http.MethodGet, modelPath("draft"), nil)
	request.Header.Set("Authorization", "Bearer manage")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response = recorder.Result()
	var loaded catalog.Draft
	decodeResponse(t, response, &loaded)
	if response.StatusCode != http.StatusOK || loaded.Revision != draft.Revision || loaded.Source.Metadata.Version != source.Metadata.Version {
		t.Fatalf("loaded draft = %#v, status = %d", loaded, response.StatusCode)
	}

	response = performJSON(handler, http.MethodPost, modelPath("publish"), "manage", httpapi.PublishRequest{ExpectedRevision: 2, Note: "second"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("publish status = %d, body = %s", response.StatusCode, readBody(t, response))
	}

	request = httptest.NewRequest(http.MethodGet, modelPath("releases"), nil)
	request.Header.Set("Authorization", "Bearer manage")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response = recorder.Result()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("release list status = %d, body = %s", response.StatusCode, readBody(t, response))
	}
	var releases []httpapi.ReleaseSummary
	decodeResponse(t, response, &releases)
	if len(releases) != 2 || !releases[1].Active {
		t.Fatalf("release summaries = %#v", releases)
	}

	response = performJSON(handler, http.MethodPost, modelPath("rollback"), "manage", httpapi.RollbackRequest{
		ReleaseID: releases[0].ID, Note: "rollback from HTTP test",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("rollback status = %d, body = %s", response.StatusCode, readBody(t, response))
	}
	var rolledBack catalog.Release
	decodeResponse(t, response, &rolledBack)
	if rolledBack.ID != releases[0].ID {
		t.Fatalf("rolled-back release = %#v", rolledBack)
	}
}

func TestHTTPBodyLimitContentTypeMethodAndNotFoundUseProblems(t *testing.T) {
	handler, _, _ := newTestServer(t, httpapi.Config{MaxBodyBytes: 32, RequestID: func() string { return "req_limits" }})

	response := performJSON(handler, http.MethodPost, queryPath("query"), "query", readQuery(t))
	assertProblem(t, response, http.StatusRequestEntityTooLarge, "request_too_large")

	request := httptest.NewRequest(http.MethodPost, queryPath("query"), strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer query")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	assertProblem(t, recorder.Result(), http.StatusUnsupportedMediaType, "unsupported_media_type")

	request = httptest.NewRequest(http.MethodDelete, modelPath("releases"), nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response = recorder.Result()
	assertProblem(t, response, http.StatusMethodNotAllowed, "method_not_allowed")
	if response.Header.Get("Allow") != http.MethodGet {
		t.Fatalf("Allow = %q", response.Header.Get("Allow"))
	}

	request = httptest.NewRequest(http.MethodGet, "/missing", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	assertProblem(t, recorder.Result(), http.StatusNotFound, "not_found")
}

func TestHTTPRejectsCrossOriginStateChange(t *testing.T) {
	handler, engine, _ := newTestServer(t, httpapi.Config{
		AllowedOrigin: "https://metrics.example.com", RequestID: func() string { return "req_origin" },
	})
	data, err := json.Marshal(readQuery(t))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, queryPath("query"), bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer query")
	request.Header.Set("Origin", "https://attacker.example")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	assertProblem(t, recorder.Result(), http.StatusForbidden, "cross_origin_request")
	if engine.executionCount() != 0 {
		t.Fatal("cross-origin request reached the query engine")
	}
}

func TestHTTPCatalogSearchAndUIUseTheSameAPI(t *testing.T) {
	handler, _, _ := newTestServer(t, httpapi.Config{RequestID: func() string { return "req_ui" }})

	request := httptest.NewRequest(http.MethodGet, httpapi.APIPrefix+"/catalog/search?namespace=demo&q=refund", nil)
	request.Header.Set("Authorization", "Bearer query")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("catalog status = %d, body = %s", response.StatusCode, readBody(t, response))
	}
	var metrics []application.MetricCatalogEntry
	decodeResponse(t, response, &metrics)
	if len(metrics) == 0 || metrics[0].Name == "" || metrics[0].ReleaseID == "" {
		t.Fatalf("catalog metrics = %#v", metrics)
	}
	data, err := json.Marshal(metrics)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("resource")) || bytes.Contains(data, []byte("expression")) {
		t.Fatalf("catalog response leaked execution details: %s", data)
	}

	request = httptest.NewRequest(http.MethodGet, "/", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response = recorder.Result()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("UI response = %d %#v", response.StatusCode, response.Header)
	}
	body := readRawBody(t, response)
	if !strings.Contains(body, "指标目录与受治理查询") || !strings.Contains(body, "/assets/app.js") ||
		!strings.Contains(body, "cancel-job-button") || !strings.Contains(body, "data-management=\"rollback\"") {
		t.Fatalf("UI body = %q", body)
	}

	request = httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response = recorder.Result()
	script := readRawBody(t, response)
	if response.StatusCode != http.StatusOK || !strings.Contains(script, "/api/v1/catalog/search") ||
		!strings.Contains(script, "operation === \"load\"") || !strings.Contains(script, "operation === \"rollback\"") ||
		!strings.Contains(script, "/cancel") {
		t.Fatal("UI JavaScript does not call the product API")
	}
}

func TestRuntimeServerHasDefensiveNetworkTimeouts(t *testing.T) {
	handler, _, _ := newTestServer(t, httpapi.Config{})
	server, err := httpapi.NewRuntimeServer("127.0.0.1:8080", handler)
	if err != nil {
		t.Fatal(err)
	}
	if server.ReadHeaderTimeout <= 0 || server.ReadTimeout <= 0 || server.WriteTimeout <= 0 || server.IdleTimeout <= 0 || server.MaxHeaderBytes <= 0 {
		t.Fatalf("runtime server limits = %#v", server)
	}
}

type observedResolvers struct {
	lastScope    application.QueryScope
	bindingCalls int
}

func newTestServer(t *testing.T, config httpapi.Config) (http.Handler, *fakeEngine, *observedResolvers) {
	return newTestServerWithEngine(t, config, nil, nil)
}

func newTestServerWithEngine(t *testing.T, config httpapi.Config, engine *fakeEngine, recorder audit.Recorder) (http.Handler, *fakeEngine, *observedResolvers) {
	t.Helper()
	repository := catalog.NewMemoryRepository()
	management, err := catalog.NewService(repository)
	if err != nil {
		t.Fatal(err)
	}
	source := readSource(t)
	draft, err := management.SaveDraft(context.Background(), catalog.SaveDraftInput{
		Namespace: "demo", Source: source, Actor: "seed", ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := management.Publish(context.Background(), "demo", "commerce", draft.Revision, "seed", "initial"); err != nil {
		t.Fatal(err)
	}
	var policy model.PolicySource
	var binding model.SourceBinding
	var capabilities model.EngineCapabilities
	root := filepath.Join("..", "..", "examples", "orders")
	readContract(t, filepath.Join(root, "policy.yaml"), &policy)
	readContract(t, filepath.Join(root, "binding.json"), &binding)
	readContract(t, filepath.Join(root, "capabilities.json"), &capabilities)
	if engine == nil {
		engine = &fakeEngine{}
	}
	engine.capabilities = capabilities
	if recorder == nil {
		recorder = audit.NewMemoryRecorder()
	}
	observed := &observedResolvers{}
	policies := application.PolicyResolverFunc(func(_ context.Context, scope application.QueryScope, _ catalog.Release) (model.PolicySource, error) {
		observed.lastScope = scope
		return policy, nil
	})
	bindings := application.BindingResolverFunc(func(_ context.Context, scope application.QueryScope, _ catalog.Release) (model.SourceBinding, error) {
		observed.lastScope = scope
		observed.bindingCalls++
		return binding, nil
	})
	queries, err := application.NewQueryService(repository, policies, bindings, engine, recorder)
	if err != nil {
		t.Fatal(err)
	}
	catalogSearch, err := application.NewCatalogService(repository, policies)
	if err != nil {
		t.Fatal(err)
	}
	jobTimeout := config.QueryTimeout
	if jobTimeout == 0 {
		jobTimeout = time.Second
	}
	jobs, err := application.NewJobManager(context.Background(), queries, jobTimeout, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(jobs.Close)
	authenticator := httpapi.AuthenticatorFunc(func(_ context.Context, request *http.Request) (httpapi.Principal, error) {
		switch request.Header.Get("Authorization") {
		case "Bearer query":
			return httpapi.Principal{Tenant: "demo", Subject: "analyst@example.com", Roles: []string{"analyst"}, Permissions: []httpapi.Permission{httpapi.PermissionQuery}}, nil
		case "Bearer other-query":
			return httpapi.Principal{Tenant: "demo", Subject: "other@example.com", Roles: []string{"analyst"}, Permissions: []httpapi.Permission{httpapi.PermissionQuery}}, nil
		case "Bearer manage":
			return httpapi.Principal{Tenant: "demo", Subject: "manager@example.com", Roles: []string{"manager"}, Permissions: []httpapi.Permission{httpapi.PermissionManage}}, nil
		default:
			return httpapi.Principal{}, httpapi.ErrUnauthenticated
		}
	})
	server, err := httpapi.NewServer(config, httpapi.Dependencies{
		Authenticator: authenticator, Management: management, Catalog: repository, CatalogSearch: catalogSearch, Queries: queries, Jobs: jobs,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return server, engine, observed
}

type fakeEngine struct {
	mu           sync.Mutex
	capabilities model.EngineCapabilities
	executions   int
	block        bool
	started      chan struct{}
	startOnce    sync.Once
}

func (engine *fakeEngine) Capabilities() model.EngineCapabilities { return engine.capabilities }

func (engine *fakeEngine) Execute(ctx context.Context, plan model.PhysicalPlan) (model.ExecutionSnapshot, error) {
	engine.mu.Lock()
	engine.executions++
	block := engine.block
	started := engine.started
	engine.mu.Unlock()
	if block {
		if started != nil {
			engine.startOnce.Do(func() { close(started) })
		}
		<-ctx.Done()
		return model.ExecutionSnapshot{Job: model.ExecutionJob{Status: model.JobCancelled}}, ctx.Err()
	}
	now := time.Now().UTC()
	return model.ExecutionSnapshot{Job: model.ExecutionJob{
		ID: "job_test", Status: model.JobSucceeded, PhysicalFingerprint: plan.Fingerprint,
		RowLimit: int64(plan.Limit), SubmittedAt: now, StartedAt: &now, FinishedAt: &now,
	}, Result: &model.TypedResult{}}, nil
}

func (engine *fakeEngine) executionCount() int {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return engine.executions
}

func performJSON(handler http.Handler, method, path, token string, body any) *http.Response {
	data, _ := json.Marshal(body)
	request := httptest.NewRequest(method, path, bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder.Result()
}

func awaitJob(t *testing.T, handler http.Handler, id, token string) application.QueryJobSnapshot {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		request := httptest.NewRequest(http.MethodGet, httpapi.APIPrefix+"/jobs/"+id, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		response := recorder.Result()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("job status = %d, body = %s", response.StatusCode, readBody(t, response))
		}
		var snapshot application.QueryJobSnapshot
		decodeResponse(t, response, &snapshot)
		if snapshot.Job.Status == model.JobSucceeded || snapshot.Job.Status == model.JobFailed || snapshot.Job.Status == model.JobCancelled {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s did not finish: %#v", id, snapshot)
		}
		time.Sleep(time.Millisecond)
	}
}

func assertProblem(t *testing.T, response *http.Response, status int, code string) {
	t.Helper()
	if response.StatusCode != status {
		t.Fatalf("status = %d, want %d, body = %s", response.StatusCode, status, readBody(t, response))
	}
	if response.Header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("problem content type = %q", response.Header.Get("Content-Type"))
	}
	var problem httpapi.Problem
	decodeResponse(t, response, &problem)
	if problem.Code != code || problem.Status != status || problem.RequestID == "" {
		t.Fatalf("problem = %#v", problem)
	}
}

func assertTrustedScope(t *testing.T, scope application.QueryScope) {
	t.Helper()
	if scope.Namespace != "demo" || scope.ModelName != "commerce" || scope.Context.Tenant != "demo" ||
		scope.Context.Principal != "analyst@example.com" || scope.Context.RequestID != "req_trusted" {
		t.Fatalf("trusted scope = %#v", scope)
	}
}

func readSource(t *testing.T) model.SemanticSource {
	t.Helper()
	var source model.SemanticSource
	readContract(t, filepath.Join("..", "..", "examples", "orders", "model.yaml"), &source)
	return source
}

func readQuery(t *testing.T) model.SemanticQuery {
	t.Helper()
	var query model.SemanticQuery
	readContract(t, filepath.Join("..", "..", "examples", "orders", "query.json"), &query)
	return query
}

func readContract(t *testing.T, path string, target any) {
	t.Helper()
	if err := contractio.ReadFile(path, target); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
}

func decodeResponse(t *testing.T, response *http.Response, target any) {
	t.Helper()
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func readBody(t *testing.T, response *http.Response) string {
	t.Helper()
	var value any
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil && !errors.Is(err, context.Canceled) {
		return err.Error()
	}
	data, _ := json.Marshal(value)
	return string(data)
}

func readRawBody(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	var buffer bytes.Buffer
	if _, err := buffer.ReadFrom(response.Body); err != nil {
		t.Fatal(err)
	}
	return buffer.String()
}

func modelPath(action string) string {
	return httpapi.APIPrefix + "/namespaces/demo/models/commerce/" + action
}

func queryPath(action string) string { return modelPath(action) }
