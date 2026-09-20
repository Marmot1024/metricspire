# HTTP API

MetricSpire serves a strict, versioned HTTP API at `/api/v1`. The browser UI and remote MCP transport use the same application services, active releases, authorization rules, and query budgets. This page describes the product boundary; it is not a deployment acceptance log.

## Trust and identity

Clients submit a namespace and a structured semantic query using metric codes. The server resolves the authorized active model, policy, source binding, and analytical engine. A query body cannot choose an identity, release, table, engine route, or raw SQL. Warehouse-native table, row, and column permissions remain authoritative.

Portable self-hosting uses OIDC with browser PKCE sessions or a separately audience-bound API access token. Databricks Apps uses managed ingress and checks the forwarded identity against the platform current-user API; only query execution receives the user's short-lived token. `model:manage` and `query:execute` are distinct product permissions.

## Endpoints

| Permission | Method and path under `/api/v1` | Purpose |
| --- | --- | --- |
| Authenticated | `GET /ui/context` | Identity, permissions, and visible namespaces. |
| `model:manage` | `GET, PUT /namespaces/{namespace}/models/{model}/draft` | Read or save a revisioned draft. |
| `model:manage` | `POST /namespaces/{namespace}/models/{model}/validate` | Validate without saving. |
| `model:manage` | `POST /namespaces/{namespace}/models/{model}/review` | Compare a candidate to the active release without writing. |
| `model:manage` | `POST /namespaces/{namespace}/models/{model}/publish` | Publish the expected revision as `certified`, or as `trial` only in an explicitly enabled namespace. |
| `model:manage` | `GET /namespaces/{namespace}/models/{model}/releases` | List releases and active state. |
| `model:manage` | `POST /namespaces/{namespace}/models/{model}/rollback` | Activate an existing immutable release. |
| `model:manage` | `POST /namespaces/{namespace}/models/{model}/deactivate` | Remove the active pointer while retaining immutable history and an audit event. |
| `query:execute` | `GET /catalog/search?namespace=...&q=...&limit=...` | Search authorized active metrics. |
| `query:execute` | `POST /namespaces/{namespace}/explain` | Return an authorized logical plan; no engine call. |
| `query:execute` | `POST /namespaces/{namespace}/plan` | Return logical and physical plans; no engine call. |
| `query:execute` | `POST /namespaces/{namespace}/query` | Submit an asynchronous bounded query; returns `202`. |
| `query:execute` | `GET /jobs/{job}` | Read a job owned by the same tenant and principal. |
| `query:execute` | `POST /jobs/{job}/cancel` | Cancel an owned job. |

Model-scoped explain, plan, and query routes exist for existing clients; new clients should use the metric-first namespace routes. `GET /health/live` and `GET /health/ready` are unauthenticated probes outside `/api/v1`; readiness checks PostgreSQL, not warehouse access.

## Request, result, and audit limits

- JSON bodies reject unknown and duplicate fields and default to a 1 MiB request limit. Catalog search returns at most 100 metrics.
- Planner limits metrics, dimensions, filters, time windows (at most 366 calendar days), joins, parameters, and result rows. Query jobs default to a two-minute deadline; cancellation propagates to the engine adapter.
- Errors use `application/problem+json` with a stable code, safe detail, optional field path, and server-generated request ID. Upstream details and credentials are not returned.
- Before execution, `query_started` must be durably audited; failure stops the engine call. If completion audit fails, the result is withheld. Audit stores fingerprints and outcome metadata, not SQL, filter values, tokens, or result rows.

See [architecture](architecture.md) for component boundaries and [development status](development-status.md) for what has been verified in deployments.
