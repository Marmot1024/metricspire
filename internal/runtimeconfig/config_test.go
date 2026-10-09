package runtimeconfig_test

import (
	"testing"

	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/runtimeconfig"
)

func TestOnlineConfigurationRequiresExplicitReviewedScopeAndBudgets(t *testing.T) {
	base := runtimeconfig.Config{
		APIVersion: runtimeconfig.APIVersion, Kind: runtimeconfig.Kind,
		HTTP:           runtimeconfig.HTTPConfig{Address: "127.0.0.1:8080", PublicURL: "https://metrics.example.com"},
		Authentication: runtimeconfig.AuthenticationConfig{Provider: runtimeconfig.AuthenticationOIDC, OIDC: &runtimeconfig.OIDCConfig{IssuerURL: "https://identity.example.com", ClientID: "metricspire", BearerAudience: "metricspire-api"}},
		Policies:       []runtimeconfig.PolicyRoute{{Namespace: "demo", ModelName: "commerce", Tenant: "demo", Path: "policy.yaml"}},
		Bindings:       []runtimeconfig.BindingRoute{{Namespace: "demo", ModelName: "commerce", Path: "binding.yaml"}},
	}
	valid := runtimeconfig.OnlineConfig{Tenant: "demo", Namespaces: []string{"demo"}, DataAccess: "tenant_shared", Resources: []model.ResourceRef{{Kind: model.ResourceTable, Schema: "serving", Table: "daily_sales"}}, MaxDataAge: "5m"}
	base.Online = &valid
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*runtimeconfig.OnlineConfig)
	}{
		{"implicit access", func(c *runtimeconfig.OnlineConfig) { c.DataAccess = "" }},
		{"other tenant", func(c *runtimeconfig.OnlineConfig) { c.Tenant = "other" }},
		{"unknown namespace", func(c *runtimeconfig.OnlineConfig) { c.Namespaces = []string{"unknown"} }},
		{"no freshness", func(c *runtimeconfig.OnlineConfig) { c.MaxDataAge = "" }},
		{"invalid freshness", func(c *runtimeconfig.OnlineConfig) { c.MaxDataAge = "0s" }},
		{"unbounded timeout", func(c *runtimeconfig.OnlineConfig) { c.Timeout = "11s" }},
		{"unbounded concurrency", func(c *runtimeconfig.OnlineConfig) { c.MaxConcurrency = 33 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.change(&candidate)
			config := base
			config.Online = &candidate
			if err := config.Validate(); err == nil {
				t.Fatal("unsafe online configuration accepted")
			}
		})
	}
}

func TestRuntimeConfigRequiresHTTPSAndUniqueTrustedRoutes(t *testing.T) {
	valid := runtimeconfig.Config{
		APIVersion: runtimeconfig.APIVersion, Kind: runtimeconfig.Kind,
		HTTP: runtimeconfig.HTTPConfig{Address: "127.0.0.1:8080", PublicURL: "https://metrics.example.com"},
		Authentication: runtimeconfig.AuthenticationConfig{
			Provider: runtimeconfig.AuthenticationOIDC,
			OIDC:     &runtimeconfig.OIDCConfig{IssuerURL: "https://identity.example.com", ClientID: "metricspire", BearerAudience: "metricspire-api"},
		},
		Policies: []runtimeconfig.PolicyRoute{{Namespace: "demo", ModelName: "commerce", Tenant: "demo", Path: "policy.yaml"}},
		Bindings: []runtimeconfig.BindingRoute{{Namespace: "demo", ModelName: "commerce", Path: "binding.yaml"}},
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	trial := valid
	trial.ReleasePolicy.TrialNamespaces = []string{"demo"}
	if err := trial.Validate(); err != nil {
		t.Fatalf("explicit trial namespace was rejected: %v", err)
	}
	unknownTrial := valid
	unknownTrial.ReleasePolicy.TrialNamespaces = []string{"unknown"}
	if err := unknownTrial.Validate(); err == nil {
		t.Fatal("trial namespace without a binding route was accepted")
	}
	insecure := valid
	insecure.HTTP.PublicURL = "http://metrics.example.com"
	if err := insecure.Validate(); err == nil {
		t.Fatal("non-loopback HTTP public URL was accepted")
	}
	duplicate := valid
	duplicate.Policies = append(duplicate.Policies, duplicate.Policies[0])
	if err := duplicate.Validate(); err == nil {
		t.Fatal("duplicate policy route was accepted")
	}
	invalidSession := valid
	invalidSessionOIDC := *invalidSession.Authentication.OIDC
	invalidSessionOIDC.SessionTTL = "25h"
	invalidSession.Authentication.OIDC = &invalidSessionOIDC
	if err := invalidSession.Validate(); err == nil {
		t.Fatal("OIDC session longer than 24 hours was accepted")
	}
	missingAudience := valid
	missingAudienceOIDC := *missingAudience.Authentication.OIDC
	missingAudienceOIDC.BearerAudience = ""
	missingAudience.Authentication.OIDC = &missingAudienceOIDC
	if err := missingAudience.Validate(); err == nil {
		t.Fatal("runtime config without API bearer audience was accepted")
	}
}

func TestRuntimeConfigRequiresExplicitFailClosedDatabricksAppsProfile(t *testing.T) {
	valid := runtimeconfig.Config{
		APIVersion: runtimeconfig.APIVersion, Kind: runtimeconfig.Kind,
		HTTP: runtimeconfig.HTTPConfig{Address: "0.0.0.0:8000", PublicURL: "https://metricspire.example.databricksapps.com"},
		Authentication: runtimeconfig.AuthenticationConfig{
			Provider: runtimeconfig.AuthenticationDatabricksApps,
			DatabricksApps: &runtimeconfig.DatabricksAppsConfig{
				ExpectedAppName: "metricspire", ExpectedWorkspaceID: "314", Tenant: "company",
				QueryRole: "analyst", PublisherGroupID: "publishers",
			},
		},
		Policies: []runtimeconfig.PolicyRoute{{Namespace: "demo", ModelName: "commerce", Tenant: "company", Path: "policy.yaml"}},
		Bindings: []runtimeconfig.BindingRoute{{Namespace: "demo", ModelName: "commerce", Path: "binding.yaml"}},
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	ambiguous := valid
	ambiguous.Authentication.OIDC = &runtimeconfig.OIDCConfig{IssuerURL: "https://identity.example.com", ClientID: "client", BearerAudience: "api"}
	if err := ambiguous.Validate(); err == nil {
		t.Fatal("ambiguous OIDC and Databricks Apps providers were accepted")
	}
	invalidPublisher := valid
	apps := *invalidPublisher.Authentication.DatabricksApps
	apps.PublisherGroupID = ""
	invalidPublisher.Authentication.DatabricksApps = &apps
	if err := invalidPublisher.Validate(); err == nil {
		t.Fatal("Apps profile without a publisher group was accepted")
	}
}
