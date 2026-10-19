package httpapi

import (
	"context"

	"github.com/marmot1024/metricspire/internal/mcpbridge"
)

func (b *remoteMCPBackend) ListBusinessDomains(ctx context.Context) ([]mcpbridge.DomainView, error) {
	if len(b.server.config.BusinessDomains) == 0 {
		return []mcpbridge.DomainView{}, nil
	}
	names, err := b.ListNamespaces(ctx)
	if err != nil {
		return nil, err // ListNamespaces already sanitizes and classifies errors.
	}
	views := []mcpbridge.DomainView{}
	allowed := map[string]bool{}
	for _, name := range names {
		allowed[name] = true
	}
	for _, d := range b.server.config.BusinessDomains {
		if d.Tenant != b.principal.Tenant {
			continue
		}
		// An alias is not an access grant.
		if allowed[d.Namespace] {
			views = append(views, mcpbridge.DomainView{Namespace: d.Namespace, DisplayName: d.DisplayName, Aliases: append([]string(nil), d.Aliases...)})
		}
	}
	return views, nil
}
