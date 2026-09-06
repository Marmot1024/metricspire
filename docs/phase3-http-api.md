# Phase 3 HTTP contract

Status: **implemented and accepted for the Databricks Apps staging publisher and ordinary query-user paths; the maintainer UI and metric-first public query route have passed staging deployment smoke checks**. The Unity Catalog denial case and deployment against an external OIDC provider are deferred release-hardening checks, not HTTP-contract completion gates.

This document freezes the first product HTTP boundary. It records what is already proved by local tests and what still needs a real deployment. It is not a claim that production authentication has passed.

## Trust boundary

Product-facing clients submit a namespace path and a strict `SemanticQuery` containing stable metric codes. The server resolves exactly one authorized active internal model; callers do not choose a table, model, binding, engine route, release, policy, identity, or SQL. Model-scoped query routes remain only as a compatibility boundary for existing clients. Management routes keep the model path because maintainers explicitly edit that internal versioned object. The server obtains trusted values through four injected dependencies:

1. `Authenticator` returns a trusted tenant, subject, roles, and product permissions.
2. `CatalogRepository` resolves the active immutable release.
3. `PolicyResolver` resolves policy by namespace/model/tenant.
4. `BindingResolver` resolves the reviewed physical binding by namespace/model.

Warehouse-native catalog/schema/table/view/row/column permissions remain authoritative. MetricSpire does not mirror them. Its policy covers metric/dimension grants, publication, budgets, and audit.

## Identity and session

The first concrete `Authenticator` uses standard OIDC and is not Databricks-specific. It verifies provider discovery, JWKS signature, issuer, audience, expiry, stable `sub`, tenant, roles, and the two recognized product permissions. Browser login uses Authorization Code + PKCE with state and nonce; its ID token must target the configured browser `client_id`. API bearer authentication separately requires a signed JWT access token targeting the required `bearer_audience`; configuration fails closed when that API audience is missing, so a browser ID token cannot silently become an API credential. Opaque access tokens are rejected because this adapter performs local verification and has no token-introspection contract. Flow/session cookies are AES-256-GCM encrypted, HTTP-only, SameSite=Lax, and Secure except for an explicitly enabled loopback development origin. Session lifetime never exceeds the ID token lifetime.

Databricks OAuth M2M is a separate engine service credential. It is never accepted as a MetricSpire end-user identity. Runtime secrets are environment variables; YAML/JSON configuration contains only issuer/client metadata and trusted policy/binding file routes.

The company Databricks Apps profile has no second login page. It starts only when configured and runtime app/workspace identities match, accepts the managed forwarded identity headers, and calls the bounded current-user endpoint with the forwarded token to ensure those headers describe one active Databricks user. Every valid user receives `query:execute` and one configured MetricSpire query-policy role; membership in one configured stable publisher-group ID additionally grants `model:manage`. There is intentionally no arbitrary group-to-role/permission rule engine. The same forwarded token is added only to actual query execution, never explain/plan/catalog/management, and is not serialized into request models or job snapshots.

## Endpoints

All paths are under `/api/v1`.

| Permission | Method and path | Purpose |
| --- | --- | --- |
| authenticated user | `GET /ui/context` | return identity, permissions, authentication profile, and configured namespaces; internal model routes are returned only to maintainers |
| `model:manage` | `GET, PUT /namespaces/{namespace}/models/{model}/draft` | read/save an optimistic draft revision |
| `model:manage` | `POST /namespaces/{namespace}/models/{model}/validate` | compile and validate without saving |
| `model:manage` | `POST /namespaces/{namespace}/models/{model}/review` | compile and compare a candidate with the active release; report publication, binding, metric-diff, and internal-dependency impact without writing |
| `model:manage` | `POST /namespaces/{namespace}/models/{model}/publish` | publish the exact expected revision |
| `model:manage` | `GET /namespaces/{namespace}/models/{model}/releases` | list release summaries and active state |
| `model:manage` | `POST /namespaces/{namespace}/models/{model}/rollback` | activate an existing immutable release |
| `query:execute` | `GET /catalog/search?namespace=...&q=...&limit=...` | search policy-authorized metrics from active releases only |
| `query:execute` | `POST /namespaces/{namespace}/explain` | resolve the internal model, authorize, and return the logical plan; no engine call |
| `query:execute` | `POST /namespaces/{namespace}/plan` | resolve the internal model and return logical/physical plans; no engine call |
| `query:execute` | `POST /namespaces/{namespace}/query` | resolve the internal model and create a server-side query job; returns `202` |
| `query:execute` | `POST /namespaces/{namespace}/models/{model}/{explain,plan,query}` | compatibility routes for existing model-scoped clients |
| `query:execute` | `GET /jobs/{job}` | read a job owned by the same tenant and principal |
| `query:execute` | `POST /jobs/{job}/cancel` | cancel an owned pending/running job |

The UI is served at `/` and calls these endpoints. All authenticated users can discover policy-authorized metrics without seeing internal model or physical-source names. Query validation uses metric codes, common dimensions, filters, explicit time presets/ranges, business timezone, grain, and a row limit; it renders typed rows, returns the resolved absolute interval, and can copy a normalized API request. Maintainers additionally get a structured basic-metric editor, current release and binding status, publication issues, semantic diff, transitive internal metric dependencies, immutable release history, publication, and rollback. Complex formulas and model structure remain versioned contract-import concerns rather than raw JSON fields in the page. The UI contains no parallel authorization, planning, or execution logic.

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

## Verification boundary

The default suite covers OIDC protocol checks, permission rejection, trusted-field and raw-SQL rejection, active-release resolution, request/result budgets, job ownership/cancellation/timeout, fail-closed audit, catalog policy filtering, health probes, UI wiring, race safety, static analysis, dependency verification, and pure-Go builds. Disposable PostgreSQL and process-level tests additionally cover migrations, catalog lifecycle, audit persistence, readiness, login redirect, and graceful shutdown.

Real-environment tests are opt-in and fail unless explicit staging switches and inputs are present:

- `TestPhase3RealHTTPAcceptance` proves the HTTP-to-real-engine lifecycle with a fixed test principal.
- `TestPhase3DeployedAcceptance` proves the deployed publisher lifecycle: publish v1, query, publish v2, query, activate retained v1, query, cancel, and reconcile audit.
- `TestPhase3DeployedIdentityAcceptance` proves one real user state without writing drafts or releases: management denial followed by either the reviewed query result or a sanitized Unity Catalog denial.

Local or fixed-principal results never count as enterprise identity evidence. Operator identities, request/job IDs, and environment-specific acceptance evidence stay in private deployment records, not in the public product documentation.

## Deployment profiles

| Profile | Identity | Control plane | Analytical execution |
| --- | --- | --- | --- |
| Open-source self-hosted | Standard OIDC | PostgreSQL | Pluggable adapter; Databricks is first |
| Company Databricks Apps | Managed ingress/current user | Lakebase through the PostgreSQL repository | Forwarded user authorization with Unity Catalog remaining authoritative |

`metricspire serve --config` loads non-secret strict configuration. Database URLs, session keys, OIDC secrets, and Databricks credentials come from environment or platform resources. Startup never migrates the database or queries an analytical engine. Migrations remain an explicit one-shot operation.

The Databricks Apps package uses a dependency-free Python bootstrap only to verify and unpack the static Go binary, then replaces itself with the Go process. Staging proved this packaging route, but Go is not an officially documented Databricks Apps development language. The current staging forwarded-user path requires the `sql` scope; production must repeat the least-privilege review rather than copying that setting blindly.

Detailed package construction, resource boundaries, secret handling, and executable commands live in `deploy/databricks-apps/README.md`.

## Accepted staging boundary

Staging has proved packaged-Go startup, managed ingress, Lakebase connectivity, short-lived database credentials, publisher management, user-authorized SQL, immutable release activation, typed limited results, cancellation, and durable audit. A real non-publisher with only App `CAN_USE` also proved successful query access and HTTP 403 on draft, publish, and rollback.

This evidence used only the dedicated staging App/control plane and reviewed public TPCH reads. It did not contact production, write analytical data, or establish the remaining Unity Catalog denial or portable OIDC cases.

## Product review and deferred release hardening

- Use the deployed staging App for product-owner feedback; only defects in the frozen catalog, maintainer-governance, and query-validation workflows belong to Phase 3 follow-up.
- Before a production rollout, use a genuinely least-privilege staging identity and a reviewed non-business fixture to prove that MetricSpire cannot bypass a Unity Catalog denial while public errors remain sanitized.
- Before publishing the self-hosted profile, deploy it against an external OIDC provider and verify browser login/logout, API audience, claim mapping, expiry, and session behavior.
- Re-run scope, secret, deployment, and security checks before any production rollout.
