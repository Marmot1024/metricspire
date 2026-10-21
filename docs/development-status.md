# Development status and verification boundary

As of **2026-10-21**, MetricSpire is a development preview. A bounded pilot needs acceptance by the intended users; current evidence does not establish production readiness. Code, deployment, successful queries and business certification are separate facts.

| Area | Verified scope | Still needs acceptance |
| --- | --- | --- |
| MCP prepared-query workflow | Regression tests cover compact ranked discovery, batched details, authorized category discovery, prepared-plan checks before asynchronous execution, bounded waiting and preservation of accepted job IDs. | Intended-user onboarding, company semantic catalogs and end-to-end latency. Fewer orchestration steps do not establish a measured live speedup. |
| Semantic core and governance | Structured compilation, policy/budgets, immutable releases, rollback and deactivation are implemented. PostgreSQL and MySQL lifecycle/HTTP governance checks cover transactional publication and immutable history. | Complete expression editing, real business-definition review and deployment-specific operational acceptance. UI regression tests do not establish browser rendering performance. |
| Native MCP and metric queries | The earlier native OAuth route loaded seven core tools plus five exploration tools on staging. The public TPCH fixture matched all five typed rows; a bounded business metric matched its pre-deployment result. Native cancellation and invalid-reference/row-limit rejection passed. | Another user's first installation/query and permission isolation; other MCP clients; business-definition certification. |
| Exploration | Deployed public-calendar native MCP checks passed weekly/partial-week grouping, conditions, two stages, share and weekend filtering, cancellation and sealed-result behavior, plus window/row/input rejection. Selected metric and exploration jobs have paired durable start/terminal audit records. | Company catalogs, associations and other expression combinations under deployed user permissions; execution timeout and concurrent-load limits. Public-calendar success does not certify all exploration features. |
| Online serving (HTTP and optional native MCP) | Earlier staging acceptance passed exact typed synthetic results, weighted totals, tenant-row exclusion and input rejection. Expired snapshots failed closed; refreshing the existing fixture restored successful reads. Selected successful and stale-failure requests have paired durable audit records. Twenty sequential requests passed the 1s p95 gate (443ms). Isolated PostgreSQL checks also covered roles, batch guards, rollback, audit failures, pool exhaustion and recovery. | Real business snapshots and refresh pipeline; second-user isolation; deployed concurrent load, cold starts, cost and availability. The small sequential sample is not a production SLA. |
| Structured metric scope | Version-bound selections reject missing/stale choices for definitions that require them. Candidate comparison exposes population, exclusions and other declared differences without choosing by rank. | Business review of definitions and the intended user's understanding. Role/history recommendations cannot silently select a meaning. See [metric definitions](metric-definitions.md). |
| PostgreSQL snapshot refresh | Typed batch export, independent reconciliation, checksums, atomic tenant replacement, idempotency, forward watermarks and rollback passed disposable synthetic acceptance, including a ten-million-row batch. | Real upstream extraction/scheduling, business reconciliation and deployed freshness. The tooling does not automatically extract Databricks/Hive data. See [refresh contract](online-refresh.md). |
| Independent exploration | Trino/Presto adapters support reviewed single-table DATE catalogs, bounded asynchronous jobs and existing HTTP/MCP authorization. Disposable Trino 484 and Presto 0.299 acceptance passed public TPCH exact results through OIDC/native MCP and durable audit; protocol failure tests cover cancellation and result bounds. | Real connector permissions, timestamp and relationship support, company catalogs and production load. See [engine contract](trino-presto.md). |
| MySQL metadata | Disposable MySQL 8.4 covers migration install/upgrade/repeat and dirty/checksum rejection, optimistic concurrency, transactional publication failures and governance rollback. OIDC native MCP serving uses separate PostgreSQL result data and durable MySQL audit. | Enterprise deployment, operational backup/restore and intended-user acceptance. PostgreSQL-to-MySQL data migration is not supplied. |
| Portable Doris serving | Disposable Doris 4.1.0 FE/BE covers native metadata types, explicit SELECT grants/write denial, effective query hints, typed integer/boolean/DATE/string filters, snapshot guards and full service OIDC/native MCP with PostgreSQL metadata. | Enterprise TLS/IdP, real snapshots, other Doris versions and production concurrency/SLA. Only bounded daily single-table aggregates are supported; no Doris producer or exploration. |
| Portable ClickHouse serving | Disposable ClickHouse 25.8 + PostgreSQL 17 passed full service startup, OIDC/JWT, exact aggregates, structured selections, denial and persisted audit. Adapter checks covered least-privilege grants, enforced server budgets, numeric extremes and NULLs. A ten-million-row synthetic batch produced exact sums/counts/ratios within the fixed read budget after producer merges settled. | Enterprise IdP, intended-user onboarding, production snapshots and deployed concurrency/SLA. ClickHouse exploration remains unsupported; native MCP online serving uses the same bounded query service. |
| Installer and release gate | Installer checks native initialization, seven core tools and an actual namespace call. Release tooling checks scopes/metadata, native MCP version, fixture queries and exact results; it preserves frozen config and has a rollback path. | Second-user and cross-platform runtime acceptance; cloud deployment version rollback. Disposable service acceptance forces a freshness failure, restores frozen configuration and checks the same typed result/release/batch after restart; deployment orchestration also tests lost responses and concurrent releases. Saved authorization alone is not success. |
| Independent OIDC onboarding | Native installer validates reviewed metadata/PKCE configuration. Real local Codex with a synthetic issuer covered expiry refresh, rotation across client restarts, revocation and independent identities. Full service fixtures separately cover JWT validation and policy denial. | An intended colleague's first installation, enterprise IdP configuration and other platform runtimes. Synthetic identities do not replace those checks. See [OIDC onboarding](oidc-mcp.md). |
| Authentication migration | Native OAuth is preserved on a backup branch after repeated loss of tool access. The installed local Codex CLI helper reused existing Databricks U2M authorization, loaded 12 tools and returned exact TPCH/calendar results without login. Real Codex recovered from synthetic 401/403 responses on the same connection. | Second-user onboarding, revoked-grant recovery and cross-platform runtime. Each environment must retain its own serving-version and natural-expiry receipts; account scope or TTL causes are not inferred from symptoms. |

The [TPCH](../testdata/acceptance/databricks-tpch) and [calendar](../testdata/acceptance/exploration-calendar) fixtures are neutral acceptance data. Trial metrics remain unverified; matching a previous business result is regression evidence, not certification. Earlier staging checks covered health, UI assets and authenticated catalog/context responses. These historical receipts do not attest the currently deployed revision; each release must check it again. Browser interaction and rendering performance remain untested.

Use [native acceptance](mcp-native-acceptance.md) for the release gate, [installer guidance](mcp-installer.md) for onboarding, [exploration](exploration.md) for query limits and [online query](online-query.md) for synchronous serving. Exact environment revisions, job receipts and prioritized tasks belong in the private handoff, not duplicate public status logs.

Before rollout, complete discovery → plan → submit → successful result with an intended colleague, reconcile agreed business definitions and exercise permission denial. Authentication lifecycle acceptance remains separate from functional checks. Async jobs/results are process-local and do not survive restart; no multi-instance job continuity or production SLA is promised. Production deployment and production MCP acceptance are not established.


## Private configuration and metric scope

Private `domains_path` configuration supplies tenant-scoped business aliases and
checksum-verified exported analysis catalogs. `validate-domains` is offline;
repository provenance is metadata, not Git synchronization or an exporter.
See [team configuration](../config/README.md). MCP candidate comparison reports
actual metadata differences and missing descriptions without inferring a
population, choosing a role default or treating search rank as semantic fit.

Alias configuration does not establish deployed-client latency, production
acceptance, automatic Git export or business-data refresh. Implemented engines
and their limits are listed in [runtime engines](runtime-engines.md).

## Query attribution and catalog scale

Terminal query jobs and successful online HTTP/MCP results expose separate
planning, engine, audit and total service wall times; see [timing boundaries](mcp.md#limits).
They cannot measure hidden model reasoning or prove an end-to-end improvement.
Generated catalogs of 50, 500 and 1000 metrics are checked page by page for exact
counts, unique entries, cursor progress and representative detail completeness,
using memory and real PostgreSQL metadata over loopback HTTP. Logged page/detail
durations include response-body reading and decoding. The 1000-entry service cap
still conservatively reports possible truncation; these fixtures are not browser
rendering, concurrent-load or production SLA acceptance.
