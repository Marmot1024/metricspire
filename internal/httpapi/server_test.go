package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/audit"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/executionauth"
	"github.com/marmot1024/metricspire/internal/governance"
	"github.com/marmot1024/metricspire/internal/httpapi"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRemoteMCPAuthenticatesEveryRequestAndExposesSevenScopedTools(t *testing.T) {
	const requestToken = "secret-mcp-request-token"
	const executionToken = "secret-mcp-execution-token"
	var credentialCalls atomic.Int32
	recorder := audit.NewMemoryRecorder()
	credential := httpapi.ExecutionCredentialFunc(func(ctx context.Context, request *http.Request, principal httpapi.Principal) (context.Context, error) {
		credentialCalls.Add(1)
		if request.Header.Get("Authorization") != "Bearer "+requestToken || principal.Subject != "analyst@example.com" {
			t.Fatalf("credential request was not scoped to the authenticated MCP user")
		}
		return executionauth.WithAccessToken(ctx, executionToken)
	})
	handler, engine, _ := newTestServerWithDependencies(t, httpapi.Config{
		RequestID: func() string { return "req_remote_mcp" }, MCPVersion: "test",
		UIModels: []httpapi.UIModelRoute{{Namespace: "demo", ModelName: "commerce"}},
	}, nil, recorder, nil, credential)

	for _, test := range []struct {
		token string
		want  int
		code  string
	}{{"", http.StatusUnauthorized, "unauthenticated"}, {"manage", http.StatusForbidden, "permission_denied"}} {
		response := performMCPInitialize(handler, test.token)
		assertProblem(t, response, test.want, test.code)
	}

	session := connectRemoteMCP(t, handler, requestToken)
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	wantTools := map[string]bool{
		"list_namespaces": true, "search_metrics": true, "explain_query": true, "plan_query": true,
		"submit_query": false, "get_query": true, "cancel_query": false,
	}
	if len(listed.Tools) != len(wantTools) {
		t.Fatalf("tools=%d, want %d", len(listed.Tools), len(wantTools))
	}
	for _, tool := range listed.Tools {
		readOnly, ok := wantTools[tool.Name]
		if !ok || tool.Annotations == nil || tool.Annotations.ReadOnlyHint != readOnly || *tool.Annotations.DestructiveHint {
			t.Fatalf("unexpected tool: %#v", tool)
		}
	}
	invalidTrend := readQuery(t)
	invalidTrend.TimeGrouping = nil
	invalidOutput := callRemoteMCP(t, session, "explain_query", map[string]any{"namespace": "demo", "query": invalidTrend}, true)
	for _, expected := range []string{"invalid_time", "time_grouping", "provide time_grouping", "req_remote_mcp"} {
		if !strings.Contains(invalidOutput, expected) {
			t.Fatalf("actionable validation detail missing %q from %q", expected, invalidOutput)
		}
	}

	query := readQuery(t)
	catalogOutput := callRemoteMCP(t, session, "search_metrics", map[string]any{"namespace": "demo", "search": "gross"}, false)
	for _, expected := range []string{`"dimension_details"`, `"name":"order_date"`, `"data_type":"timestamp"`} {
		if !strings.Contains(catalogOutput, expected) {
			t.Fatalf("catalog dimension metadata missing %q from %q", expected, catalogOutput)
		}
	}
	outputs := []string{
		callRemoteMCP(t, session, "list_namespaces", map[string]any{}, false),
		catalogOutput,
		callRemoteMCP(t, session, "explain_query", map[string]any{"namespace": "demo", "query": query}, false),
		callRemoteMCP(t, session, "plan_query", map[string]any{"namespace": "demo", "query": query}, false),
	}
	if credentialCalls.Load() != 0 || engine.executionCount() != 0 {
		t.Fatal("discovery, explain, or plan requested an execution credential or ran the engine")
	}
	submitted := callRemoteMCP(t, session, "submit_query", map[string]any{"namespace": "demo", "query": query}, false)
	outputs = append(outputs, submitted)
	var submission application.QueryJobSnapshot
	if err := json.Unmarshal([]byte(submitted), &submission); err != nil || submission.Job.ID == "" {
		t.Fatalf("invalid submission: %q, %v", submitted, err)
	}

	other := connectRemoteMCP(t, handler, "other-query")
	for _, name := range []string{"get_query", "cancel_query"} {
		out := callRemoteMCP(t, other, name, map[string]any{"job_id": submission.Job.ID}, true)
		if !strings.Contains(out, "job_not_found") {
			t.Fatalf("%s cross-user response=%q", name, out)
		}
		outputs = append(outputs, out)
	}

	deadline := time.Now().Add(time.Second)
	for {
		out := callRemoteMCP(t, session, "get_query", map[string]any{"job_id": submission.Job.ID}, false)
		outputs = append(outputs, out)
		var snapshot application.QueryJobSnapshot
		if err := json.Unmarshal([]byte(out), &snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot.Job.Status == model.JobSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not finish: %s", out)
		}
		time.Sleep(time.Millisecond)
	}
	outputs = append(outputs, callRemoteMCP(t, session, "cancel_query", map[string]any{"job_id": submission.Job.ID}, false))

	if credentialCalls.Load() != 1 || engine.executionTokenValue() != executionToken {
		t.Fatalf("credential calls=%d, execution token=%q", credentialCalls.Load(), engine.executionTokenValue())
	}
	encodedAudit, err := json.Marshal(recorder.Events())
	if err != nil {
		t.Fatal(err)
	}
	clientOutput := strings.Join(outputs, "\n")
	if strings.Contains(clientOutput, requestToken) || strings.Contains(clientOutput, executionToken) || strings.Contains(clientOutput, `"model_name"`) {
		t.Fatal("MCP response or audit exposed a credential or internal model route")
	}
	if strings.Contains(string(encodedAudit), requestToken) || strings.Contains(string(encodedAudit), executionToken) {
		t.Fatal("query audit exposed an MCP or execution credential")
	}
}

func TestRemoteMCPPublishesOAuthProtectedResourceMetadataAndChallenge(t *testing.T) {
	handler, _, _ := newTestServer(t, httpapi.Config{
		AllowedOrigin:          "https://metrics.example.com",
		MCPAuthorizationServer: "https://identity.example.com/",
		RequestID:              func() string { return "req_mcp_oauth" },
	})

	for _, endpoint := range []string{"/mcp", "/api/v1/mcp"} {
		t.Run(endpoint, func(t *testing.T) {
			response := performMCPInitializeAt(handler, "", endpoint)
			assertProblem(t, response, http.StatusUnauthorized, "unauthenticated")
			metadataPath := "/.well-known/oauth-protected-resource" + endpoint
			if got, want := response.Header.Get("WWW-Authenticate"), `Bearer resource_metadata="https://metrics.example.com`+metadataPath+`"`; got != want {
				t.Fatalf("WWW-Authenticate = %q, want %q", got, want)
			}

			request := httptest.NewRequest(http.MethodGet, metadataPath, nil)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("metadata status=%d; body=%s", recorder.Code, recorder.Body.String())
			}
			var metadata struct {
				Resource             string   `json:"resource"`
				AuthorizationServers []string `json:"authorization_servers"`
			}
			if err := json.NewDecoder(recorder.Result().Body).Decode(&metadata); err != nil {
				t.Fatal(err)
			}
			if metadata.Resource != "https://metrics.example.com"+endpoint || len(metadata.AuthorizationServers) != 1 || metadata.AuthorizationServers[0] != "https://identity.example.com/" {
				t.Fatalf("protected resource metadata = %#v", metadata)
			}
		})
	}
}

func TestRemoteMCPAPIPathConnectsWithAuthenticatedUser(t *testing.T) {
	handler, _, _ := newTestServer(t, httpapi.Config{})
	session := connectRemoteMCPAt(t, handler, "query", "/api/v1/mcp")
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 7 {
		t.Fatalf("API-path MCP tools=%d, want 7", len(listed.Tools))
	}
	output := callRemoteMCP(t, session, "list_namespaces", map[string]any{}, false)
	if !strings.Contains(output, `"namespaces"`) {
		t.Fatalf("API-path MCP discovery response=%q", output)
	}
}

func TestRemoteMCPDoesNotExposeExecutionCredentialErrors(t *testing.T) {
	const secret = "credential-secret-must-not-escape"
	handler, _, _ := newTestServerWithCredential(t, httpapi.Config{RequestID: func() string { return "req_mcp_secret" }}, nil,
		httpapi.ExecutionCredentialFunc(func(context.Context, *http.Request, httpapi.Principal) (context.Context, error) {
			return nil, errors.New(secret)
		}))
	session := connectRemoteMCP(t, handler, "query")
	output := callRemoteMCP(t, session, "submit_query", map[string]any{"namespace": "demo", "query": readQuery(t)}, true)
	if strings.Contains(output, secret) || !strings.Contains(output, "internal_error") || !strings.Contains(output, "req_mcp_secret") {
		t.Fatalf("unsafe remote MCP error: %q", output)
	}
}

func TestRemoteMCPRejectsOversizedRequests(t *testing.T) {
	handler, _, _ := newTestServer(t, httpapi.Config{})
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(strings.Repeat("x", (1<<20)+1)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Authorization", "Bearer query")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d, want %d; body=%s", recorder.Code, http.StatusRequestEntityTooLarge, recorder.Body.String())
	}
}

func TestRemoteMCPWorksBehindAuthenticatedReverseProxy(t *testing.T) {
	handler, _, _ := newTestServer(t, httpapi.Config{})
	server := httptest.NewServer(handler)
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"proxy-test","version":"1"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "192.0.2.10:8000"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Authorization", "Bearer query")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want %d; body=%s", response.StatusCode, http.StatusOK, readBody(t, response))
	}
}

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

func TestHTTPMetricFirstQueryResolvesTheInternalModel(t *testing.T) {
	handler, engine, observed := newTestServer(t, httpapi.Config{RequestID: func() string { return "req_metric_first" }})
	query := readQuery(t)
	path := httpapi.APIPrefix + "/namespaces/demo/query"
	response := performJSON(handler, http.MethodPost, path, "query", query)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("metric-first query status = %d, body = %s", response.StatusCode, readBody(t, response))
	}
	var submitted application.QueryJobSnapshot
	decodeResponse(t, response, &submitted)
	finished := awaitJob(t, handler, submitted.Job.ID, "query")
	if finished.Job.Status != model.JobSucceeded || engine.executionCount() != 1 || observed.lastScope.ModelName != "commerce" {
		t.Fatalf("metric-first result = %#v, scope = %#v", finished, observed.lastScope)
	}

	query.Metrics = []string{"does_not_exist"}
	response = performJSON(handler, http.MethodPost, path, "query", query)
	assertProblem(t, response, http.StatusUnprocessableEntity, "metric_route_not_found")
}

func TestHTTPAddsExecutionCredentialOnlyToQuery(t *testing.T) {
	engine := &fakeEngine{}
	credentialCalls := 0
	credential := httpapi.ExecutionCredentialFunc(func(ctx context.Context, _ *http.Request, principal httpapi.Principal) (context.Context, error) {
		credentialCalls++
		if principal.Subject != "analyst@example.com" {
			t.Fatalf("credential principal = %#v", principal)
		}
		return executionauth.WithAccessToken(ctx, "request-user-token")
	})
	handler, _, _ := newTestServerWithCredential(t, httpapi.Config{RequestID: func() string { return "req_credential" }}, engine, credential)
	query := readQuery(t)
	for _, operation := range []string{"explain", "plan"} {
		response := performJSON(handler, http.MethodPost, queryPath(operation), "query", query)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d, body = %s", operation, response.StatusCode, readBody(t, response))
		}
	}
	if credentialCalls != 0 {
		t.Fatalf("credential provider called %d times before execution", credentialCalls)
	}
	response := performJSON(handler, http.MethodPost, queryPath("query"), "query", query)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("query status = %d, body = %s", response.StatusCode, readBody(t, response))
	}
	var submitted application.QueryJobSnapshot
	decodeResponse(t, response, &submitted)
	finished := awaitJob(t, handler, submitted.Job.ID, "query")
	if finished.Job.Status != model.JobSucceeded || credentialCalls != 1 || engine.executionTokenValue() != "request-user-token" {
		t.Fatalf("finished = %#v, credential calls = %d, engine token = %q", finished, credentialCalls, engine.executionTokenValue())
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

func TestHTTPGovernanceImportIsVersionedAndRollbackRemovesInitialBatch(t *testing.T) {
	t.Parallel()
	handler, _, _ := newTestServer(t, httpapi.Config{})
	definition := governance.MetricDefinition{
		Code: "pending_metric", DisplayName: "待治理指标", Description: "保留来源定义，不补造公式。",
		Owner: "owner", Status: "unverified", BusinessType: governance.BusinessDerived,
		SemanticReadiness:   governance.ReadinessNeedsRemediation,
		AuthoritativeSource: governance.SourceReference{Reference: "sheet:1", Resource: "daily", Field: "metric_value"},
		ValueType:           model.DataTypeDecimal, Verification: model.Verification{Status: model.VerificationUnverified},
	}
	path := "/api/v1/namespaces/game/governance/imports"
	response := performJSON(handler, http.MethodPost, path, "manage", httpapi.GovernanceImportRequest{
		SourceFingerprint: "not-a-fingerprint", Records: []governance.MetricDefinition{definition},
	})
	assertProblem(t, response, http.StatusUnprocessableEntity, "invalid_request")
	response = performJSON(handler, http.MethodPost, path, "manage", httpapi.GovernanceImportRequest{
		SourceFingerprint: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Records:           []governance.MetricDefinition{definition},
	})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("import status = %d, body = %s", response.StatusCode, readRawBody(t, response))
	}
	var batch governance.ImportBatch
	decodeResponse(t, response, &batch)
	if batch.RecordCount != 1 || batch.CreatedBy != "manager@example.com" {
		t.Fatalf("import batch = %#v", batch)
	}
	response = performJSON(handler, http.MethodGet, "/api/v1/namespaces/game/governance/metrics?limit=1000", "manage", nil)
	var records []governance.MetricRecord
	decodeResponse(t, response, &records)
	if len(records) != 1 || records[0].Definition.Code != definition.Code {
		t.Fatalf("governance records = %#v", records)
	}
	response = performJSON(handler, http.MethodPost, path+"/"+batch.ID+"/rollback", "manage", map[string]any{})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("rollback status = %d, body = %s", response.StatusCode, readRawBody(t, response))
	}
	response = performJSON(handler, http.MethodGet, "/api/v1/namespaces/game/governance/metrics?limit=1000", "manage", nil)
	decodeResponse(t, response, &records)
	if len(records) != 0 {
		t.Fatalf("records after rollback = %#v", records)
	}
}

func TestHTTPDraftPreviewRequiresBothPermissionsAndDoesNotPublish(t *testing.T) {
	t.Parallel()
	handler, engine, _ := newTestServer(t, httpapi.Config{})
	path := modelPath("draft-query")
	response := performJSON(handler, http.MethodPost, path, "manage", readQuery(t))
	assertProblem(t, response, http.StatusForbidden, "permission_denied")
	response = performJSON(handler, http.MethodPost, path, "query", readQuery(t))
	assertProblem(t, response, http.StatusForbidden, "permission_denied")
	response = performJSON(handler, http.MethodPost, path, "preview", readQuery(t))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("preview status = %d, body = %s", response.StatusCode, readRawBody(t, response))
	}
	var output application.QueryOutput
	decodeResponse(t, response, &output)
	if output.Release.ID != "draft:r1" || output.Execution.Job.Status != model.JobSucceeded || engine.executionCount() != 1 {
		t.Fatalf("draft preview output = %#v, executions = %d", output, engine.executionCount())
	}
}

func TestHTTPGovernanceReviewIsReadOnlyAndManagerOnly(t *testing.T) {
	handler, _, observed := newTestServer(t, httpapi.Config{RequestID: func() string { return "req_review" }})
	source := readSource(t)

	response := performJSON(handler, http.MethodPost, modelPath("review"), "query", httpapi.ReviewRequest{Source: source})
	assertProblem(t, response, http.StatusForbidden, "permission_denied")

	response = performJSON(handler, http.MethodPost, modelPath("review"), "manage", httpapi.ReviewRequest{Source: source})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("review status = %d, body = %s", response.StatusCode, readBody(t, response))
	}
	var review application.GovernanceReview
	decodeResponse(t, response, &review)
	if review.ActiveRelease == nil || review.ActiveRelease.ID == "" || review.Binding.Status != application.BindingReady ||
		len(review.MetricChanges) != 0 || observed.bindingCalls != 1 {
		t.Fatalf("review = %#v, binding calls = %d", review, observed.bindingCalls)
	}

	request := httptest.NewRequest(http.MethodGet, modelPath("draft"), nil)
	request.Header.Set("Authorization", "Bearer manage")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var draft catalog.Draft
	decodeResponse(t, recorder.Result(), &draft)
	if draft.Revision != 1 {
		t.Fatalf("review changed draft revision to %d", draft.Revision)
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
	handler, _, _ := newTestServer(t, httpapi.Config{
		AuthenticationProfile: "databricks_apps",
		UIModels:              []httpapi.UIModelRoute{{Namespace: "demo", ModelName: "commerce"}},
		RequestID:             func() string { return "req_ui" },
	})

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
	if bytes.Contains(data, []byte("resource")) || bytes.Contains(data, []byte("expression")) || bytes.Contains(data, []byte("model_name")) {
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
	if !strings.Contains(body, "<h1>指标库</h1>") || !strings.Contains(body, "/assets/app.js") ||
		!strings.Contains(body, "catalog-status-filter") || !strings.Contains(body, "query-metric-search") ||
		!strings.Contains(body, "business-verification-note") ||
		!strings.Contains(body, "run-query-button") || !strings.Contains(body, "governance-tab") ||
		!strings.Contains(body, "<h1>治理发布</h1>") || !strings.Contains(body, "editor-status") ||
		!strings.Contains(body, "<h1>查询验证</h1>") {
		t.Fatal("UI is missing a catalog, governance, query or feedback entry")
	}

	request = httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response = recorder.Result()
	script := readRawBody(t, response)
	if response.StatusCode != http.StatusOK || !strings.Contains(script, "/api/v1/ui/context") ||
		!strings.Contains(script, "/api/v1/catalog/search") || !strings.Contains(script, "reviewGovernance") ||
		!strings.Contains(script, "rollbackRelease") || !strings.Contains(script, "/cancel") ||
		!strings.Contains(script, "result-table") || !strings.Contains(script, "previous_month") {
		t.Fatal("UI JavaScript does not call the product API")
	}
}

func TestHTTPUIContextUsesAuthenticatedIdentityAndConfiguredModels(t *testing.T) {
	handler, _, _ := newTestServer(t, httpapi.Config{
		AuthenticationProfile: "databricks_apps",
		UIModels: []httpapi.UIModelRoute{
			{Namespace: "acceptance", ModelName: "tpch_orders"},
			{Namespace: "demo", ModelName: "commerce"},
		},
		RequestID: func() string { return "req_ui_context" },
	})

	request := httptest.NewRequest(http.MethodGet, httpapi.APIPrefix+"/ui/context", nil)
	request.Header.Set("Authorization", "Bearer query")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("context status = %d, body = %s", response.StatusCode, readBody(t, response))
	}
	var context httpapi.UIContext
	decodeResponse(t, response, &context)
	if context.AuthenticationProfile != "databricks_apps" || context.DisplayName != "Analyst" ||
		len(context.Permissions) != 1 || context.Permissions[0] != httpapi.PermissionQuery ||
		len(context.Namespaces) != 2 || context.Namespaces[0] != "acceptance" || len(context.Models) != 0 {
		t.Fatalf("UI context = %#v", context)
	}

	request = httptest.NewRequest(http.MethodGet, httpapi.APIPrefix+"/ui/context", nil)
	request.Header.Set("Authorization", "Bearer manage")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response = recorder.Result()
	decodeResponse(t, response, &context)
	if len(context.Models) != 2 || context.Models[0].ModelName != "tpch_orders" {
		t.Fatalf("manager UI context = %#v", context)
	}

	request = httptest.NewRequest(http.MethodGet, httpapi.APIPrefix+"/ui/context", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	assertProblem(t, recorder.Result(), http.StatusUnauthorized, "unauthenticated")

	request = httptest.NewRequest(http.MethodPost, httpapi.APIPrefix+"/ui/context", nil)
	request.Header.Set("Authorization", "Bearer query")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response = recorder.Result()
	assertProblem(t, response, http.StatusMethodNotAllowed, "method_not_allowed")
	if response.Header.Get("Allow") != http.MethodGet {
		t.Fatalf("Allow = %q", response.Header.Get("Allow"))
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
	if server.Protocols == nil || !server.Protocols.HTTP1() || !server.Protocols.HTTP2() || !server.Protocols.UnencryptedHTTP2() {
		t.Fatalf("runtime server protocols = %v", server.Protocols)
	}
}

func TestRuntimeServerAcceptsUnencryptedHTTP2(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	server, err := httpapi.NewRuntimeServer(listener.Addr().String(), http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.ProtoMajor != 2 {
			t.Errorf("request protocol = %s", request.Proto)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	if err != nil {
		t.Fatal(err)
	}
	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.Serve(listener)
	}()

	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	transport := &http.Transport{Protocols: protocols}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	t.Cleanup(transport.CloseIdleConnections)
	response, err := client.Get("http://" + listener.Addr().String())
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent || response.ProtoMajor != 2 {
		t.Fatalf("H2C response = %s %d", response.Proto, response.StatusCode)
	}

	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
	if err := <-serveErrors; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Serve returned %v", err)
	}
}

func TestHTTPHealthEndpointsAreUnauthenticatedAndReadinessFailsClosed(t *testing.T) {
	handler, _, _ := newTestServer(t, httpapi.Config{RequestID: func() string { return "req_health" }})
	for path, wantStatus := range map[string]int{"/health/live": http.StatusOK, "/health/ready": http.StatusOK} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != wantStatus || !strings.Contains(recorder.Body.String(), "status") {
			t.Fatalf("GET %s = %d %q", path, recorder.Code, recorder.Body.String())
		}
	}

	failing, _, _ := newTestServerWithEngineAndReadiness(t, httpapi.Config{RequestID: func() string { return "req_not_ready" }}, nil, nil,
		httpapi.ReadinessFunc(func(context.Context) error { return errors.New("database address must stay private") }))
	request := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	recorder := httptest.NewRecorder()
	failing.ServeHTTP(recorder, request)
	response := recorder.Result()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("failed readiness status = %d", response.StatusCode)
	}
	body := readRawBody(t, response)
	if strings.Contains(body, "database address") || !strings.Contains(body, "not_ready") {
		t.Fatalf("failed readiness body = %q", body)
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
	return newTestServerWithEngineAndReadiness(t, config, engine, recorder, nil)
}

func newTestServerWithEngineAndReadiness(t *testing.T, config httpapi.Config, engine *fakeEngine, recorder audit.Recorder, readiness httpapi.ReadinessChecker) (http.Handler, *fakeEngine, *observedResolvers) {
	return newTestServerWithDependencies(t, config, engine, recorder, readiness, nil)
}

func newTestServerWithCredential(t *testing.T, config httpapi.Config, engine *fakeEngine, credential httpapi.ExecutionCredentialProvider) (http.Handler, *fakeEngine, *observedResolvers) {
	return newTestServerWithDependencies(t, config, engine, nil, nil, credential)
}

func newTestServerWithDependencies(t *testing.T, config httpapi.Config, engine *fakeEngine, recorder audit.Recorder, readiness httpapi.ReadinessChecker, credential httpapi.ExecutionCredentialProvider) (http.Handler, *fakeEngine, *observedResolvers) {
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
	catalogSearch, err := application.NewCatalogService(repository, policies, bindings)
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
	governanceService, err := governance.NewService(governance.NewMemoryRepository())
	if err != nil {
		t.Fatal(err)
	}
	authenticator := httpapi.AuthenticatorFunc(func(_ context.Context, request *http.Request) (httpapi.Principal, error) {
		switch request.Header.Get("Authorization") {
		case "Bearer query":
			return httpapi.Principal{Tenant: "demo", Subject: "analyst@example.com", DisplayName: "Analyst", Roles: []string{"analyst"}, Permissions: []httpapi.Permission{httpapi.PermissionQuery}}, nil
		case "Bearer secret-mcp-request-token":
			return httpapi.Principal{Tenant: "demo", Subject: "analyst@example.com", DisplayName: "Analyst", Roles: []string{"analyst"}, Permissions: []httpapi.Permission{httpapi.PermissionQuery}}, nil
		case "Bearer other-query":
			return httpapi.Principal{Tenant: "demo", Subject: "other@example.com", Roles: []string{"analyst"}, Permissions: []httpapi.Permission{httpapi.PermissionQuery}}, nil
		case "Bearer manage":
			return httpapi.Principal{Tenant: "demo", Subject: "manager@example.com", Roles: []string{"manager"}, Permissions: []httpapi.Permission{httpapi.PermissionManage}}, nil
		case "Bearer preview":
			return httpapi.Principal{Tenant: "demo", Subject: "manager@example.com", Roles: []string{"manager"}, Permissions: []httpapi.Permission{httpapi.PermissionManage, httpapi.PermissionQuery}}, nil
		default:
			return httpapi.Principal{}, httpapi.ErrUnauthenticated
		}
	})
	if readiness == nil {
		readiness = httpapi.ReadinessFunc(func(context.Context) error { return nil })
	}
	server, err := httpapi.NewServer(config, httpapi.Dependencies{
		Authenticator: authenticator, Readiness: readiness,
		Management: management, Catalog: repository, CatalogSearch: catalogSearch,
		Governance: governanceService,
		Bindings:   bindings, Queries: queries, Jobs: jobs,
		ExecutionCredential: credential,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return server, engine, observed
}

type fakeEngine struct {
	mu             sync.Mutex
	capabilities   model.EngineCapabilities
	executions     int
	executionToken string
	block          bool
	started        chan struct{}
	startOnce      sync.Once
}

func (engine *fakeEngine) Capabilities() model.EngineCapabilities { return engine.capabilities }

func (engine *fakeEngine) Execute(ctx context.Context, plan model.PhysicalPlan) (model.ExecutionSnapshot, error) {
	engine.mu.Lock()
	engine.executions++
	engine.executionToken, _ = executionauth.AccessToken(ctx)
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

func (engine *fakeEngine) executionTokenValue() string {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return engine.executionToken
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

func performMCPInitialize(handler http.Handler, token string) *http.Response {
	return performMCPInitializeAt(handler, token, "/mcp")
}

func performMCPInitializeAt(handler http.Handler, token, endpoint string) *http.Response {
	request := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder.Result()
}

type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (transport bearerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header = request.Header.Clone()
	request.Header.Set("Authorization", "Bearer "+transport.token)
	return transport.base.RoundTrip(request)
}

func connectRemoteMCP(t *testing.T, handler http.Handler, token string) *mcp.ClientSession {
	return connectRemoteMCPAt(t, handler, token, "/mcp")
}

func connectRemoteMCPAt(t *testing.T, handler http.Handler, token, endpoint string) *mcp.ClientSession {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "remote-test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: server.URL + endpoint, HTTPClient: &http.Client{Transport: bearerRoundTripper{token: token, base: http.DefaultTransport}},
		DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func callRemoteMCP(t *testing.T, session *mcp.ClientSession, name string, arguments any, wantError bool) string {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError != wantError {
		t.Fatalf("%s IsError=%v: %#v", name, result.IsError, result.Content)
	}
	var output strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			output.WriteString(text.Text)
		}
	}
	return output.String()
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
