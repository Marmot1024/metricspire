package trinoquery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/exploration"
	"github.com/marmot1024/metricspire/internal/model"
)

func fixture(t *testing.T, driver string) (exploration.Catalog, exploration.Query, model.RequestContext) {
	t.Helper()
	var c exploration.Catalog
	if err := contractio.ReadFile("../../../testdata/trino/catalog.json", &c); err != nil {
		t.Fatal(err)
	}
	c.Engine = driver
	q := exploration.Query{APIVersion: model.APIVersion, Kind: exploration.Kind, Source: "shipments", GroupBy: []string{"flag"}, TimeRange: &model.TimeRange{Dimension: "day", Start: "1995-01-01T00:00:00Z", End: "1996-01-01T00:00:00Z", Timezone: "UTC"}, Measures: []exploration.Measure{{Name: "items", Op: "count"}, {Name: "order_sum", Op: "sum", Field: "order"}}, OrderBy: []model.OrderBy{{Field: "flag", Direction: model.SortAscending}}, Limit: 100}
	return c, q, model.RequestContext{Tenant: "demo", Principal: "synthetic", Roles: []string{"analyst"}, RequestID: "synthetic"}
}
func plan(t *testing.T, driver string) exploration.Plan {
	t.Helper()
	c, q, rc := fixture(t, driver)
	p, err := exploration.PlanCatalog(c, q, rc)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPagedProtocolAndFailures(t *testing.T) {
	for _, mode := range []string{"success", "foreign", "redirect", "late_error", "columns", "overflow", "cancel", "trailing", "decimal_float"} {
		t.Run(mode, func(t *testing.T) {
			cancelled := make(chan struct{}, 1)
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					cancelled <- struct{}{}
					w.WriteHeader(204)
					return
				}
				if r.Header.Get("X-Trino-User") != "reader" || r.Header.Get("Authorization") != "" {
					t.Error("identity or secret forwarded incorrectly")
				}
				if r.Method == http.MethodPost {
					sql, _ := io.ReadAll(r.Body)
					if !strings.Contains(string(sql), `"tpch"."sf1"."lineitem"`) || !strings.HasSuffix(string(sql), "LIMIT 100") {
						t.Error("unbounded compiler")
					}
					next := server.URL + "/v1/statement/query/1"
					if mode == "foreign" {
						next = "https://evil.example/v1/statement/query/1"
					}
					if mode == "redirect" {
						http.Redirect(w, r, server.URL+"/leak", 302)
						return
					}
					fmt.Fprintf(w, `{"id":"q","nextUri":%q}`, next)
					return
				}
				if mode == "cancel" {
					<-r.Context().Done()
					return
				}
				if mode == "late_error" {
					fmt.Fprint(w, `{"id":"q","error":{"message":"SQL private secret"}}`)
					return
				}
				names := `[{"name":"flag","type":"varchar(1)"},{"name":"items","type":"bigint"},{"name":"order_sum","type":"bigint"}]`
				if mode == "columns" {
					names = `[{"name":"private","type":"varchar"}]`
				}
				if mode == "decimal_float" {
					names = strings.Replace(names, `"bigint"`, `"double"`, 1)
				}
				rows := `[["A",9007199254740993,9007199254740994]]`
				if mode == "overflow" {
					rows = `[["A",9223372036854775808,"1"]]`
				}
				fmt.Fprintf(w, `{"id":"q","columns":%s,"data":%s}`, names, rows)
				if mode == "trailing" {
					fmt.Fprint(w, `{}`)
				}
			}))
			defer server.Close()
			e, err := New(Config{Driver: Trino, Connection: server.URL, User: "reader"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, stop := context.WithTimeout(t.Context(), 150*time.Millisecond)
			defer stop()
			out, err := e.Execute(ctx, plan(t, Trino))
			if mode == "success" {
				if err != nil || out.Result.Rows[0][1] != int64(9007199254740993) || out.Result.Rows[0][2] != int64(9007199254740994) {
					t.Fatal("exact result lost", err)
				}
			} else {
				if err == nil || strings.Contains(err.Error(), "SQL private secret") {
					t.Fatal("failed response exposed or accepted", err)
				}
			}
			if mode == "cancel" {
				select {
				case <-cancelled:
				case <-time.After(time.Second):
					t.Fatal("server cancellation missing")
				}
			}
		})
	}
}

func TestCatalogAndCompilerBoundaries(t *testing.T) {
	c, q, rc := fixture(t, Trino)
	for _, driver := range []string{Trino, Presto} {
		c.Engine = driver
		if err := ValidateCatalog(c, driver); err != nil {
			t.Fatal(err)
		}
		p, err := exploration.PlanCatalog(c, q, rc)
		if err != nil {
			t.Fatal(err)
		}
		sql, err := Compile(p, driver)
		if err != nil || !strings.Contains(sql, "NULLS FIRST") {
			t.Fatal(err)
		}
		p.Query.Limit++
		if _, err := Compile(p, driver); err == nil {
			t.Fatal("tampered plan accepted")
		}
	}
	for _, s := range []string{"http://reader:secret@127.0.0.1:8080", "http://remote.example", "https://remote.example/?secret=x", "https://remote.example/path"} {
		if _, err := New(Config{Driver: Trino, Connection: s, User: "reader"}); err == nil {
			t.Fatal("unsafe endpoint accepted")
		}
	}
	rc.Roles = nil
	if _, err := exploration.PlanCatalog(c, q, rc); err == nil {
		t.Fatal("unauthorized plan accepted")
	}
}

// The public TPCH connector generates ~6M rows at sf1; no business data is read.
func TestRealTPCH(t *testing.T) {
	for _, driver := range []string{Trino, Presto} {
		t.Run(driver, func(t *testing.T) {
			raw := os.Getenv("METRICSPIRE_" + strings.ToUpper(driver) + "_TEST_URL")
			if raw == "" {
				if os.Getenv("METRICSPIRE_REQUIRE_"+strings.ToUpper(driver)+"_TESTS") == "1" {
					t.Fatal("engine URL required")
				}
				t.Skip("requires disposable TPCH engine")
			}
			e, err := New(Config{Driver: driver, Connection: raw, User: "synthetic_reader"})
			if err != nil {
				t.Fatal(err)
			}
			c, q, rc := fixture(t, driver)
			if err = ValidateCatalog(c, driver); err != nil {
				t.Fatal(err)
			}
			p, err := exploration.PlanCatalog(c, q, rc)
			if err != nil {
				t.Fatal(err)
			}
			got, err := e.Execute(t.Context(), p)
			if err != nil {
				t.Fatal(err)
			}
			// Independently written SQL oracle exercises the protocol directly, not Compile.
			u := *e.endpoint
			u.Path = "/v1/statement"
			method, body := http.MethodPost, `SELECT returnflag, count(*), sum(orderkey) FROM tpch.sf1.lineitem WHERE shipdate >= DATE '1995-01-01' AND shipdate < DATE '1996-01-01' GROUP BY returnflag ORDER BY returnflag`
			oracle := [][]any{}
			for i := 0; i < 1000; i++ {
				r, err := e.request(t.Context(), method, &u, body, "")
				if err != nil {
					t.Fatal(err)
				}
				var pg page
				d := json.NewDecoder(r.Body)
				d.UseNumber()
				err = d.Decode(&pg)
				r.Body.Close()
				if err != nil || len(pg.Error) > 0 {
					t.Fatal("oracle failed", err)
				}
				for _, row := range pg.Data {
					n, err := scalar(row[1], model.DataTypeInteger)
					if err != nil {
						t.Fatal(err)
					}
					v, err := scalar(row[2], model.DataTypeInteger)
					if err != nil {
						t.Fatal(err)
					}
					oracle = append(oracle, []any{row[0], n, v})
				}
				if pg.Next == "" {
					break
				}
				next, err := e.next(pg.Next)
				if err != nil {
					t.Fatal(err)
				}
				u = *next
				method, body = http.MethodGet, ""
			}
			a, _ := json.Marshal(got.Result.Rows)
			b, _ := json.Marshal(oracle)
			if string(a) != string(b) || len(oracle) != 3 {
				t.Fatalf("oracle mismatch: got %s want %s", a, b)
			}
			q.Limit = 1
			p, err = exploration.PlanCatalog(c, q, rc)
			if err != nil {
				t.Fatal(err)
			}
			limited, err := e.Execute(t.Context(), p)
			if err != nil || len(limited.Result.Rows) != 1 {
				t.Fatal("limit", err)
			}
			// Exact decimal average and a final-stage denominator run on real engines.
			q.Limit = 100
			q.Measures = append(q.Measures, exploration.Measure{Name: "average", Op: "avg", Field: "order"})
			q.Calculations = []exploration.Calculation{{Name: "share", Expression: exploration.Expression{Op: "share", Args: []exploration.Expression{{Op: "ref", Ref: "items"}}}}}
			p, err = exploration.PlanCatalog(c, q, rc)
			if err != nil {
				t.Fatal(err)
			}
			ratios, err := e.Execute(t.Context(), p)
			if err != nil {
				t.Fatal(err)
			}
			var total int64
			for _, row := range oracle {
				total += row[1].(int64)
			}
			for i, row := range ratios.Result.Rows {
				average, _ := new(big.Rat).SetString(row[3].(string))
				wantAvg := big.NewRat(oracle[i][2].(int64), oracle[i][1].(int64))
				delta := new(big.Rat).Sub(average, wantAvg)
				delta.Abs(delta)
				if delta.Cmp(big.NewRat(1, 1000000)) > 0 {
					t.Fatal("decimal average lost precision")
				}
				share, _ := new(big.Rat).SetString(row[4].(string))
				wantShare := big.NewRat(oracle[i][1].(int64), total)
				delta.Sub(share, wantShare)
				delta.Abs(delta)
				if delta.Cmp(big.NewRat(1, 1000000)) > 0 {
					t.Fatal("incorrect final window denominator")
				}
			}
			q.Calculations = nil
			q.Measures = q.Measures[:2]
			q.GroupBy = []string{"day", "flag"}
			q.Summarize = &exploration.Summary{GroupBy: []string{"flag"}, Measures: []exploration.Measure{{Name: "items", Op: "sum", Field: "items"}, {Name: "order_sum", Op: "sum", Field: "order_sum"}}}
			p, err = exploration.PlanCatalog(c, q, rc)
			if err != nil {
				t.Fatal(err)
			}
			two, err := e.Execute(t.Context(), p)
			if err != nil {
				t.Fatal(err)
			}
			a, _ = json.Marshal(two.Result.Rows)
			if string(a) != string(b) {
				t.Fatal("two-stage aggregation differs from oracle")
			}
			// High-scale values and mixed CASE branches must survive an AVG stage.
			lit := func(value string, typ model.DataType) exploration.Expression {
				return exploration.Expression{Op: "literal", Type: typ, Value: value}
			}
			ref := func(name string) exploration.Expression { return exploration.Expression{Op: "ref", Ref: name} }
			tiny := exploration.Expression{Op: "multiply", Args: []exploration.Expression{lit("0.0000000001", model.DataTypeDecimal), lit("0.0000000001", model.DataTypeDecimal)}}
			condition := exploration.Expression{Op: "eq", Args: []exploration.Expression{ref("flag"), lit("unmatched", model.DataTypeString)}}
			cases := []exploration.Expression{
				{Op: "case", Args: []exploration.Expression{condition, lit("0", model.DataTypeInteger), tiny}},
				{Op: "case", Args: []exploration.Expression{condition, lit("0", model.DataTypeInteger), condition, tiny, tiny}},
				{Op: "multiply", Args: []exploration.Expression{lit("0.0000000001", model.DataTypeDecimal), lit("0.0000000001", model.DataTypeDecimal)}},
			}
			huge := exploration.Expression{Op: "multiply", Args: []exploration.Expression{lit("10000000000", model.DataTypeDecimal), lit("10000000000", model.DataTypeDecimal)}}
			cases = append(cases, exploration.Expression{Op: "case", Args: []exploration.Expression{condition, lit("0", model.DataTypeInteger), huge}})
			for index, expression := range cases {
				q.GroupBy = []string{"flag"}
				q.Measures = []exploration.Measure{{Name: "items", Op: "count"}}
				q.Calculations = []exploration.Calculation{{Name: "tiny", Expression: expression}}
				q.Summarize = &exploration.Summary{Measures: []exploration.Measure{{Name: "average", Op: "avg", Field: "tiny"}}, Calculations: []exploration.Calculation{{Name: "ratio", Expression: exploration.Expression{Op: "divide", Args: []exploration.Expression{ref("average"), lit("1", model.DataTypeInteger)}}}}}
				q.OrderBy = nil
				p, err = exploration.PlanCatalog(c, q, rc)
				if err != nil {
					t.Fatal(err)
				}
				exact, err := e.Execute(t.Context(), p)
				if err != nil {
					t.Fatal(err)
				}
				wantValue := "0.00000000000000000001"
				if index == len(cases)-1 {
					wantValue = "100000000000000000000"
				}
				wantExact, _ := new(big.Rat).SetString(wantValue)
				for _, value := range exact.Result.Rows[0] {
					r, ok := new(big.Rat).SetString(value.(string))
					if !ok || r.Cmp(wantExact) != 0 {
						t.Fatalf("high-scale result changed: %v", value)
					}
				}
			}
			q.Summarize = nil
			q.Calculations = []exploration.Calculation{{Name: "tiny", Expression: tiny}, {Name: "share", Expression: exploration.Expression{Op: "share", Args: []exploration.Expression{ref("tiny")}}}}
			p, err = exploration.PlanCatalog(c, q, rc)
			if err != nil {
				t.Fatal(err)
			}
			exact, err := e.Execute(t.Context(), p)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range exact.Result.Rows {
				if row[3] != "0.333333333333333333" {
					t.Fatalf("high-scale share changed: %v", row[3])
				}
			}
			t.Logf("%s public TPCH sf1 (~6M generated rows), exact grouped count/sum and final limit passed", driver)
		})
	}
}
