package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
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
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if !reflect.DeepEqual(names, []string{"cancel_query", "explain_query", "get_query", "list_namespaces", "search_metrics", "submit_query"}) {
		t.Fatalf("unexpected tools: %v", names)
	}
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
	t.Logf("MCP stdio -> existing HTTP -> engine: job=%s release=%s rows=%d truncated=%v; six tools passed", jobID, job.ReleaseID, len(job.Result.Rows), job.Result.Truncated)
}
