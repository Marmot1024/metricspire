package httpapi_test

import (
	"strings"
	"testing"

	"github.com/marmot1024/metricspire/internal/httpapi"
	"github.com/marmot1024/metricspire/internal/runtimeconfig"
)

func TestMCPBusinessAliasesAreAuthorizedAndDoNotExposeSources(t *testing.T) {
	h, engine, _ := newTestServer(t, httpapi.Config{BusinessDomains: []runtimeconfig.BusinessDomain{
		{Tenant: "demo", Namespace: "demo", DisplayName: "Demo Commerce", Aliases: []string{"Orders"}, SemanticSource: &runtimeconfig.SemanticSourceRef{Repository: "https://github.com/private/definitions", Revision: strings.Repeat("a", 40)}},
		{Tenant: "other", Namespace: "demo", DisplayName: "Other tenant secret"},
		{Tenant: "demo", Namespace: "inaccessible", DisplayName: "Inaccessible secret"},
	}})
	s := connectRemoteMCP(t, h, "query")
	text := callRemoteMCP(t, s, "list_namespaces", map[string]any{}, false)
	if !strings.Contains(text, "Demo Commerce") || !strings.Contains(text, "Orders") {
		t.Fatal("missing authorized business aliases:", text)
	}
	for _, forbidden := range []string{"Other tenant secret", "Inaccessible secret", "github.com", "revision", "semantic_source"} {
		if strings.Contains(text, forbidden) {
			t.Fatal("business discovery leaked", forbidden)
		}
	}
	if engine.executionCount() != 0 {
		t.Fatal("discovery executed a data query")
	}
}
