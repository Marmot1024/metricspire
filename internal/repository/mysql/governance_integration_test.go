package mysql

import (
	"strings"
	"testing"

	"github.com/marmot1024/metricspire/internal/governance"
	"github.com/marmot1024/metricspire/internal/model"
)

func TestMySQLGovernanceImportFailureIsAtomic(t *testing.T) {
	s := isolatedMySQL(t)
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	svc, _ := governance.NewService(s)
	definition := governance.MetricDefinition{Code: "first", DisplayName: "Synthetic", Description: "Neutral fixture", Owner: "fixture", Status: "unverified", BusinessType: governance.BusinessDerived, SemanticReadiness: governance.ReadinessNeedsRemediation, AuthoritativeSource: governance.SourceReference{Reference: "fixture", Resource: "daily", Field: "value"}, ValueType: model.DataTypeDecimal, Verification: model.Verification{Status: model.VerificationUnverified}}
	other := definition
	other.Code = "second"
	// Fail during the second record's revision, after its current pointer and
	// the first record/revision/import item were already written.
	if _, err := s.DB().ExecContext(t.Context(), `CREATE TRIGGER reject_revision BEFORE INSERT ON metricspire_governance_record_revisions FOR EACH ROW BEGIN IF NEW.metric_code='second' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='private import detail'; END IF; END`); err != nil {
		t.Fatal(err)
	}
	input := governance.ImportInput{Namespace: "demo", Actor: "fixture", SourceFingerprint: "sha256:" + strings.Repeat("a", 64), Records: []governance.MetricDefinition{definition, other}}
	if _, err := svc.Import(t.Context(), input); err == nil || strings.Contains(err.Error(), "private import") {
		t.Fatal("import failure not propagated safely", err)
	}
	for _, table := range []string{"metricspire_governance_records", "metricspire_governance_record_revisions", "metricspire_governance_imports", "metricspire_governance_import_items"} {
		var n int
		if err := s.DB().QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != 0 {
			t.Fatal("partial import persisted", table, err)
		}
	}
	if _, err := s.DB().ExecContext(t.Context(), `DROP TRIGGER reject_revision`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Import(t.Context(), input); err != nil {
		t.Fatal("retry after rollback failed", err)
	}
}
