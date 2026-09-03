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
- the response reports HTTP/2 and the observed architecture;
- logs contain only startup/shutdown metadata;
- restart and stop signals complete cleanly;
- no database, SQL warehouse, production workspace, or business data is contacted.

A passing probe proves technical execution in that staging runtime. It does not by itself establish official Databricks support for Go or complete MetricSpire deployment acceptance.
