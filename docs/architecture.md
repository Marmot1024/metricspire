# Architecture

MetricSpire v1alpha1 is a modular monolith with a deliberately small trusted core. The current repository implements compilation and planning only; execution, HTTP, MCP, and real engine adapters enter later phases without changing the semantic truth model.

## Boundaries

```text
reviewed source                         trusted runtime input
──────────────                          ─────────────────────
SemanticModel ──> SemanticManifest      SemanticQuery
PolicySource  ──> PolicyBundle          RequestContext (after authentication)
SourceBinding (per environment)         EngineCapabilities
                         │                  │
                         └──── planner ─────┘
                                  │
                         LogicalPlan -> PhysicalPlan
```

- `SemanticModel` is portable and contains no endpoint, credential, tenant, or principal.
- `SemanticManifest` is normalized, validated, immutable, and addressed by SHA-256.
- `PolicyBundle` has a separate lifecycle and is bound to one manifest fingerprint and tenant.
- `SemanticQuery` describes intent only. Strict decoding makes `context` and `sql` unknown fields.
- `RequestContext` is trusted only when a transport creates it after authentication.
- `SourceBinding` maps logical datasets and fields to structured table (`catalog/schema/table`) or file references; it contains no credentials or SQL fragments.
- `LogicalPlan` explains metric dependencies, dimensions, safe join paths, time semantics, output order, and lineage without choosing SQL.
- `PhysicalPlan` binds that plan to one engine after capability checks. Phase 2 adapters will turn it into parameterized engine requests.

## Determinism and integrity

The compiler sorts definition sets and preserves expression/query argument order where order changes meaning. Fingerprints use compact UTF-8 JSON with deterministic object keys and no timestamps. A compiled manifest or policy and a logical plan are re-fingerprinted before use, so mutated artifacts are rejected.

Fingerprints identify content; they are not signatures. Production publication must still authenticate the artifact channel and protect it from unauthorized replacement.

## Metric semantics

The first contract intentionally supports a constrained expression tree:

- `sum(field)` for additive aggregates;
- field-based arithmetic for ratios;
- metric references plus arithmetic for derived metrics;
- exact numeric literals encoded as strings.

Fields are qualified by entity. Derived dependencies form a checked acyclic graph and are emitted in dependency order. Draft metrics and draft dependencies are not queryable.

Arithmetic is exact at the contract boundary. Decimal literals are strings, nulls propagate, and division by a zero or null denominator is defined to return null; every SQL emitter must preserve this behavior.

## Time and joins

`TimeRange` only filters RFC 3339 instants over a half-open interval and normalizes them to UTC. `TimeGrouping` is a separate contract, so filtering by time cannot silently add a group. Day, week, and month grouping carries an IANA timezone because a UTC timestamp does not define a business day. Weekly queries must additionally choose Monday or Sunday as the week start; fiscal calendars remain a future explicit contract rather than an implicit engine default.

Relationships are directed from a fact-like entity to a lookup-like entity and must be `many_to_one`. The planner rejects missing and ambiguous paths. One-to-many, many-to-many, cross-engine, and implicit joins are outside v0.1.

## Authorization

Policy matching uses trusted tenant, principal, and roles. Matching allow rules can jointly grant requested metrics and dimensions; any matching explicit deny wins. Missing identity, tenant mismatch, missing grants, and mismatched manifest fingerprints all fail closed.

The same policy call will be used by CLI, HTTP, MCP, and SDK entry points. Transport middleware may authenticate, but it cannot replace core authorization.

## Engine boundary

An adapter declares expression operations, join cardinalities, time granularities, and join limits. The planner refuses an unsupported plan before execution. The semantic core does not claim all engines are equivalent and contains no database driver.

Phase 2 adds Databricks SQL asynchronous submit/status/cancel and result limits. DuckDB will be a separately built local demonstration and conformance oracle. No production service binary will link DuckDB or CGO.
