# MCP integration

MetricSpire serves stateless Streamable HTTP MCP at `/api/v1/mcp` on Databricks Apps, and also `/mcp` on self-hosted deployments. Users connect to a deployed service; no repository checkout is needed.

## Connect

For teams using local Codex CLI, use the [single-file installer](mcp-installer.md) with an existing Databricks CLI user profile. Databricks CLI handles OAuth token acquisition and refresh; Codex calls the remote MCP through a local HTTP-header helper. The connection is named `metricspire-cli`. Users retain their own App and data permissions. No custom OAuth client registration is required for this route.

After installation, reopen or reconnect Codex and call `list_namespaces`. An authenticated CLI profile does not itself prove MCP access. Never mix stored native OAuth or static bearer headers into this connection. Keep the profile and workspace stable, and do not place credentials in prompts, configuration, Git or issue reports.

URL-only native OAuth remains an alternative for other clients, requiring a registered public client and validated refresh behavior; its implementation is preserved on `backup/native-oauth-20261012`. It is not the default local Codex onboarding route. See [operator setup](../deploy/databricks-apps/README.md#oauth-client-onboarding) and [release acceptance](mcp-native-acceptance.md).

## Query workflow

| Tool | Behavior |
| --- | --- |
| `list_namespaces`, `search_metrics` | Discover permitted metric codes, definitions, dimensions and time semantics. |
| `explain_query` | Validate a structured `SemanticQuery` and explain its meaning without analytical SQL. |
| `plan_query` | Resolve a reviewed execution plan without running it. |
| `submit_query` | Submit a bounded query job; warehouse costs may apply. |
| `get_query`, `cancel_query` | Read or cancel a job owned by the same tenant and principal. |

Follow **discover → explain/plan → submit → poll**. An explicit query request authorizes submission within its scope; ask about missing business definitions or an expansion of scope. Report absent metrics or capabilities rather than inventing them. MCP exposes no arbitrary SQL, publication or identity-override tool.

Each request authenticates independently. Product policy, active releases and query budgets apply before execution; engine permissions remain authoritative. Only submission forwards the user's short-lived execution credential to the job.

## Failure handling

| Evidence | Next action |
| --- | --- |
| Connection absent from the current tool inventory | Check the loaded connection once. This does not prove the service is down. |
| Client reports authentication required, or application returns `unauthenticated` | Check the failing stage once. For `metricspire-cli`, inspect the existing CLI profile; only a confirmed expired grant calls for `databricks auth login` on that same workspace/profile. Do not run `codex mcp login` for this route. |
| `authentication_unavailable` (503/504) | Identity verification could not complete; access remains denied. Retry later or report the request ID. Repeated login is not the remedy. |
| `permission_denied` | Report the denied operation and request ID; do not infer a missing OAuth scope. |
| `unknown_reference`, `metric_route_not_found`, `invalid_time`, `capability_missing` | Use the public field/capability detail and catalog to identify the unsupported query. |
| Empty search | No authorized match was found; this does not prove that warehouse data is absent. |
| Submission accepted | Retain `job_id` and poll to a final status. A queued job is not a completed analysis. |

Limit user-facing recovery to one connection check, then report the failing stage and unresolved cause. Advertised scopes alone do not justify expanding authorization. Databricks Apps ingress may return an anonymous 401 without the application's authentication challenge; that response alone cannot identify the cause or certify OAuth onboarding.

## Limits

Requests and responses are capped at 1 MiB and 8 MiB; query results are bounded, typed JSON. Cancelling a network request may leave an accepted job running: use `cancel_query` with its ID. After an uncertain submission response, check any known job before retrying; without an ID, report the uncertainty instead of blindly resubmitting.

The local `metricspire mcp --api-url <origin>` bridge is for development compatibility. See [verification status](development-status.md) for acceptance gaps.

Optional [exploration](exploration.md) adds `discover_analysis`, `plan_analysis`,
`submit_analysis`, `get_analysis`, and `cancel_analysis` for temporary structured
analyses over approved detail sources. Discover and plan first; no result-metric
registration is required. Hosted deployments enable them through explicit runtime
configuration. Permission/timeout failures never silently select another path.
