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

The first concrete `Authenticator` uses standard OIDC and is not Databricks-specific. It verifies provider discovery, JWKS signature, issuer, audience, expiry, stable `sub`, tenant, roles, and the two recognized product permissions. Browser login uses Authorization Code + PKCE with state and nonce. Flow/session cookies are AES-256-GCM encrypted, HTTP-only, SameSite=Lax, and Secure except for an explicitly enabled loopback development origin. Session lifetime never exceeds the ID token lifetime.

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

The minimal UI is served at `/` and calls these endpoints. It contains no parallel authorization, planning, or execution logic.

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

`metricspire serve --config` is the deployment entry point. It loads strict non-secret runtime configuration, requires environment-provided PostgreSQL/session/OIDC/Databricks secrets, refuses non-Databricks bindings in the current single-adapter release, and does not auto-migrate or query on startup.

## Remaining acceptance gates

- Register a real deployment OIDC client and verify authentic enterprise login, logout, redirect URI, tenant/role/permission claim mapping, expiry, and session behavior. The local signed protocol fixture is not production identity evidence.
- Run deployed HTTP/UI acceptance against disposable PostgreSQL and an explicitly approved read-only Databricks fixture. No Databricks DDL/DML is required or permitted.
- Re-run the deployment checks and record sanitized evidence. Until these pass, Phase 3 is **not complete**.
