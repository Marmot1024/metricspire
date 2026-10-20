package dorisquery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	driver "github.com/go-sql-driver/mysql"
	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/planner"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestDorisRealSnapshotsAndCredentialBoundary(t *testing.T) {
	raw := os.Getenv("METRICSPIRE_DORIS_TEST_URL")
	if raw == "" {
		if os.Getenv("METRICSPIRE_REQUIRE_DORIS_TESTS") == "1" {
			t.Fatal("Doris URL required")
		}
		t.Skip("requires disposable Doris")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") {
		t.Fatal("disposable loopback Doris required")
	}
	admin, err := open(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	var querySeconds, memory, parallel int64
	var cache bool
	if err := admin.QueryRowContext(t.Context(), "SELECT "+queryHint+" @@query_timeout,@@exec_mem_limit,@@parallel_pipeline_task_num,@@enable_sql_cache").Scan(&querySeconds, &memory, &parallel, &cache); err != nil {
		t.Fatal("budget probe", err)
	}
	if querySeconds != 2 || memory != 268435456 || parallel != 2 || cache {
		t.Fatal("Doris did not apply query budget hints")
	}

	db, user := fmt.Sprintf("metricspire_test_%d", time.Now().UnixNano()), fmt.Sprintf("metricspire_reader_%d", time.Now().UnixNano())
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.ExecContext(t.Context(), sql, args...); err != nil {
			t.Fatal("fixture SQL failed", err)
		}
	}
	exec("CREATE DATABASE " + quote(db))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, q := range []string{"DROP USER IF EXISTS " + quote(user), "DROP DATABASE IF EXISTS " + quote(db)} {
			if _, err := admin.ExecContext(ctx, q); err != nil {
				t.Error("cleanup failed")
			}
		}
	})
	table := quote(db) + ".daily_sales"
	exec("CREATE TABLE " + table + " (row_key VARCHAR(64) NOT NULL,day DATE NOT NULL,region VARCHAR(255),revenue DECIMAL(18,6),orders BIGINT,_metricspire_tenant VARCHAR(64) NOT NULL,_metricspire_batch_id VARCHAR(128) NOT NULL,_metricspire_data_as_of DATETIME(3) NOT NULL,_metricspire_data_contract VARCHAR(71) NOT NULL) ENGINE=OLAP DUPLICATE KEY(row_key) DISTRIBUTED BY HASH(row_key) BUCKETS 1 PROPERTIES('replication_num'='1')")
	exec("CREATE USER " + quote(user) + " IDENTIFIED BY 'synthetic-only'")
	exec("GRANT SELECT_PRIV ON " + table + " TO " + quote(user))
	reader := *u
	reader.User = url.UserPassword(user, "synthetic-only")
	m, b, p := fixture(t, db)
	hash, err := DataContractFingerprint(m, b)
	if err != nil {
		t.Fatal(err)
	}
	config := Config{Tenant: "demo", Resources: []model.ResourceRef{b.Datasets[0].Resource}, MaxDataAge: time.Minute, MaxBytes: 4 << 20}
	var actualCatalog, actualEngine, actualType string
	if err := admin.QueryRowContext(t.Context(), "SELECT TABLE_CATALOG,ENGINE,TABLE_TYPE FROM information_schema.tables WHERE TABLE_SCHEMA=? AND TABLE_NAME='daily_sales'", db).Scan(&actualCatalog, &actualEngine, &actualType); err != nil {
		t.Fatal(err)
	}
	if actualCatalog != "internal" || actualEngine != "Doris" || actualType != "BASE TABLE" {
		t.Fatal("unexpected internal table metadata")
	}
	engine, err := NewQueryEngine(t.Context(), reader.String(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	cols, err := engine.sourceColumns(t.Context(), b.Datasets[0].Resource)
	if err != nil {
		t.Fatal(err)
	}
	if cols["orders"] != "bigint" || cols["revenue"] != "decimal(18, 6)" {
		t.Fatal("unsupported normalized native types")
	}
	asOf := time.Now().UTC().Format("2006-01-02 15:04:05.000")
	exec("INSERT INTO "+table+" VALUES ('one','2026-09-01','east',100,2,'demo','batch-1',?,?),('two','2026-09-01','east',50,1,'demo','batch-1',?,?),('other','2026-09-01','east',999,999,'other','batch-1',?,?)", asOf, hash, asOf, hash, asOf, hash)

	result, err := engine.ExecuteBound(t.Context(), m, b, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Result.Rows) != 1 || result.Result.Rows[0][1] != "150" || result.Result.Rows[0][2] != int64(3) || result.Result.Rows[0][3] != "50" {
		t.Fatal("wrong exact result", result.Result.Rows)
	}
	exec("GRANT LOAD_PRIV ON " + table + " TO " + quote(user))
	if e, err := NewQueryEngine(t.Context(), reader.String(), config); err == nil {
		e.Close()
		t.Fatal("write credential accepted")
	}
	exec("REVOKE LOAD_PRIV ON " + table + " FROM " + quote(user))
	exec("INSERT INTO "+table+" VALUES ('mixed','2026-09-01','east',1,1,'demo','batch-2',NOW(3),?)", hash)
	_, err = engine.ExecuteBound(t.Context(), m, b, p)
	requireProblem(t, err, "online_data_unavailable")
	exec("TRUNCATE TABLE " + table)
	exec("INSERT INTO "+table+" VALUES ('old','2026-09-01','east',100,2,'demo','batch-old',DATE_SUB(NOW(3),INTERVAL 1 DAY),?)", hash)
	_, err = engine.ExecuteBound(t.Context(), m, b, p)
	requireProblem(t, err, "online_data_stale")

	exec("TRUNCATE TABLE " + table)
	exec("ALTER TABLE " + table + " ADD COLUMN sequence BIGINT, ADD COLUMN enabled BOOLEAN")
	// Doris schema changes are asynchronous; wait only for this disposable table.
	deadline := time.Now().Add(30 * time.Second)
	for {
		var columns int
		if err := admin.QueryRowContext(t.Context(), "SELECT count(*) FROM information_schema.columns WHERE TABLE_SCHEMA=? AND TABLE_NAME='daily_sales' AND COLUMN_NAME IN ('sequence','enabled')", db).Scan(&columns); err == nil && columns == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("typed fixture schema not ready")
		}
		time.Sleep(100 * time.Millisecond)
	}
	special := "east'\\quoted"
	filters := []model.Filter{{Dimension: "sequence", Operator: model.FilterEqual, Values: []string{"9007199254740993"}}, {Dimension: "enabled", Operator: model.FilterEqual, Values: []string{"true"}}, {Dimension: "region", Operator: model.FilterEqual, Values: []string{special}}, {Dimension: "date", Operator: model.FilterEqual, Values: []string{"2026-09-01"}}}
	tm, tb, tp := typedPlan(t, db, filters)
	contract, err := DataContractFingerprint(tm, tb)
	if err != nil {
		t.Fatal(err)
	}
	exec("INSERT INTO "+table+" (row_key,day,region,revenue,orders,_metricspire_tenant,_metricspire_batch_id,_metricspire_data_as_of,_metricspire_data_contract,sequence,enabled) VALUES ('typed','2026-09-01',?,1,7,'demo','typed',?,?,9007199254740993,true),('adjacent','2026-09-01',?,1,99,'demo','typed',?,?,9007199254740992,true),('false','2026-09-01',?,1,3,'demo','typed',?,?,9007199254740993,false)", special, asOf, contract, special, asOf, contract, special, asOf, contract)
	result, err = engine.ExecuteBound(t.Context(), tm, tb, tp)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Result.Rows) != 1 || result.Result.Rows[0][1] != int64(7) {
		t.Fatal("typed filter lost integer precision, boolean, DATE or string semantics", result.Result.Rows)
	}
	filters[1].Values = []string{"false"}
	tm, tb, tp = typedPlan(t, db, filters)
	result, err = engine.ExecuteBound(t.Context(), tm, tb, tp)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Result.Rows) != 1 || result.Result.Rows[0][1] != int64(3) {
		t.Fatal("false filter lost semantics", result.Result.Rows)
	}

	if os.Getenv("METRICSPIRE_DORIS_LARGE_TEST") == "1" {
		exec("TRUNCATE TABLE " + table)
		writerConfig := driver.NewConfig()
		writerConfig.User = u.User.Username()
		writerConfig.Passwd, _ = u.User.Password()
		writerConfig.Net = "tcp"
		writerConfig.Addr = u.Host
		writerConfig.InterpolateParams = true
		writerConfig.Timeout = 5 * time.Second
		writerConfig.ReadTimeout = 120 * time.Second
		writerConfig.WriteTimeout = 5 * time.Second
		writerConfig.Params = map[string]string{"time_zone": "'+00:00'"}
		writerConnector, err := driver.NewConnector(writerConfig)
		if err != nil {
			t.Fatal("fixture writer config")
		}
		writer := sql.OpenDB(writerConnector)
		defer writer.Close()
		started := time.Now()
		asOf = time.Now().UTC().Format("2006-01-02 15:04:05.000")
		_, err = writer.ExecContext(t.Context(), "INSERT INTO "+table+" (row_key,day,revenue,orders,_metricspire_tenant,_metricspire_batch_id,_metricspire_data_as_of,_metricspire_data_contract) SELECT CAST(number AS STRING),CAST('2026-09-01' AS DATE),CAST('1.000001' AS DECIMAL(18,6)),1,'demo','generated-10m',CAST(? AS DATETIME(3)),? FROM numbers('number'='10000000')", asOf, hash)
		if err != nil {
			t.Fatal("generated load failed", err)
		}
		loaded := time.Since(started)
		started = time.Now()
		result, err := engine.ExecuteBound(t.Context(), m, b, p)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Result.Rows) != 1 || result.Result.Rows[0][1] != "10000010" || result.Result.Rows[0][2] != int64(10000000) || result.Result.Rows[0][3] != "1.000001" {
			t.Fatal("generated 10m result mismatch")
		}
		t.Logf("generated 10000000 rows; load=%s bounded query=%s", loaded, time.Since(started))
	}
	for _, raw := range []string{"doris://u:p@remote?tls=false", "doris://u:p@remote?tls=skip-verify", "doris://u:p@remote?multiStatements=true"} {
		if db, err := open(t.Context(), raw); err == nil {
			db.Close()
			t.Fatal("unsafe URL accepted")
		}
	}
	if !strings.Contains(p.Engine, "doris") {
		t.Fatal("wrong fixture engine")
	}
}

func typedPlan(t *testing.T, database string, filters []model.Filter) (model.SemanticManifest, model.SourceBinding, model.PhysicalPlan) {
	t.Helper()
	m, b, p := fixture(t, database)
	s := model.SemanticSource{APIVersion: model.APIVersion, Kind: model.KindSemanticModel, Metadata: m.Metadata, Spec: m.Definitions}
	for _, f := range []model.Field{{Name: "sequence", DataType: model.DataTypeInteger}, {Name: "enabled", DataType: model.DataTypeBoolean}} {
		s.Spec.Datasets[0].Fields = append(s.Spec.Datasets[0].Fields, f)
		s.Spec.Dimensions = append(s.Spec.Dimensions, model.Dimension{Name: f.Name, Entity: "sales", Field: f.Name, Type: model.DimensionCategorical})
		b.Datasets[0].Fields = append(b.Datasets[0].Fields, model.FieldBinding{Name: f.Name, Column: f.Name})
		for i := range s.Spec.Metrics {
			s.Spec.Metrics[i].AllowedDimensions = append(s.Spec.Metrics[i].AllowedDimensions, f.Name)
		}
	}
	m, err := compiler.Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	var policy model.PolicySource
	if err = contractio.ReadFile(filepath.Join("..", "..", "..", "examples", "online", "policy.yaml"), &policy); err != nil {
		t.Fatal(err)
	}
	policy.Rules[0].Dimensions = append(policy.Rules[0].Dimensions, "sequence", "enabled")
	bundle, err := compiler.CompilePolicy(policy, m)
	if err != nil {
		t.Fatal(err)
	}
	query := model.SemanticQuery{APIVersion: model.APIVersion, Kind: model.KindSemanticQuery, Metrics: []string{"orders"}, GroupBy: []string{"date"}, TimeRange: &model.TimeRange{Dimension: "date", Start: "2026-09-01T00:00:00Z", End: "2026-09-03T00:00:00Z", Timezone: "UTC"}, TimeGrouping: &model.TimeGrouping{Dimension: "date", Timezone: "UTC", Granularity: model.GrainDay}, Limit: 10, Filters: filters}
	logical, err := planner.BuildLogical(m, bundle, model.RequestContext{Tenant: "demo", Principal: "analyst", Roles: []string{"analyst"}, RequestID: "typed"}, query)
	if err != nil {
		t.Fatal(err)
	}
	p, err = planner.BuildPhysical(m, logical, b, (&QueryEngine{}).Capabilities())
	if err != nil {
		t.Fatal(err)
	}
	return m, b, p
}

func TestNormalizeNativeTypes(t *testing.T) {
	for raw, want := range map[string]string{"BIGINT(20)": "bigint", "tinyint(1)": "tinyint", "decimalv3(18, 6)": "decimal(18, 6)", "bigint unsigned": "bigint unsigned", "bigint(20) unsigned": "bigint(20) unsigned", "decimalv3(38,18)": "decimal(38,18)"} {
		if got := normalizeType(raw); got != want {
			t.Fatalf("%s => %s", raw, got)
		}
	}
	for _, raw := range []string{"bigint unsigned", "bigint(20) unsigned", "decimalv3(38,18)"} {
		if supportedNumeric(normalizeType(raw)) {
			t.Fatal("unsupported numeric type admitted")
		}
	}
}
