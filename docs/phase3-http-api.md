# Phase 3 HTTP contract

Status: **implemented and accepted through the single-publisher Databricks Apps staging path; Phase 3 remains in progress until the separate negative-authorization identities and portable OIDC deployment are verified**.

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

The company Databricks Apps profile has no second login page. It starts only when configured and runtime app/workspace identities match, accepts the managed forwarded identity headers, and calls the bounded current-user endpoint with the forwarded token to ensure those headers describe one active Databricks user. Every valid user receives `query:execute` and one configured MetricSpire query-policy role; membership in one configured stable publisher-group ID additionally grants `model:manage`. There is intentionally no arbitrary group-to-role/permission rule engine. The same forwarded token is added only to actual query execution, never explain/plan/catalog/management, and is not serialized into request models or job snapshots.

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

On 2026-09-03, the multi-stage Dockerfile built successfully as local image `metricspire:phase3-local`. Docker reported a 4,282,007-byte scratch image configured as user `65532:65532`, entrypoint `/metricspire`, and the expected `serve` command. `metricspire version` also ran successfully with a read-only root filesystem, every Linux capability removed, and `no-new-privileges`. This closes local OCI buildability and the basic runtime-user check; it does not prove PostgreSQL/OIDC startup, health probes, secret injection, or any deployed environment.

A disposable local PostgreSQL 17 instance proved migration 002 idempotency, catalog lifecycle, optimistic concurrency, and structured audit write/read. A process-level local acceptance also started `metricspire serve` against PostgreSQL and an isolated OIDC discovery service, exercised the UI and PKCE login redirect, then proved graceful shutdown. It did not send a Databricks request.

On 2026-09-03, two read-only `DESCRIBE DETAIL` statements in the separate Databricks staging workspace confirmed that `samples.tpch.orders` and `samples.tpch.customer` exist as Delta tables for the fixed acceptance binding. The fixed real-acceptance harness then passed publication v1, a parameterized read-only query, publication v2, a second query, rollback to v1, a third query, golden-result reconciliation, and release-event checks through the same catalog and query application services used by Phase 3. No production-workspace request, business-data query, DDL, or DML was issued. This closes the real engine/application prerequisite, but it is not the remaining HTTP/UI query acceptance.

The opt-in `TestPhase3RealHTTPAcceptance` then passed the UI root, HTTP draft and publication v1, policy-filtered catalog search, physical planning, asynchronous query and golden typed result, publication v2, release listing, rollback to v1, a second query through the restored active release, and durable start/success audit for both jobs. It deliberately uses a fixed test principal, so it proves the HTTP-to-real-engine path without pretending to prove enterprise OIDC. It is disabled in the default suite and cannot contact Databricks unless its explicit real-acceptance switch and environment are provided.

`metricspire serve --config` is the deployment entry point. It loads strict non-secret runtime configuration, requires environment-provided PostgreSQL/session/OIDC/Databricks secrets, refuses non-Databricks bindings in the current single-adapter release, and does not auto-migrate or query on startup.

For the Databricks Apps profile, `METRICSPIRE_LAKEBASE_ENDPOINT` selects a platform database resource instead of a static `METRICSPIRE_DATABASE_URL`. Platform-injected `PG*` fields build a password-free TLS PostgreSQL URL, while the App service principal obtains a short-lived Lakebase database credential before each new physical connection. The credential response and token-source errors are not copied into public errors. This adapter is locally unit-tested; a real Lakebase connection remains a separate acceptance step.

`TestPhase3DeployedAcceptance` is the opt-in black-box staging check for an already deployed service. It requires an HTTPS origin, a dedicated empty acceptance namespace, direct access to that deployment's PostgreSQL audit table, and two distinct real principals. In `oidc` mode their JWTs remain least-privilege and disjoint (`model:manage` versus `query:execute`). In `databricks_apps` mode the publisher belongs to the configured publisher group and therefore has both permissions, while the ordinary user has query only; the test still proves that the ordinary user cannot call management APIs. Managed ingress replaces the service's OIDC login route in that mode. Fixture paths use the same reviewed TPCH model/query/expected result as the earlier real-engine test. Operators must inject tokens and database credentials through their secret runner rather than a committed file or shell history. The explicit safety switch is:

```bash
METRICSPIRE_RUN_DEPLOYED_PHASE3_ACCEPTANCE=staging-read-only \
METRICSPIRE_DEPLOYED_AUTH_PROFILE='oidc' \
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

When only the real publisher identity is available in staging, setting `METRICSPIRE_DEPLOYED_SINGLE_PUBLISHER_ACCEPTANCE=staging-read-only` allows that Databricks Apps publisher token to be supplied as both tokens. This still runs the complete publish/query/version/rollback/audit lifecycle, but deliberately skips the query-only principal's management-denial assertion. It is partial evidence and does not close the separate ordinary-user or negative Unity Catalog gates.

The deployed runtime's reviewed `SourceBinding` must point to a read-only staging fixture. The check writes two immutable releases and a rollback to the PostgreSQL control plane, queries v1, v2, and the restored v1 only through the governed service, compares every typed result, and reconciles every `query_started`/`query_succeeded` audit row. In OIDC mode it cannot prove interactive browser login because it does not handle MFA. In Databricks Apps mode bearer access through the managed ingress can prove forwarded identity and user-authorized SQL, but the staging setup must also include a separate user who passes MetricSpire policy and is denied by Unity Catalog to close the negative data-permission gate.

## Deployment decision and current evidence

Phase 3 now distinguishes two deployment profiles. The open-source profile uses the existing static binary and OCI image with generic OIDC. The company profile targets Databricks Apps and reuses the same application services. Its local adapter verifies the managed Apps runtime and current user, applies the fixed query/publisher product-permission split, requires a request user token for interactive SQL, propagates it across asynchronous execution and cancellation, and masks it from control-plane and audit dependencies. App service-principal authorization is limited to reviewed background/shared operations. Unit tests prove this boundary and fail-closed behavior; only a full staging deployment can prove the actual forwarded headers, scopes, Unity Catalog enforcement, and non-persistence in platform logs.

Databricks documents an Ubuntu 22.04 runtime, `app.yaml` custom commands, automatic Python/Node.js build steps, `DATABRICKS_APP_PORT`, H2C ingress, and a 10 MB limit per app file. Its development and dependency documentation names Python and Node.js, not Go; the custom-command field does not by itself prove that a packaged Go executable is supported. A local measurement on 2026-09-03 produced stripped static Linux binaries of approximately 12 MB (amd64) and 11 MB (arm64); gzip artifacts were approximately 4.5 MB and 4.0 MB. MetricSpire enables HTTP/1.1 plus H2C through Go 1.26 `http.Protocols`; a real H2C client/server test passed. `serve --http-address` also accepts the deployment-provided `0.0.0.0:<port>` as a trusted process argument.

The reviewable `deploy/databricks-apps-probe` package now builds checksum-protected amd64 and arm64 gzip artifacts of approximately 2.7 MB and 2.4 MB. Its dependency-free Python 3.11 bootstrap selects the observed architecture, verifies SHA-256, extracts to ephemeral storage, and uses `exec` so signals reach the Go process. A local Linux/Python 3.11 container selected arm64 and returned `status=ok` over both HTTP/1.1 and H2C, logged only startup metadata, and stopped cleanly on SIGTERM. This validates the package itself without PostgreSQL, a SQL warehouse, secrets, or Databricks. It is not managed-runtime evidence.

On 2026-09-03, the immutable-name custom App `isolated-runtime-probe` was created and deployed only in staging. The uploaded snapshot contained the six generated probe files and no configuration, credential, or data. Databricks selected `amd64`, ran Go 1.27.0 on port 8000, and reported the App `RUNNING`. A short-lived staging user token was passed to `curl` through standard input and was neither printed nor persisted; the authenticated managed-ingress request returned HTTP/2 200, while the JSON emitted by the Go process reported `protocol=HTTP/1.1`, `goarch=amd64`, and `status=ok`. This proves that the edge negotiates HTTP/2 but the observed internal forwarding path uses HTTP/1.1. Stop/start returned to the same 200 response. A final stop reached `STOPPED`, and the App was left stopped.

No PostgreSQL, database, SQL warehouse, secret, production workspace, business table, DDL, DML, or analytical query was touched. Databricks automatically created the App service principal and reported the baseline read-only `iam.access-control:read` and `iam.current-user:read` scopes; no additional user scopes or resources were configured. Both a late `probe stopped` log and an immediate `probe stopping` log were lost when the platform terminated the log stream, so platform stop/start is proved but managed SIGTERM delivery is not. This staging result closes the technical question of executing packaged Go in the observed Apps runtime. It does not make Go an officially documented Databricks language and does not close full-service identity, control-plane, SQL authorization, or deployment acceptance.

Later on 2026-09-03, a new dedicated staging Lakebase project was explicitly authorized and created. Effective settings are PostgreSQL 17, a single 0.5 CU read-write endpoint, 60-second autosuspend, two-day history retention, and native password login disabled; read-only inspection found the endpoint `IDLE`. The initial CLI ignored unsupported create-only branch fields but the endpoint inherited every cost/suspension setting from the accepted project defaults. No credential API call, connection, SQL, migration, table, App resource binding, production request, or business data access followed. This is resource-configuration evidence only.

The full-service `deploy/databricks-apps` staging package builds the real binary, checksum bootstrap, generated strict runtime configuration, and fixed TPCH policy/binding. On 2026-09-04 a dedicated full App was deployed only in staging with one dedicated publisher group, one Lakebase resource, and one SQL warehouse resource. An approved one-shot migration created the dedicated `metricspire` schema and seven control-plane tables; the normal package was then redeployed without the migration switch. Rotating OAuth database credentials, managed ingress, current-user binding, publisher management, health probes, and the minimal UI all worked.

The staging lifecycle then published v1, returned the five reviewed typed rows through the active release, published v2 with only a version/description change, returned the same golden result, switched the active pointer back to the retained immutable v1, and returned the same golden result again. The rollback was an intentional version-management check, not failure recovery. A separate query submitted through the App was cancelled and finished as `cancelled` without a result. Forced read-only Lakebase reconciliation found the expected publish/publish/rollback events, three successful started/succeeded audit pairs, and one started/cancelled pair.

The first forwarded-user query attempt used `sql:restricted-query` and received HTTP 403 from the Statement API. The same user could run a direct read-only statement on the same warehouse, and the same full App query succeeded after the staging user scope changed to `sql`; resource bindings did not change. This localizes the incompatibility to the forwarded App authorization path, but does not establish its undocumented internal cause. The current company profile therefore uses `sql` in staging and relies on MetricSpire's structured-query restrictions plus Unity Catalog for data authorization. Production must repeat the scope review instead of copying this setting blindly.

Final scans found no JWT, bearer value, password, private key, or assigned secret in the complete App log; all seven control-plane tables had zero suspicious credential-shaped rows. The repository scan found only fixed test placeholders, configuration field names, and the documented local-development password. Unit, race, vet, module verification, Go 1.26 compatibility, pure-Go build, Python syntax, gzip, SHA-256, and diff checks passed after the lifecycle. `govulncheck` found no reachable vulnerability and `gosec` reported no issue; one precise `G101` annotation documents that `X-Forwarded-Access-Token` is a header name, not a hard-coded credential.

The staging App compute was stopped after these checks to avoid idle cost. Its deployment snapshot, resource bindings, Lakebase releases, and audit records were retained for a future reviewed restart.

## Remaining acceptance gates

- Keep generic OIDC acceptance for the portable self-hosted profile; verify a real issuer, browser login/logout, API audience, claim mapping, expiry, and session behavior. The local signed protocol fixture is not production identity evidence.
- Obtain a separate ordinary App user who is not in the publisher group and prove that this user can query but receives 403 from draft/publish/rollback management APIs.
- Obtain a separate user who passes MetricSpire query policy but lacks Unity Catalog access to the fixed fixture, and prove that the engine denial is preserved without leaking upstream details.
- Keep the proven checksum/bootstrap route reviewable and re-run it after material runtime or platform changes. Full-service Go execution, dynamic port binding, managed ingress, Lakebase, user-authorized SQL, cancellation, and audit passed in staging; managed signal delivery remains unverified because the log stream closes on stop.
- Run the full OCI service against a disposable PostgreSQL/OIDC fixture and inspect health probes and secret injection; the basic image build, non-root metadata, and hardened `version` execution have passed locally.
- Re-run the deployment checks and scope review before any production deployment. Until the remaining identity cases pass, Phase 3 is **not complete**.

### 2026-09-04 resumed-Goal revalidation

Read-only staging inspection after resuming the Phase 3 Goal confirmed that the latest deployment is still `SUCCEEDED`, the dedicated Lakebase and SQL Warehouse resources remain bound, the effective user scopes remain `iam.access-control:read`, `iam.current-user:read`, and `sql`, and App compute remains deliberately `STOPPED`. No App, resource, group, or data state was changed.

The initial App access-control list granted direct `CAN_MANAGE` only to the existing publisher account, plus inherited administrator access. The dedicated `metricspire-publishers-staging` group had exactly that same account as its sole member. After explicit user approval, one named active staging user was verified outside that group and granted only App `CAN_USE`; the existing deployment was started and reached `RUNNING` with one active compute instance. The user's token, password, and browser session were not requested or accessed, and no Unity Catalog permission changed. Their browser query and management-denial results are still pending, so this prepares but does not complete the ordinary-user gate.

The minimum closure is to grant App `CAN_USE` to a named staging test user who is not added to the publisher group, restart the stopped App, and let that user prove catalog/query success plus HTTP 403 on draft, publish, and rollback. A second controlled permission state must then prove that a user accepted by MetricSpire is denied by Unity Catalog for the reviewed fixture without upstream details appearing in the public job error. The two states may use two test users, or one test user whose Unity Catalog grants are changed between separately recorded checks; no password or long-lived token should be shared. App ACL or Unity Catalog changes are staging side effects and require an explicit reviewed principal and grant scope before execution.

The opt-in `TestPhase3DeployedIdentityAcceptance` now isolates these checks from the already accepted release lifecycle. It does not need a PostgreSQL URL and cannot write drafts, releases, or rollbacks: it first requires management 403, then checks the existing catalog and plan, and finally expects either the reviewed golden query result or a narrowly recognized, sanitized Databricks permission denial. Each user can run one expectation locally with their own short-lived token, so credentials do not need to be transferred to another operator.

A later read-only permission reconciliation submitted `SHOW GRANTS ON TABLE samples.tpch.orders` to the staging warehouse. Databricks rejected the statement with SQLSTATE `42832` and `[SAMPLE_TABLE_PERMISSIONS] Permissions not supported on sample databases/tables` (`statement_id=01f1a80f-99b5-1042-85a2-77360f0efe23`). This did not change data or permissions and does not prove the named non-publisher's query result. It does prove that the public `samples.tpch.orders` fixture cannot be turned into a controlled Unity Catalog denial case through ordinary grants. The positive ordinary-user check should continue against that reviewed sample; the negative check must instead use a separately reviewed, non-business staging table or view with explicit access boundaries, followed by a reviewed binding/release change. No such fixture, grant, binding, or release was created by this reconciliation.
