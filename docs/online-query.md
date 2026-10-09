# PostgreSQL online query (opt-in preview)

This is the small, synchronous data-service path: numeric metric codes and dimensions in, typed results out. It reads reviewed precomputed aggregates in PostgreSQL. It does not call or fall back to a SQL Warehouse, create a query job, or require MCP.

## Contract

`POST /api/v1/namespaces/{namespace}/online-query` requires `query:execute`. The endpoint is disabled unless an administrator explicitly configures it. The existing name-based analytical endpoints are unchanged.

```json
{
  "metric_codes": ["10001", "10002", "10003"],
  "dimensions": ["date"],
  "time_range": {
    "dimension": "date",
    "start": "2026-09-01T00:00:00Z",
    "end": "2026-09-03T00:00:00Z",
    "timezone": "UTC"
  },
  "time_grouping": {"dimension": "date", "granularity": "day", "timezone": "UTC"},
  "filters": [{"dimension": "region", "operator": "eq", "values": ["east"]}],
  "limit": 100
}
```

Use numeric **strings**, retaining leading zeroes. Codes must resolve uniquely within the namespace to one active certified model. Up to 32 distinct output metrics are allowed. Derived metrics expand their dependencies into the same SQL statement; average order value is `SUM(revenue) / SUM(orders)`, not the average of daily ratios. A zero denominator returns null. Cross-model requests, joins, distinct counts and unsupported expressions are rejected, not silently routed elsewhere. Existing publication compatibility checks protect assigned external codes from changes and allow descriptive wording to evolve. A namespace-wide allocation/reservation registry is not implemented: ambiguous active codes fail at query time, rather than choosing an arbitrary model.

The synchronous `200` body contains `release_id`, `manifest_fingerprint`, `data_snapshot` (`batch_id`, `data_as_of`) and `result` (typed `columns`, `rows`, `truncated=false`). Output metric column names are the requested numeric codes; decimal values are strings to preserve precision. No SQL or physical binding is exposed.

Request identity, table, release and engine cannot be supplied in the body. Existing strict JSON decoding and problem responses apply. Overload is `429 online_overloaded` with `Retry-After: 1`. Disabled serving is `404 online_not_enabled`. Missing/incompatible/partial data is `503 online_data_unavailable`, stale or invalid coverage time is `503 online_data_stale`, and unavailable storage is `503 online_engine_unavailable`. Unsupported requests and oversized results return `422`. No matching data is an error in this preview, **not proof that a metric equals zero**.

## Bounded reads and trusted data

Only a single explicitly enabled schema-qualified table, numeric SUM-based aggregates/arithmetic, categorical dimensions and calendar DATE/day grouping are supported. Time ranges are required, half-open, aligned to calendar-day boundaries in the bound timezone and at most 31 elapsed days. Partial-day requests are rejected rather than silently widened. Results default to at most 1,000 rows and have a 4 MiB row-payload budget; exceeding the budget fails instead of returning a misleading truncated result.

Each request resolves one immutable release, checks the configured policy, plans once, durably records query start, reads PostgreSQL in a read-only repeatable-read transaction, and durably records completion before returning results. There is no release, policy, plan or result cache in this slice. The control database and audit writes remain on the request path. Concurrency is bounded per process; this is not a distributed quota.

The source table must include these reserved columns on **every aggregate row**:

| Column | Meaning |
| --- | --- |
| `_metricspire_tenant` | Tenant supplied by the trusted serving configuration, not the request. |
| `_metricspire_batch_id` | One completed snapshot identifier. |
| `_metricspire_data_as_of` | One UTC coverage time for that snapshot, not the row insertion time. |
| `_metricspire_manifest_fingerprint` | The active semantic definition used to produce the values. |

Publish aggregate rows and their metadata atomically in one transaction, with a business-validated unique key at the retained tenant/date/dimension grain. Queries verify non-null, matching metadata across selected rows and returned groups, and enforce `max_data_age`. These checks cannot prove that an upstream producer included every expected row, computed correctly or declared truthful coverage. Refresh scheduling, a verified producer, missing-row coverage and late-data corrections are **not implemented** here. Do not load raw events, mix grains or sum non-additive distinct-user counts into this path.

## Runtime configuration

The existing OIDC/runtime configuration needs policy and binding routes for the model. Add the following non-secret block and bind the model to engine `postgres_online` as illustrated in [`examples/online`](../examples/online/README.md):

```yaml
online:
  tenant: demo
  namespaces: [demo]
  data_access: tenant_shared
  resources:
    - {kind: table, schema: serving, table: daily_sales}
  max_data_age: 5m
  timeout: 2s
  max_concurrency: 4
```

`5m` is an example, not an agreed freshness requirement. `tenant_shared` explicitly acknowledges that authorized users query a tenant-level PostgreSQL snapshot; original Warehouse user-level row/column policies are **not automatically transferred**. This first slice only accepts portable OIDC, not Databricks Apps export authorization. Review the export and data access boundary before enabling any real dataset.

Set `METRICSPIRE_ONLINE_DATABASE_URL` through secret management to a dedicated non-superuser, non-BYPASSRLS role with SELECT and no write privileges on each enabled table. It must differ from the control-store credential. Source schemas `public`, `metricspire` and system schemas are rejected. The serving process does no business-table DDL. Readiness checks both database pools when online serving is enabled; it does not certify freshness or upstream completeness. The current process still assembles the analytical route and requires its existing Databricks configuration, even though online requests never contact that engine.

The serving pool is bounded to four connections; the default online query deadline and PostgreSQL statement timeout are two seconds. Completion auditing retains the existing detached timeout of up to five seconds, so **two seconds is not a hard end-to-end latency promise** on an audit failure. Cancellation and timeouts propagate to reads. This preview has no durable synchronous-query jobs; retries perform another bounded read. It neither repairs existing asynchronous job persistence nor establishes multi-instance high availability.

## Verification and release gate

For code review, read the layers in this order: `internal/model/contracts.go` (snapshot/result types), `internal/adapter/postgresquery` (capabilities, SQL and read guards), `internal/application/online.go` (numeric-code routing and reused policy/audit), `internal/httpapi/online.go` (trusted HTTP identity), then `cmd/metricspire/serve.go` and `internal/runtimeconfig` (opt-in assembly). The deterministic [package dependency graph](online-query-dependencies.json) records direct imports, not runtime calls. Existing analytical adapter, job manager and MCP contracts are regression surfaces; none is the subject of the online endpoint.

Unit tests cover code resolution, policy denial, budgets, parameter binding, source allowlists, disabled behavior, overload and cancellation. Opt-in local PostgreSQL integration tests use a real HTTP server, isolated schemas, a SELECT-only role and durable audit, then clean up their synthetic data:

```sh
METRICSPIRE_ONLINE_TEST_DATABASE_URL='postgres://USER:PASSWORD@127.0.0.1:PORT/DISPOSABLE_DB?sslmode=disable' \
  go test ./internal/httpapi -run TestOnlinePostgresHTTP -count=1 -timeout=120s
```

The connection must be loopback and disposable: these tests create and drop their own schemas/roles and require a fixture administrator. To add one million synthetic rows, set `METRICSPIRE_ONLINE_LOAD_ROWS=1000000`. The load sample remains a selective indexed query, not a million-row aggregation benchmark. It reports 1/5/10/20 concurrent callers and includes HTTP, policy/planning and durable audit; it is not a production throughput or cost claim.

Before deployment, agree a real metric/dimension grain and freshness target; reconcile results with its source; test intended OIDC identities and denied access; measure warm and after-idle first requests, p95/p99, sustained throughput, timeouts/429s, database restart and audit failure under realistic load. Measure database compute/storage, refresh and audit costs. PostgreSQL can also suspend or have cold connections; bypassing Warehouse does not eliminate that operational risk. No staging or production acceptance follows from local tests alone.
