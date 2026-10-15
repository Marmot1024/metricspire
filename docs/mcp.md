# MCP integration

MetricSpire serves stateless Streamable HTTP MCP at `/api/v1/mcp` on Databricks Apps, and also `/mcp` on self-hosted deployments. Users connect to a deployed service; no repository checkout is needed.

## Connect

For teams using local Codex CLI, use the [single-file installer](mcp-installer.md) with an existing Databricks CLI user profile. Databricks CLI handles OAuth token acquisition and refresh; Codex calls the remote MCP through a local HTTP-header helper. The connection is named `metricspire-cli`. Users retain their own App and data permissions. No custom OAuth client registration is required for this route.

After installation, reopen or reconnect Codex and call `list_namespaces`. An authenticated CLI profile does not itself prove MCP access. Never mix stored native OAuth or static bearer headers into this connection. Keep the profile and workspace stable, and do not place credentials in prompts, configuration, Git or issue reports.

URL-only native OAuth remains an alternative for other clients, requiring a registered public client and validated refresh behavior; its implementation is preserved on `backup/native-oauth-20261012`. It is not the default local Codex onboarding route. See [operator setup](../deploy/databricks-apps/README.md#oauth-client-onboarding) and [release acceptance](mcp-native-acceptance.md).

## Business discovery and metric selection

When configured on hosted MCP, `list_namespaces` adds `business_domains`: authorized logical
names and aliases mapped to canonical namespaces. Use these instead of asking
users for repository URLs; source provenance, artifact paths and physical
bindings are not included. Operators configure them in [team configuration](../config/README.md).
The legacy local `mcp --api-url` bridge returns canonical namespaces only.

Search returns `selection.candidate_count`, `at_limit` and next-step guidance.
A full result page can omit candidates; even one lexical match is not proof of
a unique business meaning. Details add `comparison.differing_fields` and
`missing_definitions` beside authorized metric definitions. Comparison covers
description, unit, type, time field, allowed dimensions/calendars and verification.
It does not extract population or denominators from prose, invent missing
metadata, or implement a semantic-match score. Different requested metrics may
intentionally differ; compare them against the user's actual question.

Use an unambiguous maintained definition directly. Present material differences
when several definitions could answer the request; keep a user's confirmed
scope through follow-up questions. Roles and history never silently alter a
definition. After refining a missing search, confirm the unmet meaning before
approved exploration. An explicit request already defining the temporary scope
does not require a redundant permission question. Permission, connection and
timeout errors never prove absence. Usage tracking and role recommendation
storage are not implemented by this change.

## Query workflow

| Tool | Behavior |
| --- | --- |
| `list_namespaces`, `search_metrics` | Discover permitted domains and compact candidates. Search defaults to five matches, ranks exact codes/names first, and omits expressions and dimension details. |
| `get_metric_details` | Fetch authorized definitions, dimension types, time grains and calendar timezones for 1–20 selected exact metric names together. |
| `discover_query_routes` | Report the current user's available published-metric and exploration categories, with semantic selection rules. No data query. |
| `prepare_query` | Validate once and return metric definitions, the normalized query and four execution identity/fingerprint guards. No data query or execution credential. |
| `run_query` | Recheck the prepared query, submit once, and wait up to 10 seconds by default (maximum 15). Return the typed result or the accepted job ID. |
| `explain_query` | Validate a structured `SemanticQuery` and explain its meaning without analytical SQL. |
| `plan_query` | Resolve a reviewed execution plan without running it. |
| `submit_query` | Submit a bounded query job; warehouse costs may apply. |
| `get_query`, `cancel_query` | Read or cancel a job owned by the same tenant and principal. |

Hosted MCP exposes eleven core tools and, when configured, five exploration tools. Prefer **search → selected details → prepare → run** after discovering the namespace. No separate explain/plan calls are needed on this path; poll only if the returned job is pending or running. The server's initialization instructions, tool descriptions and returned next steps describe this workflow without local skills or conversation history. Clients still control tool selection; this is guidance, not guaranteed client behavior or a latency SLA.

Pass the normalized `query` and all four `expected_plan` values from `prepare_query` unchanged to `run_query`. They are equality guards, not release-selection authority. The service rechecks current authorization and the actual plan immediately before engine execution, including asynchronous execution. A changed query, release, policy or binding is rejected; prepare again and review the new scope. Published verification/channel status remains visible throughout this workflow.

Existing explain/plan/submit/status/cancel tools remain available. `search_metrics` now defaults to summaries; callers needing the full legacy response must explicitly set `view: "detail"`. The local development compatibility bridge retains its legacy tool set; use advertised capabilities rather than assuming fast tools exist there.

Choose the query category by the business definition, population, denominator and counting grain. Use maintained published metrics when they answer the question; use approved exploration for an explicitly requested temporary formula/cohort/grain after `discover_analysis`. `discover_query_routes` reports actual authorized catalog access, not business certification or execution readiness. An empty search or a permission, connection or timeout failure never justifies a category switch. Ask about the precise missing definition when neither category supports it.

An explicit query request authorizes submission within its scope; ask about missing business definitions or an expansion of scope. Report absent metrics or capabilities rather than inventing them. MCP exposes no arbitrary SQL, publication or identity-override tool.

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
| `prepared_query_changed` | Prepare again and inspect the changed definition or scope before executing. No engine execution occurred for the rejected plan. |
| Submission accepted | Retain the returned job ID and poll to a final status. A queued job is not a completed analysis. |
| `run_query` returns `wait_timeout`, `request_cancelled` or `status_unavailable` | The wait ended, not the accepted job. Keep its ID; use `get_query` or `cancel_query`, never resubmit to continue waiting. |

Limit user-facing recovery to one connection check, then report the failing stage and unresolved cause. Advertised scopes alone do not justify expanding authorization. Databricks Apps ingress may return an anonymous 401 without the application's authentication challenge; that response alone cannot identify the cause or certify OAuth onboarding.

## Limits

Requests and responses are capped at 1 MiB and 8 MiB; query results are bounded, typed JSON. Cancelling a network request may leave an accepted job running: use `cancel_query` with its ID. After an uncertain submission response, check any known job before retrying; without an ID, report the uncertainty instead of blindly resubmitting.

`run_query` submits once per call; separate calls are not deduplicated. Its wait only polls the same tenant/principal-owned job. Process-local job storage and restart/multi-instance limitations are unchanged. Tool responses include server duration in `_meta["metricspire/elapsed_ms"]`; prepare/run also expose `elapsed_ms`, and catalog logs record request ID, duration and match count without search text. These measure service time, not client discovery, network transport, model thinking or answer writing; compare those separately when diagnosing latency.

The local `metricspire mcp --api-url <origin>` bridge is for development compatibility. See [verification status](development-status.md) for acceptance gaps.

Optional [exploration](exploration.md) adds `discover_analysis`, `plan_analysis`,
`submit_analysis`, `get_analysis`, and `cancel_analysis` for temporary structured
analyses over approved detail sources. Discover and plan first; no result-metric
registration is required. Hosted deployments enable them through explicit runtime
configuration. Permission/timeout failures never silently select another path.
