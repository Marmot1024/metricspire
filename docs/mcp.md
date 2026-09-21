# MCP integration

MetricSpire exposes the same governed query workflow as its HTTP API through stateless Streamable HTTP MCP. The hosted endpoint is `/api/v1/mcp` for Databricks Apps and also `/mcp` for direct/self-hosted deployments. Clients connect to a deployed URL; they do **not** clone or run this repository.

## What a client can do

| Tool | Behavior |
| --- | --- |
| `list_namespaces`, `search_metrics` | Discover permitted namespaces, metric codes, descriptions, dimensions, and time semantics. |
| `explain_query` | Check a structured `SemanticQuery` and its meaning without running analytical SQL. |
| `plan_query` | Resolve the reviewed physical plan without executing it. |
| `submit_query` | Submit a bounded query job; this may incur warehouse cost. |
| `get_query`, `cancel_query` | Read or cancel a job owned by the same tenant and principal. |

The typical path is **discover → explain/plan → confirm → submit → poll**. MetricSpire does not host an LLM, guarantee that a natural-language request was interpreted correctly, or expose raw SQL, model publication, and identity-override tools. If a metric code or dimension is absent, the client should report that rather than invent one.

Every MCP request authenticates independently. Product policy, active release, and query budgets are applied before execution; the analytical engine's catalog, row, and column permissions remain authoritative. Only query submission forwards the current user's short-lived execution credential to the engine job. Job reads and cancellation remain principal-scoped.

## Connect a client

The operator supplies an HTTPS origin, a user-accessible App/service, and an authentication method supported by that deployment. For Databricks Apps, use the API path so anonymous MCP discovery is not redirected to an interactive browser page:

```text
https://<your-app-origin>/api/v1/mcp
```

Databricks does not offer dynamic client registration at the staging OIDC endpoint. A Databricks account administrator must register a **public** OAuth client with the exact callback URI required by each MCP client; each user then signs in with their own Databricks identity. This is not an app-wide shared token or a way around App `CAN USE` and Unity Catalog permissions. The [Databricks Apps deployment guide](../deploy/databricks-apps/README.md#oauth-client-onboarding) documents the registration and the optional local CLI-helper fallback.

For Codex, register the exact redirect URI shown for the fixed MCP URL, then add and log into the connection:

```bash
codex mcp add metricspire --url 'https://<your-app-origin>/api/v1/mcp' --oauth-client-id '<public-client-id>'
codex mcp login metricspire
```

### First connection and re-authentication

The first connection has two separate steps: the MCP client must start OAuth, and the user must complete the browser consent. Typing `metricspire` or `use metricspire` selects a server; it does not start OAuth by itself. In Codex, use the server's **Authenticate** action in `/mcp`, or run `codex mcp login metricspire` immediately when `/mcp` reports `authentication required (0 tools)`. Do not wait for an MCP tool call to time out.

After the command reports that login completed, start a fresh Codex session (or reload the MCP configuration if the client exposes that action) and call `metricspire.list_namespaces`. A successful browser callback alone is not a tool-call acceptance test. With a valid refresh credential, later access-token renewal should be silent; a browser should be required only after the refresh credential is no longer valid or has been revoked.

For Databricks Apps, the platform ingress authenticates the initial request before it reaches MetricSpire. An anonymous `401` may therefore contain no application-generated `WWW-Authenticate` header even though the platform publishes the protected-resource metadata and the OAuth flow is configured correctly. This is expected for the Apps deployment profile and is not a reason to add a static token or a broader scope.

The observed Databricks staging scope is `sql`, but client scope and redirect settings depend on the actual deployment and installed client version. The redirect must match its host, port, and callback path exactly; changing the complete MCP URL may change the callback path. Do not put a `client_secret`, access token, or static Authorization header in a checked-in file. Keep the final server name stable because stored OAuth credentials are keyed to that connection; after a rename, log in again under the final name. Start a new Codex session and call `metricspire.list_namespaces`; an OAuth success screen alone does not prove MCP tool calls work. See [Codex MCP documentation](https://learn.chatgpt.com/docs/extend/mcp?surface=cli).

For a deployment that intentionally uses bearer-token trials, `bearer_token_env_var` is supported, but it is a separate path from native OAuth. Do not combine a helper, static bearer header, and OAuth on one connection. Never paste a token into prompts, command arguments, checked-in files, or issue reports.

## Operational limits

- Requests and responses are capped at 1 MiB and 8 MiB respectively; analytical results are bounded, typed JSON rather than large exports.
- A cancelled MCP network request does not necessarily cancel an accepted query job; call `cancel_query` using its `job_id`.
- After an uncertain submission response, inspect the job state before retrying to avoid duplicate warehouse work.
- The local `metricspire mcp --api-url <origin>` stdio bridge is for development compatibility, not the normal external delivery path.

See [current verification status](development-status.md) before treating a client, credential refresh, or deployment as production-accepted.
