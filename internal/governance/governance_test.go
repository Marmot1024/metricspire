package governance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/marmot1024/metricspire/internal/governance"
	"github.com/marmot1024/metricspire/internal/model"
)

func TestImportPreservesPendingMetricsAndRollsBackTheBatch(t *testing.T) {
	t.Parallel()
	repository := governance.NewMemoryRepository()
	service, err := governance.NewService(repository)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := service.Import(context.Background(), governance.ImportInput{
		Namespace: "game", SourceFingerprint: "sha256:" + string(make([]byte, 0)),
		Actor: "owner", Records: []governance.MetricDefinition{definition("pending_metric", governance.ReadinessNeedsRemediation, "")},
	})
	if err == nil {
		t.Fatal("invalid fingerprint was accepted")
	}
	batch, err = service.Import(context.Background(), governance.ImportInput{
		Namespace: "game", SourceFingerprint: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Actor: "owner", Records: []governance.MetricDefinition{
			definition("pending_metric", governance.ReadinessNeedsRemediation, ""),
			definition("ready_metric", governance.ReadinessExecutableUnverified, "daily"),
		},
	})
	if err != nil || batch.RecordCount != 2 {
		t.Fatalf("Import() = %#v, %v", batch, err)
	}
	records, err := service.List(context.Background(), "game", "", 10)
	if err != nil || len(records) != 2 || records[0].Definition.Code != "pending_metric" || records[1].Definition.SemanticModelName != "daily" {
		t.Fatalf("List() = %#v, %v", records, err)
	}
	rolledBack, err := service.RollbackImport(context.Background(), "game", batch.ID, "owner")
	if err != nil || rolledBack.RolledBackAt == nil {
		t.Fatalf("RollbackImport() = %#v, %v", rolledBack, err)
	}
	records, _ = service.List(context.Background(), "game", "", 10)
	if len(records) != 0 {
		t.Fatalf("records after rollback = %#v", records)
	}
}

func TestImportRejectsStaleBatchAndInventedExecutableMapping(t *testing.T) {
	t.Parallel()
	repository := governance.NewMemoryRepository()
	service, _ := governance.NewService(repository)
	input := governance.ImportInput{
		Namespace: "game", SourceFingerprint: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Actor: "owner", Records: []governance.MetricDefinition{definition("metric_one", governance.ReadinessNeedsDefinitionReview, "daily")},
	}
	if _, err := service.Import(context.Background(), input); err == nil {
		t.Fatal("non-executable record with a semantic model was accepted")
	}
	input.Records[0] = definition("metric_one", governance.ReadinessNeedsDefinitionReview, "")
	first, err := service.Import(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.SourceFingerprint = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if _, err := service.Import(context.Background(), input); !errors.Is(err, governance.ErrConflict) {
		t.Fatalf("stale import error = %v", err)
	}
	input.ExpectedPreviousImportID = first.ID
	if _, err := service.Import(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RollbackImport(context.Background(), "game", first.ID, "owner"); !errors.Is(err, governance.ErrConflict) {
		t.Fatalf("stale rollback error = %v", err)
	}
}

func TestConsecutiveImportsCanBeRolledBackInReverseOrder(t *testing.T) {
	t.Parallel()
	repository := governance.NewMemoryRepository()
	service, _ := governance.NewService(repository)
	metric := definition("metric_one", governance.ReadinessNeedsDefinitionReview, "")
	first, err := service.Import(context.Background(), governance.ImportInput{
		Namespace: "game", SourceFingerprint: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		Actor: "owner", Records: []governance.MetricDefinition{metric},
	})
	if err != nil {
		t.Fatal(err)
	}
	metric.Description = "second definition"
	second, err := service.Import(context.Background(), governance.ImportInput{
		Namespace: "game", SourceFingerprint: "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		ExpectedPreviousImportID: first.ID, Actor: "owner", Records: []governance.MetricDefinition{metric},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RollbackImport(context.Background(), "game", second.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	records, err := service.List(context.Background(), "game", "", 10)
	if err != nil || len(records) != 1 || records[0].Revision != 1 || records[0].Definition.Description == "second definition" {
		t.Fatalf("records after second rollback = %#v, %v", records, err)
	}
	metric.Description = "third definition"
	third, err := service.Import(context.Background(), governance.ImportInput{
		Namespace: "game", SourceFingerprint: "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		ExpectedPreviousImportID: first.ID, Actor: "owner", Records: []governance.MetricDefinition{metric},
	})
	if err != nil {
		t.Fatal(err)
	}
	records, err = service.List(context.Background(), "game", "", 10)
	if err != nil || len(records) != 1 || records[0].Revision != 3 || records[0].Definition.Description != "third definition" {
		t.Fatalf("records after third import = %#v, %v", records, err)
	}
	if _, err := service.RollbackImport(context.Background(), "game", third.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RollbackImport(context.Background(), "game", first.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	records, err = service.List(context.Background(), "game", "", 10)
	if err != nil || len(records) != 0 {
		t.Fatalf("records after first rollback = %#v, %v", records, err)
	}
}

func definition(code string, readiness governance.SemanticReadiness, semanticModel string) governance.MetricDefinition {
	return governance.MetricDefinition{
		Code: code, DisplayName: code, Description: code, Owner: "owner", Status: "governed",
		BusinessType: governance.BusinessDerived, SemanticReadiness: readiness, SemanticModelName: semanticModel,
		AuthoritativeSource: governance.SourceReference{Reference: "sheet:1", Resource: "daily", Field: code},
		ValueType:           model.DataTypeInteger, Verification: model.Verification{Status: model.VerificationUnverified},
	}
}
