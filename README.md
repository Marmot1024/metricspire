<p align="center"><img src="docs/assets/metricspire-mark.svg" width="44" height="44" alt="MetricSpire"></p>

Portable startup and engine configuration: [runtime engines](docs/runtime-engines.md).

Structured scope selection: [metric definitions](docs/metric-definitions.md).

<h1 align="center">MetricSpire</h1>

<p align="center"><strong>A metric catalog and data service with MCP access.</strong></p>

<p align="center"><strong>English</strong> · <a href="README.zh-CN.md">简体中文</a> · <a href="README.ja.md">日本語</a></p>

MetricSpire is an open-source metric catalog and data service connecting your analytical databases, applications and AI assistants. Your team manages what each metric means and how it is calculated; applications and AI query data through shared interfaces.

- **Metric catalog:** maintain definitions, calculation rules and versions for your team to discover, review and publish.
- **Data service:** query an OLAP engine (a database for analytical workloads) by metric, time range and grouping, and return result tables to applications.
- **MCP access:** let AI assistants find metrics, understand their definitions and call the data service to answer questions.
- **Exploratory analysis, optionally:** connect an external semantic-layer repository and use its field meanings, table relationships and business rules for temporary analyses.

## Walk through an order example

The following fictional data illustrates the workflow.

**1. Your team defines a metric.** For example, “paid orders” counts distinct order IDs with successful payments, using the payment date. A maintainer connects this definition to existing OLAP tables and publishes it.

**2. A user asks a question.** In a connected AI assistant:

> How many paid orders did each region have last week?

**3. The service queries and returns data.** The AI assistant finds the metric and turns the question into a query request. MetricSpire checks access, queries the OLAP engine using the published definition and returns data for the assistant to present:

| Region | Paid orders |
| --- | ---: |
| East | 120 |
| South | 80 |

An application can query the same metric through the HTTP API and reuse its calculation rules. Your team needs to connect its data and publish definitions before querying.

**What about a follow-up without an existing metric?** For example, “What share of these orders exceeded 500 in order value?” With exploration enabled, the AI can use order amount, region and relationship rules configured in the external semantic layer to build a temporary query, without creating a fixed metric first. The semantic layer describes the data and how to use it; the OLAP engine still queries the actual data. See [external semantic-layer configuration](docs/exploration.md) for the input format.

## Where it fits in your data stack

**Your existing pipelines collect and prepare data. The OLAP engine stores and computes business data. MetricSpire manages metrics and serves queries.** A separate relational database stores metric definitions and versions; an optional external semantic layer provides context for exploratory analysis.

![MetricSpire architecture: web users, applications and AI call the catalog and data service; adapters query the OLAP engine, a relational store holds metric definitions, and an optional external semantic-layer repository supplies analysis definitions.](docs/assets/architecture.svg)

Solid arrows show service calls or data writes; the dashed arrow supplies optional semantic configuration. **The OLAP engine, metric store and external semantic layer have distinct responsibilities.**

The adapter design targets OLAP engines such as ClickHouse and Doris, and metric stores such as MySQL and PostgreSQL. **This repository currently ships a Databricks SQL analytical adapter and a PostgreSQL metric store**, plus optional HTTP/MCP queries over precomputed PostgreSQL or ClickHouse aggregates and bounded [Trino/Presto exploration](docs/trino-presto.md). MySQL 8.4 metadata storage is also available. Doris is not shipped yet; ClickHouse exploration and analytical MCP queries are not supported. See [architecture and adapter support](docs/architecture.md#adapter-support) for the scope of each path.

## Choose where to start

> **Development preview.** Querying your data requires a configured service and data permissions. There is no published production-ready release. [Check the current status](docs/development-status.md).

| Your goal | Start here | You will need |
| --- | --- | --- |
| Use your team's service from local Codex CLI | [Get the team installer](docs/mcp-installer.md#get-the-installer) | A deployed instance, its team installer, and the Databricks access required by that deployment. |
| Connect another MCP client | [MCP connection guide](docs/mcp.md#connect) | An instance URL and the authentication setup supported by that deployment. |
| Understand metric definitions without connecting to data | [Run the offline example](docs/getting-started.md#compile-and-inspect-a-plan-without-a-database) | Go; no database or cloud account. This produces query plans, not data results. |
| Run a service for your team | [Service setup](docs/getting-started.md#run-the-http-service) | PostgreSQL metadata, identity configuration and a [supported query profile](docs/runtime-engines.md). |

Already have web access? Open your team's instance and start in the **metric catalog**. This repository does not include a public instance or your company's metric definitions.

## Documentation and project status

- [Getting started](docs/getting-started.md) — offline example, local catalog and service setup.
- [MCP integration](docs/mcp.md) · [HTTP API](docs/http-api.md) — connect a client or application.
- [Architecture and adapters](docs/architecture.md) — system responsibilities, query paths and current database support.
- [ClickHouse aggregate serving](docs/clickhouse-online.md) · [runtime.clickhouse.example.yaml](examples/runtime.clickhouse.example.yaml).
- [Team configuration](config/README.md) · [External semantic layer](docs/exploration.md) — maintain private settings and definitions for exploration.
- [Development status](docs/development-status.md) — implemented features, verification evidence and remaining gaps.
- [Contributing](CONTRIBUTING.md) · [All documentation](docs/README.md).

The order scenario above explains the product; the [TPCH acceptance fixture](testdata/acceptance/databricks-tpch) provides reproducible technical requests and expected results. The semantic contract, [`metricspire.io/v1alpha1`](contracts/metricspire.schema.json), is versioned separately from the planned product `v0.1.0` release.

Licensed under [Apache 2.0](LICENSE). Public examples use neutral data; keep company definitions, credentials and production data outside this repository.
