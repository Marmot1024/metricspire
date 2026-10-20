package onlineaggregate

import (
	"context"
	"errors"
	"fmt"
	"github.com/marmot1024/metricspire/internal/model"
	"math/big"
	"testing"
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
	if p, ok := err.(*model.Problem); !ok || p.Code != "budget_exceeded" {
		t.Fatal(err)
	}
}
