package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/marmot1024/metricspire/internal/adapter/clickhousequery"
	"github.com/marmot1024/metricspire/internal/adapter/databricks"
	"github.com/marmot1024/metricspire/internal/adapter/postgresquery"
	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/exploration"
	"github.com/marmot1024/metricspire/internal/mcpbridge"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/repository/mysql"
	"github.com/marmot1024/metricspire/internal/repository/postgres"
	"github.com/marmot1024/metricspire/internal/runtimeconfig"
)

// Exercise runServe itself, not just a manually assembled service. All identity,
// Warehouse and business data fixtures are synthetic and loopback-only.
func TestServeOnlinePostgresWithOIDCAndSeparateAnalyticalRoute(t *testing.T) {
	testServeOnlinePostgres(t, "", false, false, false)
}

func TestServeOnlinePostgresWithoutDatabricks(t *testing.T) {
	testServeOnlinePostgres(t, "", false, true, false)
}

func TestServeOnlinePostgresStructuredChoices(t *testing.T) {
	testServeOnlinePostgres(t, "", false, true, true)
}

func TestServeOnlinePostgresRejectsWriteCredential(t *testing.T) {
	for _, privilege := range []string{"UPDATE", "INSERT(revenue)", "UPDATE(revenue)", "REFERENCES(revenue)"} {
		for _, inherited := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/inherited=%t", privilege, inherited), func(t *testing.T) {
				testServeOnlinePostgres(t, privilege, inherited, false, false)
			})
		}
	}
}

func testServeOnlinePostgres(t *testing.T, writePrivilege string, inherited, portable, structured bool) {
	testServeOnlineEngine(t, writePrivilege, inherited, portable, structured, postgresquery.EngineName)
}

func TestServeOnlineClickHouseWithPostgresMetadata(t *testing.T) {
	if os.Getenv("METRICSPIRE_CLICKHOUSE_TEST_URL") == "" {
		if os.Getenv("METRICSPIRE_REQUIRE_CLICKHOUSE_TESTS") == "1" {
			t.Fatal("ClickHouse URL required")
		}
		t.Skip("requires disposable ClickHouse")
	}
	testServeOnlineEngine(t, "", false, true, true, clickhousequery.EngineName)
}

func testServeOnlineEngine(t *testing.T, writePrivilege string, inherited, portable, structured bool, driver string, metadataDriver ...string) {
	testServeOnlineExplorationEngine(t, writePrivilege, inherited, portable, structured, driver, "", false, metadataDriver...)
}
func TestServeOnlinePostgresFailureRollback(t *testing.T) {
	testServeOnlineExplorationEngine(t, "", false, true, true, postgresquery.EngineName, "", true)
}

func TestServeTrinoPrestoWithoutDatabricks(t *testing.T) {
	for _, engine := range []string{"trino", "presto"} {
		t.Run(engine, func(t *testing.T) {
			if os.Getenv("METRICSPIRE_"+strings.ToUpper(engine)+"_TEST_URL") == "" {
				if os.Getenv("METRICSPIRE_REQUIRE_"+strings.ToUpper(engine)+"_TESTS") == "1" {
					t.Fatal("external engine URL required")
				}
				t.Skip("requires disposable external engine")
			}
			testServeOnlineExplorationEngine(t, "", false, true, true, postgresquery.EngineName, engine, false)
		})
	}
}
func testServeOnlineExplorationEngine(t *testing.T, writePrivilege string, inherited, portable, structured bool, driver, explorationDriver string, rollback bool, metadataDriver ...string) {
	t.Helper()
	raw := os.Getenv("METRICSPIRE_ONLINE_TEST_DATABASE_URL")
	if raw == "" {
		if os.Getenv("METRICSPIRE_REQUIRE_POSTGRES_TESTS") == "1" || os.Getenv("METRICSPIRE_REQUIRE_CLICKHOUSE_TESTS") == "1" || os.Getenv("METRICSPIRE_REQUIRE_MYSQL_TESTS") == "1" {
			t.Fatal("PostgreSQL test URL required")
		}
		t.Skip("requires a disposable loopback PostgreSQL administrator")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatal("startup fixture accepts loopback only")
	}
	admin, err := pgxpool.New(t.Context(), raw)
	if err != nil {
		t.Fatal("open fixture administrator")
	}
	t.Cleanup(admin.Close)
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	control, data, role := "online_start_control_"+id, "online_start_data_"+id, "online_start_reader_"+id
	qualified := func(parts ...string) string { return pgx.Identifier(parts).Sanitize() }
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("CREATE SCHEMA " + qualified(control) + "; CREATE SCHEMA " + qualified(data) + "; CREATE ROLE " + qualified(role) + " LOGIN PASSWORD 'synthetic-local-only'")
	writer := ""
	if inherited {
		writer = role + "_writer"
		exec("CREATE ROLE " + qualified(writer) + "; GRANT " + qualified(writer) + " TO " + qualified(role))
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+qualified(data)+" CASCADE; DROP SCHEMA "+qualified(control)+" CASCADE; DROP ROLE "+qualified(role)); err != nil {
			t.Errorf("fixture cleanup: %v", err)
		}
		if writer != "" {
			if _, err := admin.Exec(ctx, "DROP ROLE "+qualified(writer)); err != nil {
				t.Errorf("writer fixture cleanup: %v", err)
			}
		}
	})
	controlURL := *u
	params := controlURL.Query()
	params.Set("search_path", control)
	controlURL.RawQuery = params.Encode()
	var store metadataStore
	var my *mysql.Store
	if len(metadataDriver) > 0 && metadataDriver[0] == "mysql" {
		instance, rawURL := mysqlStartupFixture(t)
		my = instance
		store = my
		controlURL = *rawURL
	} else {
		pool, err := pgxpool.New(t.Context(), controlURL.String())
		if err != nil {
			t.Fatal("open fixture control pool")
		}
		t.Cleanup(pool.Close)
		if err := postgres.Migrate(t.Context(), pool); err != nil {
			t.Fatal(err)
		}
		store, _ = postgres.New(pool)
	}
	management, _ := catalog.NewService(store)
	var source model.SemanticSource
	var binding model.SourceBinding
	var query application.OnlineQuery
	base, err := filepath.Abs(filepath.Join("..", "..", "examples", "online"))
	if err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]any{"model.yaml": &source, "binding.yaml": &binding, "query.json": &query} {
		if err := contractio.ReadFile(filepath.Join(base, name), target); err != nil {
			t.Fatal(err)
		}
	}
	if structured {
		for i := range source.Spec.Metrics {
			source.Spec.Metrics[i].Definition = &model.MetricDefinition{Family: "sales", Variant: source.Spec.Metrics[i].Name, Population: "Synthetic sales", Exclusions: []string{}, Numerator: "Reviewed aggregate", Denominator: "Reviewed formula", CountingUnit: "Order", TimeBasis: "Sales date"}
		}
	}
	binding.Datasets[0].Resource.Schema = data
	binding.Engine = driver
	draft, err := management.SaveDraft(t.Context(), catalog.SaveDraftInput{Namespace: "demo", Source: source, Actor: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	release, err := management.Publish(t.Context(), "demo", source.Metadata.Name, draft.Revision, "fixture", "synthetic startup fixture")
	if err != nil {
		t.Fatal(err)
	}
	contract, err := postgresquery.DataContractFingerprint(release.Manifest, binding)
	if driver == clickhousequery.EngineName {
		contract, err = clickhousequery.DataContractFingerprint(release.Manifest, binding)
	}
	if err != nil {
		t.Fatal(err)
	}
	table := qualified(data, "daily_sales")
	exec("CREATE TABLE " + table + " (row_key text PRIMARY KEY, day date NOT NULL, region text NOT NULL, revenue numeric NOT NULL, orders bigint NOT NULL, _metricspire_tenant text NOT NULL, _metricspire_batch_id text NOT NULL, _metricspire_data_as_of timestamptz NOT NULL, _metricspire_data_contract text NOT NULL)")
	exec("INSERT INTO "+table+" VALUES ('one','2026-09-01','east',100,2,'demo','batch-1',clock_timestamp(),$1)", contract)
	exec("GRANT USAGE ON SCHEMA " + qualified(data) + " TO " + qualified(role) + "; GRANT SELECT ON " + table + " TO " + qualified(role))
	if writePrivilege != "" {
		grantee := role
		if inherited {
			grantee = writer
		}
		// Privileges are fixed test cases, never caller input.
		exec("GRANT " + writePrivilege + " ON " + table + " TO " + qualified(grantee))
		var tableWrite, columnWrite bool
		if err := admin.QueryRow(t.Context(), "SELECT has_table_privilege($1,$2,'INSERT,UPDATE,REFERENCES'),has_any_column_privilege($1,$2,'INSERT,UPDATE,REFERENCES')", role, table).Scan(&tableWrite, &columnWrite); err != nil {
			t.Fatal(err)
		}
		if !columnWrite || (strings.Contains(writePrivilege, "(") && tableWrite) {
			t.Fatalf("invalid write fixture: table_write=%t column_write=%t", tableWrite, columnWrite)
		}
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/authorize", "token_endpoint": issuer.URL + "/token", "jwks_uri": issuer.URL + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/keys":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{"kty": "RSA", "kid": "fixture", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(issuer.Close)
	warehouse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("startup/online/plan contacted Warehouse")
		w.WriteHeader(500)
	}))
	t.Cleanup(warehouse.Close)
	dir := t.TempDir()
	if err := contractio.WriteJSON(filepath.Join(dir, "online.json"), binding); err != nil {
		t.Fatal(err)
	}
	binding.Engine = databricks.EngineName
	binding.Datasets[0].Resource = model.ResourceRef{Kind: model.ResourceTable, Catalog: "synthetic", Schema: "serving", Table: "daily_sales"}
	if err := contractio.WriteJSON(filepath.Join(dir, "warehouse.json"), binding); err != nil {
		t.Fatal(err)
	}
	var readerPolicy model.PolicySource
	if err := contractio.ReadFile(filepath.Join(base, "reader-policy.yaml"), &readerPolicy); err != nil {
		t.Fatal(err)
	}
	if err := contractio.WriteJSON(filepath.Join(dir, "reader-policy.json"), readerPolicy); err != nil {
		t.Fatal(err)
	}
	config := runtimeconfig.Config{
		APIVersion: runtimeconfig.APIVersion, Kind: runtimeconfig.Kind,
		HTTP:           runtimeconfig.HTTPConfig{Address: "127.0.0.1:0", PublicURL: "http://127.0.0.1:3000"},
		Authentication: runtimeconfig.AuthenticationConfig{Provider: runtimeconfig.AuthenticationOIDC, OIDC: &runtimeconfig.OIDCConfig{IssuerURL: issuer.URL, ClientID: "fixture", BearerAudience: "fixture-api", DevelopmentAllowInsecureHTTP: true}},
		Policies:       []runtimeconfig.PolicyRoute{{Namespace: "demo", ModelName: source.Metadata.Name, Tenant: "demo", Path: filepath.Join(base, "policy.yaml")}},
		Bindings:       []runtimeconfig.BindingRoute{{Namespace: "demo", ModelName: source.Metadata.Name, Path: "warehouse.json"}},
		Online:         &runtimeconfig.OnlineConfig{Tenant: "demo", Namespaces: []string{"demo"}, DataAccess: "tenant_shared", Resources: []model.ResourceRef{{Kind: model.ResourceTable, Schema: data, Table: "daily_sales"}}, MaxDataAge: "5m", Timeout: "1s", Bindings: []runtimeconfig.BindingRoute{{Namespace: "demo", ModelName: source.Metadata.Name, Path: "online.json"}}},
	}
	if my != nil {
		config.Storage.Driver = "mysql"
	}
	config.Online.Driver = driver
	config.Online.Policies = []runtimeconfig.PolicyRoute{{Namespace: "demo", ModelName: source.Metadata.Name, Tenant: "demo", Path: "reader-policy.json"}}
	if portable {
		config.QueryEngine.Driver = "disabled"
		config.Policies, config.Bindings = nil, nil
		config.Storage.DatabaseURLEnv = "SYNTHETIC_METADATA_URL"
		config.Online.DatabaseURLEnv = "SYNTHETIC_RESULTS_URL"
	}
	if explorationDriver != "" {
		var ec exploration.Catalog
		if err := contractio.ReadFile("../../testdata/trino/catalog.json", &ec); err != nil {
			t.Fatal(err)
		}
		ec.Engine = explorationDriver
		config.Exploration = &runtimeconfig.ExplorationConfig{Driver: explorationDriver, ReviewedCatalogs: []exploration.Catalog{ec}, ConnectionEnv: "SYNTHETIC_EXPLORATION_URL", User: "synthetic_reader", Concurrency: 1, Capacity: 10, ResultTTL: "10m"}
		// ReviewedCatalogs deliberately has no serialized form; deployment loads a file.
		if err := contractio.WriteJSON(filepath.Join(dir, "exploration.json"), ec); err != nil {
			t.Fatal(err)
		}
		config.Exploration.ReviewedCatalogs = nil
		config.Exploration.CatalogPaths = []string{"exploration.json"}
		t.Setenv("SYNTHETIC_EXPLORATION_URL", os.Getenv("METRICSPIRE_"+strings.ToUpper(explorationDriver)+"_TEST_URL"))
	}
	configPath := filepath.Join(dir, "runtime.json")
	if err := contractio.WriteJSON(configPath, config); err != nil {
		t.Fatal(err)
	}
	readerURL := *u
	readerURL.User = url.UserPassword(role, "synthetic-local-only")
	resultsURL := readerURL.String()
	if driver == clickhousequery.EngineName {
		resultsURL = clickHouseStartupFixture(t, data, contract)
	}
	// Process-local test environment only: no Databricks profile, OAuth flow,
	// credential cache or shared configuration is read or written.
	for name, value := range map[string]string{
		"METRICSPIRE_DATABASE_URL": controlURL.String(), "METRICSPIRE_ONLINE_DATABASE_URL": resultsURL,
		"METRICSPIRE_SESSION_KEY": base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")), "METRICSPIRE_OIDC_CLIENT_SECRET": "",
		"DATABRICKS_HOST": warehouse.URL, "DATABRICKS_SQL_WAREHOUSE_ID": "fixture", "DATABRICKS_TOKEN": "synthetic-local-token", "DATABRICKS_CLIENT_ID": "", "DATABRICKS_CLIENT_SECRET": "",
	} {
		t.Setenv(name, value)
	}
	if portable {
		for _, name := range []string{"DATABRICKS_HOST", "DATABRICKS_SQL_WAREHOUSE_ID", "DATABRICKS_TOKEN", "DATABRICKS_CLIENT_ID", "DATABRICKS_CLIENT_SECRET", "METRICSPIRE_DATABASE_URL", "METRICSPIRE_ONLINE_DATABASE_URL", "METRICSPIRE_LAKEBASE_ENDPOINT"} {
			t.Setenv(name, "")
		}
		t.Setenv(config.Storage.DatabaseURLEnv, controlURL.String())
		t.Setenv(config.Online.DatabaseURLEnv, resultsURL)
	}
	if writePrivilege != "" {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		var stdout bytes.Buffer
		err := runServe(ctx, []string{"--config", configPath}, &stdout, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "online credential must have SELECT and no write privileges") {
			t.Fatalf("writable credential was not rejected at startup: error=%v", err)
		}
		if strings.Contains(stdout.String(), "listening") {
			t.Fatal("writable credential reached listening state")
		}
		return
	}
	baseURL, stopServe := startOnlineFixtureService(t, configPath)
	token := onlineFixtureToken(t, key, map[string]any{"iss": issuer.URL, "aud": "fixture-api", "sub": "synthetic-online-reader", "tenant": "demo", "roles": []string{"analyst"}, "permissions": []string{"query:execute"}, "exp": time.Now().Add(time.Minute).Unix()})
	call := func(path string, body any, bearer string) (int, []byte) {
		t.Helper()
		encoded, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewReader(encoded))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+bearer)
		client := &http.Client{Timeout: 3 * time.Second}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		content, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, content
	}
	if structured {
		status, body := call("/api/v1/namespaces/demo/online-query", query, token)
		if status != 422 || !strings.Contains(string(body), "metric_selection_required") {
			t.Fatalf("missing choice: %d %s", status, body)
		}
		query.MetricSelections = map[string]string{}
		for _, code := range query.MetricCodes {
			query.MetricSelections[code] = "sha256:stale"
		}
		status, body = call("/api/v1/namespaces/demo/online-query", query, token)
		if status != 422 || !strings.Contains(string(body), "metric_selection_changed") {
			t.Fatalf("stale choice: %d %s", status, body)
		}
		for _, code := range query.MetricCodes {
			query.MetricSelections[code] = release.ManifestFingerprint
		}
	}
	status, body := call("/api/v1/namespaces/demo/online-query", query, token)
	if status != 200 {
		t.Fatalf("online startup result: %d %s", status, body)
	}
	var result application.OnlineResult
	if err := json.Unmarshal(body, &result); err != nil || len(result.Result.Rows) != 1 || result.Result.Columns[1].Name != "10001" || result.Snapshot.BatchID != "batch-1" {
		t.Fatalf("invalid startup result: %s", body)
	}
	if result.Timings == nil || result.Timings.ServiceMS <= 0 || result.Timings.EngineMS <= 0 || result.Timings.AuditMS <= 0 {
		t.Fatal("online HTTP lost query timings")
	}
	for col, want := range map[int]int64{1: 100, 3: 50} {
		raw, ok := result.Result.Rows[0][col].(string)
		amount, valid := new(big.Rat).SetString(raw)
		if !ok || !valid || amount.Cmp(big.NewRat(want, 1)) != 0 {
			t.Fatalf("wrong metric value in column %d", col)
		}
	}
	if result.Result.Rows[0][0] != "2026-09-01" || result.Result.Rows[0][2] != float64(2) {
		t.Fatal("wrong date or count")
	}
	nonReader := onlineFixtureToken(t, key, map[string]any{"iss": issuer.URL, "aud": "fixture-api", "sub": "another-analyst", "tenant": "demo", "roles": []string{"analyst"}, "permissions": []string{"query:execute"}, "exp": time.Now().Add(time.Minute).Unix()})
	if status, body := call("/api/v1/namespaces/demo/online-query", query, nonReader); status != 403 {
		t.Fatalf("analyst role bypassed the online reader policy: %d %s", status, body)
	}
	if portable {
		for bearer, wantCount := range map[string]int{token: 3, nonReader: 0} {
			req, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/catalog/search?namespace=demo", nil)
			req.Header.Set("Authorization", "Bearer "+bearer)
			res, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var entries []application.MetricCatalogEntry
			err = json.NewDecoder(res.Body).Decode(&entries)
			res.Body.Close()
			if err != nil || res.StatusCode != 200 || len(entries) != wantCount {
				t.Fatalf("portable catalog policy: status=%d count=%d want=%d error=%v", res.StatusCode, len(entries), wantCount, err)
			}
		}
	}
	analytical := model.SemanticQuery{APIVersion: model.APIVersion, Kind: model.KindSemanticQuery, Metrics: []string{"revenue"}, GroupBy: query.Dimensions, TimeRange: query.TimeRange, TimeGrouping: query.TimeGrouping, Limit: 10}
	status, body = call("/api/v1/namespaces/demo/models/daily_sales/plan", analytical, token)
	if portable {
		if status != 404 || !strings.Contains(string(body), "query_engine_disabled") {
			t.Fatalf("disabled route: %d %s", status, body)
		}
		// Disabled execution still authenticates, including job endpoints.
		if status, _ := call("/api/v1/jobs/not-a-job/cancel", nil, "invalid"); status != 401 {
			t.Fatalf("disabled job auth: %d", status)
		}
		if status, body := call("/api/v1/jobs/not-a-job/cancel", nil, token); status != 404 || !strings.Contains(string(body), "query_engine_disabled") {
			t.Fatalf("disabled job: %d %s", status, body)
		}
	} else {
		if status != 200 || !strings.Contains(string(body), `"engine":"`+databricks.EngineName+`"`) {
			t.Fatalf("analytical route lost its binding: %d %s", status, body)
		}
		if status, body := call("/api/v1/namespaces/demo/models/daily_sales/plan", analytical, nonReader); status != 200 {
			t.Fatalf("online reader restriction changed analytical permissions: %d %s", status, body)
		}
	}

	// Native MCP uses the same serving service and trusted OIDC identity even
	// when there is no analytical engine or Databricks configuration.
	session := mcpHTTPTransport(t, baseURL+"/api/v1/mcp", token)
	var routes mcpbridge.QueryRoutes
	mcpCall(t, session, "discover_query_routes", map[string]any{"namespace": "demo"}, &routes)
	if !routes.OnlineMetrics.Available || routes.PublishedMetrics.Available == portable {
		t.Fatal("incorrect portable MCP routes")
	}
	onlineInput := mcpbridge.OnlineQueryInput{Namespace: "demo", Query: query}
	if structured {
		onlineInput.Query.MetricSelections = nil
		if text := mcpCallError(t, session, "query_online_metrics", onlineInput); !strings.Contains(text, "metric_selection_required") {
			t.Fatal(text)
		}
		onlineInput.Query.MetricSelections = query.MetricSelections
	}
	var detail struct {
		Metrics []mcpbridge.MetricDetail `json:"metrics"`
	}
	mcpCall(t, session, "get_metric_details", mcpbridge.MetricDetailsInput{Namespace: "demo", Route: "online_metrics", Metrics: []string{"revenue"}}, &detail)
	if len(detail.Metrics) != 1 || detail.Metrics[0].ExternalCode != "10001" || detail.Metrics[0].ManifestFingerprint != release.ManifestFingerprint {
		t.Fatal("online definition metadata differs")
	}
	var native application.OnlineResult
	mcpCall(t, session, "query_online_metrics", onlineInput, &native)
	if native.ReleaseID != result.ReleaseID || native.ManifestFingerprint != result.ManifestFingerprint || native.Snapshot.BatchID != result.Snapshot.BatchID || len(native.Result.Rows) != len(result.Result.Rows) {
		t.Fatal("MCP and HTTP serving evidence differ")
	}
	if native.Timings == nil || native.Timings.ServiceMS <= 0 || native.Timings.EngineMS <= 0 || native.Timings.AuditMS <= 0 {
		t.Fatal("online MCP lost query timings")
	}
	for col, want := range map[int]int64{1: 100, 3: 50} {
		raw, ok := native.Result.Rows[0][col].(string)
		amount, valid := new(big.Rat).SetString(raw)
		if !ok || !valid || amount.Cmp(big.NewRat(want, 1)) != 0 {
			t.Fatal("wrong native MCP metric value")
		}
	}
	otherSession := mcpHTTPTransport(t, baseURL+"/api/v1/mcp", nonReader)
	mcpCall(t, otherSession, "discover_query_routes", map[string]any{"namespace": "demo"}, &routes)
	if routes.OnlineMetrics.Available || routes.PublishedMetrics.Available == portable {
		t.Fatal("online discovery ignored separate reader policy")
	}
	var hidden struct {
		Metrics []mcpbridge.MetricSummary `json:"metrics"`
	}
	mcpCall(t, otherSession, "search_metrics", mcpbridge.SearchInput{Namespace: "demo", Route: "online_metrics"}, &hidden)
	if len(hidden.Metrics) != 0 {
		t.Fatal("online metadata leaked to analytical-only reader")
	}
	if text := mcpCallError(t, otherSession, "query_online_metrics", onlineInput); !strings.Contains(text, "permission_denied") {
		t.Fatal(text)
	}
	if status, _ := call("/api/v1/namespaces/demo/online-query", query, "invalid"); status != 401 {
		t.Fatalf("invalid OIDC token: %d", status)
	}
	if explorationDriver != "" {
		var views struct {
			Sources []application.AnalysisSourceView `json:"sources"`
		}
		mcpCall(t, session, "discover_analysis", mcpbridge.AnalysisDiscoveryInput{Namespace: "demo"}, &views)
		if len(views.Sources) != 1 {
			t.Fatal("external source discovery failed")
		}
		in := mcpbridge.AnalysisInput{Namespace: "demo", Query: exploration.Query{APIVersion: model.APIVersion, Kind: exploration.Kind, Source: "shipments", GroupBy: []string{"flag"}, TimeRange: &model.TimeRange{Dimension: "day", Start: "1995-01-01T00:00:00Z", End: "1996-01-01T00:00:00Z", Timezone: "UTC"}, Measures: []exploration.Measure{{Name: "items", Op: "count"}, {Name: "order_sum", Op: "sum", Field: "order"}}, Limit: 100}}
		var planned application.AnalysisPlanView
		mcpCall(t, session, "plan_analysis", in, &planned)
		var job application.AnalysisJobView
		mcpCall(t, session, "submit_analysis", in, &job)
		if job.ID == "" {
			t.Fatal("missing job ID")
		}
		deadline := time.Now().Add(65 * time.Second)
		for job.Status == model.JobRunning && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
			mcpCall(t, session, "get_analysis", mcpbridge.AnalysisJobInput{Namespace: "demo", JobID: job.ID}, &job)
		}
		if job.Status != model.JobSucceeded || job.Result == nil || len(job.Result.Rows) != 3 {
			t.Fatalf("external analysis failed: status=%s code=%s", job.Status, job.ErrorCode)
		}
		if text := mcpCallError(t, otherSession, "get_analysis", mcpbridge.AnalysisJobInput{Namespace: "demo", JobID: job.ID}); text == "" {
			t.Fatal("cross-principal result access")
		}
		denied := onlineFixtureToken(t, key, map[string]any{"iss": issuer.URL, "aud": "fixture-api", "sub": "denied", "tenant": "demo", "roles": []string{"none"}, "permissions": []string{"query:execute"}, "exp": time.Now().Add(time.Minute).Unix()})
		deniedSession := mcpHTTPTransport(t, baseURL+"/api/v1/mcp", denied)
		if text := mcpCallError(t, deniedSession, "submit_analysis", in); text == "" {
			t.Fatal("unapproved exploration executed")
		}
	}
	var count int
	wantAudits := 4
	if explorationDriver != "" {
		wantAudits = 6
	}
	var auditErr error
	if my != nil {
		auditErr = my.DB().QueryRowContext(t.Context(), "SELECT count(*) FROM metricspire_query_audit WHERE event_kind IN ('query_started','query_succeeded')").Scan(&count)
	} else {
		auditErr = admin.QueryRow(t.Context(), "SELECT count(*) FROM "+qualified(control, "metricspire_query_audit")+" WHERE event_kind IN ('query_started','query_succeeded')").Scan(&count)
	}
	if auditErr != nil || count != wantAudits {
		t.Fatalf("startup durable audit count=%d error=%v", count, auditErr)
	}

	if my != nil {
		for _, kind := range []string{"query_started", "query_succeeded"} {
			ddl := `CREATE TRIGGER reject_query_audit BEFORE INSERT ON metricspire_query_audit FOR EACH ROW BEGIN IF NEW.event_kind='` + kind + `' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='private audit detail'; END IF; END`
			if _, err := my.DB().ExecContext(t.Context(), ddl); err != nil {
				t.Fatal(err)
			}
			message := mcpCallError(t, session, "query_online_metrics", onlineInput)
			if !strings.Contains(message, "audit_unavailable") || strings.Contains(message, "private audit") || strings.Contains(message, "data_snapshot") {
				t.Fatal("audit failure returned unsafe result", message)
			}
			if _, err := my.DB().ExecContext(t.Context(), `DROP TRIGGER reject_query_audit`); err != nil {
				t.Fatal(err)
			}
		}
	}

	if rollback {
		frozen, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		stopServe()
		config.Online.MaxDataAge = "1ns"
		if err := contractio.WriteJSON(configPath, config); err != nil {
			t.Fatal(err)
		}
		candidateURL, stopCandidate := startOnlineFixtureService(t, configPath)
		baseURL = candidateURL
		status, body := call("/api/v1/namespaces/demo/online-query", query, token)
		if status == 200 || !strings.Contains(string(body), "online_data_stale") {
			t.Fatalf("candidate did not fail acceptance: %d %s", status, body)
		}
		stopCandidate()
		if err := os.WriteFile(configPath, frozen, 0600); err != nil {
			t.Fatal(err)
		}
		restoredURL, _ := startOnlineFixtureService(t, configPath)
		baseURL = restoredURL
		status, body = call("/api/v1/namespaces/demo/online-query", query, token)
		var restored application.OnlineResult
		if status != 200 || json.Unmarshal(body, &restored) != nil || restored.ReleaseID != result.ReleaseID || restored.ManifestFingerprint != result.ManifestFingerprint || restored.Snapshot.BatchID != result.Snapshot.BatchID || !bytes.Equal(mustJSON(t, restored.Result), mustJSON(t, result.Result)) {
			t.Fatal("restored service changed reviewed result")
		}
		current, err := os.ReadFile(configPath)
		if err != nil || !bytes.Equal(current, frozen) {
			t.Fatal("rollback did not restore frozen config")
		}
		t.Log("forced candidate freshness failure; frozen service restarted; exact typed result, release and batch restored")
	}

}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func startOnlineFixtureService(t *testing.T, configPath string) (string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r, w := io.Pipe()
	statuses := make(chan map[string]string, 2)
	decodeErrors := make(chan error, 1)
	go func() {
		decoder := json.NewDecoder(r)
		for range 2 {
			var s map[string]string
			if err := decoder.Decode(&s); err != nil {
				decodeErrors <- err
				return
			}
			statuses <- s
		}
	}()
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, []string{"--config", configPath}, w, io.Discard); _ = w.Close() }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("serve exit: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("serve did not stop")
			}
			_ = r.Close()
		})
	}
	t.Cleanup(stop)
	var started map[string]string
	select {
	case started = <-statuses:
	case err := <-decodeErrors:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not start")
	}
	if started["status"] != "listening" {
		t.Fatalf("startup status: %#v", started)
	}
	return "http://" + started["address"], stop

}

func onlineFixtureToken(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "fixture", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	value := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	hash := sha256.Sum256([]byte(value))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	return value + "." + base64.RawURLEncoding.EncodeToString(signature)
}
