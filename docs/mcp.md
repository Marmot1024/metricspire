# MCP integration

MetricSpire serves stateless Streamable HTTP MCP at `/api/v1/mcp` on Databricks Apps, and also `/mcp` on self-hosted deployments. Users connect to a deployed service; no repository checkout is needed.

## Connect

For Databricks Apps, an administrator registers a public OAuth client with the exact callback URI required by the MCP client. Users sign in with their own identity; App access and warehouse permissions still apply. See [operator setup](../deploy/databricks-apps/README.md#oauth-client-onboarding).

```bash
codex mcp add metricspire --url 'https://<your-app-origin>/api/v1/mcp' --oauth-client-id '<public-client-id>'
codex mcp login metricspire
```

Use the client's authentication action when it reports authentication required. After first login, reload the connection or open a fresh session and call `list_namespaces` to verify it. A successful browser callback alone does not verify tool access. Valid refresh credentials should allow silent renewal; [maintainer acceptance](mcp-native-acceptance.md) checks this separately.

Keep the connection name and approved scopes stable. Do not combine native OAuth with a helper or static bearer header on the same connection, and never put credentials in prompts, command arguments, Git or issue reports.

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
| Client reports authentication required, or application returns `unauthenticated` | Use the existing connection's normal authentication action. |
| `authentication_unavailable` (503/504) | Identity verification could not complete; access remains denied. Retry later or report the request ID. Repeated login is not the remedy. |
| `permission_denied` | Report the denied operation and request ID; do not infer a missing OAuth scope. |
| `unknown_reference`, `metric_route_not_found`, `invalid_time`, `capability_missing` | Use the public field/capability detail and catalog to identify the unsupported query. |
| Empty search | No authorized match was found; this does not prove that warehouse data is absent. |
| Submission accepted | Retain `job_id` and poll to a final status. A queued job is not a completed analysis. |

Limit user-facing recovery to one connection check, then report the failing stage and unresolved cause. Advertised scopes alone do not justify expanding authorization. Databricks Apps ingress may return an anonymous 401 without the application's authentication challenge; that response alone cannot identify the cause or certify OAuth onboarding.

## Limits

Requests and responses are capped at 1 MiB and 8 MiB; query results are bounded, typed JSON. Cancelling a network request may leave an accepted job running: use `cancel_query` with its ID. After an uncertain submission response, check any known job before retrying; without an ID, report the uncertainty instead of blindly resubmitting.

The local `metricspire mcp --api-url <origin>` bridge is for development compatibility. See [verification status](development-status.md) for acceptance gaps.
