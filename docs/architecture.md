# Architecture

MetricSpire `v0.1.0-dev` is a modular monolith with a deliberately small trusted core. The public contracts remain `metricspire.io/v1alpha1`; product and contract versions have independent lifecycles.

Phase 2 implements one complete backend path: PostgreSQL catalog lifecycle, deterministic semantic planning, and bounded Databricks SQL execution. Phase 3 will add HTTP management/query APIs, end-user authentication, audit delivery, and a basic UI around the same application services.

## Component boundaries

```text
                        transactional control plane
SemanticModel ──> CatalogService ──> CatalogRepository ──> PostgreSQL
     │                 │
     │                 └── draft -> validate -> publish -> active -> rollback
     │
     └──> SemanticManifest + PolicyBundle
                         │
trusted RequestContext + SemanticQuery
                         └──> LogicalPlan
SourceBinding + EngineCapabilities
                         └──> PhysicalPlan
                                   └──> QueryEngine ──> TypedResult
                                           │
                                           └── Databricks SQL (first adapter)
```

- `CatalogRepository` owns transactional drafts, revisions, releases, the active pointer, and release events. PostgreSQL stores JSONB definitions and manifests, not analytical facts or query results.
- `QueryEngine` owns analytical execution, capabilities, timeout/cancellation, and typed results. It is a different adapter family from `CatalogRepository`.
- `SourceBinding` maps logical datasets and fields to structured physical resources for one engine and environment. It contains no credentials or SQL fragments.
- Databricks-specific SQL and HTTP behavior stays under `internal/adapter/databricks`; the semantic, catalog, application, and planner packages do not depend on it.

## Semantic truth and portability

`SemanticModel` contains portable metric codes, descriptions, ownership, dimensions, relationships, and a constrained expression tree. It never accepts an arbitrary SQL expression. `SemanticManifest` is normalized, validated, immutable, and addressed by SHA-256.

`LogicalPlan` explains metric dependencies, dimensions, safe join paths, time semantics, output order, and lineage without selecting a SQL dialect. `PhysicalPlan` applies one validated `SourceBinding` after capability checks. An engine adapter then emits its own parameterized statement.

This is portability by explicit translation, not a claim that all engines are equivalent. The common core stays small; each adapter declares supported expressions, filters, time grouping, join cardinalities, and limits. Unsupported plans fail before execution. Engine-specific features require a typed capability and a reviewed contract change, not a raw-SQL escape hatch.

## Catalog lifecycle

Drafts use optimistic revisions. Publication recompiles the exact expected revision, enforces required ownership/business/verification metadata, compares it with the active release, and inserts a new immutable release. A published metric code cannot disappear or change execution semantics silently; deprecation is explicit and irreversible. Rollback only changes the active pointer to an existing immutable release and records an event.

Query execution resolves the active release on the server. A caller cannot select an unpublished manifest or stale fingerprint.

## Authorization boundary

MetricSpire and the analytical engine enforce different layers:

- the engine remains authoritative for physical catalog/schema/table/view access and row/column security;
- MetricSpire governs metric discovery, publication, allowed metrics/dimensions, release state, request budgets, and audit context;
- a matching engine denial always wins; MetricSpire does not copy or bypass warehouse ACLs.

`RequestContext` becomes trusted only when a transport constructs it after authentication. The current CLI accepts a context file solely for local and integration testing. Phase 3 transports must reject caller-supplied identity and invoke the same fail-closed policy path.

The initial deployment model uses a least-privileged read-only engine identity restricted to approved data resources. End-user identity passthrough remains a later, evidence-driven option because its implementation and guarantees differ across engines.

## Determinism and integrity

The compiler sorts definition sets and preserves expression/query argument order where order changes meaning. Fingerprints use compact UTF-8 JSON with deterministic object keys and no timestamps. Compiled artifacts and plans are re-fingerprinted before use, so mutated content is rejected.

Fingerprints identify content; they are not signatures. A deployed control plane must still authenticate writers, protect PostgreSQL, and secure the artifact channel.

## Query and execution budgets

The planner limits metrics, dimensions, filters, values per filter, total parameters, SQL size, joins, and result rows before submission. The Databricks adapter carries the actual request row limit into the execution job, validates all result chunks cumulatively against the byte budget, and issues remote cancellation after caller cancellation or timeout.

Small typed JSON results are the v0.1 boundary. Streaming export, external result locations, distributed cross-engine execution, and materialization are later capabilities.

## Current exclusions

Phase 2 has no public HTTP/MCP transport, UI, generalized identity provider integration, shared result cache, message queue, arbitrary SQL, cross-engine joins, or multi-engine routing. DuckDB is not a production data engine. ClickHouse, Doris, Trino/Presto, and other adapters must prove conformance independently before being advertised.
