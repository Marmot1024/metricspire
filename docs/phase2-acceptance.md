# Phase 2 acceptance

Status: **passed on 2026-09-02**.

Phase 2 is the first complete backend path. It proves that a reviewed metric definition can be stored, published as an immutable release, queried only through the active release, executed on a real analytical engine with bounded results, revised, and rolled back.

## Acceptance matrix

| Requirement | Evidence |
| --- | --- |
| PostgreSQL control plane | idempotent migration; draft and revision history; immutable releases; active pointer; publish and rollback events |
| Concurrent editing | optimistic revision checks allow one writer and reject a stale competing writer |
| Publication gate | display name, business definition, owner, and non-empty verification evidence are required |
| Compatibility gate | a published metric code cannot be removed or change execution semantics silently; deprecation is explicit and irreversible |
| Structured formulas | common aggregates, metric references, arithmetic, and governed filters compile without accepting SQL fragments |
| Request budgets | metric, dimension, filter, filter-value, parameter, SQL-size, join, and row limits reject before remote execution |
| Engine boundary | the application depends on `QueryEngine`; Databricks-specific code remains in one adapter package |
| Databricks SQL | validated identifiers, named parameters, deterministic SQL, timeout, cancellation, typed results, and chunk pagination |
| Result budgets | the request's actual row limit and cumulative bytes across all result chunks are enforced |
| Real vertical slice | draft -> publish v1 -> active query -> publish v2 -> active query -> rollback v1 -> active query, with release events and identical fixed expected results |

## Reproducible local checks

The default suite is offline and never contacts Databricks:

```bash
go test ./...
go test -race ./...
go vet ./...
go mod verify
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' ./cmd/metricspire
git diff --check
```

PostgreSQL repository and CLI integration tests run only when a disposable database URL is provided:

```bash
METRICSPIRE_TEST_DATABASE_URL='postgres://USER:PASSWORD@HOST:PORT/DATABASE?sslmode=disable' \
  go test ./internal/repository/postgres ./cmd/metricspire
```

The real vertical-slice test is additionally gated by `METRICSPIRE_RUN_REAL_ACCEPTANCE=1`. It requires explicit PostgreSQL and Databricks environment inputs plus all fixed fixture paths under `testdata/acceptance/databricks-tpch/`. Missing inputs fail rather than silently substituting a mock.

Exceptional row/byte/cancellation/timeout statements are isolated behind `METRICSPIRE_RUN_REAL_DATABRICKS_CONTROLS=1`. Enabling the ordinary real acceptance test does not execute them.

## Observed evidence

- PostgreSQL 17 completed migration, concurrent revision, two-release publication, active queries, rollback, and event-history checks.
- A read-only Databricks SQL warehouse executed the fixed public `samples.tpch.orders` and `samples.tpch.customer` fixture. No table was created or modified.
- An independently captured five-row expected result matched the adapter output after the first release, second release, and rollback.
- Separate opt-in controls proved row truncation, cumulative response-byte rejection, caller cancellation, and timeout-driven remote cancellation.
- After real acceptance, the complete unit/race/vet/module/build checks passed again.

Statement IDs, workspace identifiers, credentials, database URLs, and local machine paths are deliberately excluded from this public-facing record. The fixed semantic inputs, exact generated SQL assertion, column types, and expected rows remain in the repository for review.

## Evidence limits

Phase 2 does **not** prove:

- production end-user authentication or identity passthrough;
- public HTTP, UI, SDK, or MCP behavior;
- compatibility with ClickHouse, Doris, Trino/Presto, or any adapter other than Databricks;
- production workload scale, large-result export, caching, or cross-engine execution;
- safety of any credential or data environment that was not part of the opt-in test.

Skipped integration tests and mock-client tests are useful local checks but are never counted as real-environment evidence.
