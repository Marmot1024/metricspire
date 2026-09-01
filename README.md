# MetricSpire

**Define metrics once. Serve every application and AI agent.**

MetricSpire is an open-source semantic metrics layer and governed data API. It compiles reviewed YAML or JSON definitions into immutable manifests, authorizes structured metric queries, and creates deterministic logical and physical plans for engine adapters.

> Current status: Phase 1 semantic core accepted on 2026-09-01. It plans queries but does not execute production data yet. The first production adapter will be Databricks SQL in Phase 2. DuckDB will remain an optional local demo and integration-test artifact, never a production data store.

## Why MetricSpire

- one reviewed definition for metrics, dimensions, time, entities, and safe joins;
- no raw SQL in the public query contract;
- caller intent is separate from transport-authenticated identity;
- runtime policy is compiled separately from portable semantic definitions;
- identical inputs produce content-addressed manifest and plan fingerprints;
- physical datasets use structured resources and columns live in environment-specific bindings;
- engine capabilities fail before execution instead of failing inside a warehouse.

## Core flow

```text
SemanticModel ──compile──> immutable SemanticManifest
PolicySource + manifest ──compile──> immutable PolicyBundle

SemanticQuery + trusted RequestContext + PolicyBundle
                         └──> LogicalPlan
LogicalPlan + SourceBinding + EngineCapabilities
                         └──> PhysicalPlan
```

The core contains no database driver, HTTP framework, Redis client, message queue, or Python runtime. It is a Go 1.26+ module with one runtime dependency for strict YAML decoding.

## Try the Phase 1 slice

Requirements: Go 1.26 or 1.27.

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

`context.json` is trusted only because this is an offline CLI demonstration. HTTP and MCP transports must create `RequestContext` after authentication; a client-supplied query can never self-report its tenant, principal, or roles.

Run the acceptance suite:

```bash
go test -race ./...
go vet ./...
CGO_ENABLED=0 go build ./cmd/metricspire
```

## Contracts and guarantees

The machine-readable v1alpha1 registry is [`contracts/metricspire.schema.json`](contracts/metricspire.schema.json). The Go implementation additionally applies reference, cycle, type, time, cardinality, policy, and capability checks that JSON Schema cannot express.

Important guarantees:

- time filtering and time grouping are separate; ranges are half-open `[start, end)`, grouping declares business timezone, and weekly queries declare their week start;
- instants normalize to UTC while grouping retains an explicit IANA business timezone;
- v0.1 relationships are only directed `many_to_one` joins, so grouping cannot silently multiply facts;
- draft metrics cannot be queried;
- policy evaluation is fail-closed and explicit deny wins;
- manifest, policy, logical plan, query, binding, and physical plan are content-addressed;
- unknown fields, duplicate JSON/YAML keys, raw SQL, and identity fields in a query are rejected.

Read [the architecture note](docs/architecture.md) for the boundary decisions and [the Phase 1 acceptance record](docs/phase1-acceptance.md) for reproducible evidence.

## Non-goals for v0.1

MetricSpire is not an ETL platform, BI dashboard, data warehouse, arbitrary SQL gateway, cross-engine execution engine, or large-result export system. PostgreSQL, MySQL, ClickHouse, Redis, and a web UI are intentionally outside the current slice.

## License

Apache License 2.0. This repository is an independent clean-room implementation using a neutral public example; it does not contain the legacy project's source, business schema, configuration, tests, or Git history.
