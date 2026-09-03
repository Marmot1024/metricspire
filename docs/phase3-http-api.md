# Phase 3 HTTP contract

Status: **implemented locally; Phase 3 remains in progress**.

This document freezes the first product HTTP boundary. It records what is already proved by local tests and what still needs a real deployment. It is not a claim that production authentication has passed.

## Trust boundary

Clients may submit a namespace/model path and a strict `SemanticQuery`. They cannot submit tenant, principal, roles, policy, source binding, engine route, active release, manifest fingerprint, or SQL. The server obtains those values through four injected dependencies:

1. `Authenticator` returns a trusted tenant, subject, roles, and product permissions.
2. `CatalogRepository` resolves the active immutable release.
3. `PolicyResolver` resolves policy by namespace/model/tenant.
4. `BindingResolver` resolves the reviewed physical binding by namespace/model.

Warehouse-native catalog/schema/table/view/row/column permissions remain authoritative. MetricSpire does not mirror them. Its policy covers metric/dimension grants, publication, budgets, and audit.

## Identity and session

The first concrete `Authenticator` uses standard OIDC and is not Databricks-specific. It verifies provider discovery, JWKS signature, issuer, audience, expiry, stable `sub`, tenant, roles, and the two recognized product permissions. Browser login uses Authorization Code + PKCE with state and nonce; its ID token must target the configured browser `client_id`. API bearer authentication separately requires a signed JWT access token targeting the required `bearer_audience`; configuration fails closed when that API audience is missing, so a browser ID token cannot silently become an API credential. Opaque access tokens are rejected because this adapter performs local verification and has no token-introspection contract. Flow/session cookies are AES-256-GCM encrypted, HTTP-only, SameSite=Lax, and Secure except for an explicitly enabled loopback development origin. Session lifetime never exceeds the ID token lifetime.

Databricks OAuth M2M is a separate engine service credential. It is never accepted as a MetricSpire end-user identity. Runtime secrets are environment variables; YAML/JSON configuration contains only issuer/client metadata and trusted policy/binding file routes.

## Endpoints

All paths are under `/api/v1`.

| Permission | Method and path | Purpose |
| --- | --- | --- |
| `model:manage` | `GET, PUT /namespaces/{namespace}/models/{model}/draft` | read/save an optimistic draft revision |
| `model:manage` | `POST /namespaces/{namespace}/models/{model}/validate` | compile and validate without saving |
| `model:manage` | `POST /namespaces/{namespace}/models/{model}/publish` | publish the exact expected revision |
| `model:manage` | `GET /namespaces/{namespace}/models/{model}/releases` | list release summaries and active state |
| `model:manage` | `POST /namespaces/{namespace}/models/{model}/rollback` | activate an existing immutable release |
| `query:execute` | `GET /catalog/search?namespace=...&q=...&limit=...` | search policy-authorized metrics from active releases only |
| `query:execute` | `POST /namespaces/{namespace}/models/{model}/explain` | authorize and return the logical plan; no engine call |
| `query:execute` | `POST /namespaces/{namespace}/models/{model}/plan` | resolve and return logical/physical plans; no engine call |
| `query:execute` | `POST /namespaces/{namespace}/models/{model}/query` | create a server-side query job; returns `202` |
| `query:execute` | `GET /jobs/{job}` | read a job owned by the same tenant and principal |
| `query:execute` | `POST /jobs/{job}/cancel` | cancel an owned pending/running job |

The minimal UI is served at `/` and calls these endpoints. It exposes catalog search, explain/plan/query, current-job cancellation, draft load/save/validation, publication, release listing, and rollback. It contains no parallel authorization, planning, or execution logic.

`GET /health/live` and `GET /health/ready` are unauthenticated deployment probes outside `/api/v1`. Readiness checks only PostgreSQL and returns a generic `503 not_ready` on failure; neither probe contacts Databricks or exposes configuration details.

## Errors and limits

- JSON requests require `Content-Type: application/json`, reject duplicate/unknown fields, and default to a 1 MiB body limit.
- Control operations default to 15 seconds. Query jobs default to two minutes and propagate cancellation to the engine adapter.
- Catalog search returns at most 100 metrics per request.
- Semantic query budgets from the planner and SQL/result budgets from the engine adapter remain mandatory.
- Errors use `application/problem+json` with stable `code`, HTTP `status`, safe `detail`, optional `path`, and a server-generated `request_id`.
- Responses set no-store, content-type, framing, referrer, and Content Security Policy headers. The runtime server also sets read-header, read, write, idle, and header-size limits.
- Browser state changes with a foreign `Origin` are rejected before authentication or application logic.

## Audit semantics

Before an engine call, a `query_started` event must be recorded. Failure is closed: the engine is not called. Completion records `query_succeeded`, `query_failed`, or `query_cancelled`. If the completion event cannot be stored, results are withheld and the job reports `audit_unavailable`.

Audit stores request/principal/model/release/plan fingerprints, the server-assigned product job ID, outcome, row count, truncation, and timestamps. It never stores generated SQL, filter values, credentials, remote tokens, or result rows.

## Local evidence

The offline suite proves OIDC discovery/JWKS/signature/audience/expiry checks, Authorization Code + PKCE state/nonce/session/logout, permission rejection, forged trusted-field/raw-SQL rejection, cross-origin rejection, body/method/content-type limits, active-release-only behavior, request budgets, explain/plan non-execution, job ownership/status/cancel/timeout, fail-closed audit, policy-filtered catalog search, UI-to-API wiring, graceful job shutdown, race safety, vet, and pure-Go buildability.

The final local gate passed `go test -count=1 ./...`, `go test -count=1 -race ./...`, `go vet ./...`, `go mod verify`, a `CGO_ENABLED=0` build, and the complete suite under the declared minimum Go 1.26.0 toolchain. `govulncheck` reported no reachable vulnerabilities after upgrading the indirect `golang.org/x/text` dependency; `gosec` and a redacted `gitleaks dir` scan also passed. These are source and dependency checks, not production deployment evidence.

A disposable local PostgreSQL 17 instance proved migration 002 idempotency, catalog lifecycle, optimistic concurrency, and structured audit write/read. A process-level local acceptance also started `metricspire serve` against PostgreSQL and an isolated OIDC discovery service, exercised the UI and PKCE login redirect, then proved graceful shutdown. It did not send a Databricks request.

On 2026-09-03, two read-only `DESCRIBE DETAIL` statements in the separate Databricks staging workspace confirmed that `samples.tpch.orders` and `samples.tpch.customer` exist as Delta tables for the fixed acceptance binding. The fixed real-acceptance harness then passed publication v1, a parameterized read-only query, publication v2, a second query, rollback to v1, a third query, golden-result reconciliation, and release-event checks through the same catalog and query application services used by Phase 3. No production-workspace request, business-data query, DDL, or DML was issued. This closes the real engine/application prerequisite, but it is not the remaining HTTP/UI query acceptance.

The opt-in `TestPhase3RealHTTPAcceptance` then passed the UI root, HTTP draft and publication v1, policy-filtered catalog search, physical planning, asynchronous query and golden typed result, publication v2, release listing, rollback to v1, a second query through the restored active release, and durable start/success audit for both jobs. It deliberately uses a fixed test principal, so it proves the HTTP-to-real-engine path without pretending to prove enterprise OIDC. It is disabled in the default suite and cannot contact Databricks unless its explicit real-acceptance switch and environment are provided.

`metricspire serve --config` is the deployment entry point. It loads strict non-secret runtime configuration, requires environment-provided PostgreSQL/session/OIDC/Databricks secrets, refuses non-Databricks bindings in the current single-adapter release, and does not auto-migrate or query on startup.

`TestPhase3DeployedAcceptance` is the opt-in black-box staging check for an already deployed service. It requires an HTTPS origin, a dedicated empty acceptance namespace, direct access to that deployment's PostgreSQL audit table, and two real least-privilege JWTs: one with only `model:manage`, one with only `query:execute`. Fixture paths use the same reviewed TPCH model/query/expected result as the earlier real-engine test. Operators must inject tokens and database credentials through their secret runner rather than a committed file or shell history. The explicit safety switch is:

```bash
METRICSPIRE_RUN_DEPLOYED_PHASE3_ACCEPTANCE=staging-read-only \
METRICSPIRE_DEPLOYED_BASE_URL='https://metricspire-staging.example' \
METRICSPIRE_DEPLOYED_DATABASE_URL='postgres://...' \
METRICSPIRE_DEPLOYED_NAMESPACE='phase3_deployed_acceptance' \
METRICSPIRE_DEPLOYED_MANAGE_TOKEN="$MANAGE_TOKEN" \
METRICSPIRE_DEPLOYED_QUERY_TOKEN="$QUERY_TOKEN" \
METRICSPIRE_TEST_DATABRICKS_MODEL='testdata/acceptance/databricks-tpch/model.yaml' \
METRICSPIRE_TEST_DATABRICKS_QUERY='testdata/acceptance/databricks-tpch/query.json' \
METRICSPIRE_TEST_DATABRICKS_EXPECTED='testdata/acceptance/databricks-tpch/expected.json' \
go test -count=1 -run TestPhase3DeployedAcceptance ./cmd/metricspire
```

The deployed runtime's reviewed `SourceBinding` must point to a read-only staging fixture. The check writes two immutable releases and a rollback to the PostgreSQL control plane, queries v1, v2, and the restored v1 only through the governed service, compares every typed result, and reconciles every `query_started`/`query_succeeded` audit row. It cannot prove interactive browser login because it does not handle a user's credentials or MFA.

## Remaining acceptance gates

- Register a real deployment OIDC client and API resource audience; verify authentic enterprise login, logout, redirect URI, JWT access-token audience, tenant/role/permission claim mapping, expiry, and session behavior. The local signed protocol fixture is not production identity evidence.
- Deploy the service and run `TestPhase3DeployedAcceptance` with reviewed staging-only inputs. No Databricks DDL/DML is required or permitted.
- Re-run the deployment checks and record sanitized evidence. Until these pass, Phase 3 is **not complete**.
