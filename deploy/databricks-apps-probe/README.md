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

## Staging result (2026-09-03)

A dedicated custom probe App was created only in an isolated staging workspace and received exactly the six generated files. No PostgreSQL, database, SQL warehouse, secret, production workspace, or business data was accessed. Databricks automatically created the App service principal and exposed its two baseline read-only IAM scopes; no additional user scopes or resources were configured. App names and environment identifiers belong in private deployment records, not this package.

The deployment reached `RUNNING`. Logs reported an `amd64` process listening on platform port 8000, and authenticated `GET /health/live` returned:

```json
{"go_version":"go1.27.0","goarch":"amd64","protocol":"HTTP/1.1","status":"ok"}
```

The managed edge response used HTTP/2, while the Go process observed HTTP/1.1. A platform stop followed by start returned to the same 200 response, proving lifecycle recovery. Stop reached `STOPPED`, but the platform log stream closed before a shutdown log could be retained, even when the probe logged immediately after cancellation. This leaves managed signal delivery unverified rather than treating platform stop state as process-level proof. Retire one-off probe Apps after acceptance when they have no remaining consumers or attached resources; retain the reproducible package and private acceptance evidence.
