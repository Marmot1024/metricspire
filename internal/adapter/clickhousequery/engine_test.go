package clickhousequery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/planner"
)

func fixture(t *testing.T, database string) (model.SemanticManifest, model.SourceBinding, model.PhysicalPlan) {
	t.Helper()
	var source model.SemanticSource
	var policy model.PolicySource
	var binding model.SourceBinding
	root := filepath.Join("..", "..", "..", "examples", "online")
	for name, target := range map[string]any{"model.yaml": &source, "binding.yaml": &binding, "policy.yaml": &policy} {
		if err := contractio.ReadFile(filepath.Join(root, name), target); err != nil {
			t.Fatal(err)
		}
	}
	binding.Engine = EngineName
	binding.Datasets[0].Resource.Schema = database
	manifest, err := compiler.Compile(source)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := compiler.CompilePolicy(policy, manifest)
	if err != nil {
		t.Fatal(err)
	}
	query := model.SemanticQuery{APIVersion: model.APIVersion, Kind: model.KindSemanticQuery, Metrics: []string{"revenue", "orders", "average_order_value"}, GroupBy: []string{"date"}, TimeRange: &model.TimeRange{Dimension: "date", Start: "2026-09-01T00:00:00Z", End: "2026-09-03T00:00:00Z", Timezone: "UTC"}, TimeGrouping: &model.TimeGrouping{Dimension: "date", Timezone: "UTC", Granularity: model.GrainDay}, Limit: 10, OrderBy: []model.OrderBy{{Field: "orders", Direction: model.SortDescending}}}
	logical, err := planner.BuildLogical(manifest, bundle, model.RequestContext{Tenant: "demo", Principal: "analyst", Roles: []string{"analyst"}, RequestID: "fixture"}, query)
	if err != nil {
		t.Fatal(err)
	}
	engine := &QueryEngine{}
	physical, err := planner.BuildPhysical(manifest, logical, binding, engine.Capabilities())
	if err != nil {
		t.Fatal(err)
	}
	return manifest, binding, physical
}
func requireProblem(t *testing.T, err error, code string) {
	t.Helper()
	var p *model.Problem
	if !errors.As(err, &p) || p.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}
func TestClickHouseTransportRejectsRedirectsLateErrorsAndLeaks(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
	}{{"late exception", "[\"ok\"]\nCode: 123 private detail", 200}, {"too large", strings.Repeat("x", maxWireBytes+1), 200}, {"error", "private credential/table", 500}, {"redirect", "", 302}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("readonly") != "1" || r.URL.Query().Get("max_execution_time") != "2" || r.URL.Query().Get("default_format") != "JSONCompactEachRow" || r.URL.Query().Has("query") || r.URL.Query().Has("param_value") {
					t.Error("missing enforced settings or values in URL")
				}
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Fatal(err)
				}
				if r.FormValue("param_value") != `quote'\\\n` {
					t.Error("parameter lost")
				}
				w.Header().Set("Location", "http://invalid.example")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer s.Close()
			u, _ := url.Parse(s.URL)
			u.User = url.UserPassword("synthetic", "synthetic-secret")
			c, err := newClient(u.String())
			if err != nil {
				t.Fatal(err)
			}
			defer c.close()
			_, err = c.query(t.Context(), "SELECT {value:String}", map[string]string{"value": "quote'\\\n"}, true)
			if err == nil || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe error %v", err)
			}
		})
	}
	for _, raw := range []string{"http://remote.example", "https://u:p@host/?readonly=0", "https://u:p@host/table", "ftp://u:p@host/"} {
		if _, err := newClient(raw); err == nil {
			t.Fatal("unsafe endpoint accepted")
		}
	}
}
func TestClickHouseRealSnapshotAndCredentialBoundary(t *testing.T) {
	raw := os.Getenv("METRICSPIRE_CLICKHOUSE_TEST_URL")
	if raw == "" {
		if os.Getenv("METRICSPIRE_REQUIRE_CLICKHOUSE_TESTS") == "1" {
			t.Fatal("ClickHouse test URL required")
		}
		t.Skip("requires disposable loopback ClickHouse")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") {
		t.Fatal("fixture requires loopback")
	}
	admin, err := newClient(raw)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.close()
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	db, user := "metricspire_test_"+id, "metricspire_reader_"+id
	role := user + "_role"
	exec := func(sql string, params map[string]string) {
		t.Helper()
		if _, err := admin.query(t.Context(), sql, params, false); err != nil {
			t.Fatalf("fixture operation failed: %v", err)
		}
	}
	exec("CREATE DATABASE "+quote(db), nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, q := range []string{"DROP USER IF EXISTS " + quote(user), "DROP ROLE IF EXISTS " + quote(role), "DROP DATABASE IF EXISTS " + quote(db)} {
			if _, err := admin.query(ctx, q, nil, false); err != nil {
				t.Error("fixture cleanup failed")
			}
		}
	})
	table := quote(db) + ".daily_sales"
	exec("CREATE TABLE "+table+" (row_key String,day Date,region String,revenue Nullable(Decimal(18,6)),orders Nullable(Int64),_metricspire_tenant String,_metricspire_batch_id String,_metricspire_data_as_of DateTime64(3,'UTC'),_metricspire_data_contract String) ENGINE=MergeTree ORDER BY (_metricspire_tenant,day,region,row_key)", nil)
	exec("CREATE USER "+quote(user)+" IDENTIFIED BY 'synthetic-only'", nil)
	for _, resource := range []string{table, "system.grants", "system.role_grants", "system.tables", "system.columns"} {
		exec("GRANT SELECT ON "+resource+" TO "+quote(user), nil)
	}
	reader := *u
	reader.User = url.UserPassword(user, "synthetic-only")
	m, b, p := fixture(t, db)
	hash, err := DataContractFingerprint(m, b)
	if err != nil {
		t.Fatal(err)
	}
	config := Config{Tenant: "demo", Resources: []model.ResourceRef{b.Datasets[0].Resource}, MaxDataAge: time.Minute, MaxBytes: 4 << 20}
	engine, err := NewQueryEngine(t.Context(), reader.String(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	for _, value := range []string{"quote'\\\n", "east\twest", `foo\Nbar`, `back\slash`, "null\x00byte", "line\rreturn", "control\b\f", "中文"} {
		rows, err := engine.client.query(t.Context(), "SELECT {value:String}", map[string]string{"value": value}, true)
		if err != nil || len(rows) != 1 || len(rows[0]) != 1 || rows[0][0] == nil || *rows[0][0] != value {
			t.Fatalf("escaped parameter roundtrip failed: %v", err)
		}
	}
	params := map[string]string{"hash": hash}
	insert := func(values string) { exec("INSERT INTO "+table+" SELECT "+values, params) }
	insert("'one',toDate('2026-09-01'),'east',toDecimal64(100,6),toInt64(2),'demo','batch',toDateTime64('2026-10-20 00:00:00',3,'UTC'),{hash:String}")
	insert("'two',toDate('2026-09-02'),'west',toDecimal64(110,6),toInt64(4),'demo','batch',toDateTime64('2026-10-20 00:00:00',3,'UTC'),{hash:String}")
	insert("'other',toDate('2026-09-01'),'east',toDecimal64(999,6),toInt64(99),'other','other-batch',now64(3),{hash:String}")
	engine.now = func() time.Time { return time.Date(2026, 10, 20, 0, 0, 1, 0, time.UTC) }
	result, err := engine.ExecuteBound(t.Context(), m, b, p)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result.Result.Rows)
	if string(encoded) != `[["2026-09-02","110",4,"27.5"],["2026-09-01","100",2,"50"]]` {
		t.Fatalf("unexpected sorted exact result %s columns=%v", encoded, result.Result.Columns)
	}
	engine.now = func() time.Time { return time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC) }
	_, err = engine.ExecuteBound(t.Context(), m, b, p)
	requireProblem(t, err, "online_data_stale")
	engine.now = func() time.Time { return time.Date(2026, 10, 20, 0, 0, 1, 0, time.UTC) }
	insert("'mixed',toDate('2026-09-01'),'east',toDecimal64(1,6),toInt64(1),'demo','different',toDateTime64('2026-10-20 00:00:00',3,'UTC'),{hash:String}")
	_, err = engine.ExecuteBound(t.Context(), m, b, p)
	requireProblem(t, err, "online_data_unavailable")
	// Read grants are checked for every exact source, including legacy prefix-grant representation.
	rejectReader := func() {
		t.Helper()
		if e, err := NewQueryEngine(t.Context(), reader.String(), config); err == nil {
			e.Close()
			t.Fatal("overbroad/incomplete credential accepted")
		}
	}
	exec("REVOKE SELECT ON "+table+" FROM "+quote(user), nil)
	rejectReader()
	exec("GRANT SELECT(revenue) ON "+table+" TO "+quote(user), nil)
	rejectReader()
	exec("REVOKE SELECT(revenue) ON "+table+" FROM "+quote(user), nil)
	exec("GRANT SELECT ON "+table+"* TO "+quote(user), nil)
	rejectReader()
	exec("REVOKE SELECT ON "+table+"* FROM "+quote(user), nil)
	exec("GRANT SELECT ON "+table+" TO "+quote(user)+" WITH GRANT OPTION", nil)
	rejectReader()
	exec("REVOKE SELECT ON "+table+" FROM "+quote(user), nil)
	exec("GRANT SELECT ON "+table+" TO "+quote(user), nil)
	exec("CREATE ROLE "+quote(role), nil)
	exec("GRANT "+quote(role)+" TO "+quote(user), nil)
	rejectReader()
	exec("REVOKE "+quote(role)+" FROM "+quote(user), nil)
	exec("DROP ROLE "+quote(role), nil)
	// Column-level writes must be rejected, even when SELECT remains present.
	exec("GRANT INSERT(revenue) ON "+table+" TO "+quote(user), nil)
	if e, err := NewQueryEngine(t.Context(), reader.String(), config); err == nil {
		e.Close()
		t.Fatal("column writer accepted")
	}
	exec("REVOKE INSERT(revenue) ON "+table+" FROM "+quote(user), nil)
	if _, err = admin.query(t.Context(), "SELECT 'ok'", nil, false); err != nil {
		t.Fatal(err)
	}

	// Verify settings at the server: multipart fields alone do not apply HTTP settings.
	settings, err := engine.client.query(t.Context(), "SELECT toString(getSetting('readonly')),toString(getSetting('max_rows_to_read')),toString(getSetting('max_execution_time'))", nil, true)
	if err != nil || len(settings) != 1 || len(settings[0]) != 3 || text(settings[0][0]) != "1" || text(settings[0][1]) != "50000000" || text(settings[0][2]) != "2" {
		t.Fatalf("remote budgets not applied: %v", err)
	}
	wide, err := engine.client.query(t.Context(), "SELECT toString(sumOrNull(toDecimal128(toUInt64('18446744073709551615'),6))) FROM numbers(2)", nil, true)
	if err != nil || len(wide) != 1 || text(wide[0][0]) != "36893488147419103230" {
		t.Fatalf("UInt64 widening failed: %v %v", wide, err)
	}
	for _, tc := range []struct{ name, revenue, orders, want string }{
		{"null", "NULL", "NULL", `[["2026-09-01",null,null,null]]`},
		{"zero", "toDecimal64(1,6)", "toInt64(0)", `[["2026-09-01","2",0,null]]`},
		{"decimal", "toDecimal64('999999999999.999999',6)", "toInt64(1)", `[["2026-09-01","1999999999999.999998",2,"999999999999.999999"]]`},
	} {
		exec("TRUNCATE TABLE "+table, nil)
		insert("toString(number),toDate('2026-09-01'),'east'," + tc.revenue + "," + tc.orders + ",'demo','numeric',toDateTime64('2026-10-20 00:00:00',3,'UTC'),{hash:String} FROM numbers(2)")
		got, err := engine.ExecuteBound(t.Context(), m, b, p)
		if err != nil {
			t.Fatal(tc.name, err)
		}
		body, _ := json.Marshal(got.Result.Rows)
		if string(body) != tc.want {
			t.Fatalf("%s result %s", tc.name, body)
		}
	}
	if rawRows := os.Getenv("METRICSPIRE_CLICKHOUSE_LOAD_ROWS"); rawRows != "" {
		count, err := strconv.Atoi(rawRows)
		if err != nil || count < 1 || count > 10000000 {
			t.Fatal("load fixture must contain 1..10000000 rows")
		}
		exec("TRUNCATE TABLE "+table, nil)

		// Bound fixture generation independently of the application's read budget.
		// One giant sort can exhaust a small lab container before acceptance starts.
		for offset := 0; offset < count; offset += 100000 {
			batch := min(100000, count-offset)
			exec(fmt.Sprintf("INSERT INTO %s SELECT toString(number),toDate('2026-09-01'),'east',toDecimal64('2.5',6),toInt64(1),'demo','load',toDateTime64('2026-10-20 00:00:00',3,'UTC'),{hash:String} FROM numbers(%d,%d) SETTINGS max_threads=1,max_insert_threads=1,max_insert_block_size=100000", table, offset, batch), params)
		}

		// Isolate read acceptance from the fixture producer's background merge work.
		deadline := time.Now().Add(30 * time.Second)
		for {
			active, err := admin.query(t.Context(), "SELECT toString(count()) FROM system.merges WHERE database={database:String}", map[string]string{"database": db}, false)
			if err != nil || len(active) != 1 || len(active[0]) != 1 {
				t.Fatal("cannot inspect fixture merge completion")
			}
			if text(active[0][0]) == "0" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("fixture background merges did not settle")
			}
			time.Sleep(100 * time.Millisecond)
		}
		started := time.Now()
		got, err := engine.ExecuteBound(t.Context(), m, b, p)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Result.Rows) != 1 || got.Result.Rows[0][3] != "2.5" || got.Result.Rows[0][2] != int64(count) || got.Result.Rows[0][1] != strconv.FormatFloat(float64(count)*2.5, 'f', -1, 64) {
			t.Fatalf("large aggregate mismatch: %v", got.Result.Rows)
		}
		t.Logf("synthetic rows=%d aggregate elapsed=%s exact ratio/count passed", count, time.Since(started))
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = engine.ExecuteBound(ctx, m, b, p)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel lost: %v", err)
	}
}
