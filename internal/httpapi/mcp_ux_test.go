package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marmot1024/metricspire/internal/auth/appsauth"
	"github.com/marmot1024/metricspire/internal/httpapi"
)

type identityTransport func(*http.Request) (*http.Response, error)

func (transport identityTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestMCPIdentityDependencyFailureDoesNotPromptOAuth(t *testing.T) {
	for _, test := range []struct {
		name     string
		upstream int
		cause    error
		status   int
		code     string
		body     string
	}{
		{name: "expired credential", upstream: 401, status: 401, code: "unauthenticated"},
		{name: "forbidden identity dependency", upstream: 403, status: 503, code: "authentication_unavailable"},
		{name: "rate limited identity dependency", upstream: 429, status: 503, code: "authentication_unavailable"},
		{name: "unavailable identity dependency", upstream: 503, status: 503, code: "authentication_unavailable"},
		{name: "network failure", cause: errors.New("Bearer secret-must-not-escape"), status: 503, code: "authentication_unavailable"},
		{name: "timeout", cause: context.DeadlineExceeded, status: 504, code: "authentication_unavailable"},
		{name: "disabled account", upstream: 200, status: 403, code: "permission_denied", body: `{"active":false,"id":"123","userName":"analyst@example.com"}`},
		{name: "incomplete identity", upstream: 200, status: 503, code: "authentication_unavailable", body: `{"active":true}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			authenticator, err := appsauth.New(appsauth.Config{
				ExpectedAppName: "metricspire", ExpectedWorkspaceID: "314", ExpectedPublicURL: "https://metrics.example.com",
				Tenant: "demo", QueryRole: "analyst", PublisherGroupID: "publishers",
				HTTPClient: &http.Client{Transport: identityTransport(func(*http.Request) (*http.Response, error) {
					if test.cause != nil {
						return nil, test.cause
					}
					body := test.body
					if body == "" {
						body = "secret-must-not-escape"
					}
					return &http.Response{StatusCode: test.upstream, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
				})},
			}, appsauth.Runtime{AppName: "metricspire", WorkspaceID: "314", Host: "https://workspace.example.com"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			handler, engine, _ := newTestServerWithAuthentication(t, httpapi.Config{
				AllowedOrigin: "https://metrics.example.com", MCPAuthorizationServer: "https://identity.example.com",
				RequestID: func() string { return "req_auth_dependency" },
			}, nil, nil, nil, nil, authenticator)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"ux-test","version":"1"}}}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			request.Header.Set(appsauth.HeaderHost, "metrics.example.com")
			request.Header.Set(appsauth.HeaderAccessToken, "secret-must-not-escape")
			request.Header.Set(appsauth.HeaderUser, "analyst@example.com")
			request.Header.Set(appsauth.HeaderPreferredUsername, "analyst@example.com")
			request.Header.Set(appsauth.HeaderEmail, "analyst@example.com")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			var problem httpapi.Problem
			if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != test.status || problem.Code != test.code || problem.RequestID != "req_auth_dependency" {
				t.Fatalf("misclassified authentication: HTTP=%d problem=%#v", recorder.Code, problem)
			}
			challenge := recorder.Header().Get("WWW-Authenticate")
			if (test.status == 401) != (challenge != "") {
				t.Fatalf("incorrect OAuth challenge for HTTP %d: %q", test.status, challenge)
			}
			if strings.Contains(recorder.Body.String(), "secret-must-not-escape") || engine.executionCount() != 0 {
				t.Fatal("dependency failure leaked a credential or executed a query")
			}
		})
	}
}

func TestRemoteMCPExplainsMissingQueryReferences(t *testing.T) {
	handler, engine, _ := newTestServer(t, httpapi.Config{RequestID: func() string { return "req_missing_reference" }})
	session := connectRemoteMCP(t, handler, "query")
	query := readQuery(t)
	query.GroupBy = append(query.GroupBy, "missing_dimension")
	output := callRemoteMCP(t, session, "plan_query", map[string]any{"namespace": "demo", "query": query}, true)
	for _, expected := range []string{"unknown_reference", "dimensions", "missing_dimension", "does not exist", "req_missing_reference"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing actionable detail %q in %q", expected, output)
		}
	}
	query.Metrics = []string{"missing_metric"}
	output = callRemoteMCP(t, session, "explain_query", map[string]any{"namespace": "demo", "query": query}, true)
	if !strings.Contains(output, "metric_route_not_found at metrics") || !strings.Contains(output, "authorized active model") {
		t.Fatalf("missing actionable route detail: %q", output)
	}
	if engine.executionCount() != 0 {
		t.Fatal("validation failures executed the engine")
	}
}

func TestRemoteMCPExplainsUnsupportedComputation(t *testing.T) {
	handler, engine, _ := newTestServer(t, httpapi.Config{RequestID: func() string { return "req_capability" }})
	engine.capabilities.MaxJoins = 0
	session := connectRemoteMCP(t, handler, "query")
	output := callRemoteMCP(t, session, "plan_query", map[string]any{"namespace": "demo", "query": readQuery(t)}, true)
	for _, expected := range []string{"capability_missing at joins", "supports at most 0 joins", "req_capability"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing actionable capability detail %q in %q", expected, output)
		}
	}
	if engine.executionCount() != 0 {
		t.Fatal("unsupported computation executed a query")
	}
}
