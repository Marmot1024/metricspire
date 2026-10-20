# Trino and Presto exploration

These optional `AnalysisEngine` adapters execute temporary structured analyses
over reviewed single-table catalogs. They do not execute arbitrary SQL, query
published metrics or switch engines after a failure. Existing online PG/ClickHouse
results and PostgreSQL metadata remain independently configured.

Start from `examples/runtime.postgres.example.yaml`, keep `query_engine.driver:
disabled`, and add this deployment-owned section:

```yaml
exploration:
  driver: trino # or presto; the catalog engine must match
  connection_env: METRICSPIRE_EXPLORATION_URL
  user: metricspire_reader
  catalog_paths: [private/analysis-catalog.yaml]
  concurrency: 2
  capacity: 20
  result_ttl: 10m
```

Set the named environment variable through your secret store to an engine HTTPS
origin. Basic authentication can be included as URL credentials; these must use
HTTPS. Only unauthenticated loopback HTTP is accepted for disposable tests. The
adapter uses a dedicated operator-configured engine user. End-user identity is
verified by MetricSpire OIDC and authorized against the catalog before execution;
the user's OIDC/Databricks token is never forwarded to the engine. Trino/Presto
connectors must restrict that engine identity to the reviewed read-only sources.
Do not use an administrator connection. This adapter does not provision grants
or claim to attest arbitrary connector privileges.

The reviewed catalog must set `engine: trino` or `engine: presto` and use fully
qualified catalog/schema/table resources. It otherwise uses the existing
[analysis catalog](exploration.md) schema. Physical mappings and business
definitions stay in your private semantic repository, exported and reviewed
before deployment. No Git URL or SQL belongs in chat input.

Supported features are DATE ranges in the declared business calendar, day/week/
month grouping, typed predicates and CASE, conditional measures, exact numeric
aggregates, two aggregation stages and final-stage share. The final LIMIT applies
after aggregation and window denominators. Decimal results stay strings and
integers remain signed 64-bit values. Decimal calculations use the engine's
native precision/scale and overflow rules; repeating division is rounded at that
scale. Integer averages are promoted to DECIMAL(38,12), integer division/share
to DECIMAL(38,18); existing decimal inputs are never forcibly rescaled. These
operators do not promise arbitrary-precision rational arithmetic. Native decimal
division can differ between engines: the scale-20 share fixture produces 18
fractional digits on Trino 484 and 20 on Presto 0.299, both retained exactly. Inexact engine DOUBLE/REAL output cannot
satisfy a decimal contract: review connector types and rounding explicitly rather
than disguising float results as exact values.

Catalogs with relationships or timestamp fields fail startup. Cross-engine joins,
caller-selected tables, timestamp timezone conversion and arbitrary functions
are unsupported. Omitted catalog engine retains Databricks behavior for existing
deployments. Legacy catalog fingerprints remain unchanged when engine is omitted.

Execution uses the catalog timeout (at most 15 minutes), engine run/execution
timeouts, 256MB query memory, bounded pages and at most 10,000 result rows / 4MiB.
Trino also receives a 1GB physical-scan limit; Presto requires the operator's
resource-group/connector scan limits. Time and byte limits do not promise a fixed
warehouse bill. Continuations must stay on the configured origin and statement
path, redirects are refused, late engine errors invalidate previous rows and
cancellation sends a bounded DELETE. Ambiguous submission errors are never
automatically retried. Raw engine errors, SQL and credentials are not returned.

Use `discover_analysis` → `plan_analysis` → `submit_analysis` → `get_analysis`
through native MCP, retaining the returned job ID. Ordinary metric discovery,
scope selection and confirmation of missing metric coverage still precede
exploration. Jobs/results are process-local and expire after the configured TTL.

The [disposable lab](../deploy/testing/README.md) supplies Trino 484 and Presto
0.299 with public TPCH data, plus PostgreSQL metadata. Tests cover independent
SQL-oracle results and native OIDC/MCP startup without Databricks variables.
These checks certify the synthetic path, not your Hive/HDFS/object-store
connector, enterprise IdP, business definitions or production performance.
