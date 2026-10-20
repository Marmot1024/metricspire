package httpapi_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/httpapi"
	"github.com/marmot1024/metricspire/internal/mcpbridge"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpOnlineService struct {
	calls atomic.Int32
	late  bool
}

func (s *mcpOnlineService) Available(scope application.QueryScope) bool {
	return scope.Namespace == "demo"
}
func (s *mcpOnlineService) Execute(ctx context.Context, scope application.QueryScope, query application.OnlineQuery) (application.OnlineResult, error) {
	s.calls.Add(1)
	if s.late {
		<-ctx.Done()
		return application.OnlineResult{}, nil
	}
	if scope.Context.Principal != "analyst@example.com" {
		return application.OnlineResult{}, &model.Problem{Code: "permission_denied", Message: "reader denied"}
	}
	return application.OnlineResult{ReleaseID: "release", ManifestFingerprint: "manifest", Snapshot: &model.DataSnapshot{BatchID: "batch"}, Result: &model.TypedResult{Rows: [][]any{{"100"}}}}, nil
}
func TestMCPOnlineWithoutAnalyticalEngine(t *testing.T) {
	service := &mcpOnlineService{}
	h, engine, _ := newTestServerWithDependencies(t, httpapi.Config{}, nil, nil, nil, nil, func(d *httpapi.Dependencies) {
		d.Queries, d.Jobs = nil, nil
		d.Online = service
		d.OnlineCatalogSearch = d.CatalogSearch
	})
	s := connectRemoteMCP(t, h, "query")
	var routes mcpbridge.QueryRoutes
	text := callRemoteMCP(t, s, "discover_query_routes", map[string]any{"namespace": "demo"}, false)
	if err := json.Unmarshal([]byte(text), &routes); err != nil || !routes.OnlineMetrics.Available || routes.PublishedMetrics.Available {
		t.Fatal(text)
	}
	in := mcpbridge.OnlineQueryInput{Namespace: "demo", Query: application.OnlineQuery{MetricCodes: []string{"10001"}}}
	text = callRemoteMCP(t, s, "query_online_metrics", in, false)
	if !strings.Contains(text, `"batch"`) {
		t.Fatal(text)
	}
	other := connectRemoteMCP(t, h, "other-query")
	text = callRemoteMCP(t, other, "query_online_metrics", in, true)
	if !strings.Contains(text, "permission_denied") {
		t.Fatal(text)
	}
	// Strict MCP schema must reject authority and SQL overrides before service.
	before := service.calls.Load()
	for _, args := range []any{
		map[string]any{"namespace": "demo", "query": map[string]any{"metric_codes": []string{"10001"}, "time_range": nil, "sql": "SELECT 1"}},
		map[string]any{"namespace": "demo", "query": in.Query, "principal": "someone"},
		map[string]any{"namespace": "../demo", "query": in.Query},
	} {
		callRemoteMCP(t, s, "query_online_metrics", args, true)
	}
	if service.calls.Load() != before || engine.executionCount() != 0 {
		t.Fatal("invalid request or online query reached wrong engine")
	}
}
func TestMCPOnlineAbsentAndLateResult(t *testing.T) {
	h, _, _ := newTestServer(t, httpapi.Config{})
	s := connectRemoteMCP(t, h, "query")
	tools, err := s.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "query_online_metrics" {
			t.Fatal("disabled tool advertised")
		}
	}
	service := &mcpOnlineService{late: true}
	h, _, _ = newTestServerWithDependencies(t, httpapi.Config{OnlineTimeout: 20 * time.Millisecond}, nil, nil, nil, nil, func(d *httpapi.Dependencies) {
		d.Online = service
		d.OnlineCatalogSearch = d.CatalogSearch
	})
	s = connectRemoteMCP(t, h, "query")
	result, err := s.CallTool(t.Context(), &mcp.CallToolParams{Name: "query_online_metrics", Arguments: mcpbridge.OnlineQueryInput{Namespace: "demo", Query: application.OnlineQuery{MetricCodes: []string{"10001"}}}})
	if err != nil || !result.IsError {
		t.Fatalf("late success: %v %#v", err, result)
	}
}

// Simulate a principal with online grants but no analytical catalog access.
type noAnalyticalCatalog struct{ httpapi.CatalogSearcher }

func (n noAnalyticalCatalog) SearchActive(context.Context, application.QueryScope, string, int) ([]application.MetricCatalogEntry, error) {
	return nil, nil
}
func (n noAnalyticalCatalog) SearchActiveCodes(context.Context, application.QueryScope, []string) ([]application.MetricCatalogEntry, error) {
	return nil, nil
}
func TestMCPOnlineOnlyReaderCanDiscoverDefinitions(t *testing.T) {
	service := &mcpOnlineService{}
	h, _, _ := newTestServerWithDependencies(t, httpapi.Config{UIModels: []httpapi.UIModelRoute{{Namespace: "demo", ModelName: "commerce"}}}, nil, nil, nil, nil, func(d *httpapi.Dependencies) {
		d.Online = service
		d.OnlineCatalogSearch = d.CatalogSearch
		d.CatalogSearch = noAnalyticalCatalog{d.CatalogSearch}
	})
	s := connectRemoteMCP(t, h, "query")
	text := callRemoteMCP(t, s, "list_namespaces", map[string]any{}, false)
	if !strings.Contains(text, `"demo"`) {
		t.Fatal("online-only namespace hidden", text)
	}
	var routes mcpbridge.QueryRoutes
	text = callRemoteMCP(t, s, "discover_query_routes", map[string]any{"namespace": "demo"}, false)
	if err := json.Unmarshal([]byte(text), &routes); err != nil || !routes.OnlineMetrics.Available || routes.PublishedMetrics.Available {
		t.Fatal(text)
	}
	text = callRemoteMCP(t, s, "get_metric_details", mcpbridge.MetricDetailsInput{Namespace: "demo", Route: "online_metrics", Metrics: []string{"gross_revenue"}}, false)
	if !strings.Contains(text, "manifest_fingerprint") {
		t.Fatal(text)
	}
	callRemoteMCP(t, s, "get_metric_details", mcpbridge.MetricDetailsInput{Namespace: "demo", Metrics: []string{"gross_revenue"}}, true)
	callRemoteMCP(t, s, "search_metrics", mcpbridge.SearchInput{Namespace: "demo", Route: "untrusted"}, true)
}
