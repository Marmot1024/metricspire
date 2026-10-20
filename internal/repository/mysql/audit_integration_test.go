package mysql

import (
	"strings"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/audit"
)

func TestMySQLAuditDurabilityUniquenessAndFailure(t *testing.T) {
	s := isolatedMySQL(t)
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	e := audit.QueryEvent{RequestID: "synthetic-request", Tenant: "demo", Principal: "synthetic", Namespace: "demo", ModelName: "orders", ReleaseID: "release", ManifestFingerprint: "sha256:" + strings.Repeat("a", 64), LogicalFingerprint: "sha256:" + strings.Repeat("b", 64), PhysicalFingerprint: "sha256:" + strings.Repeat("c", 64), JobID: "job", Kind: audit.EventQueryStarted, OccurredAt: time.Now().UTC()}
	if err := s.Record(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if s.Record(t.Context(), e) == nil {
		t.Fatal("duplicate start audit accepted")
	}
	e.Kind = audit.EventQuerySucceeded
	e.RowCount = 3
	if err := s.Record(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM metricspire_query_audit WHERE request_id=?`, e.RequestID).Scan(&count); err != nil || count != 2 {
		t.Fatal("paired audit missing", err)
	}
	if _, err := s.DB().ExecContext(t.Context(), `CREATE TRIGGER reject_audit BEFORE INSERT ON metricspire_query_audit FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='private SQL credential'`); err != nil {
		t.Fatal(err)
	}
	e.RequestID = "failed-request"
	if err := s.Record(t.Context(), e); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("audit failure not propagated safely", err)
	}
}
