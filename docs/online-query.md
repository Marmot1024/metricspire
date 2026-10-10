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

Request identity, table, release and engine cannot be supplied in the body. Existing strict JSON decoding and problem responses apply. Deadline expiry is `504 timeout`, not an authentication error or a missing endpoint. Overload is `429 online_overloaded` with `Retry-After: 1`. Disabled serving is `404 online_not_enabled`. Missing/incompatible/partial data is `503 online_data_unavailable`, stale or invalid coverage time is `503 online_data_stale`, and unavailable storage is `503 online_engine_unavailable`. Audit failure without deadline expiry is `503 audit_unavailable`; computed results are not delivered without a durable completion record. Unsupported requests and oversized results return `422`. No matching data is an error in this preview, **not proof that a metric equals zero**.

## Bounded reads and trusted data

Only a single explicitly enabled schema-qualified table, numeric SUM-based aggregates/arithmetic, categorical dimensions and calendar DATE/day grouping are supported. Time ranges are required, half-open, aligned to calendar-day boundaries in the bound timezone and at most 31 elapsed days. Partial-day requests are rejected rather than silently widened. Results default to at most 1,000 rows and have a 4 MiB row-payload budget; exceeding the budget fails instead of returning a misleading truncated result.

Each request resolves one immutable release, checks the configured policy, plans once, durably records query start, reads PostgreSQL in a read-only repeatable-read transaction, and durably records completion before returning results. There is no release, policy, plan or result cache in this slice. The control database and audit writes remain on the request path. Concurrency is bounded per process; this is not a distributed quota.

The source table must include these reserved columns on **every aggregate row**:

| Column | Meaning |
| --- | --- |
| `_metricspire_tenant` | Tenant supplied by the trusted serving configuration, not the request. |
| `_metricspire_batch_id` | One completed snapshot identifier. |
| `_metricspire_data_as_of` | One UTC coverage time for that snapshot, not the row insertion time. |
| `_metricspire_data_contract` | The calculation and source contract used to validate the completed batch. |

The producer derives the batch contract with `postgresquery.DataContractFingerprint(manifest, binding)`. This model-level check includes formulas, codes, grain, types, units and physical source/column/calendar bindings; it excludes presentation metadata such as descriptions, display labels and release version text. Publishing description-only changes therefore immediately returns the new release ID with the existing compatible batch. Calculation/source changes require an independently validated compatible batch; do not simply relabel old data. New metrics conservatively require a new model-level validation too. Per-metric batch compatibility is not implemented. Immutable release IDs and full manifest fingerprints remain in responses and audit history; callers do not supply any fingerprint.

The preview previously checked `_metricspire_manifest_fingerprint` on data rows. That column is no longer a serving contract: it cannot distinguish descriptions from calculations. A producer/schema transition must be reviewed before enabling an existing fixture or dataset; the service does not rename, alter or populate business tables automatically.

Publish aggregate rows and their metadata atomically in one transaction, with a business-validated unique key at the retained tenant/date/dimension grain. Queries verify non-null, matching metadata across selected rows and returned groups, and enforce `max_data_age`. These checks cannot prove that an upstream producer included every expected row, computed correctly or declared truthful coverage. Refresh scheduling, a verified producer, missing-row coverage and late-data corrections are **not implemented** here. Do not load raw events, mix grains or sum non-additive distinct-user counts into this path.

## Runtime configuration

The existing OIDC/runtime configuration needs a policy route for the model. Top-level `bindings` continue to select the Warehouse analytical route. Configure separate `online.bindings` with engine `postgres_online`, as illustrated in [`examples/online`](../examples/online/README.md); both routes can serve the same model without replacing each other:

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
  bindings:
    - namespace: demo
      model_name: daily_sales
      path: examples/online/binding.yaml
  policies:
    - namespace: demo
      model_name: daily_sales
      tenant: demo
      path: examples/online/reader-policy.yaml
```

Paths are relative to the runtime configuration file; adjust the example to its location. Online binding routes must match enabled namespaces and tenant policy routes and reference one allowlisted PostgreSQL table. Do not pin an online binding's optional `manifest_fingerprint` when description-only releases should remain usable without editing configuration.

`online.policies` uses the existing `PolicySource` contract and evaluator, but is resolved independently from analytical policies. Explicit online allow rules must list authenticated principal IDs, metric names and dimension names; roles, wildcards and empty allowlists are rejected at startup. A missing explicit model route is never filled from the analytical policy. No new user directory, permission database or management UI is introduced. Update the reviewed configuration and restart to change grants; this is not a live permission-management API. For backward compatibility, portable OIDC configurations without `online.policies` retain their previously reviewed top-level policies. Those legacy role-based policies do **not** establish a restricted online reader list.

The project enforces API access, not the company's entire entitlement system. Authentication supplies trusted caller identity; administrators approve the permitted snapshot, models, metrics and dimensions; PostgreSQL bounds the service account's physical reads. All approved readers of a `tenant_shared` snapshot must be entitled to the whole retained data scope. Metric/dimension allowlists are **not row-level authorization**: allowing `region` does not restrict its values, and a caller's filter is not a security boundary. If readers require different regions/player populations, do not enable that dataset through this shared path until mandatory row restrictions or separately isolated snapshots are reviewed. The service does not reproduce or synchronize Unity Catalog policies.

`5m` is an example, not an agreed freshness requirement. `tenant_shared` explicitly acknowledges that authorized users query a tenant-level PostgreSQL snapshot; original Warehouse user-level row/column policies are **not automatically transferred**. Portable OIDC remains supported. Databricks Apps requires independent `online.policies` and a tenant matching the verified Apps identity configuration; it cannot fall back to the broadly assigned analytical role. The existing managed-ingress/current-user authenticator is reused unchanged. In Apps policies, principals are verified current-user IDs, not caller-supplied names or headers. Review the export and data access boundary before enabling any real dataset.

Set `METRICSPIRE_ONLINE_DATABASE_URL` through secret management to a dedicated non-superuser, non-BYPASSRLS role with SELECT and no write privileges on each enabled table. Startup rejects effective table-level writes and column-level INSERT, UPDATE or REFERENCES, including inherited grants. Deployment review must also exclude source ownership and role-escalation paths; startup does not perform a complete privilege audit. The role must differ from the control-store credential: different connection URL strings alone do not prove identity separation. Source schemas `public`, `metricspire` and system schemas are rejected. The serving process does no business-table DDL. Readiness checks both database pools when online serving is enabled; it does not certify freshness or upstream completeness. The current process still assembles the analytical route and requires its existing Databricks configuration, even though online requests never contact that engine.

The serving pool is bounded to four connections; the default online handling budget and PostgreSQL statement timeout are two seconds. Online HTTP handling starts the budget before authentication and body decoding. Policy resolution, planning, start audit, reading and completion audit share the remaining deadline; online completion auditing does not extend it by another five seconds. Supported real HTTP connections also apply a read deadline to slow request bodies. Timeouts are propagated and late successes are discarded. This is a cooperative server budget, not a guarantee that every dependency, response transmission or client network completes within two seconds. Analytical job completion auditing keeps its existing detached behavior unchanged. This preview has no durable synchronous-query jobs; retries perform another bounded read. It neither repairs existing asynchronous job persistence nor establishes multi-instance high availability.

## Verification and release gate

For code review, read the layers in this order: `internal/model/contracts.go` (snapshot/result types), `internal/adapter/postgresquery` (capabilities, SQL and read guards), `internal/application/online.go` (numeric-code routing and reused policy/audit), `internal/httpapi/online.go` (trusted HTTP identity), then `cmd/metricspire/serve.go` and `internal/runtimeconfig` (opt-in assembly). The deterministic [package dependency graph](online-query-dependencies.json) records direct imports, not runtime calls. Existing analytical adapter, job manager and MCP contracts are regression surfaces; none is the subject of the online endpoint.

Unit tests cover code resolution, policy denial, budgets, parameter binding, source allowlists, disabled behavior, overload and cancellation. Opt-in local PostgreSQL integration tests use a real HTTP server, isolated schemas, a SELECT-only role and durable audit, then clean up their synthetic data:

```sh
METRICSPIRE_ONLINE_TEST_DATABASE_URL='postgres://USER:PASSWORD@127.0.0.1:PORT/DISPOSABLE_DB?sslmode=disable' \
  go test ./internal/httpapi -run TestOnlinePostgres -count=1 -timeout=120s
```

To test the actual startup assembly, separate analytical/online routes and signed bearer-token verification against a synthetic local OIDC issuer, use the same disposable database with `go test ./cmd/metricspire -run TestServeOnlinePostgres -count=1 -timeout=60s`. Approved online readers succeed; an unlisted user with the same analytical role is denied online while retaining analytical planning access. Startup regression cases also require rejection of table UPDATE and column INSERT/UPDATE/REFERENCES, both directly granted and inherited. This fixture does not log in to Databricks or alter authorization profiles/caches. It is not acceptance of a real organization's OIDC configuration.

`TestOnlinePostgresAppsIdentityAndReaderBoundary` combines the real Apps authenticator, HTTP endpoint, PostgreSQL and durable audit with a **synthetic** current-user HTTP transport. It checks allowed/unlisted, inactive/invalid and spoofed identities, the audited caller and absence of a user token in the PostgreSQL engine. It does not prove real managed ingress, token revocation or a deployed App. The existing identity cache is 15 seconds by default: revocation/disablement can remain invisible until that entry expires; real acceptance must measure this bound rather than claim instant revocation. No extra login or OAuth scope changes are required by this adapter. See the [optional staging package](../deploy/databricks-apps/README.md).

For an already approved deployed neutral fixture, `TestOnlineDeployedAcceptance` is opt-in with `METRICSPIRE_RUN_ONLINE_DEPLOYED_ACCEPTANCE=approved-staging`. Provide `METRICSPIRE_ONLINE_DEPLOYED_BASE_URL` (HTTPS origin), `..._NAMESPACE`, `..._QUERY` (reviewed query file), `..._USER_TOKEN` (reuse that user's existing short-lived token) and `..._EXPECTATION=allowed|denied`. An allowed run also requires `..._EXPECTED`, a private `OnlineResult` file pinning the reviewed release, manifest, batch and exact typed results. Never obtain this expected file by merely copying the response under test; independently calculate or reconcile the fixture. No token is printed or stored by the probe, and redirects are not followed.

Each identity run first checks an authenticated catalog route, then performs 20 bounded online reads. Allowed results must match the reviewed snapshot; a denied user must receive the product's `403 permission_denied`, not an ingress rejection or `401`. The probe reports the first online query **after that identity check** and sequential p95; it is not a completely cold identity/connection sample. The allowed default p95 budget is 1s, configurable via `..._P95_BUDGET` up to 10s only after an explicit acceptance decision. This is not peak-load, failover, cost, suspended-database or immediate-revocation acceptance. Real audit reconciliation, cache-expiry/revocation, concurrent load, refresh correctness and rollback remain separate deployment gates. Reads create the normal server audit entries but the probe does no DDL, loading, publication or deployment. It skips by default and must not be reported as passed before it actually runs.

The connection must be loopback and disposable: these tests create and drop their own schemas/roles and require a fixture administrator. To add one million synthetic rows, set `METRICSPIRE_ONLINE_LOAD_ROWS=1000000`. The load sample remains a selective indexed query, not a million-row aggregation benchmark. It reports 1/5/10/20 concurrent callers and includes HTTP, policy/planning and durable audit; it is not a production throughput or cost claim.

For a bounded sustained sample, also set `METRICSPIRE_ONLINE_SOAK_DURATION=30s` (accepted range: 1s–1m) and run `TestOnlinePostgresHTTPSustainedLoad`. Ten concurrent callers validate release, batch and revenue values on each result and reconcile durable audit counts; the test reports p95/p99, throughput and failures. Samples are capped to bound test memory. `TestOnlinePostgresHTTPColdConnectionsAndRecovery` checks first/pooled/reset connections and loss of one fixture-owned database connection. Reconnecting to an already-running local PostgreSQL instance is **not** a test of suspended Lakebase wake-up or a full database restart.

Non-finite PostgreSQL numeric values such as NaN and Infinity are rejected with `503 online_data_unavailable`, not exposed as successful metric values. Zero-denominator nulls remain supported.

Before deployment, agree a real metric/dimension grain and freshness target; reconcile results with its source; test intended OIDC identities and denied access; measure warm and after-idle first requests, p95/p99, sustained throughput, timeouts/429s, database restart and audit failure under realistic load. Measure database compute/storage, refresh and audit costs. PostgreSQL can also suspend or have cold connections; bypassing Warehouse does not eliminate that operational risk. No staging or production acceptance follows from local tests alone.
