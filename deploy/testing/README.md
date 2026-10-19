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
docker compose --profile analytical up -d clickhouse trino
docker compose ps
```

PostgreSQL is on port 15432, ClickHouse HTTP on 18123 and Trino on 18080.
The default Trino image includes the synthetic TPCH connector; no company
warehouse connector or secret is supplied. Limits total 8 GiB and 6 CPUs with all
three services enabled. Check host capacity and occupied ports before starting.
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
