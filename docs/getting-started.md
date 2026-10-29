# Getting started

Choose the path that matches your goal:

- **Use an existing team service:** start with the team's [Databricks installer](mcp-installer.md#get-the-installer) or [OIDC installer](oidc-mcp.md#user-installation), [other MCP clients](mcp.md#connect), or web metric catalog. You do not need Go, Docker or this repository.
- **Learn how metric definitions work:** follow the offline example below. You will generate a query plan without connecting to data.
- **Operate a service:** read [service setup](#run-the-http-service) for the required database, identity provider and warehouse configuration. The offline example alone does not set these up.

For a first maintained metric, use [definition → publication → query](metric-maintenance.md).
For independent deployment, start with [PostgreSQL + OIDC](self-hosted.md).
For external semantics, follow [the export walkthrough](semantic-layer.md).

## Prerequisites for the local examples

- Go 1.26 or 1.27
- Docker with Compose, only for the PostgreSQL workflow

Clone the repository if you have not already done so:

```bash
git clone https://github.com/Marmot1024/metricspire.git
cd metricspire
```

Run the following commands from the repository root. The `dist/` directory contains generated local artifacts.

## Compile and inspect a plan without a database

The example uses public, neutral order data. It compiles a semantic model and policy, then produces logical and physical plans without submitting an analytical query.

```sh
sh examples/quickstart.sh
```

The [script](../examples/quickstart.sh) is the maintained command sequence. It
prints the logical plan and writes four JSON files into a new `dist/quickstart.*`
directory: compiled definitions (`manifest.json`), access rules (`policy.json`),
`logical-plan.json` and `physical-plan.json`.

This example requests average order value and refund rate, grouped by day and customer segment, filtered to the east and south regions. Open `logical-plan.json` to inspect the selected metrics, grouping and time range, then `physical-plan.json` to inspect the approved table mappings. **There are no revenue rows or other data results:** the example plans the query but does not execute it. The example's `context.json` is trusted **only** in this offline CLI path. HTTP and MCP callers cannot submit their own tenant, identity, policy, binding, release, engine, or SQL.

## Try the catalog lifecycle locally

Follow [metric maintenance](metric-maintenance.md#2-save-a-draft) for the single
maintained example: a disposable PostgreSQL catalog, draft revision, review,
publication and the prerequisites for a reconciled query. Publication stores
control metadata; it does not create or fill business tables.

## Configure your team

Use the [team configuration templates](../config/README.md) for a private runtime
file, business names/aliases and checksum-verified semantic exports. The
`validate-domains` command checks them offline, without database or Git access.

## Run the HTTP service

Choose a deployment profile before configuring connections. Metric definitions
and audit use **PostgreSQL or MySQL 8.4**; DWS/ADS results use a separate store and
read credential. Ordinary query users connect to the team's deployed service;
they do not need to provision these components themselves.

| Team environment | Start from | Query path |
| --- | --- | --- |
| Databricks | [Portable OIDC runtime](../examples/runtime.example.yaml), or the dedicated [Databricks Apps staging package](../deploy/databricks-apps/README.md) | Published metrics through Databricks SQL; optional approved exploration. |
| No Databricks, PostgreSQL results | [Independent runtime](../examples/runtime.postgres.example.yaml) | Precomputed DWS/ADS through HTTP and native MCP. No Databricks account or variables are required. |
| No Databricks, ClickHouse or Doris results | [ClickHouse guide](clickhouse-online.md) or [Doris guide](doris-online.md) | Bounded precomputed aggregates, with each adapter's type and query limits. |

To use MySQL for definitions/versions/audit, follow [MySQL metadata](mysql-metadata.md).
Optional [Trino/Presto exploration](trino-presto.md) uses reviewed single-table
DATE catalogs and separate access/budgets. It does not enable arbitrary SQL,
automatic table discovery or cross-engine joins. The full driver matrix and
configuration boundaries are in [runtime engines](runtime-engines.md).

1. Provision the metadata store and run `metricspire migrate` separately, using
   the appropriate driver's migration instructions and a deployment secret.
   `serve` never migrates or populates business tables.
2. Configure your identity provider and map the intended tenant, roles and
   permissions. OIDC self-hosting and Databricks Apps identity are separate
   deployment choices. Keep credentials and the session key in the environment
   or deployment secret store, outside Git and distributable installers.
3. Copy the selected runtime template into private deployment configuration.
   Configure reviewed policies/bindings, publish metric definitions and prepare
   the matching data. Online serving needs precomputed results and a valid
   snapshot/freshness contract; starting the service does not synchronize data.
   [PostgreSQL refresh tooling](online-refresh.md) is an operator workflow, not
   an upstream extraction or scheduling service.
4. Configure private business aliases and reviewed semantic exports through
   [team configuration](../config/README.md). Users select logical business
   names; they do not supply repository URLs in chat. Registration does not
   clone Git or export a warehouse schema.
5. Run `metricspire serve --config <private-runtime.yaml>`. `/health/live`
   checks the process; `/health/ready` checks metadata and, when enabled, the
   online engine connection. Neither proves analytical/exploration access,
   result correctness or snapshot freshness.
6. Distribute the matching [Databricks installer](mcp-installer.md) or
   [OIDC installer](oidc-mcp.md) privately. In an independent serving profile,
   discover `online_metrics`, compare definitions and use `query_online_metrics`;
   disabled analytical tools do not fall back automatically.

Before inviting a team, use an intended query user's own account to discover a
metric, confirm any ambiguous scope, execute a bounded query and reconcile its
typed result. Also verify a denied scope and token renewal. Keep publication
permission separate from query permission. A successful installer, health probe
or administrator query alone does not establish team readiness. See
[development status](development-status.md) for the remaining acceptance scope.

The [Dockerfile](../Dockerfile) builds a non-root static-binary image. It is the self-hosted packaging path; the Databricks Apps staging package has a separate bootstrap and resource contract in [`deploy/databricks-apps`](../deploy/databricks-apps/README.md).

## Verify a change

Run [the local quality gate](../CONTRIBUTING.md#local-quality-gate). Optional
real-engine tests require their explicitly configured environment; default
fixture tests do not establish deployed authentication or business correctness.
