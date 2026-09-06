package mcpbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connect(t *testing.T, handler http.HandlerFunc) *mcp.ClientSession {
	t.Helper()
	apiServer := httptest.NewServer(handler)
	t.Cleanup(apiServer.Close)
	server, err := New(apiServer.URL, "test-bearer", "test")
	if err != nil {
		t.Fatal(err)
	}
	a, b := mcp.NewInMemoryTransports()
	ss, err := server.Connect(t.Context(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "protocol-test", Version: "1"}, nil)
	cs, err := client.Connect(t.Context(), b, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, in any, wantError bool) string {
	t.Helper()
	out, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: in})
	if err != nil {
		t.Fatal(err)
	}
	if out.IsError != wantError {
		t.Fatalf("%s IsError=%v: %+v", name, out.IsError, out.Content)
	}
	var text strings.Builder
	for _, c := range out.Content {
		if c, ok := c.(*mcp.TextContent); ok {
			text.WriteString(c.Text)
		}
	}
	return text.String()
}

func queryArgs() map[string]any {
	return map[string]any{"namespace": "acceptance", "query": map[string]any{
		"api_version": "metricspire.io/v1alpha1", "kind": "SemanticQuery", "metrics": []string{"revenue"}, "limit": 10,
	}}
}

func TestProtocolToolsUseOnlyExistingAPI(t *testing.T) {
	var mu sync.Mutex
	var routes []string
	cs := connect(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-bearer" {
			t.Error("missing fixed bearer")
		}
		mu.Lock()
		routes = append(routes, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/ui/context":
			fmt.Fprint(w, `{"namespaces":["acceptance"],"models":[{"model_name":"private_route"}],"display_name":"private_person"}`)
		case "/api/v1/catalog/search":
			if r.URL.Query().Get("namespace") != "acceptance" || r.URL.Query().Get("q") != "a&b" || r.URL.Query().Get("limit") != "20" {
				t.Error("search not encoded/defaulted")
			}
			fmt.Fprint(w, `[{"name":"revenue","description":"Sum of paid orders","allowed_dimensions":["channel"],"model_name":"private_route"}]`)
		case "/api/v1/namespaces/acceptance/explain":
			fmt.Fprint(w, `{"release":{"id":"rel_a","manifest_fingerprint":"sha256:a","name":"private_route","manifest":{"definitions":{"metrics":[{"name":"revenue","description":"Sum of paid orders","value_type":"decimal"},{"name":"secret_metric","description":"not authorized"}]}}},"logical_plan":{"limit":10,"metrics":[{"name":"revenue","output":true},{"name":"secret_metric","output":false}]}}`)
		case "/api/v1/namespaces/acceptance/query":
			var got map[string]any
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Error(err)
			}
			if got["limit"] != float64(10) || got["kind"] != "SemanticQuery" || got["namespace"] != nil {
				t.Errorf("query body changed: %v", got)
			}
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"job":{"id":"job_1","status":"running"}}`)
		case "/api/v1/jobs/job_1":
			fmt.Fprint(w, `{"job":{"id":"job_1","status":"succeeded"},"result":{"rows":[[9007199254740993,"228570.520000000000"]],"truncated":false}}`)
		case "/api/v1/jobs/job_1/cancel":
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"job":{"id":"job_1","status":"succeeded"}}`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	listed, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	wantNames := map[string]bool{"list_namespaces": true, "search_metrics": true, "explain_query": true, "submit_query": false, "get_query": true, "cancel_query": false}
	if len(listed.Tools) != len(wantNames) {
		t.Fatalf("tools=%d", len(listed.Tools))
	}
	for _, tool := range listed.Tools {
		want, exists := wantNames[tool.Name]
		if !exists || tool.Annotations.ReadOnlyHint != want || *tool.Annotations.DestructiveHint {
			t.Fatalf("unexpected tool %v", tool)
		}
	}
	for _, tc := range []struct {
		name string
		in   any
		want string
	}{
		{"list_namespaces", map[string]any{}, `"acceptance"`},
		{"search_metrics", SearchInput{Namespace: "acceptance", Search: "a&b"}, `"Sum of paid orders"`},
		{"explain_query", queryArgs(), `"release_id":"rel_a"`},
		{"submit_query", queryArgs(), `"status":"running"`},
		{"get_query", JobInput{JobID: "job_1"}, `9007199254740993,"228570.520000000000"`},
		{"cancel_query", JobInput{JobID: "job_1"}, `"status":"succeeded"`},
	} {
		out := call(t, cs, tc.name, tc.in, false)
		if !strings.Contains(out, tc.want) || strings.Contains(out, "private_") || strings.Contains(out, "secret_metric") {
			t.Fatalf("%s output=%s", tc.name, out)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	wantRoutes := []string{"GET /api/v1/ui/context", "GET /api/v1/catalog/search", "POST /api/v1/namespaces/acceptance/explain", "POST /api/v1/namespaces/acceptance/query", "GET /api/v1/jobs/job_1", "POST /api/v1/jobs/job_1/cancel"}
	if !reflect.DeepEqual(routes, wantRoutes) {
		t.Fatalf("routes=%v", routes)
	}
}

func TestProtocolRejectsIdentitySQLRoutingAndUnknownFields(t *testing.T) {
	var requests atomic.Int32
	cs := connect(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1); fmt.Fprint(w, `{}`) })
	for _, field := range []string{"principal", "tenant", "model", "headers", "token", "api_url", "sql"} {
		for _, nested := range []bool{false, true} {
			in := queryArgs()
			if nested {
				in["query"].(map[string]any)[field] = "forbidden"
			} else {
				in[field] = "forbidden"
			}
			call(t, cs, "submit_query", in, true)
		}
	}
	in := queryArgs()
	in["query"].(map[string]any)["filters"] = []any{map[string]any{"dimension": "x", "operator": "eq", "values": []string{"1"}, "sql": "unsafe"}}
	call(t, cs, "submit_query", in, true)
	in = queryArgs()
	in["namespace"] = "../other"
	call(t, cs, "submit_query", in, true)
	call(t, cs, "get_query", JobInput{JobID: "../admin"}, true)
	in = queryArgs()
	in["query"].(map[string]any)["metrics"] = []string{strings.Repeat("x", maxRequestBytes)}
	if got := call(t, cs, "submit_query", in, true); !strings.Contains(got, "size limit") {
		t.Fatal(got)
	}
	if requests.Load() != 0 {
		t.Fatalf("sent %d invalid requests", requests.Load())
	}
}

func TestConfigurationRejectsUnsafeOriginsAndCredentials(t *testing.T) {
	for _, origin := range []string{"", "http://example.com", "https://user:secret@example.com", "https://example.com/api", "https://example.com?token=secret", "https://example.com#fragment", "file:///tmp/x"} {
		if _, err := New(origin, "token", "test"); err == nil {
			t.Fatalf("accepted %q", origin)
		}
	}
	for _, token := range []string{"", "token\nleak", "token token"} {
		if _, err := New("https://example.com", token, "test"); err == nil {
			t.Fatal("accepted invalid token")
		}
	}
	for _, origin := range []string{"http://127.0.0.1:8000", "http://[::1]:8000", "https://example.com"} {
		if _, err := New(origin, "token", "test"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNamespaceIsEscapedWithoutInventingAnIdentifierRule(t *testing.T) {
	const namespace = "产品.v1&a?"
	cs := connect(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/namespaces/"+namespace+"/query" || r.URL.RawQuery != "" {
			t.Error("namespace changed routing or query parameters")
		}
		fmt.Fprint(w, `{"job":{"id":"job_1"}}`)
	})
	in := queryArgs()
	in["namespace"] = namespace
	call(t, cs, "submit_query", in, false)
}

func TestAPIFailuresAreBoundedSanitizedAndNeverRetried(t *testing.T) {
	for _, status := range []int{401, 403, 422, 429, 500, 502} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var count atomic.Int32
			cs := connect(t, func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				w.WriteHeader(status)
				fmt.Fprint(w, `{"code":"permission_denied","request_id":"req_a","detail":"Bearer test-bearer confidential"}`)
			})
			out := call(t, cs, "submit_query", queryArgs(), true)
			if !strings.Contains(out, fmt.Sprint(status)) || !strings.Contains(out, "req_a") || strings.Contains(out, "test-bearer") || strings.Contains(out, "confidential") || count.Load() != 1 {
				t.Fatal(out)
			}
		})
	}
	t.Run("redirect", func(t *testing.T) {
		var reached atomic.Bool
		sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
		defer sink.Close()
		cs := connect(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, sink.URL, 307) })
		call(t, cs, "submit_query", queryArgs(), true)
		if reached.Load() {
			t.Fatal("followed redirect with bearer")
		}
	})
	for _, body := range []string{`<html>secret proxy response</html>`, strings.Repeat("x", maxResponseBytes+1)} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			cs := connect(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
			out := call(t, cs, "get_query", JobInput{JobID: "job_1"}, true)
			if strings.Contains(out, "secret") || len(out) > 300 {
				t.Fatal("unsafe error output")
			}
		})
	}
}

func TestToolCancellationReachesHTTPRequest(t *testing.T) {
	started, canceled := make(chan struct{}), make(chan struct{})
	cs := connect(t, func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(canceled) })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_query", Arguments: JobInput{JobID: "job_1"}})
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request not started")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("API request not canceled")
	}
	<-done
}

func TestHTTPTimeoutDoesNotRetry(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer s.Close()
	a := &api{origin: s.URL, token: "test", client: &http.Client{Timeout: 10 * time.Millisecond}}
	var out json.RawMessage
	err := a.request(t.Context(), "POST", "/query", struct{}{}, &out)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatal(err)
	}
}

// Response consumption must be bounded even without Content-Length.
func TestChunkedResponseLimit(t *testing.T) {
	cs := connect(t, func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		_, _ = io.Copy(w, strings.NewReader(strings.Repeat("x", maxResponseBytes+1)))
	})
	if out := call(t, cs, "get_query", JobInput{JobID: "job_1"}, true); !strings.Contains(out, "size limit") {
		t.Fatal(out)
	}
}
