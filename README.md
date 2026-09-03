# MetricSpire

**Define metrics once. Serve every application and AI agent.**

MetricSpire is an open-source semantic metrics layer and governed data API. It compiles reviewed metric definitions into immutable releases, authorizes structured queries, and executes bounded plans through replaceable analytical-engine adapters.

> Current status: Phase 2 passed on 2026-09-02. Phase 3 remains in progress. The portable HTTP/OIDC/OCI profile is implemented and locally tested. On 2026-09-04, the company Databricks Apps profile also passed a single-publisher staging path: the packaged Go service ran behind managed ingress, used a dedicated Lakebase schema for its control plane, published immutable v1 and v2 definitions, queried a fixed read-only TPCH fixture with the real user's authorization, switched back to v1, propagated cancellation, and recorded the expected release/query audit events. Results matched the reviewed golden data, and scans found no credential values in the App logs, control tables, or repository. A separate ordinary query user and a user denied by Unity Catalog are still required to prove the two negative authorization cases. This is not a production-ready platform release.

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
- verifies generic OIDC issuer/signature/expiry claims, separates the browser client audience from the API JWT access-token audience, and supports Authorization Code + PKCE browser login with encrypted, HTTP-only sessions;
- supports a fail-closed Databricks Apps identity profile in which authenticated users receive query access, one stable publisher-group ID additionally grants management, and the forwarded user token exists only in the in-memory engine-execution context;
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

`context.json` is trusted only in this offline CLI demonstration. The HTTP transport creates `RequestContext` from its configured trusted identity adapter; callers cannot self-report tenant, principal, roles, policy, binding, engine, or manifest fingerprint in `SemanticQuery`. The portable OIDC profile selects the issuer, browser client ID, API bearer audience, and claim names without binding MetricSpire to Databricks identity. Browser sessions derive from verified ID tokens; API bearer authentication requires a locally verifiable JWT access token for the configured API audience. The Databricks Apps profile instead consumes only managed-ingress identity plus the current-user API, and has no separate MetricSpire login endpoint.

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

Databricks Apps may use Lakebase without a static database password. In that profile, set `METRICSPIRE_LAKEBASE_ENDPOINT` to the App database-resource endpoint and use the platform-injected `PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER`, `PGSSLMODE`, `DATABRICKS_HOST`, `DATABRICKS_CLIENT_ID`, and `DATABRICKS_CLIENT_SECRET`. MetricSpire obtains a one-hour database credential only when the PostgreSQL pool opens a physical connection. `METRICSPIRE_DATABASE_URL` and `METRICSPIRE_LAKEBASE_ENDPOINT` are mutually exclusive; `serve` still never runs migrations automatically.

## HTTP service

`metricspire serve --config <runtime.yaml>` composes the existing catalog, query, audit, identity, job, UI, and Databricks adapter boundaries. Runtime configuration selects exactly one authentication provider: portable OIDC or Databricks Apps. Files contain only non-secret routes and provider metadata; database URLs, OIDC session/client secrets, and engine credentials remain environment inputs. See [`examples/runtime.example.yaml`](examples/runtime.example.yaml), [`examples/runtime.databricks-apps.example.yaml`](examples/runtime.databricks-apps.example.yaml), and the [Phase 3 HTTP contract](docs/phase3-http-api.md).

Platforms that allocate a port at runtime can pass `--http-address 0.0.0.0:<port>` as a trusted process argument without rewriting the reviewed configuration. The server accepts HTTP/1.1, HTTP/2 over TLS, and unencrypted HTTP/2 (H2C) on the same listener; TLS normally terminates at the deployment ingress.

Run `metricspire migrate` explicitly before starting the service. `serve` never migrates PostgreSQL on startup and never creates analytical tables. The current Databricks adapter emits bounded, parameterized `SELECT` statements only.

The repository also provides a multi-stage OCI [`Dockerfile`](Dockerfile). The runtime image is a static binary plus CA certificates and license notices, runs as numeric non-root user `65532`, and contains no shell or credentials. Its local Phase 3 build produced a 4.28 MB image and ran the version command with a read-only root filesystem, all Linux capabilities removed, and privilege escalation disabled. Mount a reviewed runtime configuration and its policy/binding files read-only under `/etc/metricspire`; inject secret environment variables through the deployment platform. Run `metricspire migrate` as a separate one-shot deployment step before starting replicas. A successful local build is packaging evidence, not deployed-service acceptance.

Unauthenticated `GET /health/live` reports process liveness. `GET /health/ready` pings only the PostgreSQL control plane and returns a generic `503 not_ready` without connection details when unavailable; it never queries Databricks. These endpoints are intended for platform probes, not as acceptance evidence.

## Distribution and deployment profiles

MetricSpire has one product core and two deployment profiles. They are release adapters, not separate products:

- **Open-source self-hosting**: the planned `v0.1.0` release will publish source, checksummed static Linux binaries, and versioned OCI images. The OCI image is the portable default for containers, Kubernetes, or a VM with a container runtime; the raw binary remains useful for minimal VM and local installations. PostgreSQL and analytical engines stay external. Release automation, an SBOM, provenance/signing, upgrade notes, and a clean-room/secret scan are Phase 5 release gates, not claims already satisfied by this development checkout. A Helm chart is intentionally deferred until real operators need one.
- **Databricks Apps company profile**: the same Go service is intended to run behind Databricks Apps ingress and use a separately managed PostgreSQL-compatible control plane and a SQL warehouse resource. Databricks Apps deploys source with its own runtime and `app.yaml`; it does not consume this repository's OCI `Dockerfile`. The official development and dependency contract covers Python and Node.js, while custom commands leave packaged executables technically possible but do not make Go an officially documented runtime. On 2026-09-03, the acceptance-only [`deploy/databricks-apps-probe`](deploy/databricks-apps-probe) package ran in a resource-free staging custom App as an `amd64` Go 1.27.0 process. Authenticated `GET /health/live` returned 200, stop/start returned to 200, and the App was left stopped. Databricks ingress negotiated HTTP/2 externally but forwarded HTTP/1.1 to the process. This closes technical Go execution for that staging runtime; it does not establish official Go support or complete the full service identity/data acceptance.

On 2026-09-04, the dedicated staging Lakebase project was bound to the full App. An explicitly approved one-shot migration created only the dedicated `metricspire` schema and its seven control-plane tables, after which the migration switch was removed and the normal package was redeployed. Short-lived OAuth database credentials worked without being persisted; a forced read-only reconciliation confirmed both registered migrations, two immutable releases, the final v1 active pointer, and the publication/rollback/query audit records. No analytical table was created or modified.

The reviewable [`deploy/databricks-apps`](deploy/databricks-apps) package runs the full service as checksum-protected amd64 and arm64 gzip files. The staging App has one dedicated Lakebase resource, one read-only SQL warehouse resource, and one dedicated publisher group. Managed-ingress identity and publisher management passed with the available real publisher. The first attempt used `sql:restricted-query`, but Databricks returned 403 for the forwarded-user Statement API request even though the same user could query the same warehouse directly. With the App's staging scope changed to `sql`, the same governed query succeeded. This is observed staging compatibility evidence, not a claim about the undocumented cause of that difference.

After acceptance, the staging App compute was stopped to avoid idle cost. The App definition, resources, releases, and audit evidence remain available for a reviewed restart.

Databricks Apps changes only the deployment authentication adapter, not the semantic or policy core. Every verified App user receives the configured query-policy role plus `query:execute`; one stable publisher group additionally receives `model:manage`. Interactive SQL uses the request's short-lived token, so Unity Catalog remains authoritative for data access. The successful staging flow and credential scans now verify this behavior for the publisher identity. The ordinary-user management denial and negative Unity Catalog case remain unverified until a second suitable real user is available. Generic OIDC remains the portable self-hosted adapter.

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
