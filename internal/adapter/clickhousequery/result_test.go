package clickhousequery

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/model"
)

func TestArithmeticMemoBudgetCancellationAndNulls(t *testing.T) {
	metrics := map[string]model.PlannedMetric{"m0": {Name: "m0", Expression: model.Expression{Op: model.OpLiteral, Value: "1"}}}
	for i := 1; i <= 14; i++ {
		name, dep := fmt.Sprintf("m%d", i), fmt.Sprintf("m%d", i-1)
		ref := model.Expression{Op: model.OpMetric, Metric: dep}
		metrics[name] = model.PlannedMetric{Name: name, Expression: model.Expression{Op: model.OpAdd, Args: []model.Expression{ref, ref}}}
	}
	ops := 100
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e := evaluator{ctx: ctx, metrics: metrics, sums: map[string]*big.Rat{}, memo: map[string]*big.Rat{}, stack: map[string]bool{}, operations: &ops}
	v, err := e.evaluate(model.Expression{Op: model.OpMetric, Metric: "m14"}, 0)
	if err != nil || v.Cmp(big.NewRat(16384, 1)) != 0 || ops < 40 {
		t.Fatalf("unbounded recomputation: ops=%d error=%v", ops, err)
	}
	zero := model.Expression{Op: model.OpLiteral, Value: "0"}
	division := model.Expression{Op: model.OpDivide, Args: []model.Expression{{Op: model.OpLiteral, Value: "1"}, zero}}
	v, err = e.evaluate(division, 0)
	if err != nil || v != nil {
		t.Fatal("division by zero must be NULL")
	}
	cancel()
	_, err = e.evaluate(division, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("ignored local cancellation")
	}
	e.ctx = t.Context()
	ops = 0
	_, err = e.evaluate(zero, 0)
	requireProblem(t, err, "budget_exceeded")
}
func TestExactSortPrecedesRoundingAndPreservesNull(t *testing.T) {
	_, _, p := fixture(t, "serving")
	p.Dimensions = []model.PhysicalDimension{{Name: "region", DataType: model.DataTypeString, Output: true}}
	p.Metrics = []model.PlannedMetric{{Name: "ratio", ValueType: model.DataTypeDecimal, Output: true, Expression: model.Expression{Op: model.OpDivide, Args: []model.Expression{{Op: model.OpSum, Field: "sales.revenue"}, {Op: model.OpSum, Field: "sales.orders"}}}}}
	p.OrderBy = []model.OrderBy{{Field: "ratio", Direction: model.SortAscending}, {Field: "region", Direction: model.SortAscending}}
	s := statement{dimensions: p.Dimensions, fields: []model.PhysicalField{{Entity: "sales", Field: "revenue"}, {Entity: "sales", Field: "orders"}}}
	str := func(v string) *string { return &v }
	row := func(region string, revenue *string) []*string {
		return []*string{str(region), revenue, str("10000000000000"), str("1"), str("batch"), str("batch"), str("1000"), str("1000"), str("contract"), str("contract")}
	}
	e := QueryEngine{config: Config{MaxDataAge: time.Minute, MaxBytes: 4096}, now: func() time.Time { return time.Unix(2, 0) }}
	result, err := e.result(t.Context(), p, s, [][]*string{row("a", str("2")), row("z", str("1")), row("null", nil)}, "contract")
	if err != nil {
		t.Fatal(err)
	}
	if result.Result.Rows[0][0] != "z" || result.Result.Rows[1][0] != "a" || result.Result.Rows[0][1] != "0" || result.Result.Rows[2][1] != nil {
		t.Fatalf("lost exact ordering/null: %v", result.Result.Rows)
	}
}
func TestPhysicalNumericTypesCannotTruncateOrOverflow(t *testing.T) {
	for _, s := range []string{"Decimal(18, 6)", "Decimal(9, 2)", "Nullable(Decimal(18, 6))", "UInt64", "Int64"} {
		if !supportedNumeric(s) {
			t.Fatalf("valid numeric %s", s)
		}
	}
	for _, s := range []string{"Float64", "Decimal(38, 6)", "Decimal(18, 9)", "UInt128", "String"} {
		if supportedNumeric(s) {
			t.Fatalf("unsafe numeric %s", s)
		}
	}
}
