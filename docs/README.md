# Documentation

New to MetricSpire? Read the [overview and sample result](../README.md) first, then choose a path. Detailed guides are currently in English; [中文](../README.zh-CN.md) and [日本語](../README.ja.md) overviews cover the same product scope.

## Use your team's service

- **Web:** open your team's instance and start in the metric catalog. The repository does not provide a public instance.
- **Local Codex CLI:** [get the team installer](mcp-installer.md#get-the-installer), install it using your existing Databricks profile, and verify a first query.
- **Other AI clients:** read [MCP integration](mcp.md#connect) for deployment-specific connection and authentication requirements.
- **Applications:** read the [HTTP API and sample request](http-api.md#example-query-a-published-metric).

## Learn or operate

- [Offline example](getting-started.md#compile-and-inspect-a-plan-without-a-database) — compile metric definitions and inspect a plan without a database.
- [Service setup](getting-started.md#run-the-http-service) — prepare catalog storage, identity, bindings and engine access.
- [Team configuration](../config/README.md) — maintain private runtime settings, business aliases and reviewed semantic exports.
- [Development status](development-status.md) — distinguish implemented features, acceptance evidence and remaining gaps before a pilot.

## Reference

| Document | Read it for |
| --- | --- |
| [Architecture and adapter support](architecture.md) | Integration diagram, OLAP versus metric storage, shipped adapters and extension contracts. |
| [MCP integration](mcp.md) | Tool workflow, metric selection, errors and job handling. |
| [HTTP API](http-api.md) | Requests, permissions, results, errors and audit limits. |
| [Exploratory analysis](exploration.md) | Connect an external semantic layer for temporary queries over approved sources. |
| [PostgreSQL online query](online-query.md) | Optional synchronous queries over precomputed aggregates. |
| [Doris online query](doris-online.md) | Bounded internal aggregate serving through the MySQL protocol. |
| [ClickHouse online query](clickhouse-online.md) | Bounded aggregate HTTP serving with PostgreSQL metadata. |
| [Runtime engines](runtime-engines.md) | Configure metadata, analytical queries, online serving and exploration separately. |
| [PostgreSQL table responsibilities](postgres-tables.md) | Control/history tables, business aggregates and cleanup boundaries. |
| [Native MCP acceptance](mcp-native-acceptance.md) | Maintainer checks for a specific deployment and client. |
| [Contributing](../CONTRIBUTING.md) | Development workflow and checks, including documentation validation. |

The [Databricks Apps package guide](../deploy/databricks-apps/README.md) is for maintainers deploying a dedicated staging instance. The machine-readable [semantic contract](../contracts/metricspire.schema.json) and neutral [examples](../examples/orders/model.yaml) are available beside the code.

Keep environment receipts, historical phase logs, credentials and private business assets outside public documentation. Use ignored local notes for non-secret working records; never commit secrets.
