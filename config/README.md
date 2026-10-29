# Team deployment configuration

First-time operators: use [the independent service walkthrough](../docs/self-hosted.md)
or [connect one semantic export](../docs/semantic-layer.md) before adapting these templates.

Start here when adapting MetricSpire to a team. These templates use public,
neutral fixtures. Copy both files to the ignored private directory:

```sh
mkdir -p config/local
cp config/runtime.example.yaml config/local/runtime.yaml
cp config/domains.example.yaml config/local/domains.yaml
metricspire validate-domains --config config/local/domains.yaml
```

Edit **runtime.yaml** for authentication, policy and physical binding routes.
Edit **domains.yaml** for tenant, business names, aliases and semantic exports.
The runtime template's fixture paths assume this destination. Paths resolve
relative to the file containing them. For deployment, mount the private files
and use `metricspire serve --config /etc/metricspire/runtime.yaml`; this is not
a one-command service demo and still requires configured databases/identity.
See [Getting started](../docs/getting-started.md) for existing environment setup.
Keep credentials in the secret store or documented environment variables;
never put them in these templates. `config/local/` is ignored by Git and Docker.

The registry maps tenant + namespace to a display name and aliases. Authorized
Hosted MCP `list_namespaces` responses include these logical business names. The
legacy local `mcp --api-url` bridge returns namespaces without this extension. Clients
resolve ordinary names using this map and submit the canonical namespace; no
GitHub URL is needed in conversation. Aliases never grant access or choose a
metric definition. Conflicting aliases within a tenant fail validation.

`semantic_source.repository` and its immutable `revision` record operator-reviewed
provenance. They do not fetch Git or prove that an export came from that commit.
Your deployment/export process owns that verification. `catalogs` lists actual
AnalysisCatalog exports with SHA-256 checksums, not arbitrary knowledge files.
Files resolve relative to the registry; runtime verifies and decodes the same
bytes, checks tenant/namespace and loads them into the configured exploration
service. Supply exploration engine/capacity separately. No catalog means source
registration only; it does not enable exploration. Restart to load changes.
Repository addresses and artifact paths are omitted from public discovery.

The control database (definitions/releases/audit), metric result database
(DWS/ADS), exploration engine and Git semantic source have distinct roles.
Choose implemented drivers and their separate credentials in the
[runtime engine guide](../docs/runtime-engines.md). For a deployment without
Databricks, start from its PostgreSQL-only profile, then select
[MySQL metadata](../docs/mysql-metadata.md),
[ClickHouse](../docs/clickhouse-online.md) or [Doris](../docs/doris-online.md)
serving, and [Trino/Presto exploration](../docs/trino-presto.md) as needed.
These settings do not create, discover or populate business tables.
