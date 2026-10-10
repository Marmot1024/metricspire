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
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/marmot1024/metricspire/internal/adapter/databricks"
	"github.com/marmot1024/metricspire/internal/adapter/postgresquery"
	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/repository/postgres"
	"github.com/marmot1024/metricspire/internal/runtimeconfig"
)

// Exercise runServe itself, not just a manually assembled service. All identity,
// Warehouse and business data fixtures are synthetic and loopback-only.
func TestServeOnlinePostgresWithOIDCAndSeparateAnalyticalRoute(t *testing.T) {
	raw := os.Getenv("METRICSPIRE_ONLINE_TEST_DATABASE_URL")
	if raw == "" {
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
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+qualified(data)+" CASCADE; DROP SCHEMA "+qualified(control)+" CASCADE; DROP ROLE "+qualified(role)); err != nil {
			t.Errorf("fixture cleanup: %v", err)
		}
	})
	controlURL := *u
	params := controlURL.Query()
	params.Set("search_path", control)
	controlURL.RawQuery = params.Encode()
	pool, err := pgxpool.New(t.Context(), controlURL.String())
	if err != nil {
		t.Fatal("open fixture control pool")
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store, _ := postgres.New(pool)
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
	binding.Datasets[0].Resource.Schema = data
	draft, err := management.SaveDraft(t.Context(), catalog.SaveDraftInput{Namespace: "demo", Source: source, Actor: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	release, err := management.Publish(t.Context(), "demo", source.Metadata.Name, draft.Revision, "fixture", "synthetic startup fixture")
	if err != nil {
		t.Fatal(err)
	}
	contract, err := postgresquery.DataContractFingerprint(release.Manifest, binding)
	if err != nil {
		t.Fatal(err)
	}
	table := qualified(data, "daily_sales")
	exec("CREATE TABLE " + table + " (row_key text PRIMARY KEY, day date NOT NULL, region text NOT NULL, revenue numeric NOT NULL, orders bigint NOT NULL, _metricspire_tenant text NOT NULL, _metricspire_batch_id text NOT NULL, _metricspire_data_as_of timestamptz NOT NULL, _metricspire_data_contract text NOT NULL)")
	exec("INSERT INTO "+table+" VALUES ('one','2026-09-01','east',100,2,'demo','batch-1',clock_timestamp(),$1)", contract)
	exec("GRANT USAGE ON SCHEMA " + qualified(data) + " TO " + qualified(role) + "; GRANT SELECT ON " + table + " TO " + qualified(role))

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
	config := runtimeconfig.Config{
		APIVersion: runtimeconfig.APIVersion, Kind: runtimeconfig.Kind,
		HTTP:           runtimeconfig.HTTPConfig{Address: "127.0.0.1:0", PublicURL: "http://127.0.0.1:3000"},
		Authentication: runtimeconfig.AuthenticationConfig{Provider: runtimeconfig.AuthenticationOIDC, OIDC: &runtimeconfig.OIDCConfig{IssuerURL: issuer.URL, ClientID: "fixture", BearerAudience: "fixture-api", DevelopmentAllowInsecureHTTP: true}},
		Policies:       []runtimeconfig.PolicyRoute{{Namespace: "demo", ModelName: source.Metadata.Name, Tenant: "demo", Path: filepath.Join(base, "policy.yaml")}},
		Bindings:       []runtimeconfig.BindingRoute{{Namespace: "demo", ModelName: source.Metadata.Name, Path: "warehouse.json"}},
		Online:         &runtimeconfig.OnlineConfig{Tenant: "demo", Namespaces: []string{"demo"}, DataAccess: "tenant_shared", Resources: []model.ResourceRef{{Kind: model.ResourceTable, Schema: data, Table: "daily_sales"}}, MaxDataAge: "5m", Timeout: "1s", Bindings: []runtimeconfig.BindingRoute{{Namespace: "demo", ModelName: source.Metadata.Name, Path: "online.json"}}},
	}
	configPath := filepath.Join(dir, "runtime.json")
	if err := contractio.WriteJSON(configPath, config); err != nil {
		t.Fatal(err)
	}
	readerURL := *u
	readerURL.User = url.UserPassword(role, "synthetic-local-only")
	// Process-local test environment only: no Databricks profile, OAuth flow,
	// credential cache or shared configuration is read or written.
	for name, value := range map[string]string{
		"METRICSPIRE_DATABASE_URL": controlURL.String(), "METRICSPIRE_ONLINE_DATABASE_URL": readerURL.String(),
		"METRICSPIRE_SESSION_KEY": base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")), "METRICSPIRE_OIDC_CLIENT_SECRET": "",
		"DATABRICKS_HOST": warehouse.URL, "DATABRICKS_SQL_WAREHOUSE_ID": "fixture", "DATABRICKS_TOKEN": "synthetic-local-token", "DATABRICKS_CLIENT_ID": "", "DATABRICKS_CLIENT_SECRET": "",
	} {
		t.Setenv(name, value)
	}
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
	t.Cleanup(func() {
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
	baseURL := "http://" + started["address"]
	token := onlineFixtureToken(t, key, map[string]any{"iss": issuer.URL, "aud": "fixture-api", "sub": "analyst", "tenant": "demo", "roles": []string{"analyst"}, "permissions": []string{"query:execute"}, "exp": time.Now().Add(time.Minute).Unix()})
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
	status, body := call("/api/v1/namespaces/demo/online-query", query, token)
	if status != 200 {
		t.Fatalf("online startup result: %d %s", status, body)
	}
	var result application.OnlineResult
	if err := json.Unmarshal(body, &result); err != nil || len(result.Result.Rows) != 1 || result.Result.Columns[1].Name != "10001" || result.Snapshot.BatchID != "batch-1" {
		t.Fatalf("invalid startup result: %s", body)
	}
	analytical := model.SemanticQuery{APIVersion: model.APIVersion, Kind: model.KindSemanticQuery, Metrics: []string{"revenue"}, GroupBy: query.Dimensions, TimeRange: query.TimeRange, TimeGrouping: query.TimeGrouping, Limit: 10}
	status, body = call("/api/v1/namespaces/demo/models/daily_sales/plan", analytical, token)
	if status != 200 || !strings.Contains(string(body), `"engine":"`+databricks.EngineName+`"`) {
		t.Fatalf("analytical route lost its binding: %d %s", status, body)
	}
	if status, _ := call("/api/v1/namespaces/demo/online-query", query, "invalid"); status != 401 {
		t.Fatalf("invalid OIDC token: %d", status)
	}
	var count int
	if err := admin.QueryRow(t.Context(), "SELECT count(*) FROM "+qualified(control, "metricspire_query_audit")+" WHERE event_kind IN ('query_started','query_succeeded')").Scan(&count); err != nil || count != 2 {
		t.Fatalf("startup durable audit count=%d error=%v", count, err)
	}
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
