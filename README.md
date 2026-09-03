# MetricSpire

**Define metrics once. Serve every application and AI agent.**

MetricSpire is an open-source semantic metrics layer and governed data API. It compiles reviewed metric definitions into immutable releases, authorizes structured queries, and executes bounded plans through replaceable analytical-engine adapters.

> Current status: Phase 2 passed on 2026-09-02. Phase 3 is in progress: the HTTP product boundary, server-resolved policy/binding, asynchronous jobs, query audit, catalog search, minimal UI, generic OIDC login, and `serve` composition are implemented and locally tested. Acceptance against a real deployment identity provider is still required; this is not yet a production-ready platform release.

## Version names

- `v0.1.0` is the planned first product release; development builds report `0.1.0-dev`.
- `metricspire.io/v1alpha1` is the machine-readable contract version. It can evolve independently of the product release.
- Internal architecture-review revisions are not product or API versions.

## What it does

- stores drafts, revisions, immutable releases, the active-release pointer, and release events in PostgreSQL;
- compiles a constrained expression tree (`sum`, `count`, `count_distinct`, `avg`, `min`, `max`, metric references, arithmetic, and governed filters), never caller-provided SQL;
- keeps portable metric definitions separate from environment-specific table and column bindings;
- evaluates metric policy and request budgets before engine execution;
- builds deterministic logical and physical plans with content fingerprints;
- executes parameterized Databricks SQL with timeout, cancellation, typed results, row limits, and cumulative response-byte limits;
- rejects unsupported engine capabilities before submitting a query.
- exposes separate management and query permissions through a strict HTTP API with Problem responses, body/deadline limits, security headers, asynchronous job status/cancellation, and fail-closed query audit;
- verifies generic OIDC issuer/audience/signature/expiry claims and supports Authorization Code + PKCE browser login with encrypted, HTTP-only sessions;
- serves a dependency-free minimal UI for catalog search, explain/plan/query, job cancellation, draft load/save/validation, publication, release listing, and rollback; every action calls the same HTTP API rather than duplicating application logic.

## Core flow

```text
SemanticModel ──compile/publish──> immutable SemanticManifest
       │                                  │
       └──── PostgreSQL draft/release lifecycle ──── active release

SemanticQuery + server-authenticated RequestContext + server-resolved PolicyBundle
                         └──> LogicalPlan
LogicalPlan + server-resolved SourceBinding + EngineCapabilities
                         └──> PhysicalPlan
PhysicalPlan ──> QueryEngine adapter ──> bounded TypedResult
```

PostgreSQL is the transactional control plane; it does not store analytical facts or query results. Databricks is the first analytical adapter, not a required control-plane dependency. Future engines implement the same `QueryEngine` boundary without changing the catalog lifecycle.

Warehouse-native permissions remain authoritative for catalogs, schemas, tables, views, rows, and columns. MetricSpire adds only product-level governance that a warehouse does not model directly: metric publication, metric/dimension grants, active releases, request budgets, and audit context.

## Local semantic workflow

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

`context.json` is trusted only in this offline CLI demonstration. The HTTP transport creates `RequestContext` from a cryptographically verified OIDC identity; callers cannot self-report tenant, principal, roles, policy, binding, engine, or manifest fingerprint in `SemanticQuery`. OIDC is provider-neutral: deployment configuration selects the issuer and claim names without binding MetricSpire to Databricks identity.

## Local PostgreSQL workflow

The Compose password below is deliberately local-development-only. Do not reuse it in a deployed environment.

```bash
docker compose up -d postgres
export METRICSPIRE_DATABASE_URL='postgres://metricspire:metricspire_dev_only@127.0.0.1:54329/metricspire?sslmode=disable'

go run ./cmd/metricspire migrate
go run ./cmd/metricspire draft-put \
  --namespace demo --source examples/orders/model.yaml \
  --actor local-user --expected-revision 0
go run ./cmd/metricspire publish \
  --namespace demo --model commerce --revision 1 --actor local-user
go run ./cmd/metricspire release-list \
  --namespace demo --model commerce
```

`query-active` additionally requires a reviewed engine binding, a read-only Databricks SQL warehouse, and OAuth M2M credentials (or an explicit token for local development). Credentials are environment inputs and must never be committed. See the [Phase 2 acceptance record](docs/phase2-acceptance.md) for the opt-in real-engine tests.

## HTTP service

`metricspire serve --config <runtime.yaml>` composes the existing catalog, query, audit, OIDC, job, UI, and Databricks adapter boundaries. The runtime file contains only non-secret routes and OIDC metadata; database URLs, session keys, OIDC client secrets, and engine credentials remain environment inputs. See [`examples/runtime.example.yaml`](examples/runtime.example.yaml) and the [Phase 3 HTTP contract](docs/phase3-http-api.md).

Run `metricspire migrate` explicitly before starting the service. `serve` never migrates PostgreSQL on startup and never creates analytical tables. The current Databricks adapter emits bounded, parameterized `SELECT` statements only.

## Contracts and guarantees

The `v1alpha1` schema registry is [`contracts/metricspire.schema.json`](contracts/metricspire.schema.json). The Go implementation additionally checks references, cycles, types, time semantics, join cardinality, policy, budgets, fingerprints, and engine capabilities.

Important guarantees:

- time ranges are half-open `[start, end)`; grouping declares a business timezone and weekly queries declare their week start;
- v0.1 relationships are directed `many_to_one` joins, so grouping cannot silently multiply facts;
- draft or inactive releases cannot be selected by a caller;
- published metric codes cannot be silently removed or change execution semantics;
- policy evaluation fails closed and explicit deny wins;
- values are parameterized and physical identifiers come only from validated bindings;
- unknown fields, duplicate JSON/YAML keys, raw SQL, and identity fields in a query are rejected.

## Verification

```bash
go test ./...
go test -race ./...
go vet ./...
go mod verify
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' ./cmd/metricspire
```

Real PostgreSQL and Databricks acceptance is explicit and opt-in; skipped tests or mock responses are never reported as real integration proof.

Read the [architecture note](docs/architecture.md), [Phase 1 acceptance](docs/phase1-acceptance.md), [Phase 2 acceptance](docs/phase2-acceptance.md), and [Phase 3 HTTP contract](docs/phase3-http-api.md) for boundaries and evidence.

## Non-goals for v0.1

MetricSpire is not an ETL platform, BI dashboard, data warehouse, arbitrary SQL gateway, cross-engine execution engine, or large-result export system. v0.1 does not include MCP, AI query generation, Redis, Kafka, a complex approval workflow, or simultaneous support for multiple analytical engines.

## License

Apache License 2.0. This repository is an independent clean-room implementation using neutral public examples. It contains no legacy source, private business schema, credentials, or inherited Git history.
