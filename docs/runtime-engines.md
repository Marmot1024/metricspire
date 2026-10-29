# Runtime engines and portable startup

Start with `examples/runtime.postgres.example.yaml` for OIDC and PostgreSQL-only
serving, or `examples/runtime.example.yaml` for Databricks analytical queries.
These are deployment-owned configuration files, not chat inputs.

For the first setup, use [PostgreSQL + OIDC](self-hosted.md); for a reviewed
external catalog, use [the semantic-layer walkthrough](semantic-layer.md).

| Configuration | Responsibility | Implemented drivers |
| --- | --- | --- |
| `storage` | Metric definitions, revisions, releases and audit records | `postgres`, `mysql` |
| `query_engine` | Published-metric analytical execution | `databricks_sql`, `disabled` |
| `online` | Bounded reads of precomputed DWS/ADS aggregates | `postgres_online`, `clickhouse_online`, `doris_online` |
| `exploration` | Temporary analysis over reviewed semantic catalogs | `databricks_sql`, `trino`, `presto` |

For Doris result serving and its DATE/SUM/type limits, see [the Doris guide](doris-online.md).
Online adapters do not imply general analytical or exploration support.

Existing configuration files retain their defaults: PostgreSQL metadata,
Databricks analytical queries, and optional PostgreSQL online serving.
`storage.database_url_env` and `online.database_url_env` name different environment
variables; connection strings and credentials belong in the deployment secret
store. Defaults are `METRICSPIRE_DATABASE_URL` and
`METRICSPIRE_ONLINE_DATABASE_URL`.

For MySQL metadata configuration and migration recovery, see [the MySQL guide](mysql-metadata.md).

For ClickHouse result serving with PostgreSQL metadata, see [the ClickHouse guide](clickhouse-online.md).

## PostgreSQL-only profile

1. Provision the metadata database and explicitly run the existing `migrate`
   command against it. Custom `storage.database_url_env` names apply to `serve`;
   the migration CLI still takes `--database-url` or `METRICSPIRE_DATABASE_URL`.
   `serve` does not migrate or synchronize business data.
2. Configure a standard OIDC issuer, audience, client and session key as described
   in the HTTP guide. `METRICSPIRE_SESSION_KEY` must contain base64 encoding of
   exactly 32 random bytes; retain it through your secret store across restarts.
   OIDC identity verification is independent of the query engine.
3. Publish a reviewed model in the metadata catalog. Populate its precomputed
   results separately, with the online snapshot/freshness contract, then grant a
   dedicated non-superuser read credential. See `examples/online/README.md`.
4. Set `query_engine.driver: disabled`, omit top-level `policies`, `bindings` and
   trial namespaces, and configure independent policies/bindings under `online`.
   Those same online policies govern catalog discovery. No analytical-policy
   fallback applies to this profile.
5. Run `metricspire serve --config examples/runtime.postgres.example.yaml`.
   The public origin must match the actual deployment, and the example `demo`
   tenant and synthetic reader policy must be replaced with your reviewed scope.

Without `exploration`, this profile needs no `DATABRICKS_*` variables. The
Databricks Apps authentication provider still requires its workspace host for
identity verification even if analytical queries are disabled. Enabling
Databricks exploration separately requires its configured warehouse plus the
appropriate workspace credential; it does not require a published-metric warehouse.

Online execution uses `POST /api/v1/namespaces/{namespace}/online-query` and the
external metric-code request contract. MCP catalog discovery remains available;
MCP analytical query tools fail explicitly with `query_engine_disabled`.
`discover_query_routes` does not advertise the disabled analytical workflow.
Online execution is exposed separately as `query_online_metrics` when configured; search and fetch details with `route: online_metrics` so independent online policies apply. Authentication and authorization still precede disabled-route
responses. There is no automatic fallback from failed, denied or disabled routes.

## Private semantic sources

`domains_path` points to a deployment-owned domain registry that maps business
names and aliases to canonical namespaces and reviewed semantic artifacts.
Maintain the private repository URL/revision there and deploy reviewed exports;
runtime does not clone or pull Git repositories. Other teams edit their own
registry and engine configuration. Never commit real company aliases, physical
mappings, semantic artifacts or credentials into the public examples.

For a synthetic external database lab, see [testing](../deploy/testing/README.md).

For independent DATE-based exploration, see [Trino and Presto](trino-presto.md).
