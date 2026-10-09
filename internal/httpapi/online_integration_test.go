package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/marmot1024/metricspire/internal/adapter/postgresquery"
	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/audit"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/governance"
	"github.com/marmot1024/metricspire/internal/httpapi"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/repository/postgres"
)

type onlineIntegrationFixture struct {
	server        *httptest.Server
	admin         *pgxpool.Pool
	dataTable     string
	fingerprint   string
	query         application.OnlineQuery
	batch         string
	controlSchema string
	asOf          time.Time
	management    *catalog.Service
	source        model.SemanticSource
	release       catalog.Release
	revision      int64
}

func setupOnlinePostgres(t *testing.T) *onlineIntegrationFixture {
	t.Helper()
	raw := os.Getenv("METRICSPIRE_ONLINE_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set METRICSPIRE_ONLINE_TEST_DATABASE_URL to a disposable loopback PostgreSQL database")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost") {
		t.Fatal("online integration tests accept disposable loopback databases only")
	}
	admin, err := pgxpool.New(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	control := "online_test_control_" + id
	data := "online_test_data_" + id
	role := "online_test_reader_" + id
	if _, err = admin.Exec(t.Context(), "CREATE SCHEMA "+pgx.Identifier{control}.Sanitize()+"; CREATE SCHEMA "+pgx.Identifier{data}.Sanitize()+"; CREATE ROLE "+pgx.Identifier{role}.Sanitize()+" NOLOGIN"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := admin.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{data}.Sanitize()+" CASCADE; DROP SCHEMA "+pgx.Identifier{control}.Sanitize()+" CASCADE; DROP ROLE "+pgx.Identifier{role}.Sanitize())
		if err != nil {
			t.Errorf("isolated fixture cleanup failed: %v", err)
		}
	})
	controlConfig, _ := pgxpool.ParseConfig(raw)
	controlConfig.ConnConfig.RuntimeParams["search_path"] = control
	controlPool, err := pgxpool.NewWithConfig(t.Context(), controlConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controlPool.Close)
	if err = postgres.Migrate(t.Context(), controlPool); err != nil {
		t.Fatal(err)
	}
	store, _ := postgres.New(controlPool)
	management, _ := catalog.NewService(store)
	governanceService, _ := governance.NewService(store)
	root := filepath.Join("..", "..", "examples", "online")
	var source model.SemanticSource
	var policy model.PolicySource
	var binding model.SourceBinding
	var query application.OnlineQuery
	for path, target := range map[string]any{"model.yaml": &source, "policy.yaml": &policy, "binding.yaml": &binding, "query.json": &query} {
		if err = contractio.ReadFile(filepath.Join(root, path), target); err != nil {
			t.Fatal(err)
		}
	}
	binding.Datasets[0].Resource.Schema = data
	draft, err := management.SaveDraft(t.Context(), catalog.SaveDraftInput{Namespace: "demo", Source: source, Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	release, err := management.Publish(t.Context(), "demo", source.Metadata.Name, draft.Revision, "test", "synthetic online fixture")
	if err != nil {
		t.Fatal(err)
	}
	table := pgx.Identifier{data, "daily_sales"}.Sanitize()
	ddl := "CREATE TABLE " + table + ` (row_key text PRIMARY KEY,day date NOT NULL,region text NOT NULL,revenue numeric NOT NULL,orders bigint NOT NULL,_metricspire_tenant text NOT NULL,_metricspire_batch_id text NOT NULL,_metricspire_data_as_of timestamptz NOT NULL,_metricspire_manifest_fingerprint text NOT NULL); CREATE INDEX ON ` + table + ` (_metricspire_tenant,region,day)`
	if _, err = admin.Exec(t.Context(), ddl); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(t.Context(), "INSERT INTO "+table+` VALUES ('one','2026-09-01','east',100,2,'demo','batch-1',clock_timestamp(),$1),('two','2026-09-02','east',50,1,'demo','batch-1',clock_timestamp(),$1),('three','2026-09-01','west',60,3,'demo','batch-1',clock_timestamp(),$1),('other-tenant','2026-09-01','east',999999,1,'other','batch-1',clock_timestamp(),$1)`, release.ManifestFingerprint); err != nil {
		t.Fatal(err)
	}
	// All rows in a batch carry one coverage time, not per-row creation times.
	asOf := time.Now().UTC().Truncate(time.Second)
	if _, err = admin.Exec(t.Context(), "UPDATE "+table+" SET _metricspire_data_as_of=$1", asOf); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(t.Context(), "GRANT USAGE ON SCHEMA "+pgx.Identifier{data}.Sanitize()+" TO "+pgx.Identifier{role}.Sanitize()+"; GRANT SELECT ON "+table+" TO "+pgx.Identifier{role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	dataConfig, _ := pgxpool.ParseConfig(raw)
	dataConfig.MaxConns = 4
	dataConfig.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE "+pgx.Identifier{role}.Sanitize())
		return err
	}
	dataPool, err := pgxpool.NewWithConfig(t.Context(), dataConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dataPool.Close)
	engine, err := postgresquery.NewQueryEngine(dataPool, postgresquery.Config{Tenant: "demo", Resources: []model.ResourceRef{binding.Datasets[0].Resource}, MaxDataAge: time.Hour, MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	policies, _ := application.NewConfiguredPolicyResolver([]application.PolicyConfiguration{{Namespace: "demo", ModelName: source.Metadata.Name, Tenant: "demo", Policy: policy}})
	bindings, _ := application.NewConfiguredBindingResolver([]application.BindingConfiguration{{Namespace: "demo", ModelName: source.Metadata.Name, Binding: binding}})
	queries, _ := application.NewQueryService(store, policies, bindings, engine, store)
	online, err := application.NewOnlineService(store, queries, "demo", []string{"demo"}, 20, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	search, _ := application.NewCatalogService(store, policies, bindings)
	jobs, _ := application.NewJobManager(context.Background(), queries, time.Second, 10, nil)
	t.Cleanup(jobs.Close)
	auth := httpapi.AuthenticatorFunc(func(ctx context.Context, r *http.Request) (httpapi.Principal, error) {
		switch r.Header.Get("Authorization") {
		case "Bearer analyst":
			return httpapi.Principal{Tenant: "demo", Subject: "analyst", Roles: []string{"analyst"}, Permissions: []httpapi.Permission{httpapi.PermissionQuery}}, nil
		case "Bearer other-tenant":
			return httpapi.Principal{Tenant: "other", Subject: "analyst", Roles: []string{"analyst"}, Permissions: []httpapi.Permission{httpapi.PermissionQuery}}, nil
		case "Bearer unprivileged":
			return httpapi.Principal{Tenant: "demo", Subject: "unprivileged", Permissions: []httpapi.Permission{httpapi.PermissionQuery}}, nil
		default:
			return httpapi.Principal{}, httpapi.ErrUnauthenticated
		}
	})
	handler, err := httpapi.NewServer(httpapi.Config{}, httpapi.Dependencies{Authenticator: auth, Readiness: store, Management: management, Catalog: store, CatalogSearch: search, Governance: governanceService, Bindings: bindings, Queries: queries, Jobs: jobs, Online: online, ExecutionCredential: httpapi.ExecutionCredentialFunc(func(context.Context, *http.Request, httpapi.Principal) (context.Context, error) {
		t.Error("online request fetched a Databricks token")
		return nil, fmt.Errorf("must not be called")
	})}, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &onlineIntegrationFixture{server: server, admin: admin, dataTable: table, fingerprint: release.ManifestFingerprint, query: query, batch: "batch-1", controlSchema: control, asOf: asOf, management: management, source: source, release: release, revision: draft.Revision}
}

func (f *onlineIntegrationFixture) request(t *testing.T, token string, body any) (int, []byte) {
	t.Helper()
	encoded, _ := json.Marshal(body)
	request, _ := http.NewRequest(http.MethodPost, f.server.URL+"/api/v1/namespaces/demo/online-query", bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	r, err := f.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return r.StatusCode, data
}
func (f *onlineIntegrationFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.admin.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func TestOnlinePostgresHTTPResultPermissionsAndFailureBoundaries(t *testing.T) {
	f := setupOnlinePostgres(t)
	status, data := f.request(t, "analyst", f.query)
	if status != 200 {
		t.Fatalf("status %d: %s", status, data)
	}
	var out application.OnlineResult
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Result.Rows) != 2 || out.Snapshot.BatchID != "batch-1" || out.Result.Columns[1].Name != "10001" || out.Result.Columns[3].Name != "10003" {
		t.Fatalf("unexpected result %s", data)
	}
	for i, want := range []string{"100", "50"} {
		got := new(big.Rat)
		if _, ok := got.SetString(out.Result.Rows[i][1].(string)); !ok || got.Cmp(new(big.Rat).SetInt64([]int64{100, 50}[i])) != 0 {
			t.Fatalf("revenue row%d %v expected%s", i, out.Result.Rows[i], want)
		}
		aov := new(big.Rat)
		if _, ok := aov.SetString(out.Result.Rows[i][3].(string)); !ok || aov.Cmp(big.NewRat(50, 1)) != 0 {
			t.Fatalf("ratio %v", out.Result.Rows[i])
		}
		if out.Result.Rows[i][2] != []float64{2, 1}[i] {
			t.Fatalf("orders %v", out.Result.Rows[i])
		}
	}
	var audits int
	if err := f.admin.QueryRow(t.Context(), "SELECT count(*) FROM "+pgx.Identifier{f.controlSchema, "metricspire_query_audit"}.Sanitize()+" WHERE event_kind IN ('query_started','query_succeeded')").Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("durable audit count %d error%v", audits, err)
	}
	for _, token := range []string{"other-tenant", "unprivileged"} {
		if status, _ := f.request(t, token, f.query); status != 403 {
			t.Fatalf("%s: status %d", token, status)
		}
	}
	if status, _ := f.request(t, "", f.query); status != 401 {
		t.Fatalf("anonymous %d", status)
	}
	invalid := map[string]any{"metric_codes": []string{"10001"}, "identity": "admin"}
	if status, _ := f.request(t, "analyst", invalid); status != 400 {
		t.Fatalf("unknown field %d", status)
	}
	query := f.query
	query.Limit = 1
	if status, data := f.request(t, "analyst", query); status != 422 || !bytes.Contains(data, []byte("online_result_too_large")) {
		t.Fatalf("row cap %d %s", status, data)
	}
	query = f.query
	query.Filters = []model.Filter{{Dimension: "region", Operator: model.FilterEqual, Values: []string{"east' OR 1=1 --"}}}
	if status, _ := f.request(t, "analyst", query); status != 503 {
		t.Fatalf("injected filter should select no rows: %d", status)
	}
	for _, test := range []struct{ name, update, code string }{
		{"stale", "_metricspire_data_as_of='2000-01-01'", "online_data_stale"},
		{"future coverage", "_metricspire_data_as_of=clock_timestamp()+interval '1 hour'", "online_data_stale"},
		{"incompatible", "_metricspire_manifest_fingerprint='sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'", "online_data_unavailable"},
		{"mixed batch", "_metricspire_batch_id=CASE WHEN row_key='one' THEN 'batch-2' ELSE 'batch-1' END", "online_data_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f.exec(t, "UPDATE "+f.dataTable+" SET "+test.update)
			status, data := f.request(t, "analyst", f.query)
			if status != 503 || !bytes.Contains(data, []byte(test.code)) {
				t.Fatalf("%d %s", status, data)
			}
			f.exec(t, "UPDATE "+f.dataTable+" SET _metricspire_data_as_of=$2,_metricspire_batch_id='batch-1',_metricspire_manifest_fingerprint=$1", f.fingerprint, f.asOf)
		})
	}
	// The tenant predicate excludes another tenant's rows even with matching dimensions.
	query = f.query
	query.Dimensions = nil
	query.TimeGrouping = nil
	query.OrderBy = nil
	status, data = f.request(t, "analyst", query)
	if status != 200 || bytes.Contains(data, []byte("999999")) {
		t.Fatalf("tenant data leaked %d %s", status, data)
	}
	// A writer's incomplete transaction remains invisible to the online read.
	tx, err := f.admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), "UPDATE "+f.dataTable+" SET _metricspire_batch_id='partial' WHERE row_key='one'"); err != nil {
		t.Fatal(err)
	}
	status, data = f.request(t, "analyst", f.query)
	_ = tx.Rollback(t.Context())
	if status != 200 || bytes.Contains(data, []byte("partial")) {
		t.Fatalf("partial update visible %d %s", status, data)
	}
	f.exec(t, "UPDATE "+f.dataTable+" SET orders=0 WHERE row_key='two'")
	status, data = f.request(t, "analyst", f.query)
	if status != 200 || !bytes.Contains(data, []byte("null")) {
		t.Fatalf("zero denominator %d %s", status, data)
	}
	// Removing the data source produces a safe error, not SQL/table details or a warehouse retry.
	f.exec(t, "ALTER TABLE "+f.dataTable+" RENAME TO disappeared")
	status, data = f.request(t, "analyst", f.query)
	if status != 503 || !bytes.Contains(data, []byte("online_engine_unavailable")) || strings.Contains(string(data), "disappeared") {
		t.Fatalf("safe database error %d %s", status, data)
	}
}

func TestOnlinePostgresReleaseSwitchAndRollback(t *testing.T) {
	f := setupOnlinePostgres(t)
	f.source.Metadata.Version = "1.0.1"
	draft, err := f.management.SaveDraft(t.Context(), catalog.SaveDraftInput{Namespace: "demo", Source: f.source, ExpectedRevision: f.revision, Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	newRelease, err := f.management.Publish(t.Context(), "demo", f.source.Metadata.Name, draft.Revision, "test", "new definition without refreshed data")
	if err != nil {
		t.Fatal(err)
	}
	if newRelease.ManifestFingerprint == f.fingerprint {
		t.Fatal("fixture must change definition fingerprint")
	}
	if status, data := f.request(t, "analyst", f.query); status != 503 || !bytes.Contains(data, []byte("online_data_unavailable")) {
		t.Fatalf("old data served under new release: %d %s", status, data)
	}
	if _, err := f.management.Rollback(t.Context(), "demo", f.source.Metadata.Name, f.release.ID, "test", "restore matching snapshot"); err != nil {
		t.Fatal(err)
	}
	if status, data := f.request(t, "analyst", f.query); status != 200 || !bytes.Contains(data, []byte(f.release.ID)) {
		t.Fatalf("rollback did not restore matching snapshot: %d %s", status, data)
	}
}

func TestOnlinePostgresHTTPBoundedLoadSample(t *testing.T) {
	f := setupOnlinePostgres(t)
	if os.Getenv("METRICSPIRE_ONLINE_LOAD_ROWS") == "1000000" {
		f.exec(t, "INSERT INTO "+f.dataTable+` SELECT 'load-'||g,'2026-09-01'::date+(g%30)::int,'region-'||g,1,1,'demo','batch-1',$2,$1 FROM generate_series(1,1000000) g`, f.fingerprint, f.asOf)
		f.exec(t, "ANALYZE "+f.dataTable)
	}
	for _, concurrency := range []int{1, 5, 10, 20} {
		t.Run(fmt.Sprintf("concurrency_%d", concurrency), func(t *testing.T) {
			var wait sync.WaitGroup
			var mu sync.Mutex
			durations := []time.Duration{}
			failures := 0
			started := time.Now()
			for range concurrency {
				wait.Add(1)
				go func() {
					defer wait.Done()
					for range 10 {
						start := time.Now()
						status, _ := f.request(t, "analyst", f.query)
						mu.Lock()
						durations = append(durations, time.Since(start))
						if status != 200 {
							failures++
						}
						mu.Unlock()
					}
				}()
			}
			wait.Wait()
			elapsed := time.Since(started)
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			p95 := durations[(len(durations)*95-1)/100]
			p99 := durations[(len(durations)*99-1)/100]
			t.Logf("n=%d failures=%d p95=%s p99=%s QPS=%.1f (local synthetic, no result cache, includes durable audit)", len(durations), failures, p95, p99, float64(len(durations))/elapsed.Seconds())
			if failures != 0 || p95 > time.Second {
				t.Fatalf("local sample failed: errors=%d p95=%s", failures, p95)
			}
		})
	}
}

// Keep compile-time conformance explicit: audit is part of the online service.
var _ audit.Recorder = (*postgres.Store)(nil)
