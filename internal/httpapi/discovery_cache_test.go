package httpapi

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/runtimeconfig"
)

type discoverySearch struct {
	CatalogSearcher
	scopes []application.QueryScope
}

func (s *discoverySearch) SearchActive(_ context.Context, scope application.QueryScope, _ string, _ int) ([]application.MetricCatalogEntry, error) {
	s.scopes = append(s.scopes, scope)
	if scope.Context.Principal == "allowed" {
		return []application.MetricCatalogEntry{{}}, nil
	}
	return nil, nil
}

func TestDiscoveryReusesAuthorizationOnlyWithinOneRequest(t *testing.T) {
	search := &discoverySearch{}
	server := &Server{
		config: Config{ControlTimeout: time.Second, BusinessDomains: []runtimeconfig.BusinessDomain{{Tenant: "demo", Namespace: "demo", DisplayName: "Demo"}}},
		deps:   Dependencies{CatalogSearch: search},
	}
	for _, subject := range []string{"allowed", "denied"} {
		backend := &remoteMCPBackend{
			server: server, request: httptest.NewRequest("POST", "/mcp", nil),
			principal: Principal{Tenant: "demo", Subject: subject, Permissions: []Permission{PermissionQuery}},
		}
		names, err := backend.ListNamespaces(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if subject == "allowed" {
			want = 1
		}
		if len(names) != want {
			t.Fatalf("names for %s = %v", subject, names)
		}
		if len(names) > 0 {
			names[0] = "caller-mutated-copy"
		}
		domains, err := backend.ListBusinessDomains(t.Context())
		if err != nil || len(domains) != want {
			t.Fatalf("domains for %s = %v, %v", subject, domains, err)
		}
	}
	if len(search.scopes) != 2 || search.scopes[0].Context.Principal != "allowed" || search.scopes[1].Context.Principal != "denied" {
		t.Fatalf("authorization should run once per request and identity: %#v", search.scopes)
	}
}
