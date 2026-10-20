package httpapi

import (
	"context"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/mcpbridge"
	"github.com/marmot1024/metricspire/internal/model"
)

func (b *remoteMCPBackend) OnlineEnabled() bool { return b.server.deps.Online != nil }

func (b *remoteMCPBackend) QueryOnlineMetrics(ctx context.Context, in mcpbridge.OnlineQueryInput) (application.OnlineResult, error) {
	if !b.OnlineEnabled() {
		return application.OnlineResult{}, b.safeError(queryEngineDisabled())
	}
	ctx, cancel := context.WithTimeout(ctx, b.server.config.OnlineTimeout)
	defer cancel()
	out, err := b.server.deps.Online.Execute(ctx, application.QueryScope{Namespace: in.Namespace, Context: b.requestContext()}, in.Query)
	// A dependency must not turn a timed-out request into apparent success.
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return out, b.safeError(err)
}

// Choosing a metadata route cannot grant execution access; every service still
// authorizes the trusted caller. Never reuse analytical policies for online discovery.
func (b *remoteMCPBackend) metricCatalog(route string) (CatalogSearcher, error) {
	switch route {
	case "", "published_metrics":
		return b.server.deps.CatalogSearch, nil
	case "online_metrics":
		if b.server.deps.OnlineCatalogSearch != nil {
			return b.server.deps.OnlineCatalogSearch, nil
		}
		return nil, &model.Problem{Code: "online_not_enabled", Message: "online catalog is not configured"}
	default:
		return nil, &model.Problem{Code: "invalid_query", Path: "route", Message: "route must be published_metrics or online_metrics"}
	}
}
