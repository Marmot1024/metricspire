# Databricks Apps full-service staging package

This package builds the full MetricSpire service for the Phase 3 staging acceptance. It is not the portable open-source deployment profile and must not be reused for production without a separate review.

The generated upload contains:

- checksum-protected static Linux binaries for `amd64` and `arm64`;
- a dependency-free Python bootstrap that selects, verifies, extracts, and executes the binary;
- a generated non-secret `app.yaml` and runtime configuration;
- the fixed TPCH acceptance policy and source binding.

Build the ignored upload directory with explicit non-secret staging metadata:

```bash
METRICSPIRE_APPS_NAME='replace-with-app-name' \
METRICSPIRE_APPS_WORKSPACE_ID='replace-with-workspace-id' \
METRICSPIRE_APPS_PUBLIC_URL='https://replace-with-app-url' \
METRICSPIRE_APPS_PUBLISHER_GROUP_ID='replace-with-stable-group-id' \
METRICSPIRE_APPS_LAKEBASE_ENDPOINT='projects/replace/branches/production/endpoints/primary' \
./deploy/databricks-apps/build.sh
```

The result is `dist/databricks-apps`. The source templates contain no workspace ID, database host, warehouse ID, group ID, token, or credential. `app.yaml` reads the SQL warehouse ID from an App resource named `metricspire-warehouse`; the first Lakebase Autoscaling resource supplies the standard `PG*` connection variables automatically.

`dist/` is intentionally ignored by Git. Do not use `databricks sync` for this generated package because ignore rules can leave an older remote binary while still reporting a completed sync. Upload it with `databricks workspace import-dir dist/databricks-apps <workspace-source> --overwrite`, verify both remote gzip sizes against the local files, and only then deploy the App from that workspace source.

The deployed service exposes authenticated Streamable HTTP MCP at `<app-origin>/api/v1/mcp`. The legacy `/mcp` route remains for direct/self-hosted compatibility; Databricks Apps ingress redirects its anonymous requests to browser login, which is unsuitable for Codex OAuth discovery. Clients need no local MetricSpire process. A short-lived bearer token is the verified staging path; token-free login additionally requires an account-admin-registered public Databricks OAuth client and fresh-session acceptance. After deployment, run `TestMCPRemoteStagingAcceptance` with the opt-in staging variables documented in the project README to verify discovery, all seven tools, and the fixed five-row query without a local MetricSpire subprocess.

Before deployment, the staging App must be reviewed with exactly these resources:

- one Lakebase Autoscaling database resource named `metricspire-postgres`, permission `CAN_CONNECT_AND_CREATE`;
- one SQL warehouse resource named `metricspire-warehouse`, permission `CAN_USE`;
- user authorization scopes required for current-user binding/group membership and read-only SQL. On 2026-09-04, staging returned 403 for the forwarded-user Statement API call under `sql:restricted-query`; the same call succeeded under `sql`. Use `sql` for the current staging package, keep structured queries and Unity Catalog enforcement enabled, and review the smallest working scope again before production.

The database resource grants the App service principal `CONNECT` and database-level `CREATE`, but not table creation in the shared `public` schema. This package sets `METRICSPIRE_DATABASE_SCHEMA=metricspire`; the approved one-shot migration creates that dedicated schema and keeps all control-plane tables there. Attaching the database remains an external permission change. Run `metricspire migrate` as a separate one-shot action only after explicit approval. `serve` never migrates or creates tables at startup.

For the first explicitly approved staging deployment only, set `METRICSPIRE_APPS_MIGRATE_ONCE=approved-staging` while building. The generated `app.yaml` then asks the bootstrap to run the idempotent control-plane migration once, using the final App service principal, before replacing itself with `metricspire serve`. Immediately rebuild and redeploy without this variable; the normal package cannot run a migration. Never use the one-time switch in production.

Acceptance must prove managed-ingress identity, default query access, the single publisher group, user-authorized SQL, a Unity Catalog denial for an otherwise valid user, cancellation, audit, and the absence of user/database tokens from logs and PostgreSQL. Resource creation, local tests, and the earlier Go runtime probe do not satisfy these checks.

The final identity checks must use real staging identities, not forged forwarded headers or the publisher token reused under two labels. Give the selected non-publisher only App `CAN_USE`; do not add that identity to the configured publisher group. The user should authenticate through managed ingress and keep their credentials on their own machine. Record request IDs and outcomes for these two states:

1. With read access to the fixed fixture, catalog/plan/query succeeds while draft, publish, and rollback return HTTP 403.
2. With no Unity Catalog access to the reviewed fixture, MetricSpire accepts the structured query but the terminal job contains only the bounded public engine-denial signal and no upstream response details.

`TestPhase3DeployedIdentityAcceptance` performs exactly one of those states without creating a draft, publishing a release, rolling back, or connecting directly to Lakebase. Run it from that user's machine through a secret runner, with `METRICSPIRE_DEPLOYED_IDENTITY_EXPECTATION` set to either `query-only` or `unity-catalog-denied`. It requires the App HTTPS origin, the existing acceptance namespace, the user's short-lived token, and the three reviewed files under `testdata/acceptance/databricks-tpch`. The test first requires 403 from management routes, then proves catalog and planning access before checking either the five-row golden result or a sanitized Unity Catalog denial. It fails if a publisher token is supplied for the query-only state.

```bash
METRICSPIRE_RUN_DEPLOYED_PHASE3_IDENTITY_ACCEPTANCE=staging-read-only \
METRICSPIRE_DEPLOYED_IDENTITY_EXPECTATION=query-only \
METRICSPIRE_DEPLOYED_BASE_URL='https://replace-with-staging-app-origin' \
METRICSPIRE_DEPLOYED_NAMESPACE=acceptance \
METRICSPIRE_DEPLOYED_USER_TOKEN="$SHORT_LIVED_USER_TOKEN" \
METRICSPIRE_TEST_DATABRICKS_MODEL=testdata/acceptance/databricks-tpch/model.yaml \
METRICSPIRE_TEST_DATABRICKS_QUERY=testdata/acceptance/databricks-tpch/query.json \
METRICSPIRE_TEST_DATABRICKS_EXPECTED=testdata/acceptance/databricks-tpch/expected.json \
go test -count=1 -run '^TestPhase3DeployedIdentityAcceptance$' ./cmd/metricspire
```

Load `SHORT_LIVED_USER_TOKEN` with a non-echoing prompt or approved secret runner; never paste its value into the command or a file. Run the second state by changing only the expectation and the controlled user's Unity Catalog permissions.

Do not change App ACLs, publisher membership, Unity Catalog grants, or fixture bindings merely to make the test pass without first recording the intended staging principal and exact permission delta. After the checks, reconcile query audit by request/job ID, remove temporary grants, and stop App compute again.
