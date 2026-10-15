package httpapi

import (
	"context"

	"github.com/marmot1024/metricspire/internal/mcpbridge"
)

func (b *remoteMCPBackend) ListBusinessDomains(ctx context.Context) ([]mcpbridge.DomainView, error) {
	ctx, cancel := context.WithTimeout(ctx, b.server.config.ControlTimeout)
	defer cancel()
	views := []mcpbridge.DomainView{}
	analysis := map[string]bool{}
	if b.server.deps.AnalysisCatalogs != nil {
		names, err := b.server.deps.AnalysisCatalogs.Namespaces(ctx, b.requestContext())
		if err != nil {
			return nil, b.safeError(err)
		}
		for _, name := range names {
			analysis[name] = true
		}
	}
	for _, d := range b.server.config.BusinessDomains {
		if d.Tenant != b.principal.Tenant {
			continue
		}
		// An alias is not an access grant; do not expose another tenant's or
		// an entirely unauthorized domain's names even if it is configured.
		allowed := analysis[d.Namespace]
		if !allowed {
			entries, err := b.SearchMetrics(ctx, mcpbridge.SearchInput{Namespace: d.Namespace, Limit: 1})
			if err != nil {
				return nil, err // SearchMetrics already sanitizes and classifies errors.
			}
			allowed = len(entries) > 0
		}
		if allowed {
			views = append(views, mcpbridge.DomainView{Namespace: d.Namespace, DisplayName: d.DisplayName, Aliases: append([]string(nil), d.Aliases...)})
		}
	}
	return views, nil
}
