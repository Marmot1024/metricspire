# Disposable engine lab

This Compose project creates new private volumes and binds ports to `127.0.0.1`
only. It does not mount the application checkout, private semantic assets or
existing database volumes. Use synthetic data exclusively. This is not a
production deployment and does not install MetricSpire engine adapters.

Create a lab-only `.env` (keep it outside Git) containing
`METRICSPIRE_LAB_PASSWORD=<random disposable password>`. Run from this directory:

```sh
docker compose up -d postgres
# Optional engine protocol / synthetic-data experiments:
docker compose --profile analytical up -d clickhouse trino presto
docker compose ps
```

PostgreSQL is on port 15432, ClickHouse HTTP on 18123 Trino on 18080 and Presto on 18081.
The default Trino image includes the synthetic TPCH connector; no company
warehouse connector or secret is supplied. Limits total 11 GiB and 8 CPUs with all
four services enabled. Check host capacity and occupied ports before starting.
On remote hosts use SSH forwarding or execute tests through SSH, never change
bindings to `0.0.0.0` to make tests reachable.

For PostgreSQL integration tests, use the disposable database administrator URL
with `METRICSPIRE_TEST_DATABASE_URL` and `METRICSPIRE_ONLINE_TEST_DATABASE_URL`.
Fixtures create and remove their own synthetic schemas and roles. The
`TestServeOnlinePostgresWithoutDatabricks` case starts the real service with a
loopback OIDC fixture and separate catalog/read credentials, clears Databricks
environment variables, and verifies result, audit, denied-reader and disabled
analytical-route behavior. It validates JWT handling against a fixture issuer,
not an external identity provider's login flow.

Use `METRICSPIRE_ONLINE_LOAD_ROWS=1000000` for the existing bounded online-load
fixture; increase only after checking resource use. Keep seed generation
repeatable, compare expected aggregates, and record row counts separately from
latency. Container startup or `SELECT 1` alone is not application acceptance.

`docker compose stop` preserves this lab's data. Removing volumes is a separate
intentional cleanup step; never apply cleanup commands to another project.
Record resolved image digests with acceptance evidence when reproducing a run.
The small ClickHouse container disables periodic metric logs to avoid unrelated
wide-log merges consuming its memory; query logs remain enabled. On an existing
lab volume, old metric-log tables may still merge: inspect those before assuming
a memory rejection came from the application query.

For ClickHouse adapter and full service tests, set `METRICSPIRE_CLICKHOUSE_TEST_URL` to this lab administrator URL and `METRICSPIRE_REQUIRE_CLICKHOUSE_TESTS=1`. The image enables account management solely to create dedicated SELECT-only fixture users. `TestServeOnlineClickHouseWithPostgresMetadata` requires both engines and verifies the real OIDC/HTTP/catalog/audit path. Runtime credentials never use this administrator.

Run `go test -race -count=1 ./internal/adapter/clickhousequery ./cmd/metricspire` with those URLs. Set `METRICSPIRE_CLICKHOUSE_LOAD_ROWS=10000000` for a ten-million-row synthetic aggregate fixture; it verifies exact sums, counts and ratios and cleans its own resources. Timings describe this lab, not production SLAs.

For Trino/Presto adapter and native MCP acceptance, set `METRICSPIRE_TRINO_TEST_URL`
or `METRICSPIRE_PRESTO_TEST_URL` to the loopback origin, plus the PostgreSQL URL
above. Set the matching `METRICSPIRE_REQUIRE_TRINO_TESTS=1` or
`METRICSPIRE_REQUIRE_PRESTO_TESTS=1` to reject missing configuration. Run
`go test -race -count=1 -v ./internal/adapter/trinoquery ./cmd/metricspire -run 'TestRealTPCH|TestServeTrinoPrestoWithoutDatabricks'`.
The public TPCH sf1 source generates about six million rows; fixtures check
exact counts/sums against an independent SQL oracle, decimal average, final
window denominator, two stages, OIDC denial, principal isolation and durable audit.
