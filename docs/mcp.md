# MCP integration

MetricSpire serves stateless Streamable HTTP MCP at `/api/v1/mcp` on Databricks Apps, and also `/mcp` on self-hosted deployments. Users connect to a deployed service; no repository checkout is needed. MetricSpire does not provide a shared public instance. Obtain your team's connection details first.

## Connect

Self-hosted teams using a standard OIDC issuer can use [native OAuth onboarding](oidc-mcp.md) without Databricks accounts or CLI profiles. Use its independent `metricspire-oidc` connection and operator-registered public client.

For teams using local Codex CLI, use the [single-file installer](mcp-installer.md) with an existing Databricks CLI user profile. Databricks CLI handles OAuth token acquisition and refresh; Codex calls the remote MCP through a local HTTP-header helper. The connection is named `metricspire-cli`. Users retain their own App and data permissions. No custom OAuth client registration is required for this route.

After installation, reopen or reconnect Codex and call `list_namespaces`. An authenticated CLI profile does not itself prove MCP access. Never mix stored native OAuth or static bearer headers into this connection. Keep the profile and workspace stable, and do not place credentials in prompts, configuration, Git or issue reports.

URL-only native OAuth remains an alternative for other clients, requiring a registered public client and validated refresh behavior; its implementation is preserved on `backup/native-oauth-20261012`. It is not the default local Codex onboarding route. See [operator setup](../deploy/databricks-apps/README.md#identity-and-mcp) and [release acceptance](mcp-native-acceptance.md).

## Business discovery and metric selection

When configured on hosted MCP, `list_namespaces` adds `business_domains`: authorized logical
names and aliases mapped to canonical namespaces. Use these instead of asking
users for repository URLs; source provenance, artifact paths and physical
bindings are not included. Operators configure them in [team configuration](../config/README.md).
Hosted namespace discovery uses the authenticated tenant and roles to find accessible active metrics or exploration sources; a configured route alone is not an access grant. It does not query the data engine.

The legacy local `mcp --api-url` bridge returns canonical namespaces from the same filtered UI context (including tenant-scoped authoring routes for managers).

Search returns `selection.candidate_count`, `at_limit` and next-step guidance.
A full result page can omit candidates; even one lexical match is not proof of
a unique business meaning. Details add `comparison.differing_fields` and
`missing_definitions` beside authorized metric definitions. Comparison covers
description, unit, type, time field, allowed dimensions/calendars and verification.
If a user has not chosen between including and excluding test accounts, stop
before execution and ask for that choice. A verified generic count plus a
warning does not resolve the ambiguity; successful preparation is not user
confirmation. The same rule applies when a requested exclusion is undocumented.

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

### Context available to a new client

The MCP connection supplies initialization instructions, tool schemas/descriptions
and tool results. These carry the minimum query workflow; a user does not need to
paste a handoff, repository path or previous conversation. Hosts decide how to
include this material in model context and whether to display it. A normal chat
may show tool calls without showing the initialization text. Local memory, project
notes and custom agent prompts are separate and are not uploaded by MetricSpire.
The Codex installer also distributes a short, readable usage skill; it is an
optional client aid, not an authorization boundary.

Inspect `list_namespaces`, `discover_query_routes` and `get_metric_details` to see
the authorized domains, workflows and definitions behind an answer. `namespace`
is the canonical business domain; an exploration source belongs in `query.source`
within that domain. If discovery is empty, check the domain before concluding
absence. On permission denial, stop the request and report its request ID rather
than probing more tools or routes. Clients still need acceptance testing: the
server cannot prove that a model understood a question or obtained human consent.
A prior result proves neither current access nor unchanged freshness. When a user
explicitly requests a fresh query, perform a new authorized read; polling an
already accepted job continues with its existing job ID.

For a repeatable test without custom agent context, see
[OpenCode acceptance](opencode-acceptance.md).

The hosted MCP implementation defines eleven core tools, with five additional exploration tools when enabled and `query_online_metrics` when online serving is configured. The installer verifies the original seven-tool compatibility baseline, not the complete current tool set. Use the connected server's advertised tools for the actual inventory.

| Tool | Behavior |
| --- | --- |
| `list_namespaces`, `search_metrics` | Discover permitted domains and compact candidates. Search defaults to five matches, ranks exact codes/names first, and omits expressions and dimension details. |
| `get_metric_details` | Fetch authorized definitions, dimension types, time grains and calendar timezones for 1–20 selected exact metric names together. |
| `discover_query_routes` | Report the current user's available published-metric and exploration categories, with semantic selection rules. No data query. |
| `query_online_metrics` | Read a matching certified online metric with numeric `external_code` values, bounded time range and code-to-manifest-fingerprint selections. Returns typed data and snapshot provenance through the same authorized service as online HTTP. No analytical fallback. |
| `prepare_query` | Validate once and return metric definitions, the normalized query and four execution identity/fingerprint guards. No data query or execution credential. |
| `run_query` | Recheck the prepared query, submit once, and wait up to 10 seconds by default (maximum 15). Return the typed result or the accepted job ID. |
| `explain_query` | Validate a structured `SemanticQuery` and explain its meaning without analytical SQL. |
| `plan_query` | Resolve a reviewed execution plan without running it. |
| `submit_query` | Submit a bounded query job; warehouse costs may apply. |
| `get_query`, `cancel_query` | Read or cancel a job owned by the same tenant and principal. |

Call `discover_query_routes` after discovering the namespace. For a matching certified metric on `online_metrics`, use **search → selected details → query_online_metrics**. Set `route: "online_metrics"` on search and detail calls to use independent online catalog policies; keep it through selection. Its `query` uses numeric `metric_codes` from detail `external_code` fields; `metric_selections` maps those codes to the reviewed `manifest_fingerprint`. The same definition selection, policy, freshness, timeout and durable audit checks apply as [online HTTP](online-query.md). A serving failure does not authorize an analytical fallback.

For analytical `published_metrics`, prefer **search → selected details → prepare → run**. No separate explain/plan calls are needed on this path; poll only if the returned job is pending or running. The server's initialization instructions, tool descriptions and returned next steps describe this workflow without local skills or conversation history. Clients still control tool selection; this is guidance, not guaranteed client behavior or a latency SLA.

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

Successful online results and terminal query jobs include `timings`: `planning_ms`
(metadata, policy and planning), `engine_ms` (connection wait, engine execution,
network and decoding), `audit_ms` (durable audit writes) and `service_ms` (total
query-service wall time). These are monotonic durations, not database CPU time.
Job admission/queueing and HTTP/MCP transport are outside `service_ms`; job
`submitted_at`/`started_at` timestamps provide queue context. Tool `_meta` duration
measures its handler and waiting/polling, so it is not the sum of query timings.

For a fresh-session latency comparison, record client request start, every tool
start/end, final answer end and these server timings. Keep the same question,
model, settings, dataset and authorization; report initialization separately and
compare correct answers. Client time outside tools includes orchestration,
network and generation. It cannot be labeled hidden model reasoning from these
measurements. This release adds attribution, not a claimed end-to-end speedup.

The local `metricspire mcp --api-url <origin>` bridge is for development compatibility. See [verification status](development-status.md) for acceptance gaps.

Optional [exploration](exploration.md) adds `discover_analysis`, `plan_analysis`,
`submit_analysis`, `get_analysis`, and `cancel_analysis` for temporary structured
analyses over approved detail sources. Discover and plan first; no result-metric
registration is required. Hosted deployments enable them through explicit runtime
configuration. Permission/timeout failures never silently select another path.
