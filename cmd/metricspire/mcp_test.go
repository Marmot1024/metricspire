package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/mcpbridge"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPRejectsInvalidCLIConfig(t *testing.T) {
	t.Setenv("METRICSPIRE_API_TOKEN", "")
	for _, args := range [][]string{{"mcp"}, {"mcp", "--api-url", "https://example.com"}, {"mcp", "--token", "forbidden"}} {
		var stdout, stderr bytes.Buffer
		if err := run(args, &stdout, &stderr); err == nil || stdout.Len() != 0 {
			t.Fatal("invalid MCP config accepted or stdout contaminated")
		}
	}
}

// Subprocess helper exercises the same CLI entry point and real stdio transport.
func TestMCPChildProcess(t *testing.T) {
	if os.Getenv("METRICSPIRE_MCP_TEST_CHILD") != "1" {
		return
	}
	if err := run([]string{"mcp", "--api-url", os.Getenv("METRICSPIRE_MCP_TEST_URL")}, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "MCP child failed")
		os.Exit(1)
	}
	os.Exit(0)
}

func mcpProcess(t *testing.T, command *exec.Cmd) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "metricspire-acceptance", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatalf("MCP initialize: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	assertMCPTools(t, session)
	return session
}

func assertMCPTools(t *testing.T, session *mcp.ClientSession) {
	t.Helper()
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if !reflect.DeepEqual(names, []string{"cancel_query", "explain_query", "get_query", "list_namespaces", "plan_query", "search_metrics", "submit_query"}) {
		t.Fatalf("unexpected tools: %v", names)
	}
}

type remoteMCPBearerTransport struct {
	token string
	base  http.RoundTripper
}

func (transport remoteMCPBearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header = request.Header.Clone()
	request.Header.Set("Authorization", "Bearer "+transport.token)
	return transport.base.RoundTrip(request)
}

func mcpHTTP(t *testing.T, endpoint, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "metricspire-remote-acceptance", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: endpoint,
		HTTPClient: &http.Client{Transport: remoteMCPBearerTransport{
			token: token, base: http.DefaultTransport,
		}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		t.Fatalf("remote MCP initialize: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	assertMCPTools(t, session)
	return session
}

func mcpCall(t *testing.T, session *mcp.ClientSession, name string, in, out any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: in})
	if err != nil {
		t.Fatalf("%s protocol: %v", name, err)
	}
	if result.IsError {
		t.Fatalf("%s returned a tool error: %+v", name, result.Content)
	}
	// Parse the wire text without converting integers through float64.
	for _, content := range result.Content {
		if content, ok := content.(*mcp.TextContent); ok {
			decoder := json.NewDecoder(bytes.NewBufferString(content.Text))
			decoder.UseNumber()
			if err := decoder.Decode(out); err != nil {
				t.Fatalf("%s output JSON: %v", name, err)
			}
			return
		}
	}
	t.Fatalf("%s returned no JSON content", name)
}

func mcpCallError(t *testing.T, session *mcp.ClientSession, name string, in any) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: in})
	if err != nil {
		t.Fatalf("%s protocol: %v", name, err)
	}
	if !result.IsError {
		t.Fatalf("%s unexpectedly succeeded: %+v", name, result.Content)
	}
	var output strings.Builder
	for _, content := range result.Content {
		if content, ok := content.(*mcp.TextContent); ok {
			output.WriteString(content.Text)
		}
	}
	return output.String()
}

func TestMCPStdioCLI(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/ui/context" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("wrong HTTP request")
		}
		fmt.Fprint(w, `{"namespaces":["demo"]}`)
	}))
	defer api.Close()
	command := exec.Command(os.Args[0], "-test.run=^TestMCPChildProcess$")
	command.Env = append(os.Environ(), "METRICSPIRE_MCP_TEST_CHILD=1", "METRICSPIRE_MCP_TEST_URL="+api.URL, "METRICSPIRE_API_TOKEN=test-token")
	session := mcpProcess(t, command)
	var out struct {
		Namespaces []string `json:"namespaces"`
	}
	mcpCall(t, session, "list_namespaces", struct{}{}, &out)
	if !reflect.DeepEqual(out.Namespaces, []string{"demo"}) {
		t.Fatal(out)
	}
}

// Explicit opt-in: this submits only the existing fixed read-only TPCH query
// through an already deployed service; it never writes catalog or engine data.
func TestMCPStagingAcceptance(t *testing.T) {
	if os.Getenv("METRICSPIRE_RUN_MCP_ACCEPTANCE") != "staging-read-only" {
		t.Skip("set METRICSPIRE_RUN_MCP_ACCEPTANCE=staging-read-only with staging URL, binary and token")
	}
	origin, binary, token := os.Getenv("METRICSPIRE_MCP_TEST_URL"), os.Getenv("METRICSPIRE_MCP_TEST_BINARY"), os.Getenv("METRICSPIRE_API_TOKEN")
	if origin == "" || !filepath.IsAbs(binary) || token == "" {
		t.Fatal("staging origin, absolute CLI binary path and API token are required")
	}
	command := exec.Command(binary, "mcp", "--api-url", origin)
	command.Env = os.Environ()
	session := mcpProcess(t, command)
	verifyMCPStagingAcceptance(t, session, "stdio -> HTTP")
}

// Explicit opt-in: this verifies that an external client can use the hosted
// Streamable HTTP endpoint without a local MetricSpire process.
func TestMCPRemoteStagingAcceptance(t *testing.T) {
	if os.Getenv("METRICSPIRE_RUN_MCP_ACCEPTANCE") != "staging-read-only" {
		t.Skip("set METRICSPIRE_RUN_MCP_ACCEPTANCE=staging-read-only with staging URL and token")
	}
	origin, token := os.Getenv("METRICSPIRE_MCP_TEST_URL"), os.Getenv("METRICSPIRE_API_TOKEN")
	if origin == "" || token == "" {
		t.Fatal("staging origin and API token are required")
	}
	session := mcpHTTP(t, strings.TrimSuffix(origin, "/")+"/api/v1/mcp", token)
	verifyMCPStagingAcceptance(t, session, "remote HTTP")
}

// This opt-in probe checks the anonymous OAuth discovery contract at the App
// ingress. It never submits a MetricSpire tool call or analytical query.
func TestMCPRemoteStagingOAuthDiscovery(t *testing.T) {
	if os.Getenv("METRICSPIRE_RUN_MCP_AUTH_ACCEPTANCE") != "staging-read-only" {
		t.Skip("set METRICSPIRE_RUN_MCP_AUTH_ACCEPTANCE=staging-read-only with the staging origin")
	}
	origin := strings.TrimSuffix(os.Getenv("METRICSPIRE_MCP_TEST_URL"), "/")
	if origin == "" {
		t.Fatal("staging origin is required")
	}
	client := &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	metadataURL := origin + "/.well-known/oauth-protected-resource/api/v1/mcp"

	t.Run("unauthenticated_initialize", func(t *testing.T) {
		body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"auth-probe","version":"1"}}}`)
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, origin+"/api/v1/mcp", body)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Errorf("anonymous MCP status=%d, want 401 without a login redirect", response.StatusCode)
		}
		if challenge := response.Header.Get("WWW-Authenticate"); !strings.HasPrefix(challenge, "Bearer ") || !strings.Contains(challenge, `resource_metadata="`+metadataURL+`"`) {
			t.Errorf("anonymous MCP response has no matching Bearer resource_metadata challenge")
		}
	})

	t.Run("protected_resource_metadata", func(t *testing.T) {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, metadataURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("protected resource metadata status=%d, want 200", response.StatusCode)
		}
		var metadata struct {
			Resource             string   `json:"resource"`
			AuthorizationServers []string `json:"authorization_servers"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<16)).Decode(&metadata); err != nil {
			t.Fatal(err)
		}
		if metadata.Resource != origin+"/api/v1/mcp" || len(metadata.AuthorizationServers) != 1 || !strings.HasPrefix(metadata.AuthorizationServers[0], "https://") {
			t.Errorf("protected resource metadata has an unexpected resource or authorization server")
		}
	})
}

// Explicit opt-in: this performs only discovery, explain, and plan calls. It
// proves the hosted time contract without submitting a warehouse statement.
func TestMCPRemoteStagingTimeContractAcceptance(t *testing.T) {
	if os.Getenv("METRICSPIRE_RUN_MCP_ACCEPTANCE") != "staging-read-only" {
		t.Skip("set METRICSPIRE_RUN_MCP_ACCEPTANCE=staging-read-only with staging URL and token")
	}
	origin, token := os.Getenv("METRICSPIRE_MCP_TEST_URL"), os.Getenv("METRICSPIRE_API_TOKEN")
	if origin == "" || token == "" {
		t.Fatal("staging origin and API token are required")
	}
	session := mcpHTTP(t, strings.TrimSuffix(origin, "/")+"/api/v1/mcp", token)
	var entries struct {
		Metrics []application.MetricCatalogEntry `json:"metrics"`
	}
	mcpCall(t, session, "search_metrics", mcpbridge.SearchInput{Namespace: "acceptance", Search: "gross_revenue"}, &entries)
	if len(entries.Metrics) != 1 {
		t.Fatalf("unexpected metric discovery: %#v", entries.Metrics)
	}
	var orderDate *application.MetricDimensionEntry
	for index := range entries.Metrics[0].DimensionDetails {
		if entries.Metrics[0].DimensionDetails[index].Name == "order_date" {
			orderDate = &entries.Metrics[0].DimensionDetails[index]
			break
		}
	}
	if orderDate == nil || orderDate.DataType != model.DataTypeDate || orderDate.CalendarTimezone != "UTC" {
		t.Fatalf("date capability is incomplete: %#v", orderDate)
	}
	trend := mcpbridge.QueryInput{Namespace: "acceptance", Query: model.SemanticQuery{
		APIVersion: model.APIVersion, Kind: model.KindSemanticQuery,
		Metrics: []string{"gross_revenue"}, GroupBy: []string{"order_date"},
		TimeRange:    &model.TimeRange{Dimension: "order_date", Start: "2026-08-02T00:00:00Z", End: "2026-09-01T00:00:00Z", Timezone: "UTC"},
		TimeGrouping: &model.TimeGrouping{Dimension: "order_date", Timezone: "UTC", Granularity: model.GrainDay},
		Limit:        30,
	}}
	var plan struct {
		TimeRange    *model.TimeRange    `json:"time_range"`
		TimeGrouping *model.TimeGrouping `json:"time_grouping"`
		Limit        int                 `json:"limit"`
	}
	mcpCall(t, session, "explain_query", trend, &map[string]any{})
	mcpCall(t, session, "plan_query", trend, &plan)
	if plan.TimeRange == nil || plan.TimeGrouping == nil || plan.Limit != 30 || plan.TimeGrouping.Granularity != model.GrainDay {
		t.Fatalf("30-day trend plan is incomplete: %#v", plan)
	}
	missingGrouping := trend
	missingGrouping.Query.TimeGrouping = nil
	if output := mcpCallError(t, session, "explain_query", missingGrouping); !strings.Contains(output, "invalid_time at time_grouping") || !strings.Contains(output, "provide time_grouping") {
		t.Fatalf("missing-grouping error is not actionable: %q", output)
	}
	wrongTimezone := trend
	wrongTimezone.Query.TimeRange = &model.TimeRange{Dimension: "order_date", Start: "2026-08-01T00:00:00Z", End: "2026-09-01T00:00:00Z", Timezone: "Asia/Shanghai"}
	wrongTimezone.Query.TimeGrouping = &model.TimeGrouping{Dimension: "order_date", Timezone: "Asia/Shanghai", Granularity: model.GrainDay}
	if output := mcpCallError(t, session, "plan_query", wrongTimezone); !strings.Contains(output, "unsupported_timezone at time_range.timezone") || !strings.Contains(output, `use "UTC"`) {
		t.Fatalf("fixed-calendar error is not actionable: %q", output)
	}
}

func verifyMCPStagingAcceptance(t *testing.T, session *mcp.ClientSession, transport string) {
	t.Helper()
	var namespaces struct {
		Namespaces []string `json:"namespaces"`
	}
	mcpCall(t, session, "list_namespaces", struct{}{}, &namespaces)
	if !slices.Contains(namespaces.Namespaces, "acceptance") {
		t.Fatal("acceptance namespace unavailable")
	}
	var entries struct {
		Metrics []application.MetricCatalogEntry `json:"metrics"`
	}
	mcpCall(t, session, "search_metrics", mcpbridge.SearchInput{Namespace: "acceptance", Search: "gross_revenue"}, &entries)
	if len(entries.Metrics) != 1 || entries.Metrics[0].Name != "gross_revenue" || entries.Metrics[0].ModelName != "" {
		t.Fatal("unexpected metric discovery")
	}
	fixture := filepath.Join("..", "..", "testdata", "acceptance", "databricks-tpch")
	var query model.SemanticQuery
	var expected model.TypedResult
	if err := contractio.ReadFile(filepath.Join(fixture, "query.json"), &query); err != nil {
		t.Fatal(err)
	}
	expectedJSON, err := os.ReadFile(filepath.Join(fixture, "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(expectedJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&expected); err != nil {
		t.Fatal(err)
	}
	input := mcpbridge.QueryInput{Namespace: "acceptance", Query: query}
	var explanation struct {
		ReleaseID string `json:"release_id"`
		Metrics   []struct {
			Name string `json:"name"`
		} `json:"metrics"`
		Query model.SemanticQuery `json:"query"`
	}
	mcpCall(t, session, "explain_query", input, &explanation)
	if explanation.ReleaseID != entries.Metrics[0].ReleaseID || len(explanation.Metrics) != 3 || !reflect.DeepEqual(explanation.Query, query) {
		t.Fatal("explanation differs from published query")
	}
	var plan struct {
		ReleaseID  string            `json:"release_id"`
		Engine     string            `json:"engine"`
		Source     model.ResourceRef `json:"source"`
		Metrics    []string          `json:"metrics"`
		Dimensions []string          `json:"dimensions"`
		Limit      int               `json:"limit"`
	}
	mcpCall(t, session, "plan_query", input, &plan)
	if plan.ReleaseID != explanation.ReleaseID || plan.Engine != "databricks_sql" || plan.Source.Table == "" || plan.Limit != query.Limit || len(plan.Metrics) != 3 || len(plan.Dimensions) != 2 {
		t.Fatal("plan differs from the governed published query")
	}
	var job application.QueryJobSnapshot
	mcpCall(t, session, "submit_query", input, &job)
	if job.Job.ID == "" {
		t.Fatal("missing job ID")
	}
	jobID := job.Job.ID
	// Best-effort cancellation of this job only if the assertion/poll fails.
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = session.CallTool(ctx, &mcp.CallToolParams{Name: "cancel_query", Arguments: mcpbridge.JobInput{JobID: jobID}})
	})
	deadline := time.NewTimer(60 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for job.Job.Status == model.JobPending || job.Job.Status == model.JobRunning {
		select {
		case <-deadline.C:
			t.Fatal("MCP query did not finish")
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-ticker.C:
		}
		mcpCall(t, session, "get_query", mcpbridge.JobInput{JobID: jobID}, &job)
	}
	if job.Job.Status != model.JobSucceeded || job.Result == nil || job.ReleaseID != explanation.ReleaseID || job.Job.RowLimit != int64(query.Limit) || !reflect.DeepEqual(*job.Result, expected) {
		t.Fatalf("query/result mismatch: status=%s release=%s", job.Job.Status, job.ReleaseID)
	}
	var after application.QueryJobSnapshot
	mcpCall(t, session, "cancel_query", mcpbridge.JobInput{JobID: jobID}, &after)
	if !reflect.DeepEqual(after, job) {
		t.Fatal("cancel changed a finished job")
	}
	t.Logf("MCP %s -> engine: job=%s release=%s rows=%d truncated=%v; seven tools passed", transport, jobID, job.ReleaseID, len(job.Result.Rows), job.Result.Truncated)
}
