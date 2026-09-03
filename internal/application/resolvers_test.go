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
)

func TestConfiguredResolversUseServerScopeAndReturnCopies(t *testing.T) {
	var policy model.PolicySource
	var binding model.SourceBinding
	root := filepath.Join("..", "..", "examples", "orders")
	if err := contractio.ReadFile(filepath.Join(root, "policy.yaml"), &policy); err != nil {
		t.Fatal(err)
	}
	if err := contractio.ReadFile(filepath.Join(root, "binding.json"), &binding); err != nil {
		t.Fatal(err)
	}
	policies, err := application.NewConfiguredPolicyResolver([]application.PolicyConfiguration{{
		Namespace: "demo", ModelName: "commerce", Tenant: "demo", Policy: policy,
	}})
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := application.NewConfiguredBindingResolver([]application.BindingConfiguration{{
		Namespace: "demo", ModelName: "commerce", Binding: binding,
	}})
	if err != nil {
		t.Fatal(err)
	}
	scope := application.QueryScope{Namespace: "demo", ModelName: "commerce", Context: model.RequestContext{Tenant: "demo"}}
	resolvedPolicy, err := policies.ResolvePolicy(context.Background(), scope, catalog.Release{})
	if err != nil {
		t.Fatal(err)
	}
	resolvedBinding, err := bindings.ResolveBinding(context.Background(), scope, catalog.Release{})
	if err != nil {
		t.Fatal(err)
	}
	resolvedPolicy.Rules[0].Name = "mutated"
	resolvedBinding.Datasets[0].Resource.Table = "mutated"
	again, _ := policies.ResolvePolicy(context.Background(), scope, catalog.Release{})
	againBinding, _ := bindings.ResolveBinding(context.Background(), scope, catalog.Release{})
	if again.Rules[0].Name == "mutated" || againBinding.Datasets[0].Resource.Table == "mutated" {
		t.Fatal("configured resolver leaked mutable state")
	}

	scope.Context.Tenant = "other"
	if _, err := policies.ResolvePolicy(context.Background(), scope, catalog.Release{}); !errors.Is(err, application.ErrResolutionNotFound) {
		t.Fatalf("unknown tenant error = %v", err)
	}
}
