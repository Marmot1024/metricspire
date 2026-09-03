package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/httpapi"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/repository/postgres"
)

// TestPhase3DeployedAcceptance is the final non-interactive Phase 3 staging
// probe. It talks only to an already deployed MetricSpire service and its
// PostgreSQL control plane. The deployed SourceBinding decides which analytical
// engine is queried, so operators must review that it targets a read-only
// staging fixture before enabling this test. Browser login remains a separate
// interactive acceptance gate and must not be inferred from bearer-token proof.
func TestPhase3DeployedAcceptance(t *testing.T) {
	if os.Getenv("METRICSPIRE_RUN_DEPLOYED_PHASE3_ACCEPTANCE") != "staging-read-only" {
		t.Skip("set METRICSPIRE_RUN_DEPLOYED_PHASE3_ACCEPTANCE=staging-read-only to run deployed acceptance")
	}
	requireDeployedPhase3Environment(t,
		"METRICSPIRE_DEPLOYED_BASE_URL",
		"METRICSPIRE_DEPLOYED_DATABASE_URL",
		"METRICSPIRE_DEPLOYED_NAMESPACE",
		"METRICSPIRE_DEPLOYED_MANAGE_TOKEN",
		"METRICSPIRE_DEPLOYED_QUERY_TOKEN",
		"METRICSPIRE_TEST_DATABRICKS_MODEL",
		"METRICSPIRE_TEST_DATABRICKS_QUERY",
		"METRICSPIRE_TEST_DATABRICKS_EXPECTED",
	)

	baseURL, err := deployedBaseURL(os.Getenv("METRICSPIRE_DEPLOYED_BASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	manageToken := strings.TrimSpace(os.Getenv("METRICSPIRE_DEPLOYED_MANAGE_TOKEN"))
	queryToken := strings.TrimSpace(os.Getenv("METRICSPIRE_DEPLOYED_QUERY_TOKEN"))
	if manageToken == queryToken {
		t.Fatal("deployed manage and query tokens must represent separate least-privilege principals")
	}
	namespace := strings.TrimSpace(os.Getenv("METRICSPIRE_DEPLOYED_NAMESPACE"))

	var source model.SemanticSource
	readPhase3Fixture(t, "METRICSPIRE_TEST_DATABRICKS_MODEL", &source)
	var query model.SemanticQuery
	readPhase3Fixture(t, "METRICSPIRE_TEST_DATABRICKS_QUERY", &query)
	var expected model.TypedResult
	readPhase3Fixture(t, "METRICSPIRE_TEST_DATABRICKS_EXPECTED", &expected)
	modelName := strings.TrimSpace(source.Metadata.Name)
	if namespace == "" || modelName == "" || len(source.Spec.Metrics) == 0 {
		t.Fatal("deployed acceptance namespace, fixture model name, and at least one metric are required")
	}

	client := &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	deployedHealth(t, client, baseURL+"/health/live", "ok")
	deployedHealth(t, client, baseURL+"/health/ready", "ready")
	deployedRootAndLogin(t, client, baseURL)

	modelPath := fmt.Sprintf("/api/v1/namespaces/%s/models/%s", url.PathEscape(namespace), url.PathEscape(modelName))
	catalogPath := "/api/v1/catalog/search?namespace=" + url.QueryEscape(namespace) + "&q=" + url.QueryEscape(source.Spec.Metrics[0].Name) + "&limit=10"
	deployedHTTPJSON(t, client, http.MethodGet, baseURL+catalogPath, "", nil, http.StatusUnauthorized, nil)
	deployedHTTPJSON(t, client, http.MethodGet, baseURL+modelPath+"/draft", queryToken, nil, http.StatusForbidden, nil)
	deployedHTTPJSON(t, client, http.MethodGet, baseURL+catalogPath, manageToken, nil, http.StatusForbidden, nil)

	var draft catalog.Draft
	deployedHTTPJSON(t, client, http.MethodPut, baseURL+modelPath+"/draft", manageToken,
		httpapi.SaveDraftRequest{ExpectedRevision: 0, Source: source}, http.StatusOK, &draft)
	var release catalog.Release
	deployedHTTPJSON(t, client, http.MethodPost, baseURL+modelPath+"/publish", manageToken,
		httpapi.PublishRequest{ExpectedRevision: draft.Revision, Note: "deployed Phase 3 acceptance v1"}, http.StatusCreated, &release)

	var entries []application.MetricCatalogEntry
	deployedHTTPJSON(t, client, http.MethodGet, baseURL+catalogPath, queryToken, nil, http.StatusOK, &entries)
	if len(entries) != 1 || entries[0].ReleaseID != release.ID {
		t.Fatalf("deployed catalog entries = %#v", entries)
	}
	var plan application.PlanOutput
	deployedHTTPJSON(t, client, http.MethodPost, baseURL+modelPath+"/plan", queryToken, query, http.StatusOK, &plan)
	if plan.Release.ID != release.ID || plan.Physical.Fingerprint == "" {
		t.Fatalf("deployed plan = %#v", plan)
	}
	firstJob := submitDeployedQuery(t, client, baseURL, modelPath, queryToken, query)
	if firstJob.ReleaseID != release.ID {
		t.Fatalf("deployed first job release = %s, want %s", firstJob.ReleaseID, release.ID)
	}
	assertExpectedRealResult(t, firstJob.Result, expected)

	source.Metadata.Version = "1.1.0"
	source.Spec.Metrics[0].Description = "Deployed Phase 3 acceptance wording revision."
	var secondDraft catalog.Draft
	deployedHTTPJSON(t, client, http.MethodPut, baseURL+modelPath+"/draft", manageToken,
		httpapi.SaveDraftRequest{ExpectedRevision: draft.Revision, Source: source}, http.StatusOK, &secondDraft)
	var secondRelease catalog.Release
	deployedHTTPJSON(t, client, http.MethodPost, baseURL+modelPath+"/publish", manageToken,
		httpapi.PublishRequest{ExpectedRevision: secondDraft.Revision, Note: "deployed Phase 3 acceptance v2"}, http.StatusCreated, &secondRelease)
	if secondRelease.ID == release.ID {
		t.Fatal("deployed second publication reused the first immutable release ID")
	}
	var releases []httpapi.ReleaseSummary
	deployedHTTPJSON(t, client, http.MethodGet, baseURL+modelPath+"/releases", manageToken, nil, http.StatusOK, &releases)
	if len(releases) != 2 || !releases[1].Active || releases[1].ID != secondRelease.ID {
		t.Fatalf("deployed release summaries = %#v", releases)
	}
	secondJob := submitDeployedQuery(t, client, baseURL, modelPath, queryToken, query)
	if secondJob.ReleaseID != secondRelease.ID {
		t.Fatalf("deployed second job release = %s, want %s", secondJob.ReleaseID, secondRelease.ID)
	}
	assertExpectedRealResult(t, secondJob.Result, expected)
	var rolledBack catalog.Release
	deployedHTTPJSON(t, client, http.MethodPost, baseURL+modelPath+"/rollback", manageToken,
		httpapi.RollbackRequest{ReleaseID: release.ID, Note: "deployed Phase 3 acceptance rollback"}, http.StatusOK, &rolledBack)
	if rolledBack.ID != release.ID {
		t.Fatalf("deployed rollback release = %#v", rolledBack)
	}
	rolledBackJob := submitDeployedQuery(t, client, baseURL, modelPath, queryToken, query)
	if rolledBackJob.ReleaseID != release.ID {
		t.Fatalf("deployed rollback job release = %s, want %s", rolledBackJob.ReleaseID, release.ID)
	}
	assertExpectedRealResult(t, rolledBackJob.Result, expected)

	assertDeployedAudit(t, firstJob.Job.ID, secondJob.Job.ID, rolledBackJob.Job.ID)
}

func TestDeployedBaseURLRequiresHTTPSOrigin(t *testing.T) {
	for _, value := range []string{
		"http://metricspire.example",
		"https://user@metricspire.example",
		"https://metricspire.example/path",
		"https://metricspire.example?query=value",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := deployedBaseURL(value); err == nil {
				t.Fatalf("invalid deployed base URL %q was accepted", value)
			}
		})
	}
	if got, err := deployedBaseURL("https://metricspire.example"); err != nil || got != "https://metricspire.example" {
		t.Fatalf("valid deployed base URL = %q, %v", got, err)
	}
}

func requireDeployedPhase3Environment(t *testing.T, names ...string) {
	t.Helper()
	missing := make([]string, 0)
	for _, name := range names {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		t.Fatalf("deployed Phase 3 acceptance is enabled but required inputs are missing: %s", strings.Join(missing, ", "))
	}
}

func deployedBaseURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("METRICSPIRE_DEPLOYED_BASE_URL must be an HTTPS origin without credentials, path, query, or fragment")
	}
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

func deployedHealth(t *testing.T, client *http.Client, endpoint, want string) {
	t.Helper()
	var status map[string]string
	deployedHTTPJSON(t, client, http.MethodGet, endpoint, "", nil, http.StatusOK, &status)
	if status["status"] != want {
		t.Fatalf("deployed health %s = %#v", endpoint, status)
	}
}

func deployedRootAndLogin(t *testing.T, client *http.Client, baseURL string) {
	t.Helper()
	response, err := client.Get(baseURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	assertDeployedSecurityHeaders(t, response)
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("MetricSpire")) {
		t.Fatalf("deployed UI = %d %q, %v", response.StatusCode, body, readErr)
	}
	response, err = client.Get(baseURL + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	assertDeployedSecurityHeaders(t, response)
	_ = response.Body.Close()
	destination, parseErr := url.Parse(response.Header.Get("Location"))
	if response.StatusCode != http.StatusFound || parseErr != nil || destination.Scheme != "https" || destination.Host == "" {
		t.Fatalf("deployed OIDC login redirect = %d %q", response.StatusCode, response.Header.Get("Location"))
	}
}

func deployedHTTPJSON(t *testing.T, client *http.Client, method, endpoint, token string, body any, wantStatus int, target any) {
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
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	assertDeployedSecurityHeaders(t, response)
	const maximumAcceptanceResponseBytes = 16 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, maximumAcceptanceResponseBytes+1))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > maximumAcceptanceResponseBytes {
		t.Fatalf("%s %s response exceeded %d bytes", method, endpoint, maximumAcceptanceResponseBytes)
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

func assertDeployedSecurityHeaders(t *testing.T, response *http.Response) {
	t.Helper()
	if response.Header.Get("Cache-Control") != "no-store" ||
		response.Header.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(response.Header.Get("Content-Security-Policy"), "default-src 'self'") ||
		strings.TrimSpace(response.Header.Get("X-Request-ID")) == "" {
		t.Fatalf("deployed security headers are incomplete: cache=%q content-type=%q csp=%q request-id=%q",
			response.Header.Get("Cache-Control"), response.Header.Get("X-Content-Type-Options"),
			response.Header.Get("Content-Security-Policy"), response.Header.Get("X-Request-ID"))
	}
}

func submitDeployedQuery(t *testing.T, client *http.Client, baseURL, modelPath, token string, query model.SemanticQuery) application.QueryJobSnapshot {
	t.Helper()
	var submitted application.QueryJobSnapshot
	deployedHTTPJSON(t, client, http.MethodPost, baseURL+modelPath+"/query", token, query, http.StatusAccepted, &submitted)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for {
		var snapshot application.QueryJobSnapshot
		deployedHTTPJSON(t, client, http.MethodGet, baseURL+"/api/v1/jobs/"+url.PathEscape(submitted.Job.ID), token, nil, http.StatusOK, &snapshot)
		if realJobTerminal(snapshot.Job.Status) {
			if snapshot.Job.Status != model.JobSucceeded {
				t.Fatalf("deployed query job = %#v", snapshot)
			}
			return snapshot
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for deployed Phase 3 job %s: %v", submitted.Job.ID, ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func assertDeployedAudit(t *testing.T, jobIDs ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := postgres.Open(ctx, os.Getenv("METRICSPIRE_DEPLOYED_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, jobID := range jobIDs {
		var started, succeeded int
		if err := store.Pool().QueryRow(ctx, `
SELECT
  count(*) FILTER (WHERE event_kind = 'query_started'),
  count(*) FILTER (WHERE event_kind = 'query_succeeded')
FROM metricspire_query_audit
WHERE job_id = $1`, jobID).Scan(&started, &succeeded); err != nil {
			t.Fatal(err)
		}
		if started != 1 || succeeded != 1 {
			t.Fatalf("deployed audit counts for %s: started=%d succeeded=%d", jobID, started, succeeded)
		}
	}
}
