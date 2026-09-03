package runtimeconfig_test

import (
	"testing"

	"github.com/marmot1024/metricspire/internal/runtimeconfig"
)

func TestRuntimeConfigRequiresHTTPSAndUniqueTrustedRoutes(t *testing.T) {
	valid := runtimeconfig.Config{
		APIVersion: runtimeconfig.APIVersion, Kind: runtimeconfig.Kind,
		HTTP:     runtimeconfig.HTTPConfig{Address: "127.0.0.1:8080", PublicURL: "https://metrics.example.com"},
		OIDC:     runtimeconfig.OIDCConfig{IssuerURL: "https://identity.example.com", ClientID: "metricspire", BearerAudience: "metricspire-api"},
		Policies: []runtimeconfig.PolicyRoute{{Namespace: "demo", ModelName: "commerce", Tenant: "demo", Path: "policy.yaml"}},
		Bindings: []runtimeconfig.BindingRoute{{Namespace: "demo", ModelName: "commerce", Path: "binding.yaml"}},
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
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
	invalidSession.OIDC.SessionTTL = "25h"
	if err := invalidSession.Validate(); err == nil {
		t.Fatal("OIDC session longer than 24 hours was accepted")
	}
	missingAudience := valid
	missingAudience.OIDC.BearerAudience = ""
	if err := missingAudience.Validate(); err == nil {
		t.Fatal("runtime config without API bearer audience was accepted")
	}
}
