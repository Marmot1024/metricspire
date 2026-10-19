package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/marmot1024/metricspire/internal/httpapi"
	"github.com/marmot1024/metricspire/internal/mcpbridge"
	"github.com/marmot1024/metricspire/internal/runtimeconfig"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNamespaceDiscoveryFiltersTenantRoleAndPhysicalDefaults(t *testing.T) {
	for _, test := range []struct {
		name       string
		principal  httpapi.Principal
		namespaces []string
		models     int
	}{
		{"query", httpapi.Principal{Tenant: "demo", Roles: []string{"analyst"}, Permissions: []httpapi.Permission{httpapi.PermissionQuery}}, []string{"demo"}, 0},
		{"other tenant", httpapi.Principal{Tenant: "other", Roles: []string{"analyst"}, Permissions: []httpapi.Permission{httpapi.PermissionQuery}}, []string{}, 0},
		{"denied role", httpapi.Principal{Tenant: "demo", Roles: []string{"outsider"}, Permissions: []httpapi.Permission{httpapi.PermissionQuery}}, []string{}, 0},
		{"no permission", httpapi.Principal{Tenant: "demo", Roles: []string{"analyst"}}, []string{}, 0},
		{"manager", httpapi.Principal{Tenant: "demo", Permissions: []httpapi.Permission{httpapi.PermissionManage}}, []string{"demo", "draft_only"}, 2},
		{"manager and query", httpapi.Principal{Tenant: "demo", Roles: []string{"analyst"}, Permissions: []httpapi.Permission{httpapi.PermissionManage, httpapi.PermissionQuery}}, []string{"demo", "draft_only"}, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := httpapi.Config{
				UIModels: []httpapi.UIModelRoute{
					{Tenant: "other", Namespace: "other_namespace", ModelName: "private_model"},
					{Tenant: "demo", Namespace: "demo", ModelName: "commerce"},
					{Tenant: "demo", Namespace: "draft_only", ModelName: "unpublished"},
					{Namespace: "unscoped", ModelName: "legacy"},
				},
				UISourcePrefixes: map[string]string{"demo": "demo_catalog.analytics", "other_namespace": "other_catalog.analytics", "unscoped": "unscoped_catalog.analytics"},
				BusinessDomains: []runtimeconfig.BusinessDomain{
					{Tenant: "demo", Namespace: "demo", DisplayName: "Demo Orders"},
					{Tenant: "other", Namespace: "other_namespace", DisplayName: "Other private alias"},
					{Tenant: "demo", Namespace: "draft_only", DisplayName: "Unpublished alias"},
				},
			}
			auth := httpapi.AuthenticatorFunc(func(context.Context, *http.Request) (httpapi.Principal, error) {
				p := test.principal
				p.Subject = "discovery-test"
				return p, nil
			})
			h, engine, observed := newTestServerWithAuthentication(t, config, nil, nil, nil, nil, auth)
			response := performJSON(h, http.MethodGet, "/api/v1/ui/context", "query", nil)
			if response.StatusCode != http.StatusOK {
				t.Fatalf("UI context status = %d: %s", response.StatusCode, readBody(t, response))
			}
			var result httpapi.UIContext
			decodeResponse(t, response, &result)
			if !reflect.DeepEqual(result.Namespaces, test.namespaces) || len(result.Models) != test.models {
				t.Fatalf("UI context = %#v; want namespaces %v, models %d", result, test.namespaces, test.models)
			}
			if test.models == 0 && len(result.SourcePrefixes) != 0 {
				t.Fatal("query UI exposed physical defaults")
			}
			if test.models > 0 && (len(result.SourcePrefixes) != 1 || result.SourcePrefixes["demo"] != "demo_catalog.analytics") {
				t.Fatalf("manager defaults not scoped: %#v", result.SourcePrefixes)
			}
			if test.principal.Has(httpapi.PermissionQuery) {
				session := connectRemoteMCP(t, h, "query")
				text := callRemoteMCP(t, session, "list_namespaces", map[string]any{}, false)
				var discovery struct {
					Namespaces []string `json:"namespaces"`
				}
				if err := json.Unmarshal([]byte(text), &discovery); err != nil {
					t.Fatal(err)
				}
				want := []string{}
				if test.principal.Tenant == "demo" && test.name != "denied role" {
					want = []string{"demo"}
				}
				if !reflect.DeepEqual(discovery.Namespaces, want) {
					t.Fatalf("MCP namespaces = %v, want %v", discovery.Namespaces, want)
				}
				for _, forbidden := range []string{"other_namespace", "draft_only", "unscoped", "Other private alias", "Unpublished alias", "catalog", "private_model"} {
					if strings.Contains(text, forbidden) {
						t.Fatalf("MCP exposed %q", forbidden)
					}
				}
			}
			if engine.executionCount() != 0 || observed.bindingCalls != 0 {
				t.Fatal("namespace discovery queried data or loaded physical bindings")
			}
		})
	}
}

func TestLegacyMCPBridgeUsesAuthorizedNamespaceContext(t *testing.T) {
	h, engine, _ := newTestServer(t, httpapi.Config{
		UIModels: []httpapi.UIModelRoute{
			{Tenant: "other", Namespace: "other_namespace", ModelName: "private_model"},
			{Tenant: "demo", Namespace: "demo", ModelName: "commerce"},
		},
		UISourcePrefixes: map[string]string{"demo": "private_catalog.analytics"},
	})
	api := httptest.NewServer(h)
	t.Cleanup(api.Close)
	bridge, err := mcpbridge.New(api.URL, "query", "test")
	if err != nil {
		t.Fatal(err)
	}
	a, b := mcp.NewInMemoryTransports()
	serverSession, err := bridge.Connect(t.Context(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "discovery-test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), b, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	text := callRemoteMCP(t, session, "list_namespaces", map[string]any{}, false)
	var result struct {
		Namespaces []string `json:"namespaces"`
	}
	if err := json.Unmarshal([]byte(text), &result); err != nil || !reflect.DeepEqual(result.Namespaces, []string{"demo"}) {
		t.Fatalf("legacy discovery = %s, err=%v", text, err)
	}
	if engine.executionCount() != 0 {
		t.Fatal("legacy namespace discovery queried the engine")
	}
}
