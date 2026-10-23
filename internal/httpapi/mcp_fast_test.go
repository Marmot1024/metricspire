package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/audit"
	"github.com/marmot1024/metricspire/internal/exploration"
	"github.com/marmot1024/metricspire/internal/httpapi"
	"github.com/marmot1024/metricspire/internal/mcpbridge"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func prepareFast(t *testing.T, session *mcp.ClientSession) mcpbridge.PreparedQuery {
	t.Helper()
	text := callRemoteMCP(t, session, "prepare_query", mcpbridge.QueryInput{Namespace: "demo", Query: readQuery(t)}, false)
	var prepared mcpbridge.PreparedQuery
	if err := json.Unmarshal([]byte(text), &prepared); err != nil {
		t.Fatal(err)
	}
	if prepared.ExpectedPlan.ReleaseID == "" || prepared.ExpectedPlan.PhysicalFingerprint == "" || len(prepared.Metrics) == 0 {
		t.Fatalf("incomplete preparation: %s", text)
	}
	for _, forbidden := range []string{`"resource":`, `"expression":`, `"binding":`, `"policy":`, `"semantic_model_name":`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("preparation leaked %s", forbidden)
		}
	}
	return prepared
}

func fastInput(p mcpbridge.PreparedQuery, wait int) mcpbridge.RunQueryInput {
	return mcpbridge.RunQueryInput{Namespace: p.Namespace, Query: p.Query, ExpectedPlan: p.ExpectedPlan, WaitSeconds: wait}
}

func TestFreshMCPClientUsesCompactDiscoveryAndPreparedExecution(t *testing.T) {
	var creds atomic.Int32
	recorder := audit.NewMemoryRecorder()
	handler, engine, observed := newTestServerWithDependencies(t, httpapi.Config{}, nil, recorder, nil, httpapi.ExecutionCredentialFunc(func(ctx context.Context, _ *http.Request, _ httpapi.Principal) (context.Context, error) {
		creds.Add(1)
		return ctx, nil
	}))
	session := connectRemoteMCP(t, handler, "query")
	summary := callRemoteMCP(t, session, "search_metrics", map[string]any{"namespace": "demo", "search": "gross_revenue"}, false)
	for _, forbidden := range []string{"dimension_details", "expression", "semantic_model_name", "usage_examples"} {
		if strings.Contains(summary, forbidden) {
			t.Fatalf("summary contains %s", forbidden)
		}
	}
	if !strings.Contains(summary, `"selection"`) || !strings.Contains(summary, `"candidate_count":1`) {
		t.Fatal("search guidance missing:", summary)
	}
	if observed.bindingCalls != 0 {
		t.Fatal("summary resolved bindings")
	}
	details := callRemoteMCP(t, session, "get_metric_details", map[string]any{"namespace": "demo", "metrics": []string{"gross_revenue", "refund_rate"}}, false)
	if !strings.Contains(details, "dimension_details") || !strings.Contains(details, "time_granularities") || strings.Contains(details, `"expression":`) {
		t.Fatal(details)
	}
	if !strings.Contains(details, `"comparison"`) || !strings.Contains(details, `"differing_fields"`) || !strings.Contains(details, "Role/history") {
		t.Fatal("comparison guidance missing:", details)
	}
	prepared := prepareFast(t, session)
	if engine.executionCount() != 0 || creds.Load() != 0 {
		t.Fatal("discovery/preparation executed analytical work")
	}
	text := callRemoteMCP(t, session, "run_query", fastInput(prepared, 1), false)
	var result mcpbridge.RunQueryOutput
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatal(err)
	}
	if result.Job.Status != "succeeded" || result.Job.ID == "" || result.Result == nil || result.ReleaseID != prepared.ExpectedPlan.ReleaseID || result.WaitError != "" || engine.executionCount() != 1 || creds.Load() != 1 || len(recorder.Events()) != 2 {
		t.Fatalf("fast result: %s", text)
	}
	other := connectRemoteMCP(t, handler, "other-query")
	callRemoteMCP(t, other, "get_query", map[string]any{"job_id": result.Job.ID}, true)
}

func TestFastMCPRejectsChangedAndIncompletePreparationBeforeSubmission(t *testing.T) {
	handler, engine, _ := newTestServer(t, httpapi.Config{})
	session := connectRemoteMCP(t, handler, "query")
	p := prepareFast(t, session)
	changed := fastInput(p, 1)
	changed.Query.Limit++
	if text := callRemoteMCP(t, session, "run_query", changed, true); !strings.Contains(text, "prepared_query_changed") {
		t.Fatal(text)
	}
	incomplete := fastInput(p, 1)
	incomplete.ExpectedPlan.PhysicalFingerprint = ""
	callRemoteMCP(t, session, "run_query", incomplete, true)
	invalidWait := fastInput(p, 16)
	callRemoteMCP(t, session, "run_query", invalidWait, true)
	for _, metrics := range [][]string{nil, {"gross_revenue", "gross_revenue"}, {"missing_metric"}, make([]string, 21)} {
		callRemoteMCP(t, session, "get_metric_details", map[string]any{"namespace": "demo", "metrics": metrics}, true)
	}
	if engine.executionCount() != 0 {
		t.Fatal("invalid preflight reached engine")
	}
}

func TestFastMCPWaitTimeoutKeepsOneJobAndAllowsCancellation(t *testing.T) {
	engine := &fakeEngine{block: true}
	handler, _, _ := newTestServerWithEngine(t, httpapi.Config{QueryTimeout: 5 * time.Second}, engine, nil)
	session := connectRemoteMCP(t, handler, "query")
	p := prepareFast(t, session)
	text := callRemoteMCP(t, session, "run_query", fastInput(p, 1), false)
	var result mcpbridge.RunQueryOutput
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatal(err)
	}
	if result.Job.ID == "" || result.Job.Status != "running" || result.WaitError != "wait_timeout" || engine.executionCount() != 1 {
		t.Fatal(text)
	}
	callRemoteMCP(t, session, "cancel_query", map[string]any{"job_id": result.Job.ID}, false)
	deadline := time.Now().Add(time.Second)
	for {
		text = callRemoteMCP(t, session, "get_query", map[string]any{"job_id": result.Job.ID}, false)
		var snapshot application.QueryJobSnapshot
		if err := json.Unmarshal([]byte(text), &snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot.Job.Status == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("accepted job did not cancel")
		}
	}
	if engine.executionCount() != 1 {
		t.Fatal("wait resubmitted work")
	}
}

type unavailableStatusJobs struct {
	httpapi.JobService
	submits atomic.Int32
}

func (j *unavailableStatusJobs) Submit(ctx context.Context, in application.QueryInput) (application.QueryJobSnapshot, error) {
	j.submits.Add(1)
	return j.JobService.Submit(ctx, in)
}
func (j *unavailableStatusJobs) Get(string, string, string) (application.QueryJobSnapshot, error) {
	return application.QueryJobSnapshot{}, errors.New("private-status-error")
}

func TestFastMCPStatusFailurePreservesAcceptance(t *testing.T) {
	var jobs *unavailableStatusJobs
	handler, _, _ := newTestServerWithDependencies(t, httpapi.Config{}, nil, nil, nil, nil, func(d *httpapi.Dependencies) { jobs = &unavailableStatusJobs{JobService: d.Jobs}; d.Jobs = jobs })
	session := connectRemoteMCP(t, handler, "query")
	p := prepareFast(t, session)
	text := callRemoteMCP(t, session, "run_query", fastInput(p, 1), false)
	var result mcpbridge.RunQueryOutput
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatal(err)
	}
	if result.Job.ID == "" || result.WaitError != "status_unavailable" || jobs.submits.Load() != 1 || strings.Contains(text, "private-status-error") {
		t.Fatal(text)
	}
}

func TestMCPQueryRoutesDescribeActualAuthorizedCapabilities(t *testing.T) {
	handler, engine, _ := newTestServer(t, httpapi.Config{})
	session := connectRemoteMCP(t, handler, "query")
	var routes mcpbridge.QueryRoutes
	text := callRemoteMCP(t, session, "discover_query_routes", map[string]any{"namespace": "demo"}, false)
	if err := json.Unmarshal([]byte(text), &routes); err != nil {
		t.Fatal(err)
	}
	if !routes.PublishedMetrics.Available || routes.Exploration.Available || engine.executionCount() != 0 {
		t.Fatal(text)
	}
	text = callRemoteMCP(t, session, "discover_query_routes", map[string]any{"namespace": "unknown"}, false)
	if err := json.Unmarshal([]byte(text), &routes); err != nil {
		t.Fatal(err)
	}
	if routes.PublishedMetrics.Available || routes.Exploration.Available || !strings.Contains(routes.NextStep, "list_namespaces") || !strings.Contains(routes.NextStep, "query.source") {
		t.Fatal(text)
	}
}

func TestMCPQueryRoutesRespectExplorationCatalogAuthorization(t *testing.T) {
	for _, scenario := range []string{"enabled", "source_role_denied", "policy_denied", "tenant_denied", "analysis_only"} {
		t.Run(scenario, func(t *testing.T) {
			f := explorationTransportFixture(t)
			namespace := "demo"
			switch scenario {
			case "source_role_denied":
				f.catalog.Sources[0].Roles = []string{"restricted"}
			case "policy_denied":
				f.catalog.Policy.Rules[0].Roles = []string{"restricted"}
			case "tenant_denied":
				f.catalog.Tenant, f.catalog.Policy.Tenant = "restricted", "restricted"
			case "analysis_only":
				namespace, f.catalog.Namespace = "exploration_only", "exploration_only"
			}
			var calls atomic.Int32
			engine := httpAnalysisEngineFunc(func(context.Context, exploration.Plan) (model.ExecutionSnapshot, error) {
				calls.Add(1)
				return model.ExecutionSnapshot{}, nil
			})
			handler, metricEngine := newExplorationTransport(t, f, engine, nil)
			session := connectAnalysisMCP(t, handler, "query")
			text := callRemoteMCP(t, session, "discover_query_routes", map[string]any{"namespace": namespace}, false)
			var routes mcpbridge.QueryRoutes
			if err := json.Unmarshal([]byte(text), &routes); err != nil {
				t.Fatal(err)
			}
			wantExploration := scenario == "enabled" || scenario == "analysis_only"
			if routes.PublishedMetrics.Available != (scenario != "analysis_only") || routes.Exploration.Available != wantExploration || metricEngine.executionCount() != 0 || calls.Load() != 0 {
				t.Fatal(text)
			}
		})
	}
}

func TestFastMCPCredentialAdmissionHasDeadline(t *testing.T) {
	var jobs *unavailableStatusJobs
	credential := httpapi.ExecutionCredentialFunc(func(ctx context.Context, _ *http.Request, _ httpapi.Principal) (context.Context, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	handler, engine, _ := newTestServerWithDependencies(t, httpapi.Config{QueryTimeout: 100 * time.Millisecond}, nil, nil, nil, credential, func(d *httpapi.Dependencies) { jobs = &unavailableStatusJobs{JobService: d.Jobs}; d.Jobs = jobs })
	session := connectRemoteMCP(t, handler, "query")
	p := prepareFast(t, session)
	callRemoteMCP(t, session, "run_query", fastInput(p, 1), true)
	if jobs.submits.Load() != 0 || engine.executionCount() != 0 {
		t.Fatal("timed-out credential admission submitted work")
	}
}

func TestFastMCPPreservesTypedIntegerAndDecimal(t *testing.T) {
	engine := &fakeEngine{result: &model.TypedResult{Columns: []model.ResultColumn{{Name: "count", TypeName: "BIGINT"}, {Name: "amount", TypeName: "DECIMAL"}}, Rows: [][]any{{int64(9007199254740993), "12345678901234567890.123456789"}}}}
	handler, _, _ := newTestServerWithEngine(t, httpapi.Config{}, engine, nil)
	session := connectRemoteMCP(t, handler, "query")
	p := prepareFast(t, session)
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "run_query", Arguments: fastInput(p, 1)})
	if err != nil || result.IsError {
		t.Fatalf("run: %v, %v", result, err)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "9007199254740993") || !strings.Contains(text, `"12345678901234567890.123456789"`) {
		t.Fatalf("typed precision lost: %s", text)
	}
	if _, ok := result.Meta["metricspire/elapsed_ms"]; !ok {
		t.Fatal("missing server timing metadata")
	}
}

func TestMCPExplorationRejectsSourceAsNamespaceWithRecovery(t *testing.T) {
	f := explorationTransportFixture(t)
	h, _ := newExplorationTransport(t, f, httpAnalysisEngineFunc(func(context.Context, exploration.Plan) (model.ExecutionSnapshot, error) {
		t.Fatal("discovery executed an engine")
		return model.ExecutionSnapshot{}, nil
	}), nil)
	session := connectRemoteMCP(t, h, "query")
	text := callRemoteMCP(t, session, "discover_analysis", map[string]any{"namespace": "not_a_domain"}, true)
	if !strings.Contains(text, "namespace_not_found") || !strings.Contains(text, "query.source") {
		t.Fatal(text)
	}
	// MCP recovery guidance must not change the existing REST empty-discovery
	// response into an internal error.
	response := performRequest(h, http.MethodGet, "/api/v1/namespaces/not_a_domain/analysis/sources", "query", nil)
	var sources []any
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &sources) != nil || len(sources) != 0 {
		t.Fatalf("REST discovery contract changed: %d %s", response.Code, response.Body.String())
	}
	text = callRemoteMCP(t, session, "discover_analysis", map[string]any{"namespace": "demo"}, false)
	var got struct {
		Namespace string `json:"namespace"`
		Sources   []any  `json:"sources"`
		NextStep  string `json:"next_step"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	if got.Namespace != "demo" || len(got.Sources) == 0 || !strings.Contains(got.NextStep, "query.source") {
		t.Fatal(text)
	}
}
