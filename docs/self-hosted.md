# Start without Databricks

MetricSpire can serve an existing team's precomputed metrics without a Databricks
account. Start with **PostgreSQL metadata + PostgreSQL results + OIDC**. This uses
one supported path; swap engines only when your team's data already needs it.
For a Go-only preview before deployment, run `sh examples/quickstart.sh` from the
checkout. That creates plans without connecting to data.

## What you need

| Component | Holds or does |
| --- | --- |
| Metadata database | Metric drafts, releases and audit. PostgreSQL initially; MySQL 8.4 is another supported store. |
| Result database | Your prepared daily aggregates and snapshot metadata. Use a separate SELECT-only serving credential. |
| OIDC provider | Authenticates your users and supplies the reviewed tenant, roles and permissions. |
| MetricSpire service | Runs as a binary or [Docker image](../Dockerfile), with private configuration and secrets. |

Metadata and result data have different permissions and lifecycles even when
they share a database server. MetricSpire does not build your upstream ETL.

## Set up the first path

1. **Prepare metadata.** Provision a PostgreSQL metadata database and run
   `metricspire migrate` with its operator credential. Keep migrations separate
   from `serve`. Follow [metric maintenance](metric-maintenance.md) to validate,
   review and publish the model. Start with `examples/online/model.yaml` in a
   disposable trial, not your company's full catalog.
2. **Prepare results.** Provision the matching [neutral fixture table](../examples/online/producer/schema.sql)
   in the disposable database using your operator, then load the two rows from
   `examples/online/producer/rows.ndjson` only into your disposable fixture.
   [The refresh walkthrough](online-refresh.md) explains provisioning, independent
   reconciliation and atomic publication for PostgreSQL. A running database or
   published model alone will not produce results. The example dates are
   historical: loading them does not make their coverage current. The default
   `5m` freshness check rejects an old watermark; choose an explicit historical
   fixture policy for a replay, never relabel old coverage with the load time.
3. **Copy a private runtime.** Copy `examples/runtime.postgres.example.yaml` and
   its `examples/online/` policy/binding files into the same layout under your
   private configuration directory. Relative paths resolve from the runtime file.
   Keep `query_engine.driver: disabled`; set the actual public service URL and
   `METRICSPIRE_DATABASE_URL`, `METRICSPIRE_ONLINE_DATABASE_URL` and
   `METRICSPIRE_SESSION_KEY` through your secret store. The session key must be
   base64 encoding of exactly 32 random bytes, generated once and retained
   across restarts. OIDC requires it even for a bearer-only trial.
   For a disposable shell, generate it without printing it using
   `export METRICSPIRE_SESSION_KEY="$(openssl rand -base64 32)"`.
   A deployment should persist the generated value in its secret store.
   The result role must have SELECT only and differ from metadata/refresh roles.
4. **Connect your identity provider.** Set `authentication.provider: oidc` and
   map the issuer, API audience, tenant and permission claims. Register the web
   client/callback if using the workbench. Replace fixture principal
   `synthetic-online-reader` in `online/reader-policy.yaml` with the intended
   verified reader ID; list the allowed metrics and dimensions. All approved
   readers of a `tenant_shared` snapshot must be entitled to that entire data
   scope. See [OIDC setup](oidc-mcp.md) and [runtime configuration](runtime-engines.md).
   Set `METRICSPIRE_OIDC_CLIENT_SECRET` privately only if the web client's IdP
   registration requires it; never include it in the public MCP installer.
5. **Start and verify.** Run `metricspire serve --config <private-runtime.yaml>`.
   Check `/health/ready`, then query with the intended reader. For a deployed
   neutral fixture, use `examples/online/query.json` through
   `POST /api/v1/namespaces/demo/online-query`. Verify typed values, release and
   snapshot, then test a denied reader. Health alone does not validate data.
6. **Give users a connection.** Follow the [OIDC installer](oidc-mcp.md), including
   its separate public MCP client registration. Users discover `online_metrics`,
   read the definitions and call `query_online_metrics`; they do not need Go,
   Docker, database credentials or a Databricks CLI profile. An HTTP application
   can use the same online endpoint with its authorized identity.

Detailed secret names, driver validation and route constraints are in
[runtime engines](runtime-engines.md) and the example configuration. Do not set
`DATABRICKS_*` variables for this independent profile. Do not enable the
analytical route just to make an online request work.

## Use a different existing engine

| Your existing system | Supported entry point | Limit to check first |
| --- | --- | --- |
| MySQL 8.4 metadata | [MySQL setup](mysql-metadata.md) | Stores definitions/audit; it is not a MySQL analytical query adapter. |
| ClickHouse aggregate results | [ClickHouse serving](clickhouse-online.md) | Bounded daily SUM/derived aggregates; no general metric-query or exploration adapter. |
| Doris aggregate results | [Doris serving](doris-online.md) | Bounded daily SUM/derived aggregates; no general metric-query or exploration adapter. |
| Trino/Presto detail tables | [External semantic walkthrough](semantic-layer.md), then [engine setup](trino-presto.md) | Reviewed single-table DATE exploration, not published-metric execution or arbitrary SQL. |

Do not assume that a supported metadata database is also a supported execution
engine. [The adapter matrix](architecture.md#adapter-support) is authoritative.

## What has been tested

CI runs real disposable database/engine checks for these paths. A reproducible
PostgreSQL/OIDC service check is documented in
[the isolated lab](../deploy/testing/README.md); it clears Databricks variables
and checks exact results and reader denial with synthetic identities. It is an
engineering test, not an interactive demo or enterprise-login acceptance.

Before inviting a team, complete a first installation and query with an intended
colleague, including permission denial and token renewal. This guide does not
claim that those people, your IdP or your business snapshots have been verified.
