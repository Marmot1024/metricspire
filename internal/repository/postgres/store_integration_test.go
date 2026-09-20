package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/audit"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/governance"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/repository/postgres"
)

func TestPostgresCatalogLifecycleAndOptimisticConcurrency(t *testing.T) {
	databaseURL := os.Getenv("METRICSPIRE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("METRICSPIRE_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := postgres.Migrate(ctx, store.Pool()); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, store.Pool()); err != nil {
		t.Fatalf("idempotent migration failed: %v", err)
	}
	service, _ := catalog.NewService(store)
	var source model.SemanticSource
	if err := contractio.ReadFile(filepath.Join("..", "..", "..", "examples", "orders", "model.yaml"), &source); err != nil {
		t.Fatal(err)
	}
	namespace := fmt.Sprintf("test_%d", time.Now().UnixNano())
	draft, err := service.SaveDraft(ctx, catalog.SaveDraftInput{Namespace: namespace, Source: source, Actor: "test", ExpectedRevision: 0})
	if err != nil {
		t.Fatal(err)
	}
	release1, err := service.Publish(ctx, namespace, source.Metadata.Name, draft.Revision, "test", "first")
	if err != nil {
		t.Fatal(err)
	}

	source.Metadata.Version = "1.1.0"
	draft, err = service.SaveDraft(ctx, catalog.SaveDraftInput{Namespace: namespace, Source: source, Actor: "test", ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	var successes int
	var conflicts int
	var mu sync.Mutex
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			candidate := source
			candidate.Metadata.Version = fmt.Sprintf("1.1.%d", time.Now().UnixNano())
			_, err := service.SaveDraft(ctx, catalog.SaveDraftInput{
				Namespace: namespace, Source: candidate, Actor: "racer", ExpectedRevision: draft.Revision,
			})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes++
			} else if errors.Is(err, catalog.ErrConflict) {
				conflicts++
			} else {
				t.Errorf("concurrent SaveDraft() error = %v", err)
			}
		}()
	}
	wait.Wait()
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent saves: successes=%d conflicts=%d", successes, conflicts)
	}
	current, err := store.GetDraft(ctx, namespace, source.Metadata.Name)
	if err != nil {
		t.Fatal(err)
	}
	release2, err := service.Publish(ctx, namespace, source.Metadata.Name, current.Revision, "test", "second")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Rollback(ctx, namespace, source.Metadata.Name, release1.ID, "test", "rollback"); err != nil {
		t.Fatal(err)
	}
	active, err := store.GetActiveRelease(ctx, namespace, source.Metadata.Name)
	if err != nil || active.ID != release1.ID {
		t.Fatalf("active release = %#v, %v", active, err)
	}
	activeReleases, err := store.ListActiveReleases(ctx, namespace)
	if err != nil || len(activeReleases) != 1 || activeReleases[0].ID != release1.ID {
		t.Fatalf("active releases = %#v, %v", activeReleases, err)
	}
	requestID := fmt.Sprintf("request_%d", time.Now().UnixNano())
	if err := store.Record(ctx, audit.QueryEvent{
		RequestID: requestID, Tenant: "demo", Principal: "integration-test",
		Namespace: namespace, ModelName: source.Metadata.Name, ReleaseID: release1.ID,
		ManifestFingerprint: release1.ManifestFingerprint, LogicalFingerprint: release1.ManifestFingerprint,
		PhysicalFingerprint: release1.ManifestFingerprint, JobID: "job_integration",
		Kind: audit.EventQuerySucceeded, RowCount: 3, OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	var eventKind string
	var rowCount int
	if err := store.Pool().QueryRow(ctx, `
SELECT event_kind, row_count FROM metricspire_query_audit WHERE request_id = $1`, requestID).Scan(&eventKind, &rowCount); err != nil {
		t.Fatal(err)
	}
	if eventKind != string(audit.EventQuerySucceeded) || rowCount != 3 {
		t.Fatalf("stored audit = %s rows=%d", eventKind, rowCount)
	}
	governanceService, err := governance.NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	definition := governance.MetricDefinition{
		Code: "governed_metric", DisplayName: "治理指标", Description: "源定义", Owner: "test", Status: "unverified",
		BusinessType: governance.BusinessDerived, SemanticReadiness: governance.ReadinessNeedsRemediation,
		AuthoritativeSource: governance.SourceReference{Reference: "sheet:1", Resource: "daily", Field: "value"},
		ValueType:           model.DataTypeDecimal, Verification: model.Verification{Status: model.VerificationUnverified},
	}
	firstImport, err := governanceService.Import(ctx, governance.ImportInput{
		Namespace: namespace, SourceFingerprint: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Actor: "test", Records: []governance.MetricDefinition{definition},
	})
	if err != nil {
		t.Fatal(err)
	}
	definition.Description = "第二版源定义"
	secondImport, err := governanceService.Import(ctx, governance.ImportInput{
		Namespace: namespace, SourceFingerprint: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ExpectedPreviousImportID: firstImport.ID, Actor: "test", Records: []governance.MetricDefinition{definition},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := governanceService.RollbackImport(ctx, namespace, secondImport.ID, "test"); err != nil {
		t.Fatal(err)
	}
	records, err := governanceService.List(ctx, namespace, "", 10)
	if err != nil || len(records) != 1 || records[0].Definition.Description != "源定义" {
		t.Fatalf("governance records after second rollback = %#v, %v", records, err)
	}
	if _, err := governanceService.RollbackImport(ctx, namespace, firstImport.ID, "test"); err != nil {
		t.Fatal(err)
	}
	records, err = governanceService.List(ctx, namespace, "", 10)
	if err != nil || len(records) != 0 {
		t.Fatalf("governance records after initial rollback = %#v, %v", records, err)
	}
	if _, err := service.Deactivate(ctx, namespace, source.Metadata.Name, "test", "retire from discovery"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetActiveRelease(ctx, namespace, source.Metadata.Name); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("active release after deactivation = %v", err)
	}
	if _, err := service.Rollback(ctx, namespace, source.Metadata.Name, release2.ID, "test", "reactivate after deactivation"); err != nil {
		t.Fatal(err)
	}
	active, err = store.GetActiveRelease(ctx, namespace, source.Metadata.Name)
	if err != nil || active.ID != release2.ID {
		t.Fatalf("active release after reactivation = %#v, %v", active, err)
	}
	events, err := store.ListEvents(ctx, namespace, source.Metadata.Name)
	if err != nil || len(events) != 5 || events[1].ToReleaseID != release2.ID || events[2].Kind != catalog.EventRollback || events[3].Kind != catalog.EventDeactivated || events[3].ToReleaseID != "" || events[4].Kind != catalog.EventRollback || events[4].ToReleaseID != release2.ID {
		t.Fatalf("events = %#v, %v", events, err)
	}
}
