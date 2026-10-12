package databricks_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/marmot1024/metricspire/internal/adapter/databricks"
	"github.com/marmot1024/metricspire/internal/exploration"
	"github.com/marmot1024/metricspire/internal/model"
)

func directFixture(t *testing.T) (exploration.Catalog, exploration.Query, model.RequestContext) {
	t.Helper()
	var c exploration.Catalog
	read(t, filepath.Join("..", "..", "..", "testdata", "exploration", "catalog.json"), &c)
	q := exploration.Query{APIVersion: model.APIVersion, Kind: exploration.Kind, Source: "purchases", GroupBy: []string{"day", "tier", "category"},
		TimeRange:    &model.TimeRange{Dimension: "day", Start: "2026-08-03T00:00:00Z", End: "2026-08-17T00:00:00Z", Timezone: "UTC"},
		TimeGrouping: &model.TimeGrouping{Dimension: "day", Granularity: model.GrainWeek, WeekStart: model.WeekStartMonday, Timezone: "UTC"},
		Measures:     []exploration.Measure{{Name: "orders", Op: "count_distinct", Field: "order"}, {Name: "revenue", Op: "sum", Field: "amount"}},
		OrderBy:      []model.OrderBy{{Field: "day", Direction: model.SortAscending}, {Field: "tier", Direction: model.SortAscending}, {Field: "category", Direction: model.SortAscending}}, Limit: 100}
	return c, q, model.RequestContext{Tenant: "demo", Principal: "analyst", Roles: []string{"analyst"}, RequestID: "direct"}
}

func TestAnalysisDirectSourceMultiTableOracle(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python numerical oracle unavailable")
	}
	for _, mode := range []string{"normal", "same_day", "week_start", "duplicate", "missing_error", "composite", "temporary_condition", "staged_customers"} {
		t.Run(mode, func(t *testing.T) {
			c, q, rc := directFixture(t)
			switch mode {
			case "same_day", "week_start":
				c.Relationships[0].Snapshot.Anchor = mode
			case "missing_error":
				c.Relationships[0].Missing = "error"
			case "composite":
				c.Datasets[0].Fields = append(c.Datasets[0].Fields, exploration.BoundField{Name: "region", Column: "region", Type: model.DataTypeString})
				c.Datasets[1].Fields = append(c.Datasets[1].Fields, exploration.BoundField{Name: "region", Column: "region", Type: model.DataTypeString})
				c.Relationships[0].Keys = append(c.Relationships[0].Keys, exploration.JoinKey{From: "region", To: "region"})
			case "temporary_condition":
				q.Where = &exploration.Expression{Op: "gte", Args: []exploration.Expression{{Op: "ref", Ref: "amount"}, {Op: "literal", Type: model.DataTypeDecimal, Value: "15"}}}
			case "staged_customers":
				read(t, filepath.Join("..", "..", "..", "testdata", "exploration", "direct-query.json"), &q)
			}
			plan, err := exploration.PlanCatalog(c, q, rc)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Base.Metrics) != 0 || len(plan.Relations) != 3 || plan.Semantics == nil {
				t.Fatal("direct planning must not depend on registered metrics")
			}
			statement, err := databricks.CompileAnalysis(plan)
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(map[string]any{"mode": mode, "statement": statement})
			cmd := exec.Command(python, filepath.Join("..", "..", "..", "testdata", "exploration", "check_joins.py"))
			cmd.Stdin = bytes.NewReader(payload)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("oracle: %v\n%s\n%s", err, out, statement.SQL)
			}
			t.Log(string(out))
		})
	}
}

func TestAnalysisDirectCatalogRejections(t *testing.T) {
	cases := map[string]func(*exploration.Catalog, *exploration.Query, *model.RequestContext){
		"tenant": func(c *exploration.Catalog, q *exploration.Query, rc *model.RequestContext) { rc.Tenant = "other" },
		"role":   func(c *exploration.Catalog, q *exploration.Query, rc *model.RequestContext) { rc.Roles = nil },
		"denied_attribute": func(c *exploration.Catalog, q *exploration.Query, rc *model.RequestContext) {
			c.Policy.Rules = append(c.Policy.Rules, model.PolicyRule{Name: "deny", Effect: model.EffectDeny, Roles: []string{"analyst"}, Dimensions: []string{"tier"}})
		},
		"ambiguous_route": func(c *exploration.Catalog, q *exploration.Query, rc *model.RequestContext) {
			c.Relationships = append(c.Relationships, c.Relationships[0])
		},
		"cycle": func(c *exploration.Catalog, q *exploration.Query, rc *model.RequestContext) {
			c.Relationships = append(c.Relationships, exploration.Relation{Name: "cycle", From: "labels", To: "orders", Keys: []exploration.JoinKey{{From: "person_id", To: "person_id"}}, Missing: "error"})
		},
		"unknown_table": func(c *exploration.Catalog, q *exploration.Query, rc *model.RequestContext) {
			c.Sources[0].Dataset = "unknown"
		},
		"key_type": func(c *exploration.Catalog, q *exploration.Query, rc *model.RequestContext) {
			c.Datasets[1].Fields[0].Type = model.DataTypeInteger
		},
		"bad_budget": func(c *exploration.Catalog, q *exploration.Query, rc *model.RequestContext) { c.Sources[0].Timeout = 0 },
		"non_boolean_where": func(c *exploration.Catalog, q *exploration.Query, rc *model.RequestContext) {
			q.Where = &exploration.Expression{Op: "ref", Ref: "amount"}
		},
		"unsafe_table": func(c *exploration.Catalog, q *exploration.Query, rc *model.RequestContext) {
			c.Datasets[0].Resource.Table = "orders;drop"
		},
		"timezone": func(c *exploration.Catalog, q *exploration.Query, rc *model.RequestContext) {
			q.TimeRange.Timezone = "Asia/Shanghai"
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			c, q, rc := directFixture(t)
			change(&c, &q, &rc)
			if _, err := exploration.PlanCatalog(c, q, rc); err == nil {
				t.Fatal("invalid catalog/query accepted")
			}
		})
	}
}

func TestAnalysisCatalogRejectsSourceWithoutRootCalendar(t *testing.T) {
	c, _, _ := directFixture(t)
	c.Sources[0].Dimensions = []string{"tier", "category", "amount"}
	if err := exploration.ValidateCatalog(c); err == nil {
		t.Fatal("unusable source accepted by startup validation")
	}
}

func TestAnalysisInternalAttributesStayOutOfResults(t *testing.T) {
	for _, mode := range []string{"entity_weeks", "distinct_count", "raw_group", "aliased_group", "derived_group", "numeric_extreme"} {
		t.Run(mode, func(t *testing.T) {
			c, q, rc := directFixture(t)
			for i := range c.Attributes {
				c.Attributes[i].InternalOnly = c.Attributes[i].Name == "person" || c.Attributes[i].Name == "amount"
			}
			q.GroupBy = []string{"day", "person"}
			q.Measures = []exploration.Measure{{Name: "orders", Op: "count"}}
			q.Summarize = &exploration.Summary{GroupBy: []string{"day"}, Measures: []exploration.Measure{{Name: "entity_weeks", Op: "count"}}}
			q.OrderBy = nil
			switch mode {
			case "distinct_count":
				q.GroupBy = []string{"day"}
				q.Measures = []exploration.Measure{{Name: "people", Op: "count_distinct", Field: "person"}}
				q.Summarize = nil
			case "raw_group":
				q.Summarize = nil
			case "aliased_group":
				q.Calculations = []exploration.Calculation{{Name: "alias", Expression: exploration.Expression{Op: "ref", Ref: "person"}}}
				q.Summarize.GroupBy = []string{"alias"}
			case "derived_group":
				q.Calculations = []exploration.Calculation{{Name: "derived", Expression: exploration.Expression{Op: "case", Args: []exploration.Expression{
					{Op: "is_null", Args: []exploration.Expression{{Op: "ref", Ref: "person"}}},
					{Op: "literal", Type: model.DataTypeString, Value: "unknown"}, {Op: "ref", Ref: "person"},
				}}}}
				q.Summarize.GroupBy = []string{"derived"}
			case "numeric_extreme":
				q.GroupBy = nil
				q.Measures = []exploration.Measure{{Name: "sensitive", Op: "max", Field: "amount"}}
				q.Summarize = nil
			}
			p, err := exploration.PlanCatalog(c, q, rc)
			if mode != "entity_weeks" && mode != "distinct_count" {
				var problem *model.Problem
				if !errors.As(err, &problem) || problem.Code != "analysis_forbidden" {
					t.Fatalf("expected output restriction, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(p.InternalOnly) != 1 || p.InternalOnly[0] != "person" {
				t.Fatal("plan did not pin internal attribute restriction")
			}
			if _, err := databricks.CompileAnalysis(p); err != nil {
				t.Fatal(err)
			}
			p.InternalOnly = nil
			if err := exploration.Verify(p); err == nil {
				t.Fatal("removing output restriction did not invalidate plan")
			}
		})
	}
}
