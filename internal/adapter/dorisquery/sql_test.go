package dorisquery

import (
	"strings"
	"testing"
)

func TestCompileBindsValuesAndRefusesIncompatibleColumns(t *testing.T) {
	_, _, p := fixture(t, "serving")
	types := map[string]string{"row_key": "varchar(255)", "day": "date", "region": "varchar(255)", "revenue": "decimal(18,6)", "orders": "bigint"}
	tenant := "tenant' OR 1=1 --"
	s, err := compile(p, tenant, types)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s.sql, tenant) || s.args[0] != tenant || !strings.Contains(s.sql, " LIMIT 11") {
		t.Fatal("values escaped parameter binding or lost overflow sentinel")
	}
	for _, tc := range []struct{ column, typ string }{{"revenue", "double"}, {"revenue", "decimal(38,9)"}, {"orders", "decimal(18,6)"}, {"day", "datetime(3)"}} {
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
