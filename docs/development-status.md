# Development status and verification boundary

As of **2026-09-18**, MetricSpire is a development preview, not a production-ready release. This page separates implemented behavior, real staging evidence, and work still needing acceptance. Environment-specific URLs, user identities, credentials, and private business schemas are intentionally omitted.

| Area | Current evidence | Not yet established |
| --- | --- | --- |
| Semantic core | Compilation, deterministic plans, immutable publication/rollback, policy, time and query budgets covered by tests. | A stable `v0.1.0` public release and upgrade contract. |
| HTTP/UI | Real Databricks Apps staging publisher and ordinary-user paths; UI and metric-first query route smoke-tested. | Self-hosted deployment against an external OIDC provider. |
| Engine | Staging Databricks SQL reads, bounded results, audit, cancellation, and user-token forwarding. | A controlled least-privilege Unity Catalog denial and any second engine adapter. |
| MCP | Hosted seven-tool contract and real staging queries tested. Codex public-client OAuth browser login plus `list_namespaces` succeeded with the formal `metricspire` connection. | OAuth query submission, post-expiry refresh, second-user isolation, and Claude Code native OAuth acceptance. |
| Production | A production Databricks App name and URL were reserved during setup. | No production deployment or MCP acceptance is claimed; recheck live resource state before rollout. |

The neutral fixture in [`testdata/acceptance/databricks-tpch`](../testdata/acceptance/databricks-tpch) is test data, not business-data certification. Staging-only test publication of unverified definitions remains explicitly labeled and is not business verification.

The pre-registered public OAuth client and exact redirect URI allowed a real Codex namespace tool call. This proves one user/client path, not universal onboarding or automatic token refresh. [MCP integration](mcp.md) explains the public connection boundary; the source-adjacent [Databricks Apps guide](../deploy/databricks-apps/README.md) is for staging maintainers.

## Release boundary

Before production use, repeat deployment-specific identity, scope, data-permission, cost, audit, query, cancellation, and failure-path checks against the intended resources and users. A build, mock test, HTTP 200, OAuth login, or one successful catalog call alone is not that acceptance.
