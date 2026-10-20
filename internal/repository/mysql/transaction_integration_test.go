package mysql

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
)

func TestMySQLPublishEventFailureRollsBackReleaseAndActivePointer(t *testing.T) {
	store := isolatedMySQL(t)
	ctx := t.Context()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	service, err := catalog.NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	var source model.SemanticSource
	if err := contractio.ReadFile(filepath.Join("..", "..", "..", "examples", "orders", "model.yaml"), &source); err != nil {
		t.Fatal(err)
	}
	draft, err := service.SaveDraft(ctx, catalog.SaveDraftInput{Namespace: "demo", Source: source, Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Publish(ctx, "demo", source.Metadata.Name, draft.Revision, "test", "initial")
	if err != nil {
		t.Fatal(err)
	}
	source.Metadata.Version = "2.0.0"
	draft, err = service.SaveDraft(ctx, catalog.SaveDraftInput{Namespace: "demo", Source: source, Actor: "test", ExpectedRevision: draft.Revision})
	if err != nil {
		t.Fatal(err)
	}
	// Fail after the release and active pointer writes, at the audit event insert.
	if _, err := store.DB().ExecContext(ctx, `
CREATE TRIGGER reject_fixture_event BEFORE INSERT ON metricspire_release_events FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='fixture event failure'`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Publish(ctx, "demo", source.Metadata.Name, draft.Revision, "test", "rejected"); err == nil || strings.Contains(err.Error(), "fixture event failure") {
		t.Fatalf("expected event failure: %v", err)
	}
	active, err := store.GetActiveRelease(ctx, "demo", source.Metadata.Name)
	if err != nil || active.ID != first.ID {
		t.Fatalf("failed publish changed active release: %v, %v", active.ID, err)
	}
	releases, err := store.ListReleases(ctx, "demo", source.Metadata.Name)
	if err != nil || len(releases) != 1 || releases[0].ID != first.ID {
		t.Fatalf("failed publish left partial release history: %v, %v", releases, err)
	}
	events, err := store.ListEvents(ctx, "demo", source.Metadata.Name)
	if err != nil || len(events) != 1 {
		t.Fatalf("failed publish altered event history: %v, %v", events, err)
	}
	if _, err := store.DB().ExecContext(ctx, `DROP TRIGGER reject_fixture_event`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Publish(ctx, "demo", source.Metadata.Name, draft.Revision, "test", "retry"); err != nil {
		t.Fatalf("publish retry after transaction rollback: %v", err)
	}
}
