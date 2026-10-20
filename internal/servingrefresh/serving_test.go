package servingrefresh

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/marmot1024/metricspire/internal/adapter/postgresquery"
	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/planner"
)

func TestRefreshServedMetricReal(t *testing.T) {
	raw := os.Getenv("METRICSPIRE_ONLINE_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	conn, err := Connect(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	schema := fmt.Sprintf("refresh_served_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = conn.Exec(t.Context(), `CREATE SCHEMA `+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := conn.Exec(ctx, `DROP SCHEMA `+quoted+` CASCADE`)
		if err != nil {
			t.Error("cleanup failed")
		}
	}()
	var source model.SemanticSource
	var binding model.SourceBinding
	var policy model.PolicySource
	for name, target := range map[string]any{"model.yaml": &source, "binding.yaml": &binding, "policy.yaml": &policy} {
		if err = contractio.ReadFile(filepath.Join("..", "..", "examples", "online", name), target); err != nil {
			t.Fatal(err)
		}
	}
	binding.Datasets[0].Resource.Schema = schema
	manifest, err := compiler.Compile(source)
	if err != nil {
		t.Fatal(err)
	}
	contract, err := NewContract(manifest, binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(t.Context(), `CREATE TABLE `+table(contract)+` (row_key text NOT NULL,day date,region text,revenue numeric(18,6),orders bigint,_metricspire_tenant text NOT NULL,_metricspire_batch_id text NOT NULL,_metricspire_data_as_of timestamptz NOT NULL,_metricspire_data_contract text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err = Init(t.Context(), conn, contract); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(filepath.Join("..", "..", "examples", "online", "producer", "rows.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	var artifact bytes.Buffer
	e := Expectations{Count: 2, Totals: map[string]*string{"orders": pointer("3"), "revenue": pointer("150.000003")}}
	asof := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	if err = Export(input, &artifact, contract, Header{Tenant: "demo", BatchID: "synthetic-source-1", DataAsOf: asof}, e); err != nil {
		t.Fatal(err)
	}
	if _, err = Refresh(t.Context(), conn, contract, bytes.NewReader(artifact.Bytes()), "demo", ""); err != nil {
		t.Fatal(err)
	}
	bundle, err := compiler.CompilePolicy(policy, manifest)
	if err != nil {
		t.Fatal(err)
	}
	query := model.SemanticQuery{APIVersion: model.APIVersion, Kind: model.KindSemanticQuery, Metrics: []string{"revenue", "orders", "average_order_value"}, TimeRange: &model.TimeRange{Dimension: "date", Start: "2026-09-01T00:00:00Z", End: "2026-09-03T00:00:00Z", Timezone: "UTC"}, Limit: 10}
	logical, err := planner.BuildLogical(manifest, bundle, model.RequestContext{Tenant: "demo", Principal: "analyst", Roles: []string{"analyst"}, RequestID: "synthetic-refresh"}, query)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	engine, err := postgresquery.NewQueryEngine(pool, postgresquery.Config{Tenant: "demo", Resources: []model.ResourceRef{contract.Resource}, MaxDataAge: time.Minute, MaxBytes: 4 << 20})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.BuildPhysical(manifest, logical, binding, engine.Capabilities())
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.ExecuteBound(t.Context(), manifest, binding, plan)
	if err != nil {
		t.Fatal(err)
	}
	got := result.Result.Rows
	if len(got) != 1 || got[0][0] != "150.000003" || got[0][1] != int64(3) || got[0][2] != "50.0000010000000000" {
		t.Fatalf("incorrect refreshed metric: %v", got)
	}
	// Loading old coverage is allowed, but the serving freshness guard must refuse it.
	old := asof.Add(-2 * time.Hour)
	if _, err = conn.Exec(t.Context(), `UPDATE `+table(contract)+` SET _metricspire_data_as_of=$1`, old); err != nil {
		t.Fatal(err)
	}
	if _, err = engine.ExecuteBound(t.Context(), manifest, binding, plan); err == nil {
		t.Fatal("stale snapshot served")
	}
}
func TestLargeGeneratedRefreshReal(t *testing.T) {
	count, _ := strconv.ParseInt(os.Getenv("METRICSPIRE_REFRESH_LARGE_ROWS"), 10, 64)
	if count == 0 {
		t.Skip("explicit generated load test")
	}
	if count < 1 || count > 10000000 {
		t.Fatal("invalid generated row count")
	}
	raw := os.Getenv("METRICSPIRE_ONLINE_TEST_DATABASE_URL")
	conn, err := Connect(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	schema := fmt.Sprintf("refresh_large_%d", time.Now().UnixNano())
	q := pgx.Identifier{schema}.Sanitize()
	if _, err = conn.Exec(t.Context(), `CREATE SCHEMA `+q); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := conn.Exec(ctx, `DROP SCHEMA `+q+` CASCADE`); err != nil {
			t.Error("cleanup failed")
		}
	}()
	c := testContract()
	c.Resource.Schema = schema
	if _, err = conn.Exec(t.Context(), `CREATE TABLE `+table(c)+` (id text NOT NULL,value numeric(18,6),_metricspire_tenant text NOT NULL,_metricspire_batch_id text NOT NULL,_metricspire_data_as_of timestamptz NOT NULL,_metricspire_data_contract text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err = Init(t.Context(), conn, c); err != nil {
		t.Fatal(err)
	}
	input, err := os.CreateTemp("", "metricspire-generated-rows-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(input.Name())
	defer input.Close()
	for i := int64(0); i < count; i++ {
		if _, err = fmt.Fprintf(input, "{\"id\":\"row-%d\",\"value\":\"1.000001\"}\n", i); err != nil {
			t.Fatal(err)
		}
	}
	input.Seek(0, 0)
	batch, err := os.CreateTemp("", "metricspire-generated-batch-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(batch.Name())
	defer batch.Close()
	micros := count * 1000001
	total := fmt.Sprintf("%d.%06d", micros/1000000, micros%1000000)
	started := time.Now()
	if err = Export(input, batch, c, Header{Tenant: "demo", BatchID: "generated", DataAsOf: started.UTC().Add(-time.Second).Truncate(time.Microsecond)}, Expectations{Count: count, Totals: map[string]*string{"value": &total}}); err != nil {
		t.Fatal(err)
	}
	exported := time.Since(started)
	batch.Seek(0, 0)
	loaded := time.Now()
	if receipt, err := Refresh(t.Context(), conn, c, batch, "demo", ""); err != nil || receipt.Rows != count {
		t.Fatalf("large load %v %v", receipt, err)
	}
	var dbcount int64
	var dbsum string
	if err = conn.QueryRow(t.Context(), `SELECT count(*),sum(value)::text FROM `+table(c)).Scan(&dbcount, &dbsum); err != nil || dbcount != count || dbsum != total {
		t.Fatalf("large reconciliation: %d %s %v", dbcount, dbsum, err)
	}
	t.Logf("synthetic rows=%d exact_sum=%s export=%s atomic_load=%s", count, total, exported, time.Since(loaded))
}
