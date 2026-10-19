package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/httpapi"
	"github.com/marmot1024/metricspire/internal/mcpbridge"
)

func TestDisabledQueryRoutesRemainAuthenticatedAndMCPFailsClosed(t *testing.T) {
	handler, engine, _ := newTestServerWithDependencies(t, httpapi.Config{}, nil, nil, nil, nil, func(d *httpapi.Dependencies) { d.Queries, d.Jobs = nil, nil })
	paths := []string{"models/commerce/explain", "models/commerce/plan", "models/commerce/query", "models/commerce/draft-explain", "models/commerce/draft-plan", "models/commerce/draft-query", "explain", "plan", "query"}
	for _, path := range paths {
		for _, token := range []string{"", "query", "manage"} {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/namespaces/demo/"+path, strings.NewReader(`{}`))
			if token != "" {
				r.Header.Set("Authorization", "Bearer "+token)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			want := 401
			if token == "query" {
				want = 404
			} else if token == "manage" {
				want = 403
			}
			if w.Code != want {
				t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
			}
		}
	}
	for _, tc := range []struct{ method, path string }{{"GET", "/api/v1/jobs/job_example"}, {"POST", "/api/v1/jobs/job_example/cancel"}} {
		for token, want := range map[string]int{"": 401, "query": 404, "manage": 403} {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			if token != "" {
				r.Header.Set("Authorization", "Bearer "+token)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != want {
				t.Fatalf("%s %s identity=%s: %d", tc.method, tc.path, token, w.Code)
			}
		}
	}
	session := connectRemoteMCP(t, handler, "query")
	for _, name := range []string{"prepare_query", "plan_query", "submit_query", "explain_query"} {
		text := callRemoteMCP(t, session, name, mcpbridge.QueryInput{Namespace: "demo", Query: readQuery(t)}, true)
		if !strings.Contains(text, "query_engine_disabled") {
			t.Fatal(text)
		}
	}
	textRun := callRemoteMCP(t, session, "run_query", mcpbridge.RunQueryInput{Namespace: "demo", Query: readQuery(t), ExpectedPlan: application.PlanExpectation{ReleaseID: "fixture", ManifestFingerprint: "fixture", LogicalFingerprint: "fixture", PhysicalFingerprint: "fixture"}}, true)
	if !strings.Contains(textRun, "query_engine_disabled") {
		t.Fatal(textRun)
	}
	for _, name := range []string{"get_query", "cancel_query"} {
		text := callRemoteMCP(t, session, name, mcpbridge.JobInput{JobID: "job_example"}, true)
		if !strings.Contains(text, "query_engine_disabled") {
			t.Fatal(text)
		}
	}
	text := callRemoteMCP(t, session, "discover_query_routes", map[string]any{"namespace": "demo"}, false)
	var routes mcpbridge.QueryRoutes
	if err := json.Unmarshal([]byte(text), &routes); err != nil {
		t.Fatal(err)
	}
	if routes.PublishedMetrics.Available || len(routes.PublishedMetrics.Workflow) != 0 || engine.executionCount() != 0 {
		t.Fatalf("disabled route advertised execution: %s", text)
	}
}
