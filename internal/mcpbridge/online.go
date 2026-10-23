package mcpbridge

import (
	"context"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// OnlineBackend is optional; analytical-only deployments retain their tool set.
type OnlineBackend interface {
	OnlineEnabled() bool
	QueryOnlineMetrics(context.Context, OnlineQueryInput) (application.OnlineResult, error)
}

type OnlineQueryInput struct {
	Namespace string                  `json:"namespace"`
	Query     application.OnlineQuery `json:"query" jsonschema:"Use numeric external_code values from get_metric_details; metric_selections maps those codes to the reviewed manifest_fingerprint. Resolve meaning/population/exclusions before execution. Required bounded time_range; no SQL, identity or physical source."`
}

func addOnlineTools(s *mcp.Server, b OnlineBackend) {
	add(s, "query_online_metrics", "Read matching certified metric results from configured online serving. Use only after comparing definitions and resolving user intent. Numeric external codes, version-bound selections and a time range are required. Returns typed values, release and data snapshot; no analytical fallback. Freshness/permission/connection errors stop this route. An explicit refresh needs a new authorized read; a previous snapshot proves neither current freshness nor current access.", false, func(ctx context.Context, in OnlineQueryInput) (any, error) {
		if _, err := namespacePath(in.Namespace); err != nil {
			return nil, err
		}
		return b.QueryOnlineMetrics(ctx, in)
	})
}
