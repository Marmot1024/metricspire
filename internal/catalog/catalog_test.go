package catalog_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
)

func TestDraftPublishUpdateRollbackLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := catalog.NewMemoryRepository()
	service, err := catalog.NewService(repository)
	if err != nil {
		t.Fatal(err)
	}
	source := loadSource(t)
	draft1, err := service.SaveDraft(ctx, catalog.SaveDraftInput{
		Namespace: "demo", Source: source, Actor: "alice", ExpectedRevision: 0,
	})
	if err != nil || draft1.Revision != 1 {
		t.Fatalf("SaveDraft() = %#v, %v", draft1, err)
	}
	release1, err := service.Publish(ctx, "demo", source.Metadata.Name, 1, "alice", "initial")
	if err != nil {
		t.Fatal(err)
	}
	active, err := repository.GetActiveRelease(ctx, "demo", source.Metadata.Name)
	if err != nil || active.ID != release1.ID {
		t.Fatalf("active release = %#v, %v", active, err)
	}

	source.Metadata.Version = "1.1.0"
	source.Spec.Metrics[0].Description = "reviewed wording"
	draft2, err := service.SaveDraft(ctx, catalog.SaveDraftInput{
		Namespace: "demo", Source: source, Actor: "bob", ExpectedRevision: 1,
	})
	if err != nil || draft2.Revision != 2 {
		t.Fatalf("SaveDraft(update) = %#v, %v", draft2, err)
	}
	release2, err := service.Publish(ctx, "demo", source.Metadata.Name, 2, "bob", "wording")
	if err != nil || release2.ID == release1.ID {
		t.Fatalf("Publish(update) = %#v, %v", release2, err)
	}
	rolledBack, err := service.Rollback(ctx, "demo", source.Metadata.Name, release1.ID, "carol", "regression")
	if err != nil || rolledBack.ID != release1.ID {
		t.Fatalf("Rollback() = %#v, %v", rolledBack, err)
	}
	events, err := repository.ListEvents(ctx, "demo", source.Metadata.Name)
	if err != nil || len(events) != 3 || events[2].Kind != catalog.EventRollback || events[2].FromReleaseID != release2.ID {
		t.Fatalf("events = %#v, %v", events, err)
	}
}

func TestCatalogRejectsLostUpdateAndUnverifiedPublication(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := catalog.NewMemoryRepository()
	service, _ := catalog.NewService(repository)
	source := loadSource(t)
	if _, err := service.SaveDraft(ctx, catalog.SaveDraftInput{Namespace: "demo", Source: source, Actor: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveDraft(ctx, catalog.SaveDraftInput{Namespace: "demo", Source: source, Actor: "bob"}); !errors.Is(err, catalog.ErrConflict) {
		t.Fatalf("lost update error = %v", err)
	}
	source.Spec.Metrics[0].Verification.Status = model.VerificationUnverified
	if _, err := service.SaveDraft(ctx, catalog.SaveDraftInput{Namespace: "demo", Source: source, Actor: "alice", ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Publish(ctx, "demo", source.Metadata.Name, 2, "alice", ""); !errors.Is(err, catalog.ErrNotPublishable) {
		t.Fatalf("unverified publish error = %v", err)
	}
}

func TestRepositoryRejectsManifestFromAnotherDraftRevision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := catalog.NewMemoryRepository()
	service, _ := catalog.NewService(repository)
	source := loadSource(t)
	if _, err := service.SaveDraft(ctx, catalog.SaveDraftInput{Namespace: "demo", Source: source, Actor: "alice"}); err != nil {
		t.Fatal(err)
	}
	stale, err := compiler.Compile(source)
	if err != nil {
		t.Fatal(err)
	}
	source.Metadata.Version = "2.0.0"
	if _, err := service.SaveDraft(ctx, catalog.SaveDraftInput{
		Namespace: "demo", Source: source, Actor: "alice", ExpectedRevision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = repository.Publish(ctx, catalog.PublishInput{
		Namespace: "demo", Name: source.Metadata.Name, ExpectedRevision: 2,
		ReleaseID: "stale", Manifest: stale, Actor: "alice",
	})
	if !errors.Is(err, catalog.ErrConflict) {
		t.Fatalf("Publish(stale manifest) error = %v", err)
	}
}

func TestPublishRequiresOperationalMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*model.Metric)
	}{
		{name: "display name", mutate: func(metric *model.Metric) { metric.DisplayName = "" }},
		{name: "business definition", mutate: func(metric *model.Metric) { metric.Description = "" }},
		{name: "owner", mutate: func(metric *model.Metric) { metric.Owner = "" }},
		{name: "evidence", mutate: func(metric *model.Metric) { metric.Verification.Evidence = nil }},
		{name: "blank evidence", mutate: func(metric *model.Metric) { metric.Verification.Evidence = []string{"  "} }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repository := catalog.NewMemoryRepository()
			service, _ := catalog.NewService(repository)
			source := loadSource(t)
			test.mutate(&source.Spec.Metrics[0])
			if _, err := service.SaveDraft(context.Background(), catalog.SaveDraftInput{
				Namespace: "demo", Source: source, Actor: "alice",
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := service.Publish(context.Background(), "demo", source.Metadata.Name, 1, "alice", "metadata gate"); !errors.Is(err, catalog.ErrNotPublishable) {
				t.Fatalf("Publish() error = %v, want ErrNotPublishable", err)
			}
		})
	}
}

func TestPublishProtectsMetricCodesAndExecutionSemantics(t *testing.T) {
	t.Parallel()
	t.Run("description may evolve", func(t *testing.T) {
		publishSecondRevision(t, func(source *model.SemanticSource) {
			source.Spec.Metrics[0].Description = "reviewed business wording"
		}, false)
	})
	t.Run("metric removal", func(t *testing.T) {
		publishSecondRevision(t, func(source *model.SemanticSource) {
			source.Spec.Metrics = source.Spec.Metrics[:len(source.Spec.Metrics)-1]
		}, true)
	})
	t.Run("execution semantics change", func(t *testing.T) {
		publishSecondRevision(t, func(source *model.SemanticSource) {
			metric := findMetric(t, source, "gross_revenue")
			metric.Expression.Field = "orders.refunded_amount"
		}, true)
	})
	t.Run("explicit deprecation is allowed but cannot be reversed", func(t *testing.T) {
		ctx := context.Background()
		repository := catalog.NewMemoryRepository()
		service, _ := catalog.NewService(repository)
		source := loadSource(t)
		if _, err := service.SaveDraft(ctx, catalog.SaveDraftInput{Namespace: "demo", Source: source, Actor: "alice"}); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Publish(ctx, "demo", source.Metadata.Name, 1, "alice", "initial"); err != nil {
			t.Fatal(err)
		}
		source.Metadata.Version = "1.1.0"
		findMetric(t, &source, "refund_rate").Deprecated = true
		if _, err := service.SaveDraft(ctx, catalog.SaveDraftInput{
			Namespace: "demo", Source: source, Actor: "alice", ExpectedRevision: 1,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Publish(ctx, "demo", source.Metadata.Name, 2, "alice", "deprecate"); err != nil {
			t.Fatalf("Publish(deprecation) error = %v", err)
		}
		source.Metadata.Version = "1.2.0"
		findMetric(t, &source, "refund_rate").Deprecated = false
		if _, err := service.SaveDraft(ctx, catalog.SaveDraftInput{
			Namespace: "demo", Source: source, Actor: "alice", ExpectedRevision: 2,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Publish(ctx, "demo", source.Metadata.Name, 3, "alice", "restore"); !errors.Is(err, catalog.ErrNotPublishable) {
			t.Fatalf("Publish(undeprecate) error = %v, want ErrNotPublishable", err)
		}
	})
}

func TestRepositoryRejectsStaleActiveReleaseAtCommit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := catalog.NewMemoryRepository()
	service, _ := catalog.NewService(repository)
	source := loadSource(t)
	if _, err := service.SaveDraft(ctx, catalog.SaveDraftInput{Namespace: "demo", Source: source, Actor: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Publish(ctx, "demo", source.Metadata.Name, 1, "alice", "initial"); err != nil {
		t.Fatal(err)
	}
	source.Metadata.Version = "1.1.0"
	source.Spec.Metrics[0].Description = "reviewed wording"
	if _, err := service.SaveDraft(ctx, catalog.SaveDraftInput{
		Namespace: "demo", Source: source, Actor: "alice", ExpectedRevision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	manifest, err := compiler.Compile(source)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.Publish(ctx, catalog.PublishInput{
		Namespace: "demo", Name: source.Metadata.Name, ExpectedRevision: 2,
		ExpectedActiveReleaseID: "stale-release", ReleaseID: "next", Manifest: manifest, Actor: "alice",
	})
	if !errors.Is(err, catalog.ErrConflict) {
		t.Fatalf("Publish(stale active) error = %v, want ErrConflict", err)
	}
}

func publishSecondRevision(t *testing.T, mutate func(*model.SemanticSource), wantRejected bool) {
	t.Helper()
	ctx := context.Background()
	repository := catalog.NewMemoryRepository()
	service, _ := catalog.NewService(repository)
	source := loadSource(t)
	if _, err := service.SaveDraft(ctx, catalog.SaveDraftInput{Namespace: "demo", Source: source, Actor: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Publish(ctx, "demo", source.Metadata.Name, 1, "alice", "initial"); err != nil {
		t.Fatal(err)
	}
	source.Metadata.Version = "1.1.0"
	mutate(&source)
	if _, err := service.SaveDraft(ctx, catalog.SaveDraftInput{
		Namespace: "demo", Source: source, Actor: "alice", ExpectedRevision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := service.Publish(ctx, "demo", source.Metadata.Name, 2, "alice", "second")
	if wantRejected && !errors.Is(err, catalog.ErrNotPublishable) {
		t.Fatalf("Publish() error = %v, want ErrNotPublishable", err)
	}
	if !wantRejected && err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
}

func findMetric(t *testing.T, source *model.SemanticSource, name string) *model.Metric {
	t.Helper()
	for i := range source.Spec.Metrics {
		if source.Spec.Metrics[i].Name == name {
			return &source.Spec.Metrics[i]
		}
	}
	t.Fatalf("metric %q not found", name)
	return nil
}

func loadSource(t *testing.T) model.SemanticSource {
	t.Helper()
	var source model.SemanticSource
	path := filepath.Join("..", "..", "examples", "orders", "model.yaml")
	if err := contractio.ReadFile(path, &source); err != nil {
		t.Fatal(err)
	}
	return source
}
