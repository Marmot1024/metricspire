package postgres_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marmot1024/metricspire/internal/repository/postgres"
)

// Install the original schema and its checksum to exercise an actual upgrade,
// rather than calling the current migrator twice on an already-current schema.
func installOriginalSchema(t *testing.T, store *postgres.Store) {
	t.Helper()
	sql, err := os.ReadFile(filepath.Join("migrations", "001_catalog.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool().Exec(t.Context(), string(sql)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool().Exec(t.Context(), `CREATE TABLE metricspire_schema_migrations (
version TEXT PRIMARY KEY, checksum TEXT NOT NULL CHECK (checksum ~ '^[0-9a-f]{64}$'),
applied_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp())`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool().Exec(t.Context(), `INSERT INTO metricspire_schema_migrations (version, checksum) VALUES ($1, $2)`, "001_catalog.sql", fmt.Sprintf("%x", sha256.Sum256(sql))); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresMigrationUpgradePreservesHistory(t *testing.T) {
	store := isolatedPostgres(t)
	installOriginalSchema(t, store)
	for index, note := range []string{"reviewed release", "STAGING TEST RELEASE - business definitions unverified: synthetic fixture"} {
		id := fmt.Sprintf("release_%d", index)
		if _, err := store.Pool().Exec(t.Context(), `INSERT INTO metricspire_releases
(namespace, model_name, release_id, source_revision, manifest_fingerprint, manifest, created_by, note)
VALUES ('demo', 'orders', $1, $2, $3, '{"fixture":"preserve"}', 'test', $4)`, id, index+1, "sha256:"+strings.Repeat(fmt.Sprint(index), 64), note); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Pool().Exec(t.Context(), `
INSERT INTO metricspire_active_releases VALUES ('demo', 'orders', 'release_0', 'test', clock_timestamp());
INSERT INTO metricspire_release_events (namespace, model_name, event_kind, to_release_id, actor)
VALUES ('demo', 'orders', 'published', 'release_0', 'test')`); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(t.Context(), store.Pool()); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(t.Context(), store.Pool()); err != nil {
		t.Fatalf("idempotent upgraded schema: %v", err)
	}
	for index, want := range []string{"certified", "trial"} {
		var channel, fixture string
		if err := store.Pool().QueryRow(t.Context(), `SELECT channel, manifest->>'fixture' FROM metricspire_releases WHERE release_id=$1`, fmt.Sprintf("release_%d", index)).Scan(&channel, &fixture); err != nil || channel != want || fixture != "preserve" {
			t.Fatalf("upgraded release: channel=%q fixture=%q err=%v", channel, fixture, err)
		}
	}
	var active string
	if err := store.Pool().QueryRow(t.Context(), `SELECT release_id FROM metricspire_active_releases`).Scan(&active); err != nil || active != "release_0" {
		t.Fatalf("upgrade lost active release: %q, %v", active, err)
	}
	events, err := store.ListEvents(t.Context(), "demo", "orders")
	if err != nil || len(events) != 1 || events[0].ToReleaseID != "release_0" {
		t.Fatalf("upgrade lost history: %v, %v", events, err)
	}
	files, err := filepath.Glob(filepath.Join("migrations", "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.Pool().QueryRow(t.Context(), `SELECT count(*) FROM metricspire_schema_migrations`).Scan(&count); err != nil || count != len(files) {
		t.Fatalf("applied migration count=%d want=%d err=%v", count, len(files), err)
	}
}

func TestPostgresMigrationFailureRollsBackAllPendingChanges(t *testing.T) {
	store := isolatedPostgres(t)
	installOriginalSchema(t, store)
	// Migration 002 would succeed, but 003 must fail on this deliberate conflict.
	if _, err := store.Pool().Exec(t.Context(), `CREATE TABLE metricspire_governance_imports (fixture TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(t.Context(), store.Pool()); err == nil || !strings.Contains(err.Error(), "003_governance_records.sql") {
		t.Fatalf("expected migration failure, got %v", err)
	}
	var count int
	var auditAbsent bool
	if err := store.Pool().QueryRow(t.Context(), `SELECT count(*) FROM metricspire_schema_migrations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("partial migration registry: count=%d err=%v", count, err)
	}
	if err := store.Pool().QueryRow(t.Context(), `SELECT to_regclass('metricspire_query_audit') IS NULL`).Scan(&auditAbsent); err != nil || !auditAbsent {
		t.Fatalf("pending DDL was not rolled back: absent=%v err=%v", auditAbsent, err)
	}
	if _, err := store.Pool().Exec(t.Context(), `DROP TABLE metricspire_governance_imports`); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(t.Context(), store.Pool()); err != nil {
		t.Fatalf("migration did not recover after removing fixture conflict: %v", err)
	}
}

func TestPostgresMigrationRejectsAppliedChecksumDrift(t *testing.T) {
	store := isolatedPostgres(t)
	installOriginalSchema(t, store)
	if _, err := store.Pool().Exec(t.Context(), `UPDATE metricspire_schema_migrations SET checksum=$1`, strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(t.Context(), store.Pool()); err == nil || !strings.Contains(err.Error(), "checksum changed") {
		t.Fatalf("expected checksum rejection, got %v", err)
	}
	var auditAbsent bool
	if err := store.Pool().QueryRow(t.Context(), `SELECT to_regclass('metricspire_query_audit') IS NULL`).Scan(&auditAbsent); err != nil || !auditAbsent {
		t.Fatalf("migration continued after checksum rejection: absent=%v err=%v", auditAbsent, err)
	}
}
