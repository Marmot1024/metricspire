<p align="center"><img src="docs/assets/metricspire-mark.svg" width="44" height="44" alt="MetricSpire"></p>

<h1 align="center">MetricSpire</h1>

<p align="center"><strong>A metric catalog and data service with MCP access.</strong></p>

<p align="center"><strong>English</strong> · <a href="README.zh-CN.md">简体中文</a> · <a href="README.ja.md">日本語</a></p>

MetricSpire is an open-source metric catalog and data service. It manages metric definitions and versions in one place, serves data queries through an HTTP API, and connects AI assistants through MCP.

Optionally, connect an external semantic knowledge base describing approved data sources, fields and relationships. This supports exploratory analysis without first registering every temporary result as a fixed metric.

> **Development preview.** Querying your data requires a configured service and data permissions. There is no published production-ready release. [Check the current status](docs/development-status.md).

## An example: from a question to a result

With the public sample order dataset (TPCH) configured, an AI assistant can turn this request into a metric query:

> For sample orders 1–5, show revenue, order count and average order value by customer segment and order status.

MetricSpire uses the published definitions to calculate the result. Here are the first two rows of the repository's [expected sample result](testdata/acceptance/databricks-tpch/expected.json):

| Customer segment | Order status | Revenue | Orders | Average order value |
| --- | --- | ---: | ---: | ---: |
| BUILDING | F | 228570.52 | 1 | 228570.52 |
| BUILDING | O | 203010.51 | 1 | 203010.51 |

In this sample, revenue is the sum of the selected orders' total prices. The [definitions](testdata/acceptance/databricks-tpch/model.yaml) and [structured query](testdata/acceptance/databricks-tpch/query.json) are available to inspect. These are stored fixture results, not a live demo. The AI client interprets the question; MetricSpire validates and executes the structured request.

## Choose where to start

| Your goal | Start here | You will need |
| --- | --- | --- |
| Use your team's service from local Codex CLI | [Get the team installer](docs/mcp-installer.md#get-the-installer) | A deployed instance, its team installer, and your own Databricks access. |
| Connect another MCP client | [MCP connection guide](docs/mcp.md#connect) | An instance URL and the authentication setup supported by that deployment. |
| Understand metric definitions without connecting to data | [Run the offline example](docs/getting-started.md#compile-and-inspect-a-plan-without-a-database) | Go; no database or cloud account. This produces query plans, not data results. |
| Run a service for your team | [Service setup](docs/getting-started.md#run-the-http-service) | PostgreSQL, identity configuration and an analytical engine. |

Already have web access? Open your team's instance and start in the **metric catalog**. This repository does not include a public instance or your company's metric definitions.

## How it fits into your data stack

Your team connects reviewed metric definitions to existing warehouse tables. MetricSpire then checks the user's access, selects the active definition, runs the query and returns a table of typed values. The web UI, HTTP API and MCP tools share the same service. Maintainers can review and publish definition changes, retain history and roll back to an earlier version.

**Databricks SQL is the first analytical engine.** PostgreSQL stores definitions, versions and audit records. Your data still needs to be collected and prepared upstream; MetricSpire does not replace that pipeline or provide a chart dashboard. It does not host an AI model or accept arbitrary SQL from callers.

Exploratory analysis uses a reviewed, structured semantic catalog; see [external semantics and configuration](docs/exploration.md). A separate opt-in path supports [online HTTP queries](docs/online-query.md) over precomputed PostgreSQL aggregates. Both require configuration and acceptance.

## Documentation and project status

- [Getting started](docs/getting-started.md) — offline example, local catalog and service setup.
- [MCP integration](docs/mcp.md) · [HTTP API](docs/http-api.md) — connect a client or application.
- [Team configuration](config/README.md) — private settings, business aliases and reviewed semantic exports.
- [Architecture](docs/architecture.md) — definitions, permissions and execution boundaries.
- [Development status](docs/development-status.md) — implemented features, verification evidence and remaining gaps.
- [Contributing](CONTRIBUTING.md) · [All documentation](docs/README.md).

Production readiness, multi-engine analytics and production latency or availability guarantees are not established. Trial metrics remain visibly unverified. The semantic contract, [`metricspire.io/v1alpha1`](contracts/metricspire.schema.json), is versioned separately from the planned product `v0.1.0` release.

Licensed under [Apache 2.0](LICENSE). Public examples use neutral data; keep company definitions, credentials and production data outside this repository.
