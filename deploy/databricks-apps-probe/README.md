# Databricks Apps Go runtime probe

This acceptance-only package answers whether a staging Databricks Apps runtime can execute a compressed static Go server. It does not open PostgreSQL, request a SQL warehouse, read data, or accept credentials.

Build the ignored upload directory:

```bash
./deploy/databricks-apps-probe/build.sh
```

The script creates `dist/databricks-apps-probe` with `app.yaml`, a standard-library Python bootstrap, and checksum-protected amd64 and arm64 archives. The bootstrap selects the runtime architecture, extracts to the ephemeral temp directory, and replaces itself with the Go process so platform signals reach the server.

After explicit approval to create external state, sync only that generated directory to the staging workspace and create a custom App with no resources and no user scopes. Acceptance requires:

- deployment reaches `RUNNING` without installing dependencies;
- `GET /health/live` returns `status=ok` through the managed ingress;
- the managed edge protocol and the protocol observed by the process are recorded separately, together with the observed architecture;
- logs contain only startup/shutdown metadata;
- stop/start reaches the expected lifecycle states; managed signal delivery is captured when observable or explicitly left unverified;
- no database, SQL warehouse, production workspace, or business data is contacted.

A passing probe proves technical execution in that staging runtime. It does not by itself establish official Databricks support for Go or complete MetricSpire deployment acceptance.

Keep dated runtime observations and deployment receipts in private acceptance records, not in this source guide. Re-run the probe on a new runtime before relying on it; the result of a prior staging deployment is not a production guarantee.
