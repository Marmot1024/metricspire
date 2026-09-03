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
	"strings"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/adapter/databricks"
	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/httpapi"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/repository/postgres"
)

// TestPhase3RealHTTPAcceptance is intentionally opt-in. It proves the Phase 3
// HTTP transport against disposable PostgreSQL state and a reviewed read-only
// Databricks fixture. Authentication is a fixed test principal: real OIDC login
// remains a separate deployment gate and must never be inferred from this test.
func TestPhase3RealHTTPAcceptance(t *testing.T) {
	if os.Getenv("METRICSPIRE_RUN_REAL_PHASE3_HTTP_ACCEPTANCE") != "1" {
		t.Skip("set METRICSPIRE_RUN_REAL_PHASE3_HTTP_ACCEPTANCE=1 to run the real Phase 3 HTTP acceptance")
	}
	requireRealDatabricksEnvironment(t,
		"METRICSPIRE_TEST_DATABASE_URL",
		"DATABRICKS_HOST",
		"DATABRICKS_SQL_WAREHOUSE_ID",
		"METRICSPIRE_TEST_DATABRICKS_MODEL",
		"METRICSPIRE_TEST_DATABRICKS_QUERY",
		"METRICSPIRE_TEST_DATABRICKS_POLICY",
		"METRICSPIRE_TEST_DATABRICKS_BINDING",
		"METRICSPIRE_TEST_DATABRICKS_EXPECTED",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	store, err := postgres.Open(ctx, os.Getenv("METRICSPIRE_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := postgres.Migrate(ctx, store.Pool()); err != nil {
		t.Fatal(err)
	}

	var source model.SemanticSource
	readPhase3Fixture(t, "METRICSPIRE_TEST_DATABRICKS_MODEL", &source)
	var query model.SemanticQuery
	readPhase3Fixture(t, "METRICSPIRE_TEST_DATABRICKS_QUERY", &query)
	var policySource model.PolicySource
	readPhase3Fixture(t, "METRICSPIRE_TEST_DATABRICKS_POLICY", &policySource)
	var binding model.SourceBinding
	readPhase3Fixture(t, "METRICSPIRE_TEST_DATABRICKS_BINDING", &binding)
	if binding.Engine != databricks.EngineName {
		t.Fatalf("fixed binding engine = %q, want %q", binding.Engine, databricks.EngineName)
	}
	var expected model.TypedResult
	readPhase3Fixture(t, "METRICSPIRE_TEST_DATABRICKS_EXPECTED", &expected)

	namespace := fmt.Sprintf("phase3_http_acceptance_%d", time.Now().UTC().UnixNano())
	modelName := strings.TrimSpace(source.Metadata.Name)
	management, err := catalog.NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	policyResolver, err := application.NewConfiguredPolicyResolver([]application.PolicyConfiguration{{
		Namespace: namespace, ModelName: modelName, Tenant: policySource.Tenant, Policy: policySource,
	}})
	if err != nil {
		t.Fatal(err)
	}
	bindingResolver, err := application.NewConfiguredBindingResolver([]application.BindingConfiguration{{
		Namespace: namespace, ModelName: modelName, Binding: binding,
	}})
	if err != nil {
		t.Fatal(err)
	}
	catalogSearch, err := application.NewCatalogService(store, policyResolver)
	if err != nil {
		t.Fatal(err)
	}
	tokenSource, err := databricksTokenSource(os.Getenv("DATABRICKS_HOST"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := databricks.NewClient(databricks.ClientConfig{
		Host: os.Getenv("DATABRICKS_HOST"), WarehouseID: os.Getenv("DATABRICKS_SQL_WAREHOUSE_ID"),
		TokenSource: tokenSource,
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := databricks.NewQueryEngine(client)
	if err != nil {
		t.Fatal(err)
	}
	queries, err := application.NewQueryService(store, policyResolver, bindingResolver, engine, store)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := application.NewJobManager(ctx, queries, 5*time.Minute, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer jobs.Close()

	principal := httpapi.Principal{
		Tenant: policySource.Tenant, Subject: "phase3-http-acceptance", Roles: []string{"analyst"},
		Permissions: []httpapi.Permission{httpapi.PermissionManage, httpapi.PermissionQuery},
	}
	handler, err := httpapi.NewServer(httpapi.Config{QueryTimeout: 5 * time.Minute}, httpapi.Dependencies{
		Authenticator: httpapi.AuthenticatorFunc(func(context.Context, *http.Request) (httpapi.Principal, error) {
			return principal, nil
		}),
		Management: management, Catalog: store, CatalogSearch: catalogSearch, Queries: queries, Jobs: jobs,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	rootResponse, err := server.Client().Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	rootBody, readErr := io.ReadAll(rootResponse.Body)
	_ = rootResponse.Body.Close()
	if readErr != nil || rootResponse.StatusCode != http.StatusOK || !bytes.Contains(rootBody, []byte("MetricSpire")) {
		t.Fatalf("UI response = %d %q, %v", rootResponse.StatusCode, rootBody, readErr)
	}

	modelPath := fmt.Sprintf("/api/v1/namespaces/%s/models/%s", namespace, modelName)
	var draft catalog.Draft
	phase3HTTPJSON(t, server.Client(), http.MethodPut, server.URL+modelPath+"/draft",
		httpapi.SaveDraftRequest{ExpectedRevision: 0, Source: source}, http.StatusOK, &draft)
	var release catalog.Release
	phase3HTTPJSON(t, server.Client(), http.MethodPost, server.URL+modelPath+"/publish",
		httpapi.PublishRequest{ExpectedRevision: draft.Revision, Note: "real Phase 3 HTTP acceptance"}, http.StatusCreated, &release)

	var entries []application.MetricCatalogEntry
	phase3HTTPJSON(t, server.Client(), http.MethodGet,
		server.URL+"/api/v1/catalog/search?namespace="+namespace+"&q=gross_revenue&limit=10",
		nil, http.StatusOK, &entries)
	if len(entries) != 1 || entries[0].Name != "gross_revenue" || entries[0].ReleaseID != release.ID {
		t.Fatalf("catalog entries = %#v", entries)
	}

	var plan application.PlanOutput
	phase3HTTPJSON(t, server.Client(), http.MethodPost, server.URL+modelPath+"/plan", query, http.StatusOK, &plan)
	if plan.Release.ID != release.ID || plan.Physical.Fingerprint == "" {
		t.Fatalf("HTTP plan = %#v", plan)
	}
	var submitted application.QueryJobSnapshot
	phase3HTTPJSON(t, server.Client(), http.MethodPost, server.URL+modelPath+"/query", query, http.StatusAccepted, &submitted)
	finished := awaitPhase3HTTPJob(t, ctx, server.Client(), server.URL, submitted.Job.ID)
	if finished.Job.Status != model.JobSucceeded || finished.ReleaseID != release.ID {
		t.Fatalf("HTTP query job = %#v", finished)
	}
	assertExpectedRealResult(t, finished.Result, expected)

	var started, succeeded int
	if err := store.Pool().QueryRow(ctx, `
SELECT
  count(*) FILTER (WHERE event_kind = 'query_started'),
  count(*) FILTER (WHERE event_kind = 'query_succeeded')
FROM metricspire_query_audit
WHERE job_id = $1`, finished.Job.ID).Scan(&started, &succeeded); err != nil {
		t.Fatal(err)
	}
	if started != 1 || succeeded != 1 {
		t.Fatalf("HTTP query audit counts: started=%d succeeded=%d", started, succeeded)
	}
}

func readPhase3Fixture(t *testing.T, environmentName string, target any) {
	t.Helper()
	if err := contractio.ReadFile(os.Getenv(environmentName), target); err != nil {
		t.Fatalf("read %s fixture: %v", environmentName, err)
	}
}

func phase3HTTPJSON(t *testing.T, client *http.Client, method, endpoint string, body any, wantStatus int, target any) {
	t.Helper()
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		requestBody = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, endpoint, requestBody)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s status = %d, want %d, body = %s", method, endpoint, response.StatusCode, wantStatus, data)
	}
	if target != nil {
		if err := json.Unmarshal(data, target); err != nil {
			t.Fatalf("decode %s %s response: %v; body = %s", method, endpoint, err, data)
		}
	}
}

func awaitPhase3HTTPJob(t *testing.T, ctx context.Context, client *http.Client, baseURL, jobID string) application.QueryJobSnapshot {
	t.Helper()
	for {
		var snapshot application.QueryJobSnapshot
		phase3HTTPJSON(t, client, http.MethodGet, baseURL+"/api/v1/jobs/"+jobID, nil, http.StatusOK, &snapshot)
		if realJobTerminal(snapshot.Job.Status) {
			return snapshot
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for Phase 3 HTTP job %s: %v", jobID, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}
