<p align="center"><img src="docs/assets/metricspire-mark.svg" width="62" height="62" alt="MetricSpire mark"></p>

<h1 align="center">MetricSpire</h1>

<p align="center"><strong>Ask for a metric, not a table.</strong></p>

<p align="center">Reviewed definitions · bounded queries · one contract across UI, API, and MCP</p>

<p align="center"><strong>English</strong> · <a href="README.zh-CN.md">简体中文</a> · <a href="README.ja.md">日本語</a></p>

<p align="center"><img src="docs/assets/semantic-workflow.svg" alt="Illustrated path through the neutral orders example: define gross revenue, request it by customer region, then authorize and execute a bounded plan."></p>

<p align="center"><sub>Illustration based on the public <code>orders</code> model; not a product screenshot or a claim of live query results.</sub></p>

<p align="center"><a href="docs/getting-started.md">Explore the example</a> · <a href="docs/mcp.md">Connect an AI client</a> · <a href="docs/architecture.md">Read the architecture</a></p>

MetricSpire is an open-source semantic metrics service. Publish a reviewed definition once; applications and AI agents can then request it by code and dimension through the same governed workflow. The service resolves the active release, applies policy and limits, and delegates physical data access to the analytical engine.

> **Development preview.** The Databricks Apps staging path has been exercised, but there is no production-ready or published `v0.1.0` release. [See the verified boundary](docs/development-status.md).

## Why it exists

Clients ask for **metric codes and dimensions** rather than choosing physical tables or generating unrestricted SQL. MetricSpire owns the definition, approved binding, active version, product policy, and query budgets. The analytical engine still decides whether the real user can read the underlying data.

This is a metric service, not a replacement for your data warehouse, BI system, or engine-native row and column security.

## What is implemented

- **Governed catalog:** optimistic drafts, validation, immutable releases, an active pointer, rollback, and release events in PostgreSQL.
- **Portable semantics:** constrained expressions and logical plans, with environment-specific table and column mappings in reviewed `SourceBinding` files. No caller-provided SQL.
- **Guarded execution:** policy checks, capability checks, deterministic plans, parameterized Databricks SQL, timeouts, cancellation, typed results, row/byte limits, and fail-closed audit.
- **One service, three surfaces:** a maintainer/query UI, strict HTTP management and query APIs, and seven remote MCP query tools using the same application services.
- **Explicit identity boundaries:** portable OIDC for self-hosting or managed identity in Databricks Apps. Product permissions never override warehouse-native data permissions.

Databricks SQL is the first analytical adapter; the core does not claim multi-engine execution today. PostgreSQL is the transactional **control plane**, not a store for analytical facts or query results. See the [architecture](docs/architecture.md) and [HTTP contract](docs/http-api.md) for the trust boundaries.

## Try the semantic workflow

Requires Go 1.26 or 1.27. This offline example compiles a neutral order model and produces plans; it does not need a database or submit a warehouse query.

```bash
mkdir -p dist/demo
go run ./cmd/metricspire compile \
  --source examples/orders/model.yaml \
  --out dist/demo/manifest.json
go run ./cmd/metricspire compile-policy \
  --source examples/orders/policy.yaml \
  --manifest dist/demo/manifest.json \
  --out dist/demo/policy-bundle.json
go run ./cmd/metricspire plan \
  --manifest dist/demo/manifest.json \
  --policy dist/demo/policy-bundle.json \
  --context examples/orders/context.json \
  --query examples/orders/query.json \
  --binding examples/orders/binding.json \
  --capabilities examples/orders/capabilities.json \
  --logical-out dist/demo/logical-plan.json \
  --physical-out dist/demo/physical-plan.json
```

The context file is trusted only in this offline demonstration; network callers cannot supply their own identity, policy, binding, release, engine, or SQL. For local PostgreSQL publication, service startup, and verification commands, continue with [Getting started](docs/getting-started.md).

## Connect an AI client

A deployed instance exposes Streamable HTTP MCP at `/api/v1/mcp` (the preferred Databricks Apps path). Clients connect by URL; they do not clone or run MetricSpire locally. The tools support discovery, explanation, planning, bounded query submission, status, and cancellation—**not** raw SQL or publication.

```text
list_namespaces → search_metrics → explain_query → plan_query
               → submit_query → get_query / cancel_query
```

Each request is authenticated. A Databricks Apps deployment needs a pre-registered public OAuth client and the user's own App and data permissions; a successful browser login alone is not query acceptance. See [MCP integration](docs/mcp.md) for client configuration, tools, safety limits, and the precise [current acceptance boundary](docs/development-status.md).

## Documentation

| Start here | For |
| --- | --- |
| [Getting started](docs/getting-started.md) | Offline plan demo, local catalog workflow, service setup, verification. |
| [MCP integration](docs/mcp.md) | Remote tools, OAuth client setup, request and job boundaries. |
| [Architecture](docs/architecture.md) | Semantic, identity, control-plane, and engine adapter boundaries. |
| [HTTP contract](docs/http-api.md) | Endpoints, permissions, errors, limits, and audit behavior. |
| [Development status](docs/development-status.md) | Maintained preview boundary and known gaps. |

The [documentation index](docs/README.md) separates product guides from maintainer-specific staging instructions. Historical phase acceptance notes are kept locally, not in the public documentation tree.

The machine-readable semantic contract is [`metricspire.io/v1alpha1`](contracts/metricspire.schema.json). That contract version is independent of the planned product `v0.1.0` release.

## Scope and license

MetricSpire does not perform ETL, host an LLM, provide arbitrary SQL access, stream large exports, or join across engines. Unsupported plans fail before execution rather than silently changing semantics.

Licensed under [Apache 2.0](LICENSE). This is an independent clean-room implementation with neutral public examples; do not commit credentials, private schemas, or production data.
