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
	"github.com/marmot1024/metricspire/internal/auth/appsauth"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/executionauth"
	"github.com/marmot1024/metricspire/internal/governance"
	"github.com/marmot1024/metricspire/internal/httpapi"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/repository/postgres"
)

type onlineIntegrationFixture struct {
	server        *httptest.Server
	admin         *pgxpool.Pool
	dataPool      *pgxpool.Pool
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
	binding       model.SourceBinding
}

func setupOnlinePostgres(t *testing.T) *onlineIntegrationFixture {
	return setupOnlinePostgresWithApps(t, false)
}

func setupOnlinePostgresWithApps(t *testing.T, apps bool) *onlineIntegrationFixture {
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
	if apps {
		if err := contractio.ReadFile(filepath.Join(root, "reader-policy.yaml"), &policy); err != nil {
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
	dataContract, err := postgresquery.DataContractFingerprint(release.Manifest, binding)
	if err != nil {
		t.Fatal(err)
	}
	ddl := "CREATE TABLE " + table + ` (row_key text PRIMARY KEY,day date NOT NULL,region text NOT NULL,revenue numeric NOT NULL,orders bigint NOT NULL,_metricspire_tenant text NOT NULL,_metricspire_batch_id text NOT NULL,_metricspire_data_as_of timestamptz NOT NULL,_metricspire_data_contract text NOT NULL); CREATE INDEX ON ` + table + ` (_metricspire_tenant,region,day)`
	if _, err = admin.Exec(t.Context(), ddl); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(t.Context(), "INSERT INTO "+table+` VALUES ('one','2026-09-01','east',100,2,'demo','batch-1',clock_timestamp(),$1),('two','2026-09-02','east',50,1,'demo','batch-1',clock_timestamp(),$1),('three','2026-09-01','west',60,3,'demo','batch-1',clock_timestamp(),$1),('other-tenant','2026-09-01','east',999999,1,'other','batch-1',clock_timestamp(),$1)`, dataContract); err != nil {
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
	queries, _ := application.NewQueryService(store, policies, bindings, onlineTokenGuard{engine, t}, store)
	online, err := application.NewOnlineService(store, queries, "demo", []string{"demo"}, 20, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	search, _ := application.NewCatalogService(store, policies, bindings)
	jobs, _ := application.NewJobManager(context.Background(), queries, time.Second, 10, nil)
	t.Cleanup(jobs.Close)
	var auth httpapi.Authenticator = httpapi.AuthenticatorFunc(func(ctx context.Context, r *http.Request) (httpapi.Principal, error) {
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
	if apps {
		// This is the real Apps authenticator and HTTP decoder. Only the platform
		// current-user endpoint is synthetic; no real ingress/OAuth is claimed.
		auth, err = appsauth.New(appsauth.Config{ExpectedAppName: "metricspire-online-fixture", ExpectedWorkspaceID: "314", ExpectedPublicURL: "https://online-fixture.example.com", Tenant: "demo", QueryRole: "analyst", PublisherGroupID: "publishers", HTTPClient: &http.Client{Transport: onlineAppsIdentityTransport{}}}, appsauth.Runtime{AppName: "metricspire-online-fixture", WorkspaceID: "314", Host: "https://workspace.example.com"}, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	handler, err := httpapi.NewServer(httpapi.Config{}, httpapi.Dependencies{Authenticator: auth, Readiness: store, Management: management, Catalog: store, CatalogSearch: search, Governance: governanceService, Bindings: bindings, Queries: queries, Jobs: jobs, Online: online, ExecutionCredential: httpapi.ExecutionCredentialFunc(func(context.Context, *http.Request, httpapi.Principal) (context.Context, error) {
		t.Error("online request fetched a Databricks token")
		return nil, fmt.Errorf("must not be called")
	})}, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &onlineIntegrationFixture{server: server, admin: admin, dataPool: dataPool, dataTable: table, fingerprint: dataContract, query: query, batch: "batch-1", controlSchema: control, asOf: asOf, management: management, source: source, release: release, revision: draft.Revision, binding: binding}
}

type onlineTokenGuard struct {
	*postgresquery.QueryEngine
	t *testing.T
}

func (e onlineTokenGuard) ExecuteBound(ctx context.Context, manifest model.SemanticManifest, binding model.SourceBinding, plan model.PhysicalPlan) (model.ExecutionSnapshot, error) {
	if _, ok := executionauth.AccessToken(ctx); ok {
		e.t.Error("online PostgreSQL received a user execution token")
	}
	return e.QueryEngine.ExecuteBound(ctx, manifest, binding, plan)
}

type onlineAppsIdentityTransport struct{}

func (onlineAppsIdentityTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "workspace.example.com" || r.URL.Path != "/api/2.0/preview/scim/v2/Me" || r.Method != http.MethodGet {
		return nil, fmt.Errorf("unexpected Apps identity endpoint")
	}
	id, active, status := "synthetic-online-reader", true, http.StatusOK
	switch r.Header.Get("Authorization") {
	case "Bearer synthetic-allowed":
	case "Bearer synthetic-denied":
		id = "synthetic-nonreader"
	case "Bearer synthetic-disabled":
		active = false
	default:
		status = http.StatusUnauthorized
	}
	payload, _ := json.Marshal(map[string]any{"active": active, "id": id, "userName": id + "@example.com", "emails": []map[string]string{{"value": id + "@example.com"}}})
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(payload))}, nil
}

func TestOnlinePostgresAppsIdentityAndReaderBoundary(t *testing.T) {
	f := setupOnlinePostgresWithApps(t, true)
	for _, test := range []struct {
		name, token, identity string
		status                int
		code                  string
	}{
		{"allowed", "synthetic-allowed", "synthetic-online-reader", 200, ""},
		{"valid but not reader", "synthetic-denied", "synthetic-nonreader", 403, "permission_denied"},
		{"disabled", "synthetic-disabled", "synthetic-online-reader", 403, "permission_denied"},
		{"revoked or expired", "synthetic-invalid", "synthetic-online-reader", 401, "unauthenticated"},
		{"spoofed reader", "synthetic-denied", "synthetic-online-reader", 401, "unauthenticated"},
		{"missing token", "", "synthetic-online-reader", 401, "unauthenticated"},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, _ := json.Marshal(f.query)
			r, _ := http.NewRequest(http.MethodPost, f.server.URL+"/api/v1/namespaces/demo/online-query", bytes.NewReader(encoded))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set(appsauth.HeaderHost, "online-fixture.example.com")
			r.Header.Set(appsauth.HeaderUser, test.identity+"@example.com")
			r.Header.Set(appsauth.HeaderEmail, test.identity+"@example.com")
			r.Header.Set(appsauth.HeaderAccessToken, test.token)
			response, err := f.server.Client().Do(r)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, _ := io.ReadAll(response.Body)
			if response.StatusCode != test.status {
				t.Fatalf("Apps boundary: status=%d want=%d", response.StatusCode, test.status)
			}
			if test.code != "" {
				var problem struct {
					Code string `json:"code"`
				}
				if err := json.Unmarshal(body, &problem); err != nil || problem.Code != test.code {
					t.Fatalf("Apps boundary: problem code=%q want=%q", problem.Code, test.code)
				}
			}
			if strings.Contains(string(body), "synthetic-allowed") || strings.Contains(string(body), "synthetic-denied") {
				t.Fatal("response exposed a forwarded token")
			}
			if test.status == 200 {
				var out application.OnlineResult
				if err := json.Unmarshal(body, &out); err != nil || out.Snapshot == nil || out.Snapshot.BatchID != f.batch || out.Result == nil || len(out.Result.Rows) != 2 {
					t.Fatal("Apps reader did not receive the expected online snapshot")
				}
			}
		})
	}
	var count int
	if err := f.admin.QueryRow(t.Context(), "SELECT count(*) FROM "+pgx.Identifier{f.controlSchema, "metricspire_query_audit"}.Sanitize()+" WHERE principal='synthetic-online-reader' AND event_kind IN ('query_started','query_succeeded')").Scan(&count); err != nil || count != 2 {
		t.Fatalf("verified Apps caller audit count=%d error=%v", count, err)
	}
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
		{"incompatible", "_metricspire_data_contract='sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'", "online_data_unavailable"},
		{"mixed batch", "_metricspire_batch_id=CASE WHEN row_key='one' THEN 'batch-2' ELSE 'batch-1' END", "online_data_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f.exec(t, "UPDATE "+f.dataTable+" SET "+test.update)
			status, data := f.request(t, "analyst", f.query)
			if status != 503 || !bytes.Contains(data, []byte(test.code)) {
				t.Fatalf("%d %s", status, data)
			}
			f.exec(t, "UPDATE "+f.dataTable+" SET _metricspire_data_as_of=$2,_metricspire_batch_id='batch-1',_metricspire_data_contract=$1", f.fingerprint, f.asOf)
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
	for _, value := range []string{"NaN", "Infinity", "-Infinity"} {
		f.exec(t, "UPDATE "+f.dataTable+" SET revenue=$1::numeric WHERE row_key='one'", value)
		if status, body := f.request(t, "analyst", f.query); status != 503 || !bytes.Contains(body, []byte("online_data_unavailable")) || bytes.Contains(body, []byte(`"rows"`)) {
			t.Fatalf("nonfinite metric delivered (%s): %d %s", value, status, body)
		}
	}
	f.exec(t, "UPDATE "+f.dataTable+" SET revenue=100 WHERE row_key='one'")
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
	f.source.Spec.Metrics[0].Description = "updated description, same calculation"
	draft, err := f.management.SaveDraft(t.Context(), catalog.SaveDraftInput{Namespace: "demo", Source: f.source, ExpectedRevision: f.revision, Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	newRelease, err := f.management.Publish(t.Context(), "demo", f.source.Metadata.Name, draft.Revision, "test", "description-only release")
	if err != nil {
		t.Fatal(err)
	}
	if newRelease.ManifestFingerprint == f.release.ManifestFingerprint {
		t.Fatal("fixture must change definition fingerprint")
	}
	if status, data := f.request(t, "analyst", f.query); status != 200 || !bytes.Contains(data, []byte(newRelease.ID)) {
		t.Fatalf("description-only release rejected compatible data: %d %s", status, data)
	}
	// A new metric is allowed by publication compatibility, but requires the
	// producer to validate the new model-level calculation contract.
	added := f.source.Spec.Metrics[0]
	added.Name, added.ExternalCode = "revenue_copy", "10004"
	f.source.Spec.Metrics = append(f.source.Spec.Metrics, added)
	f.source.Metadata.Version = "1.0.2"
	draft, err = f.management.SaveDraft(t.Context(), catalog.SaveDraftInput{Namespace: "demo", Source: f.source, ExpectedRevision: draft.Revision, Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	incompatible, err := f.management.Publish(t.Context(), "demo", f.source.Metadata.Name, draft.Revision, "test", "expanded calculation contract")
	if err != nil {
		t.Fatal(err)
	}
	if status, data := f.request(t, "analyst", f.query); status != 503 || !bytes.Contains(data, []byte("online_data_unavailable")) {
		t.Fatalf("old data served under incompatible model: %d %s", status, data)
	}
	newContract, err := postgresquery.DataContractFingerprint(incompatible.Manifest, f.binding)
	if err != nil {
		t.Fatal(err)
	}
	if newContract == f.fingerprint {
		t.Fatal("calculation change did not change data contract")
	}
	f.exec(t, "UPDATE "+f.dataTable+" SET _metricspire_data_contract=$1,_metricspire_batch_id='batch-2'", newContract)
	if status, data := f.request(t, "analyst", f.query); status != 200 || !bytes.Contains(data, []byte(incompatible.ID)) {
		t.Fatalf("validated new batch unavailable: %d %s", status, data)
	}
	f.exec(t, "UPDATE "+f.dataTable+" SET _metricspire_data_contract=$1,_metricspire_batch_id='batch-1'", f.fingerprint)
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

func TestOnlinePostgresHTTPDeadlineAndAuditFaults(t *testing.T) {
	t.Run("pool exhausted", func(t *testing.T) {
		f := setupOnlinePostgres(t)
		for range 4 {
			conn, err := f.dataPool.Acquire(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Release()
		}
		started := time.Now()
		status, body := f.request(t, "analyst", f.query)
		if status != 504 || !bytes.Contains(body, []byte(`"timeout"`)) || bytes.Contains(body, []byte(`"rows"`)) || time.Since(started) > 3*time.Second {
			t.Fatalf("pool wait escaped deadline: %d %s elapsed=%s", status, body, time.Since(started))
		}
	})
	t.Run("slow completion audit", func(t *testing.T) {
		f := setupOnlinePostgres(t)
		function := pgx.Identifier{f.controlSchema, "delay_completion"}.Sanitize()
		auditTable := pgx.Identifier{f.controlSchema, "metricspire_query_audit"}.Sanitize()
		f.exec(t, "CREATE FUNCTION "+function+` () RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_kind='query_succeeded' THEN PERFORM pg_sleep(3); END IF; RETURN NEW; END $$`)
		f.exec(t, "CREATE TRIGGER delay_completion BEFORE INSERT ON "+auditTable+" FOR EACH ROW EXECUTE FUNCTION "+function+"()")
		started := time.Now()
		status, body := f.request(t, "analyst", f.query)
		if status != 504 || !bytes.Contains(body, []byte(`"timeout"`)) || bytes.Contains(body, []byte(`"rows"`)) || time.Since(started) > 3*time.Second {
			t.Fatalf("completion audit escaped deadline: %d %s elapsed=%s", status, body, time.Since(started))
		}
		f.exec(t, "DROP TRIGGER delay_completion ON "+auditTable)
		if status, body = f.request(t, "analyst", f.query); status != 200 {
			t.Fatalf("did not recover after audit delay: %d %s", status, body)
		}
	})
	t.Run("failed completion audit", func(t *testing.T) {
		f := setupOnlinePostgres(t)
		function := pgx.Identifier{f.controlSchema, "reject_completion"}.Sanitize()
		auditTable := pgx.Identifier{f.controlSchema, "metricspire_query_audit"}.Sanitize()
		f.exec(t, "CREATE FUNCTION "+function+` () RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_kind='query_succeeded' THEN RAISE EXCEPTION 'synthetic audit failure'; END IF; RETURN NEW; END $$`)
		f.exec(t, "CREATE TRIGGER reject_completion BEFORE INSERT ON "+auditTable+" FOR EACH ROW EXECUTE FUNCTION "+function+"()")
		status, body := f.request(t, "analyst", f.query)
		if status != 503 || !bytes.Contains(body, []byte(`"audit_unavailable"`)) || bytes.Contains(body, []byte(`"rows"`)) || bytes.Contains(body, []byte("synthetic audit failure")) {
			t.Fatalf("unaudited result delivered or error leaked: %d %s", status, body)
		}
	})
}

func TestOnlinePostgresHTTPColdConnectionsAndRecovery(t *testing.T) {
	f := setupOnlinePostgres(t)
	check := func(label string) {
		t.Helper()
		started := time.Now()
		status, body := f.request(t, "analyst", f.query)
		if status != 200 {
			t.Fatalf("%s: %d %s", label, status, body)
		}
		t.Logf("%s=%s (local connection establishment, not suspended database wake-up)", label, time.Since(started))
	}
	check("first query")
	check("warm query")
	f.dataPool.Reset()
	check("after pool reset")
	conn, err := f.dataPool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	pid := conn.Conn().PgConn().PID()
	// Terminate only this fixture's own SELECT connection, not other sessions.
	var terminated bool
	if err := f.admin.QueryRow(t.Context(), "SELECT pg_terminate_backend($1)", pid).Scan(&terminated); err != nil || !terminated {
		conn.Release()
		t.Fatalf("terminate fixture connection: %v", err)
	}
	conn.Release()
	status, body := f.request(t, "analyst", f.query)
	if status != 200 && (status != 503 || !bytes.Contains(body, []byte("online_engine_unavailable"))) {
		t.Fatalf("connection loss not safely reported: %d %s", status, body)
	}
	check("recovered connection")
}

func TestOnlinePostgresHTTPSustainedLoad(t *testing.T) {
	raw := os.Getenv("METRICSPIRE_ONLINE_SOAK_DURATION")
	if raw == "" {
		t.Skip("opt in with METRICSPIRE_ONLINE_SOAK_DURATION=30s for bounded local sustained load")
	}
	duration, err := time.ParseDuration(raw)
	if err != nil || duration < time.Second || duration > time.Minute {
		t.Fatal("local soak duration must be between 1s and 1m")
	}
	f := setupOnlinePostgres(t)
	if os.Getenv("METRICSPIRE_ONLINE_LOAD_ROWS") == "1000000" {
		f.exec(t, "INSERT INTO "+f.dataTable+` SELECT 'load-'||g,'2026-09-01'::date+(g%30)::int,'region-'||g,1,1,'demo','batch-1',$2,$1 FROM generate_series(1,1000000) g`, f.fingerprint, f.asOf)
		f.exec(t, "ANALYZE "+f.dataTable)
	}
	started := time.Now()
	deadline := started.Add(duration)
	var wait sync.WaitGroup
	var mu sync.Mutex
	samples := []time.Duration{}
	failures := 0
	for range 10 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			// Cap sample memory even if the fixture runs much faster than expected.
			for n := 0; n < 5000 && time.Now().Before(deadline); n++ {
				begin := time.Now()
				status, body := f.request(t, "analyst", f.query)
				var result application.OnlineResult
				valid := status == 200 && json.Unmarshal(body, &result) == nil && result.Snapshot != nil && result.Result != nil
				if valid {
					valid = result.ReleaseID == f.release.ID && result.Snapshot.BatchID == f.batch && len(result.Result.Rows) == 2 && len(result.Result.Columns) == 4
				}
				if valid {
					for i, amount := range []int64{100, 50} {
						row := result.Result.Rows[i]
						if len(row) != 4 {
							valid = false
							break
						}
						value, ok := row[1].(string)
						actual, parsed := new(big.Rat).SetString(value)
						if !ok || !parsed || actual.Cmp(new(big.Rat).SetInt64(amount)) != 0 {
							valid = false
							break
						}
					}
				}
				mu.Lock()
				samples = append(samples, time.Since(begin))
				if !valid {
					failures++
				}
				mu.Unlock()
			}
		}()
	}
	wait.Wait()
	elapsed := time.Since(started)
	if len(samples) == 0 {
		t.Fatal("no sustained samples")
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	p95, p99 := samples[(len(samples)*95-1)/100], samples[(len(samples)*99-1)/100]
	var audits int
	if err := f.admin.QueryRow(t.Context(), "SELECT count(*) FROM "+pgx.Identifier{f.controlSchema, "metricspire_query_audit"}.Sanitize()+" WHERE event_kind IN ('query_started','query_succeeded')").Scan(&audits); err != nil {
		t.Fatal(err)
	}
	t.Logf("duration=%s concurrency=10 n=%d errors=%d p95=%s p99=%s QPS=%.1f audit_rows=%d (synthetic indexed selection, no cache, not deployment SLA)", elapsed, len(samples), failures, p95, p99, float64(len(samples))/elapsed.Seconds(), audits)
	if failures != 0 || audits != 2*len(samples) || p95 > time.Second {
		t.Fatalf("sustained fixture failed: errors=%d audit_rows=%d samples=%d p95=%s", failures, audits, len(samples), p95)
	}
}
