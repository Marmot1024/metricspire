# Databricks Apps staging package

This source-adjacent guide is for maintainers deploying MetricSpire to a **dedicated staging** Databricks App. It is not a portable installation path or production rollout approval. For the public client-facing flow, start with [MCP integration](../../docs/mcp.md).

The package builds static Linux binaries, a checksum-verifying Python bootstrap, non-secret runtime configuration, and a fixed neutral test binding. The generated upload lives in ignored `dist/databricks-apps`. Source templates contain no workspace, warehouse, database, group, token, or credential values.

## Build

Provide reviewed, non-secret staging resource identifiers:

```bash
METRICSPIRE_APPS_NAME='replace-with-app-name' \
METRICSPIRE_APPS_WORKSPACE_ID='replace-with-workspace-id' \
METRICSPIRE_APPS_PUBLIC_URL='https://replace-with-app-url' \
METRICSPIRE_APPS_PUBLISHER_GROUP_ID='replace-with-stable-group-id' \
METRICSPIRE_APPS_LAKEBASE_ENDPOINT='projects/replace/branches/production/endpoints/primary' \
./deploy/databricks-apps/build.sh
```

For a dedicated trial environment only, add `METRICSPIRE_APPS_TRIAL_NAMESPACES='acceptance'`. The renderer writes both the structured namespace allowlist and the deployment acknowledgement. Omit it in production.

Review generated `app.yaml` and configuration before upload. The App expects a SQL warehouse resource named `metricspire-warehouse` and a Lakebase Autoscaling resource supplying PostgreSQL connection variables. Use `databricks workspace import-dir` for the generated package, verify remote artifact sizes, and then deploy from that exact workspace source. `databricks sync` may skip ignored generated artifacts.

Serving does not migrate PostgreSQL. Run the separate, explicitly approved one-shot migration only after reviewing the resource, identity, schema, and permission scope. Never carry a trial-release allowlist into production. Trial releases retain `unverified` business status and are not certification; they can be deactivated without deleting immutable history.

## Identity and MCP

The hosted Streamable HTTP MCP endpoint is `<app-origin>/api/v1/mcp`. Databricks Apps ingress authenticates the user; MetricSpire binds the managed identity to a current Databricks user and forwards that user's short-lived token **only** for SQL execution. The App service principal is not a substitute for end-user data permissions. Users need App `CAN USE` and appropriate Unity Catalog access; publication additionally requires the configured publisher group.

A Databricks account administrator registers a **public OAuth client**, without a client secret, for each client's exact loopback redirect URI. Codex or Claude Code performs its own PKCE browser login and token management. The observed staging scope is `sql`; review the current platform metadata and requested scopes before deployment rather than copying a broad `all-apis` grant. A successful browser callback does not prove tool calls, user isolation, query execution, or refresh. See [MCP integration](../../docs/mcp.md) and [development status](../../docs/development-status.md).

The optional local CLI-profile header helper under `tools/` is a fallback for local trials, not a prerequisite for URL-only OAuth. Do not store tokens in a repository config or commit generated `dist/` content.

## Optional online HTTP fixture

The default package still has no PostgreSQL online endpoint. To include one, set `METRICSPIRE_APPS_ONLINE_CONFIG` to a reviewed private JSON file containing only an `OnlineConfig` object. Its tenant must match this neutral package's `acceptance` tenant, with explicit `tenant_shared` approval, namespaces, sources, freshness, `bindings` and independent `policies`. Each referenced artifact must be a regular JSON file in the same directory with a flat `online-*.json` filename. Only those files are copied; credentials, models, directories and unrelated assets are not automatically uploaded. This renderer is for the neutral dedicated fixture, not a replacement for a shared App's existing runtime configuration.

Online allow rules must explicitly list verified principal IDs, metric names and dimension names; analytical `analyst` roles or wildcard grants are rejected. Readers share the approved aggregate scope, not arbitrary region/player-level restrictions. The package references an App secret resource named `metricspire-online-database-url` to supply `METRICSPIRE_ONLINE_DATABASE_URL`; provision and review that resource separately. Never put the connection URL into the JSON or command output. Business tables, source grants, model publication and data loading are not performed by the package. `METRICSPIRE_MIGRATE_ONCE` is not needed for online business-table setup and remains omitted by default.

Run `python3 -m unittest discover -s deploy/databricks-apps -p test_render.py` before packaging. The Go runtime performs further route, source and read-role validation. Deploy only after independent review, the target resource and actual permitted/denied users are confirmed, and the exact integrated commit preserves already deployed behavior. A user token is used for existing Apps identity verification, never for PostgreSQL execution; analytical queries retain their separate per-user authorization path.

## Acceptance boundary

Before declaring a new environment ready, verify managed identity, query-only and publisher separation, the intended warehouse, a bounded real query, a controlled Unity Catalog denial, cancellation, audit, credential non-persistence, and post-expiry client refresh. Real-environment tests are opt-in and must use the explicitly reviewed fixture and users. A build, health response, browser login, or successful `list_namespaces` call alone is not production acceptance.

This repository intentionally does not contain live environment identifiers, operator receipts, temporary grants, or secret-bearing deployment transcripts. Keep them in approved private records, not in a public README.
