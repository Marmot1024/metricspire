package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/adapter/databricks"
	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/audit"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/executionauth"
	"github.com/marmot1024/metricspire/internal/exploration"
	"github.com/marmot1024/metricspire/internal/httpapi"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Use an explicit IPv6 loopback listener for exploration transport tests.
// Some development hosts cannot allocate IPv4 loopback client addresses.
func connectAnalysisMCP(t *testing.T, handler http.Handler, token string) *mcp.ClientSession {
	t.Helper()
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "analysis-acceptance", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerRoundTripper{token: token, base: http.DefaultTransport}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

type explorationHTTPFixture struct {
	catalog exploration.Catalog
	query   exploration.Query
}

func performRequest(handler http.Handler, method, path, token string, body io.Reader) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, body)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}
func explorationTransportFixture(t *testing.T) explorationHTTPFixture {
	t.Helper()
	var c exploration.Catalog
	if err := contractio.ReadFile(filepath.Join("..", "..", "testdata", "exploration", "catalog.json"), &c); err != nil {
		t.Fatal(err)
	}
	for i := range c.Datasets {
		c.Datasets[i].Resource.Catalog = "private_catalog"
		c.Datasets[i].Resource.Schema = "private_schema"
	}
	q := exploration.Query{APIVersion: model.APIVersion, Kind: exploration.Kind, Source: "purchases", GroupBy: []string{"tier", "category"}, TimeRange: &model.TimeRange{Dimension: "day", Start: "2026-08-03T00:00:00Z", End: "2026-08-17T00:00:00Z", Timezone: "UTC"}, Measures: []exploration.Measure{{Name: "total", Op: "sum", Field: "amount"}}, Limit: 20}
	return explorationHTTPFixture{c, q}
}

type httpAnalysisEngineFunc func(context.Context, exploration.Plan) (model.ExecutionSnapshot, error)

func (f httpAnalysisEngineFunc) Execute(ctx context.Context, p exploration.Plan) (model.ExecutionSnapshot, error) {
	return f(ctx, p)
}

func newExplorationTransport(t *testing.T, fixture explorationHTTPFixture, engine application.AnalysisEngine, credential httpapi.ExecutionCredentialProvider, configure ...func(*httpapi.Dependencies)) (http.Handler, *fakeEngine) {
	t.Helper()
	recorder := audit.NewMemoryRecorder()
	routes, err := application.NewConfiguredAnalysisCatalogs([]exploration.Catalog{fixture.catalog})
	if err != nil {
		t.Fatal(err)
	}
	service, err := application.NewAnalysisService(nil, routes, engine, recorder)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := application.NewAnalysisJobs(service, 2, 20, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(jobs.Close)
	handler, metricEngine, _ := newTestServerWithDependencies(t, httpapi.Config{RequestID: func() string { return "req_analysis" }}, nil, recorder, nil, credential, func(d *httpapi.Dependencies) {
		d.Analysis = service
		d.AnalysisJobs = jobs
		d.AnalysisCatalogs = routes
		for _, f := range configure {
			f(d)
		}
	})
	return handler, metricEngine
}

func TestAnalysisHTTPAndMCPUseDirectSemantics(t *testing.T) {
	f := explorationTransportFixture(t)
	var calls, creds atomic.Int32
	engine := httpAnalysisEngineFunc(func(ctx context.Context, p exploration.Plan) (model.ExecutionSnapshot, error) {
		calls.Add(1)
		token, ok := executionauth.AccessToken(ctx)
		if !ok || token != "test-existing-credential" {
			t.Error("execution credential not propagated")
		}
		if len(p.Base.Metrics) != 0 || len(p.Relations) != 3 {
			t.Error("analysis incorrectly depends on metric registry")
		}
		if _, err := databricks.CompileAnalysis(p); err != nil {
			t.Error(err)
		}
		return model.ExecutionSnapshot{Job: model.ExecutionJob{Status: model.JobSucceeded, PhysicalFingerprint: p.Fingerprint}, Result: &model.TypedResult{Columns: []model.ResultColumn{{Name: "tier", TypeName: "STRING"}, {Name: "category", TypeName: "STRING"}, {Name: "total", TypeName: "DECIMAL"}}, Rows: [][]any{{"end1", "books", "30.00"}}}}, nil
	})
	credential := httpapi.ExecutionCredentialFunc(func(ctx context.Context, _ *http.Request, _ httpapi.Principal) (context.Context, error) {
		creds.Add(1)
		return executionauth.WithAccessToken(ctx, "test-existing-credential")
	})
	handler, metricEngine := newExplorationTransport(t, f, engine, credential)
	base := "/api/v1/namespaces/demo/analysis"
	for _, token := range []string{"", "manage"} {
		response := performRequest(handler, http.MethodGet, base+"/sources", token, nil)
		if response.Code != http.StatusUnauthorized && response.Code != http.StatusForbidden {
			t.Fatalf("unauthorized discovery: %d", response.Code)
		}
	}
	response := performRequest(handler, http.MethodGet, base+"/sources", "query", nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "week_end") {
		t.Fatalf("discovery: %s", response.Body.String())
	}
	session := connectAnalysisMCP(t, handler, "query")
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil || len(tools.Tools) != 16 {
		t.Fatalf("tools=%v err=%v", tools, err)
	}
	args := map[string]any{"namespace": "demo", "query": f.query}
	plan := callRemoteMCP(t, session, "plan_analysis", args, false)
	if calls.Load() != 0 || creds.Load() != 0 {
		t.Fatal("planning executed or requested credentials")
	}
	for _, field := range []string{"private_catalog", "private_schema", "person_id", `"policy":`, `"resource":`} {
		if strings.Contains(plan, field) {
			t.Fatalf("plan leaked %s", field)
		}
	}
	submitted := callRemoteMCP(t, session, "submit_analysis", args, false)
	var job application.AnalysisJobView
	if err := json.Unmarshal([]byte(submitted), &job); err != nil || job.ID == "" {
		t.Fatal(submitted)
	}
	other := connectAnalysisMCP(t, handler, "other-query")
	for _, tool := range []string{"get_analysis", "cancel_analysis"} {
		callRemoteMCP(t, other, tool, map[string]any{"namespace": "demo", "job_id": job.ID}, true)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		out := callRemoteMCP(t, session, "get_analysis", map[string]any{"namespace": "demo", "job_id": job.ID}, false)
		if err := json.Unmarshal([]byte(out), &job); err != nil {
			t.Fatal(err)
		}
		if job.Status != model.JobRunning {
			if job.Status != model.JobSucceeded || job.Result == nil || job.Result.Rows[0][2] != "30.00" || job.Plan.Semantics == nil {
				t.Fatal(out)
			}
			for _, field := range []string{"private_catalog", "private_schema", "test-existing-credential"} {
				if strings.Contains(out, field) {
					t.Fatalf("result leaked %s", field)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job timeout")
		}
		time.Sleep(time.Millisecond)
	}
	if metricEngine.executionCount() != 0 || calls.Load() != 1 || creds.Load() != 1 {
		t.Fatal("wrong execution path or credential scope")
	}
	// Namespace discovery includes an exploration-only namespace despite an
	// empty UI/model route list; both remote MCP and stdio's HTTP catalog see it.
	out := callRemoteMCP(t, session, "list_namespaces", map[string]any{}, false)
	if !strings.Contains(out, `"demo"`) {
		t.Fatal("exploration namespace missing from MCP discovery")
	}
	response = performRequest(handler, http.MethodGet, "/api/v1/ui/context", "query", nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"demo"`) {
		t.Fatal("exploration namespace missing from stdio namespace catalog")
	}
	// HTTP strictly rejects caller-provided identity and physical configuration.
	body, _ := json.Marshal(f.query)
	var fields map[string]any
	_ = json.Unmarshal(body, &fields)
	fields["context"] = map[string]any{"roles": []string{"admin"}}
	bad, _ := json.Marshal(fields)
	response = performRequest(handler, http.MethodPost, base+"/plan", "query", bytes.NewReader(bad))
	if response.Code != 400 {
		t.Fatalf("injected context status %d", response.Code)
	}
}

func TestAnalysisDiscoveryHonorsAttributeDeny(t *testing.T) {
	f := explorationTransportFixture(t)
	f.catalog.Policy.Rules = append(f.catalog.Policy.Rules, model.PolicyRule{Name: "deny_tier", Effect: model.EffectDeny, Roles: []string{"analyst"}, Dimensions: []string{"tier"}})
	handler, _ := newExplorationTransport(t, f, httpAnalysisEngineFunc(func(context.Context, exploration.Plan) (model.ExecutionSnapshot, error) {
		t.Fatal("denied query executed")
		return model.ExecutionSnapshot{}, nil
	}), nil)
	response := performRequest(handler, http.MethodGet, "/api/v1/namespaces/demo/analysis/sources", "query", nil)
	if strings.Contains(response.Body.String(), `"name":"tier"`) {
		t.Fatal("denied attribute exposed")
	}
	q, _ := json.Marshal(f.query)
	response = performRequest(handler, http.MethodPost, "/api/v1/namespaces/demo/analysis/plan", "query", bytes.NewReader(q))
	if response.Code != 403 {
		t.Fatalf("permission status %d: %s", response.Code, response.Body.String())
	}
}

func TestAnalysisTransportEnforcesInternalAttributes(t *testing.T) {
	f := explorationTransportFixture(t)
	for i := range f.catalog.Attributes {
		f.catalog.Attributes[i].InternalOnly = f.catalog.Attributes[i].Name == "person"
	}
	handler, _ := newExplorationTransport(t, f, httpAnalysisEngineFunc(func(context.Context, exploration.Plan) (model.ExecutionSnapshot, error) {
		t.Fatal("output-denied query reached engine")
		return model.ExecutionSnapshot{}, nil
	}), nil)
	base := "/api/v1/namespaces/demo/analysis"
	response := performRequest(handler, http.MethodGet, base+"/sources", "query", nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"internal_only":true`) {
		t.Fatal("discovery omitted internal-only contract")
	}
	f.query.GroupBy = []string{"person"}
	body, _ := json.Marshal(f.query)
	for _, endpoint := range []string{"/plan", "/jobs"} {
		response = performRequest(handler, http.MethodPost, base+endpoint, "query", bytes.NewReader(body))
		if response.Code != 403 {
			t.Fatalf("internal output accepted at %s: %d", endpoint, response.Code)
		}
	}
	session := connectAnalysisMCP(t, handler, "query")
	args := map[string]any{"namespace": "demo", "query": f.query}
	callRemoteMCP(t, session, "plan_analysis", args, true)
	callRemoteMCP(t, session, "submit_analysis", args, true)
}
