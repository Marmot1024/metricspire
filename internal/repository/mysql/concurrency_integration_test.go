package mysql

import (
	"errors"
	"sync"
	"testing"

	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
)

func TestMySQLConcurrentInitialDraftAndPublication(t *testing.T) {
	s := isolatedMySQL(t)
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	service, _ := catalog.NewService(s)
	var source model.SemanticSource
	if err := contractio.ReadFile("../../../examples/orders/model.yaml", &source); err != nil {
		t.Fatal(err)
	}
	race := func(run func() error) {
		t.Helper()
		var wg sync.WaitGroup
		results := make(chan error, 2)
		for range 2 {
			wg.Add(1)
			go func() { defer wg.Done(); results <- run() }()
		}
		wg.Wait()
		close(results)
		successes, conflicts := 0, 0
		for err := range results {
			if err == nil {
				successes++
			} else if errors.Is(err, catalog.ErrConflict) {
				conflicts++
			} else {
				t.Fatal(err)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("race success=%d conflict=%d", successes, conflicts)
		}
	}
	race(func() error {
		_, err := service.SaveDraft(t.Context(), catalog.SaveDraftInput{Namespace: "demo", Source: source, Actor: "fixture"})
		return err
	})
	race(func() error {
		_, err := service.Publish(t.Context(), "demo", source.Metadata.Name, 1, "fixture", "concurrent publish")
		return err
	})
	history, err := s.ListReleases(t.Context(), "demo", source.Metadata.Name)
	if err != nil || len(history) != 1 {
		t.Fatal("publication race left history", err)
	}
	events, err := s.ListEvents(t.Context(), "demo", source.Metadata.Name)
	if err != nil || len(events) != 1 {
		t.Fatal("publication race left events", err)
	}
}
