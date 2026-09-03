# Architecture

MetricSpire `v0.1.0-dev` is a modular monolith with a deliberately small trusted core. The public contracts remain `metricspire.io/v1alpha1`; product and contract versions have independent lifecycles.

Phase 2 implements one complete backend path: PostgreSQL catalog lifecycle, deterministic semantic planning, and bounded Databricks SQL execution. Phase 3 adds provider-neutral HTTP management/query APIs, generic OIDC authentication, server-resolved trusted inputs, durable query audit, asynchronous jobs, a basic UI, and explicit runtime composition around the same application services. A full Databricks Apps staging deployment has passed with one real publisher identity; multi-user negative authorization and a real portable OIDC deployment remain open.

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
              │          └──> LogicalPlan
PolicyResolver┘
BindingResolver + EngineCapabilities
                         └──> PhysicalPlan
                                   └──> audited async job ──> QueryEngine ──> TypedResult
                                           │
                                           └── Databricks SQL (first adapter)
```

- `CatalogRepository` owns transactional drafts, revisions, releases, the active pointer, and release events. PostgreSQL stores JSONB definitions and manifests, not analytical facts or query results.
- `QueryEngine` owns analytical execution, capabilities, timeout/cancellation, and typed results. It is a different adapter family from `CatalogRepository`.
- `SourceBinding` maps logical datasets and fields to structured physical resources for one engine and environment. It contains no credentials or SQL fragments.
- Databricks-specific SQL and HTTP behavior stays under `internal/adapter/databricks`; the semantic, catalog, application, and planner packages do not depend on it.
- `Authenticator` returns a trusted principal; the HTTP layer creates `RequestContext`. `PolicyResolver` and `BindingResolver` select trusted configuration by namespace/model/tenant. None of these values are accepted in a public query body.
- `JobManager` owns product job IDs, deadlines, ownership checks, status, and cancellation. The engine adapter still owns its remote statement lifecycle.
- query audit is fail-closed: an admission event must be durable before execution; completion-audit failure withholds the result. Audit rows contain fingerprints and outcome metadata, never SQL, filter values, credentials, or result rows.
- The PostgreSQL repository remains provider-neutral. Static deployments use a normal URL; a deployment adapter may instead supply a short-lived password before each new physical pool connection. The Databricks Apps adapter exchanges its service-principal OAuth token for a one-hour Lakebase database credential without persisting it, putting it in the URL, or exposing provider APIs to catalog code. Static URLs and Lakebase endpoint configuration are mutually exclusive, and TLS-disabled Lakebase connections fail closed.

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

`RequestContext` becomes trusted only when a transport constructs it after authentication. The current CLI accepts a context file solely for local and integration testing. The Phase 3 HTTP transport strictly rejects unknown query fields, including caller-supplied identity, policy, binding, engine, manifest fingerprint, or SQL, and invokes the same fail-closed policy path.

The HTTP package keeps a provider-neutral `Authenticator`; the first concrete adapter is standards-based OIDC. It verifies discovery metadata, JWKS signatures, issuer, audience, expiry, tenant, roles, and product permissions. Browser login uses Authorization Code + PKCE, state, nonce, and AES-256-GCM encrypted HTTP-only sessions derived from an ID token for the browser client. API callers use a signed JWT access token for the independently configured `bearer_audience`; the adapter rejects opaque tokens rather than introspecting them or accepting an ID token as an API credential. Databricks OAuth M2M remains a separate service-to-engine credential and can never represent an end user.

OIDC implementation tests use locally generated RSA signatures and an isolated protocol fixture. They prove the cryptographic and session behavior, not compatibility with a particular enterprise identity provider; issuer registration, redirect URI, claim mapping, and real login/logout remain deployment acceptance work.

The portable self-hosted profile initially uses a least-privileged read-only engine identity restricted to approved data resources. End-user identity passthrough is engine- and host-specific, so it must be implemented as an adapter rather than changing the semantic or policy core.

For the company Databricks Apps profile, the platform already authenticates access to the app. The Apps adapter is enabled only when its configured app name, workspace ID, public origin, and the managed runtime environment agree. It derives the principal from managed-ingress headers, uses the forwarded token with the bounded current-user API to bind those headers to one active Databricks identity, gives every such user `query:execute` plus one configured query-policy role, and grants `model:manage` only to one configured stable publisher group. This deliberately avoids a generic group-to-role permission engine. User-triggered SQL requires that request's short-lived token, so Unity Catalog evaluates the real user's catalog, table, row-filter, and column-mask permissions. The token is copied only into the asynchronous engine context and explicitly masked from policy, planning, audit, persistence, errors, and public job state. App service-principal authorization remains reserved for reviewed background/shared operations. The implementation and isolated tests exist locally; the Apps scopes, full-service deployment, negative Unity Catalog case, and token non-persistence still require staging acceptance.

## Determinism and integrity

The compiler sorts definition sets and preserves expression/query argument order where order changes meaning. Fingerprints use compact UTF-8 JSON with deterministic object keys and no timestamps. Compiled artifacts and plans are re-fingerprinted before use, so mutated content is rejected.

Fingerprints identify content; they are not signatures. A deployed control plane must still authenticate writers, protect PostgreSQL, and secure the artifact channel.

## Query and execution budgets

The planner limits metrics, dimensions, filters, values per filter, total parameters, SQL size, joins, and result rows before submission. The Databricks adapter carries the actual request row limit into the execution job, validates all result chunks cumulatively against the byte budget, and issues remote cancellation after caller cancellation or timeout.

Small typed JSON results are the v0.1 boundary. Streaming export, external result locations, distributed cross-engine execution, and materialization are later capabilities.

## HTTP and UI boundary

Management and query permissions are separate. Management endpoints validate, save drafts, publish, list releases, and rollback; query endpoints search the active catalog, explain, plan, submit jobs, fetch status, and cancel. Explain creates only an authorized logical plan. Plan may resolve a physical binding but never calls an analytical engine. Query submission is asynchronous, so network write deadlines are independent of query deadlines.

All dynamic JSON is decoded with unknown-field and duplicate-key rejection plus a byte limit. Responses carry request IDs, no-store and browser security headers; cross-origin state changes are rejected. Errors use one `application/problem+json` shape. The embedded UI has no separate business logic, token field, or fake login; catalog, explain/plan/query, job cancellation, draft load/save/validation, publication, release listing, and rollback all call the same API and use the OIDC session.

`metricspire serve --config` performs explicit dependency assembly. Non-secret HTTP/OIDC/policy/binding routes come from strict YAML or JSON. PostgreSQL URLs, the 32-byte session key, optional OIDC client secret, and Databricks credentials come only from environment variables. Startup does not run migrations or perform an analytical query. Shutdown stops HTTP admission, cancels and waits for background jobs and their completion audit, then closes PostgreSQL.

The generic OCI image runs a static binary as a non-root numeric user with no shell. Migrations remain a separate one-shot command. `/health/live` checks only the process; `/health/ready` checks PostgreSQL with a bounded context and never probes Databricks, so deployment health cannot create warehouse traffic.

## Distribution and deployment profiles

The public project remains vendor-neutral and uses conventional release artifacts:

- source plus license and third-party notices;
- checksummed static Linux binaries for direct installation;
- immutable, versioned OCI images for containers, Kubernetes, and VM-based container runtimes;
- release-time SBOM, provenance/signing, upgrade notes, secret scanning, and clean-room review.

These artifacts run the same `metricspire serve` binary and keep PostgreSQL and analytical engines external. Docker Compose is a development convenience, not the production topology. A Helm chart and package-manager integrations are deliberately outside `v0.1` until operator evidence justifies their maintenance cost.

Databricks Apps is a separate company deployment profile, not the open-source runtime contract. It uses Databricks-managed ingress, identity, app resources, and source deployment. Databricks Apps does not build from the OCI `Dockerfile`; its documented development and dependency flows cover Python and Node.js before running an optional `app.yaml` command. The custom command is evidence that a packaged executable may be startable, not an official Go support statement. MetricSpire accepts HTTP/1.1 and H2C on one listener using the Go 1.26 standard library, and `serve --http-address` lets `app.yaml` supply `0.0.0.0:$DATABRICKS_APP_PORT` without editing reviewed configuration. An acceptance-only probe packages checksum-protected amd64 and arm64 static servers below the 10 MB per-file limit; a standard-library Python bootstrap selects, verifies, extracts, and replaces itself with the Go process.

The resource-free staging probe passed on 2026-09-03. Databricks selected the `amd64` archive and ran Go 1.27.0 on port 8000; authenticated managed-ingress requests returned 200 before and after stop/start. The edge connection was HTTP/2 while the request observed by the Go process was HTTP/1.1, so H2C remains supported but is not required by the observed forwarding path. No PostgreSQL, database, SQL warehouse, secret, production workspace, or business data was contacted. Stop/start reached the expected platform states, but the log stream closed before shutdown logs could be retained, so managed-runtime signal delivery remains unproven. The probe was left stopped; later full-service evidence is described below.

A separate dedicated Lakebase project was created in staging with PostgreSQL 17, a single 0.5 CU read-write endpoint, 60-second autosuspend, two-day history retention, and native password login disabled. The full App later obtained rotating OAuth credentials and connected successfully. One approved migration created a dedicated `metricspire` schema and seven control-plane tables; the normal serving package contains no migration switch. Read-only reconciliation proved both migrations, v1/v2 immutable releases, the active pointer, release events, and query audit. Lakebase stores control metadata only, not analytical facts or query results.

The full staging package under `deploy/databricks-apps` reuses the checksum/bootstrap route with the actual `metricspire` binary and fixed read-only TPCH acceptance policy/binding. On 2026-09-04 the full App was `RUNNING` with active compute, a dedicated publisher group, one Lakebase resource, and one SQL warehouse resource. The available publisher identity passed managed-ingress binding, draft/publication, catalog/plan, asynchronous query, v1/v2 active-release switching, rollback, cancellation, golden-result reconciliation, and durable audit. App-log, control-table, and repository scans found no credential values. `sql:restricted-query` returned 403 only on the forwarded-user Statement API path; staging therefore currently uses `sql`, which succeeded for the same user and warehouse. The precise platform reason remains unknown and the broader scope must be reviewed again before production.

The App compute was stopped after acceptance to avoid idle cost. Stopping compute did not delete the App configuration, Lakebase control data, releases, or audit evidence.

## Current exclusions

Phase 3 still has no accepted full-service self-hosted enterprise identity-provider deployment. The Databricks Apps single-publisher path has passed in staging, but an ordinary query-only user's management denial and a MetricSpire-authorized user's Unity Catalog denial still need separate real identities. MCP transport, shared result cache, message queue, arbitrary SQL, cross-engine joins, and multi-engine routing remain excluded. DuckDB is not a production data engine. ClickHouse, Doris, Trino/Presto, and other adapters must prove conformance independently before being advertised.
