package postgres

import "testing"

func TestSchemaNamePatternKeepsDedicatedSchemaIdentifierSafe(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"metricspire", "metricspire_phase3_staging", "m1"} {
		if err := ValidateSchemaName(value); err != nil {
			t.Fatalf("valid schema %q was rejected", value)
		}
	}
	for _, value := range []string{"", "Public", "1metricspire", "metric-spire", "metricspire;drop schema public", "metricspire.schema"} {
		if err := ValidateSchemaName(value); err == nil {
			t.Fatalf("unsafe schema %q was accepted", value)
		}
	}
}
