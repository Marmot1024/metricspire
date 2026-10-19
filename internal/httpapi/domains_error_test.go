package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/runtimeconfig"
)

type failingDomainSearch struct {
	CatalogSearcher
	err error
}

func (s failingDomainSearch) SearchActive(context.Context, application.QueryScope, string, int) ([]application.MetricCatalogEntry, error) {
	return nil, s.err
}

func TestBusinessDomainDiscoveryPreservesSearchErrorClassification(t *testing.T) {
	for _, test := range []struct {
		code string
		err  error
	}{
		{"timeout", context.DeadlineExceeded},
		{"permission_denied", &model.Problem{Code: "permission_denied", Message: "private upstream detail"}},
	} {
		t.Run(test.code, func(t *testing.T) {
			backend := &remoteMCPBackend{
				principal: Principal{Tenant: "demo", Permissions: []Permission{PermissionQuery}},
				request:   httptest.NewRequest("POST", "/api/v1/mcp", nil),
				server: &Server{
					config: Config{ControlTimeout: time.Second, BusinessDomains: []runtimeconfig.BusinessDomain{{Tenant: "demo", Namespace: "demo", DisplayName: "Demo"}}},
					deps:   Dependencies{CatalogSearch: failingDomainSearch{err: test.err}},
					logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
				},
			}
			names, err := backend.ListNamespaces(t.Context())
			if err == nil || !strings.Contains(err.Error(), test.code) || strings.Contains(err.Error(), "private upstream detail") || len(names) != 0 {
				t.Fatalf("expected classified namespace failure; names=%v error=%v", names, err)
			}
			views, err := backend.ListBusinessDomains(t.Context())
			if err == nil || !strings.Contains(err.Error(), test.code) || strings.Contains(err.Error(), "private upstream detail") || len(views) != 0 {
				t.Fatalf("expected classified, sanitized failure with no domains; views=%v error=%v", views, err)
			}
		})
	}
}
