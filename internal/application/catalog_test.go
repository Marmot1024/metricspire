package application_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/planner"
)

func TestCatalogSearchReturnsOnlyAuthorizedMetricsFromActiveReleases(t *testing.T) {
	repository := catalog.NewMemoryRepository()
	management, _ := catalog.NewService(repository)
	root := filepath.Join("..", "..", "examples", "orders")
	var source model.SemanticSource
	var policySource model.PolicySource
	var binding model.SourceBinding
	if err := contractio.ReadFile(filepath.Join(root, "model.yaml"), &source); err != nil {
		t.Fatal(err)
	}
	if err := contractio.ReadFile(filepath.Join(root, "policy.yaml"), &policySource); err != nil {
		t.Fatal(err)
	}
	if err := contractio.ReadFile(filepath.Join(root, "binding.json"), &binding); err != nil {
		t.Fatal(err)
	}
	for index := range source.Spec.Metrics {
		if source.Spec.Metrics[index].Name == "refund_rate" {
			source.Spec.Metrics[index].UsageExamples = []string{"查看上月退款率"}
		}
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
	bindings, err := application.NewConfiguredBindingResolver([]application.BindingConfiguration{{
		Namespace: "demo", ModelName: source.Metadata.Name, Binding: binding,
	}})
	if err != nil {
		t.Fatal(err)
	}
	service, err := application.NewCatalogService(repository, policies, bindings)
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
	if len(results) != 1 || results[0].Name != "refund_rate" || len(results[0].AllowedDimensions) != 1 || results[0].AllowedDimensions[0] != "order_date" ||
		results[0].TimeDimension != "order_date" || len(results[0].TimeGranularities) != 3 || len(results[0].UsageExamples) != 1 ||
		len(results[0].DimensionDetails) != 1 || results[0].DimensionDetails[0].Name != "order_date" ||
		results[0].DimensionDetails[0].Description == "" || results[0].DimensionDetails[0].DataType != model.DataTypeTimestamp {
		t.Fatalf("authorized catalog = %#v", results)
	}
	modelName, err := service.ResolveActiveModel(context.Background(), application.QueryScope{
		Namespace: "demo", Context: model.RequestContext{
			Tenant: "demo", Principal: "analyst", Roles: []string{"analyst"}, RequestID: "route-test",
		},
	}, []string{"refund_rate"})
	if err != nil || modelName != source.Metadata.Name {
		t.Fatalf("resolved model = %q, %v", modelName, err)
	}
	tooManyMetrics := make([]string, planner.MaximumMetrics+1)
	if _, err := service.ResolveActiveModel(context.Background(), application.QueryScope{
		Namespace: "demo", Context: model.RequestContext{
			Tenant: "demo", Principal: "analyst", Roles: []string{"analyst"}, RequestID: "route-budget-test",
		},
	}, tooManyMetrics); err == nil {
		t.Fatal("expected metric route budget error")
	} else {
		var problem *model.Problem
		if !errors.As(err, &problem) || problem.Code != "budget_exceeded" {
			t.Fatalf("route budget error = %v", err)
		}
	}
}
