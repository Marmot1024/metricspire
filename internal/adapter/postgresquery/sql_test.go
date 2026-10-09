package postgresquery

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/marmot1024/metricspire/internal/canonical"
	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/planner"
)

func physicalFixture(t *testing.T) model.PhysicalPlan {
	t.Helper()
	root := filepath.Join("..", "..", "..", "examples", "online")
	var source model.SemanticSource
	var policy model.PolicySource
	var binding model.SourceBinding
	for path, target := range map[string]any{"model.yaml": &source, "policy.yaml": &policy, "binding.yaml": &binding} {
		if err := contractio.ReadFile(filepath.Join(root, path), target); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := compiler.Compile(source)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := compiler.CompilePolicy(policy, manifest)
	if err != nil {
		t.Fatal(err)
	}
	query := model.SemanticQuery{APIVersion: model.APIVersion, Kind: model.KindSemanticQuery, Metrics: []string{"revenue", "orders", "average_order_value"}, GroupBy: []string{"date"}, TimeRange: &model.TimeRange{Dimension: "date", Start: "2026-09-01T00:00:00Z", End: "2026-09-03T00:00:00Z", Timezone: "UTC"}, TimeGrouping: &model.TimeGrouping{Dimension: "date", Timezone: "UTC", Granularity: model.GrainDay}, Filters: []model.Filter{{Dimension: "region", Operator: model.FilterEqual, Values: []string{"east' OR 1=1 --"}}}, Limit: 10, OrderBy: []model.OrderBy{{Field: "orders", Direction: model.SortDescending}}}
	logical, err := planner.BuildLogical(manifest, bundle, model.RequestContext{Tenant: "demo", Principal: "analyst", Roles: []string{"analyst"}, RequestID: "fixture"}, query)
	if err != nil {
		t.Fatal(err)
	}
	physical, err := planner.BuildPhysical(manifest, logical, binding, Capabilities())
	if err != nil {
		t.Fatal(err)
	}
	return physical
}

func rehash(t *testing.T, p *model.PhysicalPlan) {
	t.Helper()
	encoded, err := canonical.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "fingerprint")
	hash, err := canonical.Fingerprint(fields)
	if err != nil {
		t.Fatal(err)
	}
	p.Fingerprint = hash
}

func TestCompileOnlineSQLHasBoundParametersTenantAndSnapshotGuards(t *testing.T) {
	plan := physicalFixture(t)
	statement, err := compile(plan, "reviewed-tenant")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`FROM "serving"."daily_sales"`, "_metricspire_tenant = $", "NULLIF(", "::numeric", "_metricspire_manifest_fingerprint", "LIMIT 11", `("orders")::numeric DESC`} {
		if !strings.Contains(statement.SQL, fragment) {
			t.Fatalf("SQL lacks %s: %s", fragment, statement.SQL)
		}
	}
	if strings.Contains(statement.SQL, "OR 1=1") || strings.Contains(statement.SQL, "reviewed-tenant") {
		t.Fatal("request values were interpolated into SQL")
	}
	if len(statement.Columns) != 4 || len(statement.Args) != 4 {
		t.Fatalf("statement %#v", statement)
	}
}

func TestCompileOnlineRejectsUntrustedPlansAndUnsupportedShapes(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*model.PhysicalPlan)
	}{
		{"tampered", func(p *model.PhysicalPlan) { p.Limit = 9 }},
		{"wrong engine", func(p *model.PhysicalPlan) { p.Engine = "databricks_sql"; rehash(t, p) }},
		{"catalog", func(p *model.PhysicalPlan) { p.Root.Resource.Catalog = "unexpected"; rehash(t, p) }},
		{"unsafe table", func(p *model.PhysicalPlan) { p.Root.Resource.Table = "x;drop table x"; rehash(t, p) }},
		{"foreign field", func(p *model.PhysicalPlan) { p.MetricFields[0].Resource.Table = "other"; rehash(t, p) }},
		{"unbounded", func(p *model.PhysicalPlan) { p.TimeRange = nil; rehash(t, p) }},
		{"partial day", func(p *model.PhysicalPlan) { p.TimeRange.Start = "2026-09-01T12:00:00Z"; rehash(t, p) }},
		{"join", func(p *model.PhysicalPlan) { p.Joins = []model.PhysicalJoin{{}}; rehash(t, p) }},
		{"timestamp", func(p *model.PhysicalPlan) { p.Dimensions[0].DataType = model.DataTypeTimestamp; rehash(t, p) }},
		{"count distinct", func(p *model.PhysicalPlan) { p.Metrics[0].Expression.Op = model.OpCountDistinct; rehash(t, p) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := physicalFixture(t)
			test.change(&p)
			if test.name != "tampered" {
				if err := planner.VerifyPhysical(p); err != nil {
					t.Fatalf("test must use a valid fingerprint to exercise the shape guard: %v", err)
				}
			}
			if _, err := compile(p, "demo"); err == nil {
				t.Fatal("unsafe or unsupported plan accepted")
			}
		})
	}
}

func TestOnlineEngineConfigurationIsFailClosed(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://localhost:1/unused")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	config := Config{Tenant: "demo", Resources: []model.ResourceRef{{Kind: model.ResourceTable, Schema: "serving", Table: "daily_sales"}}, MaxDataAge: time.Minute, MaxBytes: 1024}
	if _, err := NewQueryEngine(pool, config); err != nil {
		t.Fatal(err)
	}
	for _, schema := range []string{"public", "metricspire", "pg_catalog", "pg_temp_1", "information_schema"} {
		copy := config
		copy.Resources = []model.ResourceRef{{Kind: model.ResourceTable, Schema: schema, Table: "some_table"}}
		if _, err := NewQueryEngine(pool, copy); err == nil {
			t.Fatalf("schema %s accepted", schema)
		}
	}
	engine, _ := NewQueryEngine(pool, config)
	p := physicalFixture(t)
	p.Root.Resource.Table = "not_allowed"
	_, err = engine.Execute(context.Background(), p)
	var problem *model.Problem
	if !errors.As(err, &problem) || problem.Code != "permission_denied" {
		t.Fatalf("allowlist error %v", err)
	}
}
