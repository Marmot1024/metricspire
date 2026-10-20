package clickhousequery

import (
	"strings"
	"testing"
)

func TestCompileBindsValuesAndRefusesIncompatibleColumns(t *testing.T) {
	_, _, p := fixture(t, "serving")
	types := map[string]string{"row_key": "String", "day": "Date", "region": "String", "revenue": "Nullable(Decimal(18, 6))", "orders": "Int64"}
	tenant := "tenant' OR 1=1 --"
	s, err := compile(p, tenant, types)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s.sql, tenant) || s.params["p0"] != tenant || !strings.Contains(s.sql, " LIMIT 11") {
		t.Fatal("values escaped parameter binding or lost overflow sentinel")
	}
	for _, tc := range []struct{ column, typ string }{{"revenue", "Float64"}, {"revenue", "Decimal(38, 9)"}, {"orders", "Decimal(18, 6)"}, {"day", "DateTime"}} {
		original := types[tc.column]
		types[tc.column] = tc.typ
		_, err := compile(p, tenant, types)
		requireProblem(t, err, "unsupported_online_query")
		types[tc.column] = original
	}
	p.Limit = 999
	if _, err := compile(p, tenant, types); err == nil {
		t.Fatal("mutated plan accepted")
	}
}
