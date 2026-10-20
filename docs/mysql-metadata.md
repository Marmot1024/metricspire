# MySQL metadata storage

MySQL **8.4** can store MetricSpire definitions, draft revisions, immutable
releases, governance imports and query audit events. It is independent of the
precomputed DWS/ADS result store. This adapter does not execute business queries
on MySQL or migrate an existing PostgreSQL catalog.

Start with `examples/runtime.postgres.example.yaml` and change:

```yaml
storage:
  driver: mysql
  database_url_env: METRICSPIRE_DATABASE_URL
```

Keep `online.driver` and its separate credential unchanged, or select another
implemented result engine. Configure OIDC independently. The runtime validates
that the metadata URL scheme matches `storage.driver` before opening the store.

Set the secret environment variable to a URL of this shape:
`mysql://USER:PASSWORD@HOST:3306/DATABASE?tls=true`. Percent-encode credential
characters. Verified TLS is the default; `tls=false` is accepted only on loopback
for disposable tests. Arbitrary driver options and TLS verification bypasses are
rejected. Provision the database and its account before migration; runtime does
not create databases. Metadata identifiers are case-sensitive, up to 128
characters; actor/principal/request IDs are bounded by their SQL column sizes.
Strict SQL mode rejects oversized values instead of truncating them.

Run `metricspire migrate` explicitly with `METRICSPIRE_DATABASE_URL` set. The CLI
also accepts `--database-url`, but an environment variable avoids placing secrets
in shell history or process arguments. MySQL selects the database from the URL;
`--schema` is rejected. `serve` requires the complete checked migration set and
never runs DDL. Give the migration identity DDL privileges; the runtime identity
needs SELECT/INSERT/UPDATE/DELETE on its dedicated metadata tables, without DDL.
The result-reading identity remains separate and read-only.

## Migration recovery

MySQL DDL commits implicitly. Each append-only migration contains one DDL
statement. A database-scoped advisory lock serializes migration processes;
checksums reject changed or reordered migrations. A durable dirty marker is
written **before** DDL and cleared after success. Failed or interrupted DDL leaves
startup blocked. Preserve a backup and inspect the named migration and database
state before an operator repairs it or restores the backup. Retrying startup
never auto-adopts an existing table or claims that DDL rolled back. There is no
automatic dirty-marker reset command.

Catalog writes use InnoDB transactions and stable per-model locks. Publishing,
activation/deactivation and release-event insertion commit together. Governance
imports use namespace locks and historical revision high-water marks. History is
retained across rollback/deactivation; result data is never copied into these
tables. Query audit write failures retain the existing fail-closed behavior.

## Disposable acceptance

`deploy/testing/compose.yaml --profile metadata` provisions isolated MySQL 8.4.
Set `METRICSPIRE_MYSQL_TEST_URL` to its loopback administrator URL and
`METRICSPIRE_REQUIRE_MYSQL_TESTS=1`; repository tests create and clean only their
own generated databases. Native startup acceptance additionally needs the
disposable PostgreSQL result fixture (`METRICSPIRE_ONLINE_TEST_DATABASE_URL`).
Never point these tests at a real metadata database or warehouse.
