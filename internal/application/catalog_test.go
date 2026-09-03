package application_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
)

func TestCatalogSearchReturnsOnlyAuthorizedMetricsFromActiveReleases(t *testing.T) {
	repository := catalog.NewMemoryRepository()
	management, _ := catalog.NewService(repository)
	root := filepath.Join("..", "..", "examples", "orders")
	var source model.SemanticSource
	var policySource model.PolicySource
	if err := contractio.ReadFile(filepath.Join(root, "model.yaml"), &source); err != nil {
		t.Fatal(err)
	}
	if err := contractio.ReadFile(filepath.Join(root, "policy.yaml"), &policySource); err != nil {
		t.Fatal(err)
	}
	draft, err := management.SaveDraft(context.Background(), catalog.SaveDraftInput{
		Namespace: "demo", Source: source, Actor: "owner", ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := management.Publish(context.Background(), "demo", source.Metadata.Name, draft.Revision, "owner", "initial"); err != nil {
		t.Fatal(err)
	}
	policySource.Rules[0].Metrics = []string{"refund_rate"}
	policySource.Rules[0].Dimensions = []string{"order_date"}
	policies, err := application.NewConfiguredPolicyResolver([]application.PolicyConfiguration{{
		Namespace: "demo", ModelName: source.Metadata.Name, Tenant: "demo", Policy: policySource,
	}})
	if err != nil {
		t.Fatal(err)
	}
	service, err := application.NewCatalogService(repository, policies)
	if err != nil {
		t.Fatal(err)
	}
	results, err := service.SearchActive(context.Background(), application.QueryScope{
		Namespace: "demo", Context: model.RequestContext{
			Tenant: "demo", Principal: "analyst", Roles: []string{"analyst"}, RequestID: "catalog-test",
		},
	}, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Name != "refund_rate" || len(results[0].AllowedDimensions) != 1 || results[0].AllowedDimensions[0] != "order_date" {
		t.Fatalf("authorized catalog = %#v", results)
	}
}
