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

Review generated `app.yaml` and configuration before upload. The App expects a SQL warehouse resource named `metricspire-warehouse` and a Lakebase Autoscaling resource supplying PostgreSQL connection variables. Use `databricks workspace import-dir` for the generated package, verify remote artifact sizes, and then deploy from that exact workspace source. `databricks sync` may skip ignored generated artifacts.

Serving does not migrate PostgreSQL. Run the separate, explicitly approved one-shot migration only after reviewing the resource, identity, schema, and permission scope. Never carry a staging migration or test-publication switch into production. Test releases with unverified definitions are staging-only and are not business certification.

## Identity and MCP

The hosted Streamable HTTP MCP endpoint is `<app-origin>/api/v1/mcp`. Databricks Apps ingress authenticates the user; MetricSpire binds the managed identity to a current Databricks user and forwards that user's short-lived token **only** for SQL execution. The App service principal is not a substitute for end-user data permissions. Users need App `CAN USE` and appropriate Unity Catalog access; publication additionally requires the configured publisher group.

A Databricks account administrator registers a **public OAuth client**, without a client secret, for each client's exact loopback redirect URI. Codex or Claude Code performs its own PKCE browser login and token management. The observed staging scope is `sql`; review the current platform metadata and requested scopes before deployment rather than copying a broad `all-apis` grant. A successful browser callback does not prove tool calls, user isolation, query execution, or refresh. See [MCP integration](../../docs/mcp.md) and [development status](../../docs/development-status.md).

The optional local CLI-profile header helper under `tools/` is a fallback for local trials, not a prerequisite for URL-only OAuth. Do not store tokens in a repository config or commit generated `dist/` content.

## Acceptance boundary

Before declaring a new environment ready, verify managed identity, query-only and publisher separation, the intended warehouse, a bounded real query, a controlled Unity Catalog denial, cancellation, audit, credential non-persistence, and post-expiry client refresh. Real-environment tests are opt-in and must use the explicitly reviewed fixture and users. A build, health response, browser login, or successful `list_namespaces` call alone is not production acceptance.

This repository intentionally does not contain live environment identifiers, operator receipts, temporary grants, or secret-bearing deployment transcripts. Keep them in approved private records, not in a public README.
