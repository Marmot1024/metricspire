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
			source.Spec.Metrics[index].ExternalCode = "1007"
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
	if len(results) != 1 || results[0].Name != "refund_rate" || results[0].ExternalCode != "1007" || len(results[0].AllowedDimensions) != 1 || results[0].AllowedDimensions[0] != "order_date" ||
		results[0].TimeDimension != "order_date" || len(results[0].TimeGranularities) != 3 || len(results[0].UsageExamples) != 1 ||
		len(results[0].DimensionDetails) != 1 || results[0].DimensionDetails[0].Name != "order_date" ||
		results[0].DimensionDetails[0].Description == "" || results[0].DimensionDetails[0].DataType != model.DataTypeTimestamp ||
		results[0].ModelName != source.Metadata.Name || results[0].Entity == "" || results[0].Expression.Op == "" {
		t.Fatalf("authorized catalog = %#v", results)
	}
	results, err = service.SearchActive(context.Background(), application.QueryScope{
		Namespace: "demo", Context: model.RequestContext{
			Tenant: "demo", Principal: "analyst", Roles: []string{"analyst"}, RequestID: "external-code-search",
		},
	}, "1007", 100)
	if err != nil || len(results) != 1 || results[0].Name != "refund_rate" {
		t.Fatalf("external-code catalog search = %#v, %v", results, err)
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

func TestCatalogHidesTrialReleaseUnlessDeploymentEnablesIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
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
		source.Spec.Metrics[index].Verification.Status = model.VerificationUnverified
	}
	binding.ManifestFingerprint = ""
	draft, err := management.SaveDraft(ctx, catalog.SaveDraftInput{Namespace: "trial", Source: source, Actor: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := management.PublishTrial(ctx, "trial", source.Metadata.Name, draft.Revision, "owner", "technical preview"); err != nil {
		t.Fatal(err)
	}
	policies := application.PolicyResolverFunc(func(context.Context, application.QueryScope, catalog.Release) (model.PolicySource, error) {
		return policySource, nil
	})
	bindings := application.BindingResolverFunc(func(context.Context, application.QueryScope, catalog.Release) (model.SourceBinding, error) {
		return binding, nil
	})
	scope := application.QueryScope{Namespace: "trial", Context: model.RequestContext{
		Tenant: "demo", Principal: "analyst", Roles: []string{"analyst"}, RequestID: "trial-catalog",
	}}
	ordinary, _ := application.NewCatalogService(repository, policies, bindings)
	if results, err := ordinary.SearchActive(ctx, scope, "", 100); err != nil || len(results) != 0 {
		t.Fatalf("ordinary catalog exposed trial release: %#v, %v", results, err)
	}
	enabled, _ := application.NewCatalogService(repository, policies, bindings, application.WithTrialCatalogReleaseNamespaces("trial"))
	results, err := enabled.SearchActive(ctx, scope, "", 100)
	if err != nil || len(results) == 0 || results[0].ReleaseChannel != catalog.ReleaseChannelTrial {
		t.Fatalf("enabled trial catalog = %#v, %v", results, err)
	}
	wrongNamespace, _ := application.NewCatalogService(repository, policies, bindings, application.WithTrialCatalogReleaseNamespaces("acceptance"))
	if results, err := wrongNamespace.SearchActive(ctx, scope, "", 100); err != nil || len(results) != 0 {
		t.Fatalf("different trial namespace exposed release: %#v, %v", results, err)
	}
}
