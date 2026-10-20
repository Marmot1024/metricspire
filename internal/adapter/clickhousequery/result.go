package clickhousequery

import (
	"context"
	"encoding/json"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/model"
)

var decimal = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

func number(s string) (*big.Rat, error) {
	if len(s) > 128 || !decimal.MatchString(s) {
		return nil, problem("online_data_unavailable", "invalid numeric value")
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, problem("online_data_unavailable", "invalid numeric value")
	}
	return r, nil
}
func scalar(v *string, t model.DataType) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch t {
	case model.DataTypeInteger:
		n, err := strconv.ParseInt(*v, 10, 64)
		return n, err
	case model.DataTypeBoolean:
		n, err := strconv.ParseBool(*v)
		return n, err
	case model.DataTypeDate:
		_, err := time.Parse(time.DateOnly, *v)
		return *v, err
	default:
		return *v, nil
	}
}
func column(name string, t model.DataType) model.ResultColumn {
	kind := map[model.DataType]string{model.DataTypeInteger: "LONG", model.DataTypeDecimal: "DECIMAL", model.DataTypeDate: "DATE", model.DataTypeBoolean: "BOOLEAN", model.DataTypeString: "STRING"}[t]
	return model.ResultColumn{Name: name, TypeName: kind, TypeText: kind}
}

func (e *QueryEngine) result(ctx context.Context, p model.PhysicalPlan, s statement, rows [][]*string, contract string) (model.ExecutionSnapshot, error) {
	fail := func() (model.ExecutionSnapshot, error) {
		return model.ExecutionSnapshot{}, problem("online_data_unavailable", "online snapshot is empty, incomplete or incompatible")
	}
	if len(rows) == 0 {
		return fail()
	}
	if len(rows) > p.Limit {
		return model.ExecutionSnapshot{}, problem("online_result_too_large", "result exceeds row budget; narrow the request")
	}
	result := &model.TypedResult{Columns: []model.ResultColumn{}, Rows: [][]any{}}
	types := []model.DataType{}
	metrics := map[string]model.PlannedMetric{}
	for _, d := range s.dimensions {
		result.Columns = append(result.Columns, column(d.Name, d.DataType))
		types = append(types, d.DataType)
	}
	for _, m := range p.Metrics {
		metrics[m.Name] = m
		if m.Output {
			if m.ValueType != model.DataTypeInteger && m.ValueType != model.DataTypeDecimal {
				return fail()
			}
			result.Columns = append(result.Columns, column(m.Name, m.ValueType))
			types = append(types, m.ValueType)
		}
	}
	var snapshot *model.DataSnapshot
	used := 0
	type exactRow struct{ values, keys []any }
	exactRows := []exactRow{}
	operations := 100000
	for _, raw := range rows {
		if err := ctx.Err(); err != nil {
			return model.ExecutionSnapshot{}, err
		}
		n := len(s.dimensions) + len(s.fields)
		if len(raw) != n+7 {
			return fail()
		}
		meta := raw[n:]
		count, err := strconv.ParseInt(text(meta[0]), 10, 64)
		if err != nil || count < 1 || count > 50000000 || text(meta[1]) == "" || text(meta[1]) != text(meta[2]) || text(meta[3]) != text(meta[4]) || text(meta[5]) != contract || text(meta[5]) != text(meta[6]) {
			return fail()
		}
		ms, err := strconv.ParseInt(text(meta[3]), 10, 64)
		if err != nil {
			return fail()
		}
		asOf := time.UnixMilli(ms).UTC()
		if asOf.After(e.now().Add(time.Second)) || e.now().Sub(asOf) > e.config.MaxDataAge {
			return model.ExecutionSnapshot{}, problem("online_data_stale", "online data is stale or has an invalid coverage time")
		}
		if snapshot == nil {
			snapshot = &model.DataSnapshot{BatchID: text(meta[1]), DataAsOf: asOf}
		} else if snapshot.BatchID != text(meta[1]) || !snapshot.DataAsOf.Equal(asOf) {
			return fail()
		}
		row := []any{}
		for i, d := range s.dimensions {
			v, err := scalar(raw[i], d.DataType)
			if err != nil {
				return fail()
			}
			row = append(row, v)
		}
		sums := map[string]*big.Rat{}
		for i, f := range s.fields {
			var v *big.Rat
			if raw[len(s.dimensions)+i] != nil {
				v, err = number(*raw[len(s.dimensions)+i])
				if err != nil {
					return fail()
				}
			}
			sums[f.Entity+"."+f.Field] = v
		}
		keys := append([]any(nil), row...)
		eval := evaluator{ctx: ctx, metrics: metrics, sums: sums, memo: map[string]*big.Rat{}, stack: map[string]bool{}, operations: &operations}
		for _, m := range p.Metrics {
			if !m.Output {
				continue
			}
			v, err := eval.evaluate(model.Expression{Op: model.OpMetric, Metric: m.Name}, 0)
			if err != nil {
				return model.ExecutionSnapshot{}, err
			}
			keys = append(keys, v)
			if v == nil {
				keys[len(keys)-1] = nil
				row = append(row, nil)
				continue
			}
			if m.ValueType == model.DataTypeInteger {
				if !v.IsInt() || !v.Num().IsInt64() {
					return fail()
				}
				row = append(row, v.Num().Int64())
			} else {
				value := v.FloatString(12)
				value = strings.TrimRight(strings.TrimRight(value, "0"), ".")
				if value == "-0" {
					value = "0"
				}
				if len(value) > 128 {
					return fail()
				}
				row = append(row, value)
			}
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			return fail()
		}
		used += len(encoded)
		if used > e.config.MaxBytes {
			return model.ExecutionSnapshot{}, problem("online_result_too_large", "result exceeds byte budget")
		}
		exactRows = append(exactRows, exactRow{values: row, keys: keys})
	}
	for _, order := range p.OrderBy {
		found := false
		for _, col := range result.Columns {
			found = found || col.Name == order.Field
		}
		if !found || (order.Direction != model.SortAscending && order.Direction != model.SortDescending) {
			return fail()
		}
	}
	sort.SliceStable(exactRows, func(i, j int) bool {
		for _, order := range p.OrderBy {
			for k, col := range result.Columns {
				if col.Name != order.Field {
					continue
				}
				comparison := compare(exactRows[i].keys[k], exactRows[j].keys[k], types[k])
				if comparison != 0 {
					if order.Direction == model.SortDescending {
						return comparison > 0
					}
					return comparison < 0
				}
			}
		}
		return false
	})
	if err := ctx.Err(); err != nil {
		return model.ExecutionSnapshot{}, err
	}
	for _, row := range exactRows {
		result.Rows = append(result.Rows, row.values)
	}
	now := e.now().UTC()
	return model.ExecutionSnapshot{Job: model.ExecutionJob{Status: model.JobSucceeded, SubmittedAt: now, FinishedAt: &now, PhysicalFingerprint: p.Fingerprint}, Result: result, DataSnapshot: snapshot}, nil
}

type evaluator struct {
	ctx        context.Context
	metrics    map[string]model.PlannedMetric
	sums, memo map[string]*big.Rat
	stack      map[string]bool
	operations *int
}

func (e *evaluator) evaluate(x model.Expression, depth int) (*big.Rat, error) {
	bad := func() (*big.Rat, error) {
		return nil, problem("unsupported_online_query", "unsupported or oversized online arithmetic")
	}
	if err := e.ctx.Err(); err != nil {
		return nil, err
	}
	*e.operations--
	if *e.operations < 0 {
		return nil, problem("budget_exceeded", "online arithmetic operation budget exceeded")
	}
	if depth > 32 || len(x.Filters) != 0 {
		return bad()
	}
	switch x.Op {
	case model.OpSum:
		v, ok := e.sums[x.Field]
		if !ok {
			return bad()
		}
		return v, nil
	case model.OpLiteral:
		return number(x.Value)
	case model.OpMetric:
		if v, ok := e.memo[x.Metric]; ok {
			return v, nil
		}
		m, ok := e.metrics[x.Metric]
		if !ok || e.stack[x.Metric] {
			return bad()
		}
		e.stack[x.Metric] = true
		defer delete(e.stack, x.Metric)
		v, err := e.evaluate(m.Expression, depth+1)
		if err == nil {
			e.memo[x.Metric] = v
		}
		return v, err
	case model.OpAdd, model.OpSubtract, model.OpMultiply, model.OpDivide:
		if len(x.Args) != 2 {
			return bad()
		}
		left, err := e.evaluate(x.Args[0], depth+1)
		if err != nil {
			return nil, err
		}
		right, err := e.evaluate(x.Args[1], depth+1)
		if err != nil {
			return nil, err
		}
		if left == nil || right == nil {
			return nil, nil
		}
		v := new(big.Rat)
		switch x.Op {
		case model.OpAdd:
			v.Add(left, right)
		case model.OpSubtract:
			v.Sub(left, right)
		case model.OpMultiply:
			v.Mul(left, right)
		case model.OpDivide:
			if right.Sign() == 0 {
				return nil, nil
			}
			v.Quo(left, right)
		}
		if v.Num().BitLen() > 1024 || v.Denom().BitLen() > 1024 {
			return bad()
		}
		return v, nil
	default:
		return bad()
	}
}
func compare(a, b any, t model.DataType) int {
	if a == nil {
		if b == nil {
			return 0
		}
		return 1
	}
	if b == nil {
		return -1
	}
	if x, ok := a.(*big.Rat); ok {
		return x.Cmp(b.(*big.Rat))
	}
	switch t {
	case model.DataTypeInteger:
		x, y := a.(int64), b.(int64)
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
		return 0
	case model.DataTypeDecimal:
		x, _ := number(a.(string))
		y, _ := number(b.(string))
		return x.Cmp(y)
	case model.DataTypeBoolean:
		if a == b {
			return 0
		}
		if a.(bool) {
			return 1
		}
		return -1
	default:
		return strings.Compare(a.(string), b.(string))
	}
}
