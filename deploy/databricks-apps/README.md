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

Before deployment, the staging App must be reviewed with exactly these resources:

- one Lakebase Autoscaling database resource named `metricspire-postgres`, permission `CAN_CONNECT_AND_CREATE`;
- one SQL warehouse resource named `metricspire-warehouse`, permission `CAN_USE`;
- user authorization scopes required for current-user binding/group membership and read-only SQL. On 2026-09-04, staging returned 403 for the forwarded-user Statement API call under `sql:restricted-query`; the same call succeeded under `sql`. Use `sql` for the current staging package, keep structured queries and Unity Catalog enforcement enabled, and review the smallest working scope again before production.

The database resource grants the App service principal `CONNECT` and database-level `CREATE`, but not table creation in the shared `public` schema. This package sets `METRICSPIRE_DATABASE_SCHEMA=metricspire`; the approved one-shot migration creates that dedicated schema and keeps all control-plane tables there. Attaching the database remains an external permission change. Run `metricspire migrate` as a separate one-shot action only after explicit approval. `serve` never migrates or creates tables at startup.

For the first explicitly approved staging deployment only, set `METRICSPIRE_APPS_MIGRATE_ONCE=approved-staging` while building. The generated `app.yaml` then asks the bootstrap to run the idempotent control-plane migration once, using the final App service principal, before replacing itself with `metricspire serve`. Immediately rebuild and redeploy without this variable; the normal package cannot run a migration. Never use the one-time switch in production.

Acceptance must prove managed-ingress identity, default query access, the single publisher group, user-authorized SQL, a Unity Catalog denial for an otherwise valid user, cancellation, audit, and the absence of user/database tokens from logs and PostgreSQL. Resource creation, local tests, and the earlier Go runtime probe do not satisfy these checks.
