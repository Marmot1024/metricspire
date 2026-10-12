package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/adapter/databricks"
	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/executionauth"
	"github.com/marmot1024/metricspire/internal/exploration"
	"github.com/marmot1024/metricspire/internal/httpapi"
	"github.com/marmot1024/metricspire/internal/model"
)

// This opt-in test issues only read-only SELECTs against the public TPC-DS
// calendar. HTTP identity and audit use local fixtures; the warehouse, SQL,
// parameter types, exact-date joins, cancellation and result decoding are real.
// It never starts a warehouse or logs in. CLI auth stdout stays in memory.
func TestAnalysisRealMCPDatabricks(t *testing.T) {
	if os.Getenv("METRICSPIRE_RUN_REAL_EXPLORATION") != "1" {
		t.Skip("opt-in real exploration acceptance")
	}
	profile, host, warehouse := os.Getenv("METRICSPIRE_EXPLORATION_PROFILE"), os.Getenv("METRICSPIRE_EXPLORATION_HOST"), os.Getenv("METRICSPIRE_EXPLORATION_WAREHOUSE")
	if profile == "" || host == "" || warehouse == "" {
		t.Fatal("explicit existing CLI profile, host and running warehouse required")
	}
	credential := httpapi.ExecutionCredentialFunc(func(ctx context.Context, _ *http.Request, _ httpapi.Principal) (context.Context, error) {
		cmd := exec.CommandContext(ctx, "databricks", "auth", "token", "--profile", profile)
		out, err := cmd.Output()
		if err != nil {
			return nil, errors.New("existing CLI credential unavailable; no login attempted")
		}
		var token struct {
			AccessToken string `json:"access_token"`
		}
		if json.Unmarshal(out, &token) != nil || token.AccessToken == "" {
			return nil, errors.New("existing CLI credential response invalid")
		}
		return executionauth.WithAccessToken(ctx, token.AccessToken)
	})
	client, err := databricks.NewClient(databricks.ClientConfig{Host: host, WarehouseID: warehouse, RequireRequestToken: true, PollInterval: time.Second})
	if err != nil {
		t.Fatal("real client configuration rejected")
	}
	engine, err := databricks.NewAnalysisEngine(client)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"staged", "temporary_condition", "missing_null", "missing_error", "duplicate_error", "excluded_missing"} {
		t.Run(mode, func(t *testing.T) {
			f := realCalendarFixture(t)
			switch mode {
			case "temporary_condition":
				f.query.Where = &exploration.Expression{Op: "gte", Args: []exploration.Expression{{Op: "ref", Ref: "dom"}, {Op: "literal", Type: model.DataTypeInteger, Value: "3"}}}
			case "missing_null", "missing_error":
				f.catalog.Relationships[0].Keys[0].From = "dom"
				if mode == "missing_error" {
					f.catalog.Relationships[0].Missing = "error"
				}
				f.query.Summarize = nil
				f.query.OrderBy = nil
			case "excluded_missing":
				f.catalog.Relationships[0].Keys = []exploration.JoinKey{{From: "key", To: "key"}}
				f.catalog.Relationships[0].Missing = "error"
				f.query.Where = &exploration.Expression{Op: "eq", Args: []exploration.Expression{{Op: "weekday", Args: []exploration.Expression{{Op: "ref", Ref: "day"}}}, {Op: "literal", Type: model.DataTypeInteger, Value: "6"}}}
			case "duplicate_error":
				f.catalog.Relationships[0].Snapshot = nil
			}
			handler, metricEngine := newExplorationTransport(t, f, engine, credential)
			session := connectAnalysisMCP(t, handler, "query")
			callRemoteMCP(t, session, "discover_analysis", map[string]any{"namespace": "demo"}, false)
			args := map[string]any{"namespace": "demo", "query": f.query}
			callRemoteMCP(t, session, "plan_analysis", args, false)
			raw := callRemoteMCP(t, session, "submit_analysis", args, false)
			var job application.AnalysisJobView
			if err := json.Unmarshal([]byte(raw), &job); err != nil || job.ID == "" {
				t.Fatal("submission failed")
			}
			deadline := time.Now().Add(2 * time.Minute)
			for job.Status == model.JobRunning {
				if time.Now().After(deadline) {
					callRemoteMCP(t, session, "cancel_analysis", map[string]any{"namespace": "demo", "job_id": job.ID}, false)
					t.Fatal("real query exceeded acceptance deadline")
				}
				time.Sleep(time.Second)
				raw = callRemoteMCP(t, session, "get_analysis", map[string]any{"namespace": "demo", "job_id": job.ID}, false)
				if err := json.Unmarshal([]byte(raw), &job); err != nil {
					t.Fatal("result decoding failed")
				}
			}
			if mode == "missing_error" || mode == "duplicate_error" {
				wantCode := "analysis_duplicate_match"
				if mode == "missing_error" {
					wantCode = "analysis_missing_match"
				}
				if job.Status != model.JobFailed || job.Result != nil || job.ErrorCode != wantCode {
					t.Fatalf("relation must fail for its specific contract: status=%s code=%s", job.Status, job.ErrorCode)
				}
				t.Logf("%s rejected: %s", mode, job.ErrorCode)
				return
			}
			if job.Status != model.JobSucceeded || job.Result == nil {
				t.Fatalf("real analysis failed: status=%s code=%s", job.Status, job.ErrorCode)
			}
			if metricEngine.executionCount() != 0 || job.Plan == nil || job.Plan.Semantics == nil {
				t.Fatal("wrong execution path or missing semantics")
			}
			if mode == "missing_null" {
				if len(job.Result.Rows) != 2 {
					t.Fatal("counting grain changed")
				}
				for _, row := range job.Result.Rows {
					if row[1] != nil {
						t.Fatal("missing snapshot was substituted")
					}
				}
			} else {
				expected := [][]any{{3, "full", 2, 14, 8, 8.5, 1.0}}
				if mode == "temporary_condition" {
					expected = [][]any{{3, "full", 1, 7, 4, 12.0, .5}, {3, "partial", 1, 6, 3, 5.5, .5}}
				}
				if mode == "excluded_missing" {
					expected = [][]any{{3, "partial", 2, 2, 0, 11.5, 1.0}}
				}
				checkRealCalendarRows(t, job.Result.Rows, expected)
			}
			t.Logf("%s passed: %d rows; semantic and plan fingerprints pinned", mode, len(job.Result.Rows))
		})
	}
}

func realCalendarFixture(t *testing.T) explorationHTTPFixture {
	f := explorationTransportFixture(t)
	field := func(n, col string, typ model.DataType) exploration.BoundField {
		f := exploration.BoundField{Name: n, Column: col, Type: typ}
		if typ == model.DataTypeDate {
			f.CalendarTimezone = "UTC"
		}
		return f
	}
	resource := model.ResourceRef{Kind: model.ResourceTable, Catalog: "samples", Schema: "tpcds_sf1", Table: "date_dim"}
	fields := []exploration.BoundField{field("day", "d_date", model.DataTypeDate), field("key", "d_date_sk", model.DataTypeInteger), field("year", "d_year", model.DataTypeInteger), field("dom", "d_dom", model.DataTypeInteger), field("quarter", "d_qoy", model.DataTypeInteger)}
	f.catalog.Datasets = []exploration.Dataset{{Name: "calendar", Resource: resource, Fields: fields}, {Name: "snapshot", Resource: resource, Fields: fields}, {Name: "period", Resource: resource, Fields: fields}}
	f.catalog.Attributes = []exploration.Attribute{{Name: "day", Dataset: "calendar", Field: "day"}, {Name: "dom", Dataset: "calendar", Field: "dom"}, {Name: "snapshot_date", Dataset: "snapshot", Field: "day"}, {Name: "quarter", Dataset: "period", Field: "quarter"}}
	f.catalog.Relationships = []exploration.Relation{{Name: "week_end", From: "calendar", To: "snapshot", Keys: []exploration.JoinKey{{From: "year", To: "year"}}, Snapshot: &exploration.SnapshotRule{From: "day", To: "day", Anchor: "week_end"}, Missing: "preserve_null"}, {Name: "period", From: "snapshot", To: "period", Keys: []exploration.JoinKey{{From: "key", To: "key"}}, Missing: "preserve_null"}}
	f.catalog.Sources = []exploration.Source{{Name: "calendar_probe", Description: "Read-only public calendar SQL acceptance", Dataset: "calendar", Dimensions: []string{"day", "dom", "snapshot_date", "quarter"}, Roles: []string{"analyst"}, MaxWindowDays: 28, MaxRows: 100, Timeout: 2 * time.Minute}}
	ref := func(n string) exploration.Expression { return exploration.Expression{Op: "ref", Ref: n} }
	integer := func(n string) exploration.Expression {
		return exploration.Expression{Op: "literal", Type: model.DataTypeInteger, Value: n}
	}
	str := func(v string) exploration.Expression {
		return exploration.Expression{Op: "literal", Type: model.DataTypeString, Value: v}
	}
	f.query = exploration.Query{APIVersion: model.APIVersion, Kind: exploration.Kind, Source: "calendar_probe", GroupBy: []string{"day", "snapshot_date", "quarter"}, TimeRange: &model.TimeRange{Dimension: "day", Start: "1999-08-02T00:00:00Z", End: "1999-08-16T00:00:00Z", Timezone: "UTC"}, TimeGrouping: &model.TimeGrouping{Dimension: "day", Timezone: "UTC", Granularity: model.GrainWeek, WeekStart: model.WeekStartMonday}, Measures: []exploration.Measure{{Name: "days", Op: "count_distinct", Field: "day"}, {Name: "dom_total", Op: "sum", Field: "dom"}, {Name: "weekday_days", Op: "count_distinct", Field: "day", Where: &exploration.Expression{Op: "lte", Args: []exploration.Expression{{Op: "weekday", Args: []exploration.Expression{ref("day")}}, integer("3")}}}}, Calculations: []exploration.Calculation{{Name: "avg_dom", Expression: exploration.Expression{Op: "divide", Args: []exploration.Expression{ref("dom_total"), ref("days")}}}, {Name: "band", Expression: exploration.Expression{Op: "case", Args: []exploration.Expression{{Op: "eq", Args: []exploration.Expression{ref("days"), integer("7")}}, str("full"), str("partial")}}}}, Summarize: &exploration.Summary{GroupBy: []string{"quarter", "band"}, Measures: []exploration.Measure{{Name: "weeks", Op: "count"}, {Name: "total_days", Op: "sum", Field: "days"}, {Name: "total_weekdays", Op: "sum", Field: "weekday_days"}, {Name: "mean_dom", Op: "avg", Field: "avg_dom"}}, Calculations: []exploration.Calculation{{Name: "proportion", Expression: exploration.Expression{Op: "share", Args: []exploration.Expression{ref("weeks")}, PartitionBy: []string{"quarter"}}}}}, OrderBy: []model.OrderBy{{Field: "band", Direction: model.SortAscending}}, Limit: 100}
	return f
}

func checkRealCalendarRows(t *testing.T, got, want [][]any) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("row count %d != %d", len(got), len(want))
	}
	for i, row := range got {
		if len(row) != len(want[i]) {
			t.Fatal("schema width differs")
		}
		for j, v := range row {
			switch w := want[i][j].(type) {
			case string:
				if v != w {
					t.Fatalf("row %d column %d differs", i, j)
				}
			default:
				a, err := strconv.ParseFloat(fmt.Sprint(v), 64)
				b, _ := strconv.ParseFloat(fmt.Sprint(w), 64)
				if err != nil || math.Abs(a-b) > 1e-8 {
					t.Fatalf("row %d column %d: %v != %v", i, j, v, w)
				}
			}
		}
	}
}
