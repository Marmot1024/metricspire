# Development status and verification boundary

As of **2026-10-20**, MetricSpire is a development preview. A bounded pilot needs acceptance by the intended users; current evidence does not establish production readiness. Code, deployment, successful queries and business certification are separate facts.

| Area | Verified scope | Still needs acceptance |
| --- | --- | --- |
| MCP prepared-query workflow | Local fixture regression covers compact ranked discovery, batched details, authorized category discovery, prepared-plan checks before asynchronous execution, bounded waiting and preservation of accepted job IDs. | Deployment, fresh-window client behavior and end-to-end latency; real external semantic-layer exploratory acceptance remains separate and pending. Fewer orchestration steps do not establish a measured live speedup. |
| Semantic core and governance | Structured compilation, policy/budgets, immutable releases, rollback and deactivation are implemented. Current local PostgreSQL lifecycle and HTTP governance tests pass; staging governance evidence is historical. | Current UI regression, complete expression editing, upgrade contract and external OIDC deployment. |
| Native MCP and metric queries | The earlier native OAuth route loaded seven core tools plus five exploration tools on staging. The public TPCH fixture matched all five typed rows; a bounded business metric matched its pre-deployment result. Native cancellation and invalid-reference/row-limit rejection passed. | Another user's first installation/query and permission isolation; other MCP clients; business-definition certification. |
| Exploration | Deployed public-calendar native MCP checks passed weekly/partial-week grouping, conditions, two stages, share and weekend filtering, cancellation and sealed-result behavior, plus window/row/input rejection. Selected metric and exploration jobs have paired durable start/terminal audit records. | Company catalogs, associations and other expression combinations under deployed user permissions; execution timeout and concurrent-load limits. Public-calendar success does not certify all exploration features. |
| Online serving (HTTP and optional native MCP) | Latest staging passed exact typed synthetic results, weighted totals, tenant-row exclusion and input rejection. Expired snapshots failed closed; refreshing the existing fixture restored successful reads. Selected successful and stale-failure requests have paired durable audit records. Twenty sequential requests passed the 1s p95 gate (443ms). Isolated PostgreSQL checks also covered roles, batch guards, rollback, audit failures, pool exhaustion and recovery. | Real business snapshots and refresh pipeline; second-user isolation; deployed concurrent load, cold starts, cost and availability. The small sequential sample is not a production SLA. |
| Independent exploration | Trino/Presto adapters support reviewed single-table DATE catalogs, bounded asynchronous jobs and existing HTTP/MCP authorization. Public TPCH fixtures and protocol failure tests are supplied. | Real connector permissions, timestamp and relationship support, company catalogs and production load. See [engine contract](trino-presto.md). |
| Portable ClickHouse serving | Disposable ClickHouse 25.8 + PostgreSQL 17 passed full service startup, OIDC/JWT, exact aggregates, structured selections, denial and persisted audit. Adapter checks covered least-privilege grants, enforced server budgets, numeric extremes and NULLs. A ten-million-row synthetic batch produced exact sums/counts/ratios within the fixed read budget after producer merges settled. | Enterprise IdP, intended-user onboarding, production snapshots and deployed concurrency/SLA. ClickHouse exploration remains unsupported; native MCP online serving uses the same bounded query service. |
| Installer and release gate | Installer checks native initialization, seven core tools and an actual namespace call. Release tooling checks scopes/metadata, native MCP version, fixture queries and exact results; it preserves frozen config and has a rollback path. | Second-user and cross-platform runtime acceptance; real failure-triggered release rollback. Saved authorization alone is not success. |
| Authentication migration | Native OAuth is preserved on a backup branch after repeated loss of tool access. The installed local Codex CLI helper reused existing Databricks U2M authorization, loaded 12 tools and returned exact TPCH/calendar results without login. Real Codex recovered from synthetic 401/403 responses on the same connection. | Second-user onboarding, revoked-grant recovery and cross-platform runtime. Each environment must retain its own serving-version and natural-expiry receipts; account scope or TTL causes are not inferred from symptoms. |

The [TPCH](../testdata/acceptance/databricks-tpch) and [calendar](../testdata/acceptance/exploration-calendar) fixtures are neutral acceptance data. Trial metrics remain unverified; matching a previous business result is regression evidence, not certification. Current staging health, UI assets and authenticated catalog/context routes responded successfully; browser interaction and rendering performance remain untested. Historical UI measurements are not fresh results for the current deployment.

Use [native acceptance](mcp-native-acceptance.md) for the release gate, [installer guidance](mcp-installer.md) for onboarding, [exploration](exploration.md) for query limits and [online query](online-query.md) for synchronous serving. Exact environment revisions, job receipts and prioritized tasks belong in the private handoff, not duplicate public status logs.

Before rollout, complete discovery → plan → submit → successful result with an intended colleague, reconcile agreed business definitions and exercise permission denial. Authentication lifecycle acceptance remains separate from functional checks. Async jobs/results are process-local and do not survive restart; no multi-instance job continuity or production SLA is promised. Production deployment and production MCP acceptance are not established.


## Local configuration and MCP contract additions

Private `domains_path` configuration supplies tenant-scoped business aliases and
checksum-verified exported analysis catalogs. `validate-domains` is offline;
repository provenance is metadata, not Git synchronization or an exporter.
See [team configuration](../config/README.md). MCP candidate comparison reports
actual metadata differences and missing descriptions without inferring a
population, choosing a role default or treating search rank as semantic fit.

These local changes do not establish deployed-client latency, production
acceptance, automatic Git export, additional engines or data refresh pipelines.
