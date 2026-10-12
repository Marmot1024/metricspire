package databricks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/adapter/databricks"
	"github.com/marmot1024/metricspire/internal/canonical"
	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/executionauth"
	"github.com/marmot1024/metricspire/internal/exploration"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/planner"
)

func analysisFixture(t *testing.T) (exploration.Query, exploration.Source, model.PhysicalPlan, model.RequestContext) {
	t.Helper()
	root := filepath.Join("..", "..", "..", "testdata", "exploration")
	var source model.SemanticSource
	var binding model.SourceBinding
	var q exploration.Query
	read(t, filepath.Join(root, "model.json"), &source)
	read(t, filepath.Join(root, "binding.json"), &binding)
	read(t, filepath.Join(root, "query.json"), &q)
	manifest, err := compiler.Compile(source)
	if err != nil {
		t.Fatal(err)
	}
	rc := model.RequestContext{Tenant: "demo", Principal: "analyst", Roles: []string{"analyst"}, RequestID: "fixture"}
	policy := model.PolicySource{APIVersion: model.APIVersion, Kind: model.KindPolicySource, Metadata: model.Metadata{Name: "demo", Version: "1"}, Tenant: "demo", Rules: []model.PolicyRule{{Name: "read", Effect: model.EffectAllow, Roles: []string{"analyst"}, Metrics: []string{"*"}, Dimensions: []string{"*"}}}}
	bundle, err := compiler.CompilePolicy(policy, manifest)
	if err != nil {
		t.Fatal(err)
	}
	logical, err := planner.BuildLogical(manifest, bundle, rc, model.SemanticQuery{APIVersion: model.APIVersion, Kind: model.KindSemanticQuery, Metrics: []string{"active_events"}, GroupBy: exploration.References(q), TimeRange: q.TimeRange, TimeGrouping: q.TimeGrouping, Limit: q.Limit})
	if err != nil {
		t.Fatal(err)
	}
	binding.ManifestFingerprint = manifest.Fingerprint
	physical, err := planner.BuildPhysical(manifest, logical, binding, databricks.Capabilities())
	if err != nil {
		t.Fatal(err)
	}
	profile := exploration.Source{Name: "activity", AnchorMetric: "active_events", Dimensions: []string{"person", "day", "tier"}, Roles: []string{"analyst"}, MaxWindowDays: 28, MaxRows: 100, Timeout: time.Minute}
	return q, profile, physical, rc
}

func TestAnalysisSQLNumericalOracle(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for the local SQL numerical oracle")
	}
	for _, mode := range []string{"weekly_tier", "overall", "limited"} {
		t.Run(mode, func(t *testing.T) {
			q, profile, base, rc := analysisFixture(t)
			if mode == "overall" {
				q.Summarize.GroupBy = []string{"band", "preference"}
				q.Summarize.Calculations[0].Expression.PartitionBy = nil
				q.OrderBy = q.OrderBy[2:]
			}
			if mode == "limited" {
				q.Limit = 1
			}
			plan, err := exploration.Prepare(q, profile, base, rc)
			if err != nil {
				t.Fatal(err)
			}
			statement, err := databricks.CompileAnalysis(plan)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(statement.SQL, "LIMIT") != 1 || statement.PhysicalFingerprint != plan.Fingerprint {
				t.Fatal("inner limit or lost fingerprint")
			}
			for _, literal := range []string{"active", "low", "medium", "high", "2026-08-03"} {
				if strings.Contains(statement.SQL, "'"+literal+"'") {
					t.Fatalf("unbound literal: %s", literal)
				}
			}
			payload, _ := json.Marshal(map[string]any{"query": q, "statement": statement})
			command := exec.Command(python, filepath.Join("..", "..", "..", "testdata", "exploration", "check_sql.py"))
			command.Stdin = bytes.NewReader(payload)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("numerical oracle: %v\n%s\n%s", err, output, statement.SQL)
			}
			t.Log(string(output))
		})
	}
}

func TestAnalysisRejectsInvalidAndTamperedWorkflow(t *testing.T) {
	cases := map[string]func(*exploration.Query, *exploration.Source, *model.RequestContext){
		"role": func(q *exploration.Query, s *exploration.Source, rc *model.RequestContext) { rc.Roles = nil },
		"source_dimension": func(q *exploration.Query, s *exploration.Source, rc *model.RequestContext) {
			s.Dimensions = []string{"day"}
		},
		"window_budget": func(q *exploration.Query, s *exploration.Source, rc *model.RequestContext) { s.MaxWindowDays = 1 },
		"row_budget":    func(q *exploration.Query, s *exploration.Source, rc *model.RequestContext) { q.Limit = 101 },
		"invalid_condition": func(q *exploration.Query, s *exploration.Source, rc *model.RequestContext) {
			q.Measures[0].Where = &exploration.Expression{Op: "ref", Ref: "person"}
		},
		"unknown_raw_field": func(q *exploration.Query, s *exploration.Source, rc *model.RequestContext) {
			q.Measures[0].Field = "secret"
		},
		"forward_ref": func(q *exploration.Query, s *exploration.Source, rc *model.RequestContext) {
			q.Calculations[0].Expression = exploration.Expression{Op: "ref", Ref: "rate_gap"}
		},
		"invalid_share_partition": func(q *exploration.Query, s *exploration.Source, rc *model.RequestContext) {
			q.Summarize.Calculations[0].Expression.PartitionBy = []string{"person"}
		},
		"window_in_inner_stage": func(q *exploration.Query, s *exploration.Source, rc *model.RequestContext) {
			q.Calculations[0].Expression = exploration.Expression{Op: "share", Args: []exploration.Expression{{Op: "ref", Ref: "days"}}}
		},
		"unsafe_output_name": func(q *exploration.Query, s *exploration.Source, rc *model.RequestContext) {
			q.Measures[0].Name = "x`); DROP TABLE events"
		},
		"expression_depth": func(q *exploration.Query, s *exploration.Source, rc *model.RequestContext) {
			e := exploration.Expression{Op: "literal", Type: model.DataTypeBoolean, Value: "true"}
			for range 15 {
				e = exploration.Expression{Op: "not", Args: []exploration.Expression{e}}
			}
			q.Measures[0].Where = &e
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			q, s, base, rc := analysisFixture(t)
			mutate(&q, &s, &rc)
			if _, err := exploration.Prepare(q, s, base, rc); err == nil {
				t.Fatal("invalid workflow accepted")
			}
		})
	}
	q, s, base, rc := analysisFixture(t)
	p, err := exploration.Prepare(q, s, base, rc)
	if err != nil {
		t.Fatal(err)
	}
	q.Calculations[0].Name = "mutated"
	s.Dimensions[0] = "mutated"
	if err = exploration.Verify(p); err != nil {
		t.Fatalf("caller mutation changed immutable plan: %v", err)
	}
	p.Query.Limit++
	if _, err = databricks.CompileAnalysis(p); err == nil {
		t.Fatal("tampered plan accepted")
	}
}

func TestAnalysisRejectsPreaggregatedOrChangedAuthorizedSource(t *testing.T) {
	for _, mode := range []string{"distinct_anchor", "join", "changed_range", "changed_filter"} {
		t.Run(mode, func(t *testing.T) {
			q, s, base, rc := analysisFixture(t)
			switch mode {
			case "distinct_anchor":
				base.Metrics[0].Expression.Op = model.OpCountDistinct
			case "join":
				base.Joins = []model.PhysicalJoin{{Name: "unapproved"}}
			case "changed_range":
				q.TimeRange.Start = "2026-08-04T00:00:00Z"
			case "changed_filter":
				q.Filters = []model.Filter{{Dimension: "tier", Operator: model.FilterEqual, Values: []string{"alpha"}}}
			}
			data, _ := json.Marshal(base)
			var payload map[string]any
			_ = json.Unmarshal(data, &payload)
			delete(payload, "fingerprint")
			base.Fingerprint, _ = canonical.Fingerprint(payload)
			if _, err := exploration.Prepare(q, s, base, rc); err == nil {
				t.Fatal("changed authorized source accepted")
			}
		})
	}
}

type analysisTransport func(*http.Request) (*http.Response, error)

func (f analysisTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAnalysisEngineReusesExistingStatementClient(t *testing.T) {
	q, s, base, rc := analysisFixture(t)
	p, err := exploration.Prepare(q, s, base, rc)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	httpClient := &http.Client{Transport: analysisTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/api/2.0/sql/statements" || r.Header.Get("Authorization") != "Bearer fixture-existing-token" {
			t.Fatal("unexpected execution request")
		}
		var body struct {
			SQL        string                 `json:"statement"`
			Parameters []databricks.Parameter `json:"parameters"`
			RowLimit   int64                  `json:"row_limit"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(body.SQL, "SELECT ") || body.RowLimit != 100 || len(body.Parameters) == 0 {
			t.Fatal("lost compiled statement or budget")
		}
		columns := []map[string]string{}
		for _, c := range p.Columns {
			typ := map[model.DataType]string{model.DataTypeDate: "DATE", model.DataTypeString: "STRING", model.DataTypeInteger: "LONG", model.DataTypeDecimal: "DOUBLE"}[c.Type]
			columns = append(columns, map[string]string{"name": c.Name, "type_name": typ, "type_text": typ})
		}
		response := map[string]any{"statement_id": "analysis-fixture", "status": map[string]string{"state": "SUCCEEDED"}, "manifest": map[string]any{"schema": map[string]any{"columns": columns}}, "result": map[string]any{"data_array": []any{}}}
		data, _ := json.Marshal(response)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}, nil
	}), Timeout: time.Second}
	client, err := databricks.NewClient(databricks.ClientConfig{Host: "https://workspace.test", WarehouseID: "demo", RequireRequestToken: true, HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := databricks.NewAnalysisEngine(client)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := executionauth.WithAccessToken(context.Background(), "fixture-existing-token")
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || result.Job.Status != model.JobSucceeded || result.Job.PhysicalFingerprint != p.Fingerprint || result.Result == nil {
		t.Fatal("lost execution result or plan fingerprint")
	}
}

func TestAnalysisEngineMatchErrorsAreSpecificAndSanitized(t *testing.T) {
	for _, code := range []string{"analysis_duplicate_match", "analysis_missing_match"} {
		t.Run(code, func(t *testing.T) {
			q, s, base, rc := analysisFixture(t)
			p, err := exploration.Prepare(q, s, base, rc)
			if err != nil {
				t.Fatal(err)
			}
			client, err := databricks.NewClient(databricks.ClientConfig{Host: "https://workspace.test", WarehouseID: "demo", TokenSource: databricks.StaticTokenSource("test-only"), HTTPClient: &http.Client{Transport: analysisTransport(func(*http.Request) (*http.Response, error) {
				data, _ := json.Marshal(map[string]any{"statement_id": "failed", "status": map[string]any{"state": "FAILED", "error": map[string]string{"error_code": "BAD_REQUEST", "message": "[USER_RAISED_EXCEPTION] " + code + "; private SQL details"}}})
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			engine, _ := databricks.NewAnalysisEngine(client)
			_, err = engine.Execute(context.Background(), p)
			var problem *model.Problem
			if !errors.As(err, &problem) || problem.Code != code || strings.Contains(err.Error(), "private SQL") {
				t.Fatal("lost specific match rejection or disclosed raw engine detail")
			}
		})
	}
}
