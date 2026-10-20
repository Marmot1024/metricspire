package clickhousequery

import (
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/model"
)

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
