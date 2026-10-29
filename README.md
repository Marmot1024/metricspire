<p align="center"><img src="docs/assets/metricspire-mark.svg" width="44" height="44" alt="MetricSpire"></p>
<h1 align="center">MetricSpire</h1>
<p align="center"><strong>Define a metric once. Query it from your apps and AI assistants.</strong></p>
<p align="center"><strong>English</strong> · <a href="README.zh-CN.md">简体中文</a> · <a href="README.ja.md">日本語</a></p>

MetricSpire is an open-source metric catalog and data service with HTTP and MCP access. Your team reviews and publishes metric definitions; applications and AI assistants discover their meaning and query through the same service.

- **Maintain metrics:** drafts, definition review, versioned publication and a web workbench.
- **Serve data:** structured metric/time/grouping requests, access checks and typed result tables.
- **Explore when needed:** load reviewed external semantics for temporary questions that published metrics do not cover.

For example, publish a definition for “paid orders,” then ask “How many paid orders did each region have last week?” The service uses the published definition and your configured data route. An application can request the same metric through HTTP.

> **Development preview.** There is no published production-ready release or public demo service. Connecting your data requires deployment, identity and reviewed definitions. See [status and acceptance boundaries](docs/development-status.md).

## Try it with Go, without a database

With Go 1.26 or 1.27 and Git installed:

```sh
git clone https://github.com/marmot1024/metricspire.git
cd metricspire
sh examples/quickstart.sh
```

The script compiles the neutral order model and access policy, then prints a query plan for **average order value and refund rate, grouped by day and customer segment**. It writes four JSON artifacts into a new `dist/quickstart.*` directory and ends with:

```text
Offline example complete. No database connection or data query was made.
```

This is a real compiler/planner walkthrough, not a data-result demo. The first run downloads Go dependencies. [Inspect each step](docs/getting-started.md#compile-and-inspect-a-plan-without-a-database).

## Choose your next step

| Your goal | Start here | What you need |
| --- | --- | --- |
| Query your team's existing service | [Databricks installer](docs/mcp-installer.md#get-the-installer) or [OIDC installer](docs/oidc-mcp.md#user-installation); [other MCP clients](docs/mcp.md#connect) | Instance URL, team connection package and your own permissions. No source checkout required. |
| Add or maintain a metric | [First definition → draft → publish → query](docs/metric-maintenance.md) · [optional maintainer skill](skills/metricspire-maintainer/SKILL.md) | Agreed definition, model-management permission and an existing data route. |
| Start without Databricks | [PostgreSQL + OIDC walkthrough](docs/self-hosted.md) | Metadata/results databases, an identity provider and prepared aggregates. Query users need no Databricks account. |
| Connect an existing semantic layer | [Export → configure → discover → reconcile](docs/semantic-layer.md) | Reviewed AnalysisCatalog export and a supported exploration engine. No automatic Git/dbt/Cube import. |

Already have web access? Open your team's instance and start in the **metric catalog**. Detailed guides are currently in English; the three project overviews cover the same scope.

## Where it fits

Your pipelines prepare data. Your query engine stores and computes it. MetricSpire manages definitions, checks access and serves structured queries.

![MetricSpire architecture: web, HTTP and MCP clients call the catalog and data service; separate adapters connect query engines, metric metadata and optional semantic exports.](docs/assets/architecture.svg)

| Responsibility | Implemented options |
| --- | --- |
| Definitions, releases and audit | PostgreSQL or MySQL 8.4 |
| Published-metric analytical queries | Databricks SQL |
| Precomputed aggregate serving | PostgreSQL, ClickHouse or Doris, through HTTP/MCP |
| Temporary analysis using reviewed semantics | Databricks; bounded single-table DATE exploration on Trino/Presto |

An independent OIDC deployment can disable Databricks analytical queries. Database support is specific to each row: a metadata adapter is not a general query adapter. MetricSpire does not replace upstream ETL or provision your business tables. See [adapter scope](docs/architecture.md#adapter-support).

## Documentation and contributing

- [All documentation](docs/README.md) · [Getting started](docs/getting-started.md)
- [MCP workflow](docs/mcp.md) · [HTTP API](docs/http-api.md)
- [Private team configuration](config/README.md) · [Runtime engines](docs/runtime-engines.md)
- [Data refresh and reconciliation](docs/online-refresh.md) · [Isolated technical acceptance](deploy/testing/README.md)
- [Architecture](docs/architecture.md) · [Development status](docs/development-status.md) · [Contributing](CONTRIBUTING.md)

Licensed under [Apache 2.0](LICENSE). Public examples are neutral; keep company definitions, physical mappings, credentials and production data in private storage. The semantic contract `metricspire.io/v1alpha1` is versioned separately from the planned product `v0.1.0` release.
