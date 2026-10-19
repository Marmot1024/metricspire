package httpapi

import (
	"context"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/model"
)

// queryNamespaces applies the same tenant/role policy as metric and analysis
// discovery. Configured routes and aliases are candidates, never access grants.
// Return raw errors here so each transport sanitizes them exactly once.
func (s *Server) queryNamespaces(ctx context.Context, principal Principal, rc model.RequestContext) ([]string, error) {
	names := []string{}
	if !principal.Has(PermissionQuery) {
		return names, nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.ControlTimeout)
	defer cancel()
	analysis := map[string]bool{}
	if s.deps.AnalysisCatalogs != nil {
		extra, err := s.deps.AnalysisCatalogs.Namespaces(ctx, rc)
		if err != nil {
			return nil, err
		}
		for _, name := range extra {
			analysis[name] = true
		}
		names = appendUniqueNamespaces(names, extra...)
	}
	candidates := configuredUINamespaces(s.config.UIModels)
	for _, domain := range s.config.BusinessDomains {
		if domain.Tenant == principal.Tenant {
			candidates = appendUniqueNamespaces(candidates, domain.Namespace)
		}
	}
	search := s.deps.CatalogSearch.SearchActive
	if summary, ok := s.deps.CatalogSearch.(interface {
		SearchActiveSummary(context.Context, application.QueryScope, string, int) ([]application.MetricCatalogEntry, error)
	}); ok {
		search = summary.SearchActiveSummary
	}
	for _, namespace := range candidates {
		if analysis[namespace] {
			continue
		}
		entries, err := search(ctx, application.QueryScope{Namespace: namespace, Context: rc}, "", 1)
		if err != nil {
			return nil, err
		}
		if len(entries) > 0 {
			names = appendUniqueNamespaces(names, namespace)
		}
	}
	return names, nil
}

func appendUniqueNamespaces(names []string, extra ...string) []string {
	seen := make(map[string]bool, len(names)+len(extra))
	for _, name := range names {
		seen[name] = true
	}
	for _, name := range extra {
		if !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	return names
}
