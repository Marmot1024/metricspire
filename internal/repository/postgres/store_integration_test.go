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

	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/contractio"
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
	events, err := store.ListEvents(ctx, namespace, source.Metadata.Name)
	if err != nil || len(events) != 3 || events[1].ToReleaseID != release2.ID || events[2].Kind != catalog.EventRollback {
		t.Fatalf("events = %#v, %v", events, err)
	}
}
