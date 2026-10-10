package main

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/marmot1024/metricspire/internal/adapter/databricks"
	"github.com/marmot1024/metricspire/internal/adapter/postgresquery"
	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/policy"
	"github.com/marmot1024/metricspire/internal/runtimeconfig"
)

func TestOnlinePolicyRequiresExplicitReadersAndDataScope(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*model.PolicySource)
	}{
		{"approved", func(*model.PolicySource) {}},
		{"no readers", func(p *model.PolicySource) { p.Rules[0].Principals = nil }},
		{"role bypass", func(p *model.PolicySource) { p.Rules[0].Roles = []string{"analyst"} }},
		{"all readers", func(p *model.PolicySource) { p.Rules[0].Principals = []string{"*"} }},
		{"all metrics", func(p *model.PolicySource) { p.Rules[0].Metrics = []string{"*"} }},
		{"all dimensions", func(p *model.PolicySource) { p.Rules[0].Dimensions = []string{"*"} }},
		{"empty metric list", func(p *model.PolicySource) { p.Rules[0].Metrics = nil }},
		{"empty dimension list", func(p *model.PolicySource) { p.Rules[0].Dimensions = nil }},
		{"ambiguous reader", func(p *model.PolicySource) { p.Rules[0].Principals = []string{" reader "} }},
		{"wrong tenant", func(p *model.PolicySource) { p.Tenant = "other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			var source model.PolicySource
			if err := contractio.ReadFile(filepath.Join("..", "..", "examples", "online", "reader-policy.yaml"), &source); err != nil {
				t.Fatal(err)
			}
			test.change(&source)
			if err := contractio.WriteJSON(filepath.Join(dir, "readers.json"), source); err != nil {
				t.Fatal(err)
			}
			_, err := loadOnlinePolicies(dir, runtimeconfig.OnlineConfig{Policies: []runtimeconfig.PolicyRoute{{Namespace: "demo", ModelName: "daily_sales", Tenant: "demo", Path: "readers.json"}}})
			if (err == nil) != (test.name == "approved") {
				t.Fatalf("online policy validation: %v", err)
			}
		})
	}
}

func TestOnlineReaderPolicyReusesEvaluatorAndFailsClosed(t *testing.T) {
	dir := t.TempDir()
	var source model.PolicySource
	var semantic model.SemanticSource
	base := filepath.Join("..", "..", "examples", "online")
	if err := contractio.ReadFile(filepath.Join(base, "reader-policy.yaml"), &source); err != nil {
		t.Fatal(err)
	}
	if err := contractio.ReadFile(filepath.Join(base, "model.yaml"), &semantic); err != nil {
		t.Fatal(err)
	}
	source.Rules[0].Metrics = []string{"revenue"}
	source.Rules[0].Dimensions = []string{"date"}
	if err := contractio.WriteJSON(filepath.Join(dir, "readers.json"), source); err != nil {
		t.Fatal(err)
	}
	policies, err := loadOnlinePolicies(dir, runtimeconfig.OnlineConfig{Policies: []runtimeconfig.PolicyRoute{{Namespace: "demo", ModelName: "daily_sales", Tenant: "demo", Path: "readers.json"}}})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := compiler.Compile(semantic)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := compiler.CompilePolicy(policies[0].Policy, manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, principal, tenant, metric, dimension string
		allowed                                    bool
	}{
		{"approved", "synthetic-online-reader", "demo", "revenue", "date", true},
		{"analyst is not reader", "another-analyst", "demo", "revenue", "date", false},
		{"other tenant", "synthetic-online-reader", "other", "revenue", "date", false},
		{"other metric", "synthetic-online-reader", "demo", "orders", "date", false},
		{"other dimension", "synthetic-online-reader", "demo", "revenue", "region", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := model.RequestContext{Tenant: test.tenant, Principal: test.principal, Roles: []string{"analyst"}, RequestID: "request"}
			decision, err := policy.Authorize(ctx, bundle, manifest.Fingerprint, model.SemanticQuery{Metrics: []string{test.metric}, GroupBy: []string{test.dimension}})
			if test.allowed {
				if err != nil || !decision.Allowed {
					t.Fatalf("approved reader rejected: %v", err)
				}
			} else {
				var problem *model.Problem
				if !errors.As(err, &problem) || problem.Code != "permission_denied" {
					t.Fatalf("out-of-scope reader accepted: %v", err)
				}
			}
		})
	}
	resolver, err := application.NewConfiguredPolicyResolver(policies)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ResolvePolicy(t.Context(), application.QueryScope{Namespace: "demo", ModelName: "unlisted_model", Context: model.RequestContext{Tenant: "demo"}}, catalog.Release{}); !errors.Is(err, application.ErrResolutionNotFound) {
		t.Fatalf("missing online policy fell back: %v", err)
	}
}

func TestAnalyticalAndOnlineBindingsRemainSeparate(t *testing.T) {
	base := t.TempDir()
	var binding model.SourceBinding
	if err := contractio.ReadFile(filepath.Join("..", "..", "examples", "online", "binding.yaml"), &binding); err != nil {
		t.Fatal(err)
	}
	if err := contractio.WriteJSON(filepath.Join(base, "online.json"), binding); err != nil {
		t.Fatal(err)
	}
	online := runtimeconfig.OnlineConfig{Bindings: []runtimeconfig.BindingRoute{{Namespace: "demo", ModelName: "daily_sales", Path: "online.json"}}, Resources: []model.ResourceRef{binding.Datasets[0].Resource}}
	loadedOnline, err := loadOnlineBindings(base, online)
	if err != nil {
		t.Fatal(err)
	}
	binding.Engine = databricks.EngineName
	if err := contractio.WriteJSON(filepath.Join(base, "warehouse.json"), binding); err != nil {
		t.Fatal(err)
	}
	_, analytical, err := loadTrustedRoutes(base, runtimeconfig.Config{Bindings: []runtimeconfig.BindingRoute{{Namespace: "demo", ModelName: "daily_sales", Path: "warehouse.json"}}, Online: &online})
	if err != nil {
		t.Fatal(err)
	}
	if loadedOnline[0].Binding.Engine != postgresquery.EngineName || analytical[0].Binding.Engine != databricks.EngineName {
		t.Fatal("routes contaminated each other")
	}
	for _, test := range []struct {
		name   string
		change func(*model.SourceBinding)
	}{
		{"warehouse", func(b *model.SourceBinding) { b.Engine = databricks.EngineName }},
		{"unapproved source", func(b *model.SourceBinding) { b.Datasets[0].Resource.Table = "unapproved" }},
		{"multiple sources", func(b *model.SourceBinding) { b.Datasets = append(b.Datasets, b.Datasets[0]) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var b model.SourceBinding
			if err := contractio.ReadFile(filepath.Join("..", "..", "examples", "online", "binding.yaml"), &b); err != nil {
				t.Fatal(err)
			}
			test.change(&b)
			if err := contractio.WriteJSON(filepath.Join(base, "online.json"), b); err != nil {
				t.Fatal(err)
			}
			if _, err := loadOnlineBindings(base, online); err == nil {
				t.Fatal("unsafe online binding accepted")
			}
		})
	}
}
