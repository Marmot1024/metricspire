# Connect an existing semantic layer

Use an external catalog for temporary questions that your published metrics do
not answer. The smallest integration is **one reviewed `AnalysisCatalog` file,
one configured engine and a service restart**. Your data stays in that engine.
Query users need their normal MCP connection, not access to the semantic Git
repository or database credentials.

## 1. Export the definitions you actually need

MetricSpire loads its own [AnalysisCatalog contract](exploration.md#external-semantics).
A Git URL, dbt/Cube project or folder of prose is not automatically executable.
An operator must map and review the relevant datasets, field types, business
calendars, permitted attributes, population rules and roles. No built-in
dbt/Cube importer is shipped. Start with one source rather than the whole warehouse.

Try the neutral [TPCH orders catalog](../examples/semantic-layer/catalog.yaml):

```sh
go run ./cmd/metricspire validate-analysis --catalog examples/semantic-layer/catalog.yaml
mkdir -p config/local
cp examples/semantic-layer/catalog.yaml config/local/analysis.yaml
```

Run from the checkout root with Go installed. Validation is offline; it checks
the contract and does not query TPCH. This example maps a DATE and integer
customer key in `tpch.tiny.orders`, grants the `analyst` role and limits analysis
to 31 days and 100 rows. Its [query](../examples/semantic-layer/query.json) counts
orders and distinct customers per day for one UTC week. These are synthetic
definitions, not a claim about your business data.

## 2. Connect the export to your service

For the example, use a Trino engine with its public TPCH connector enabled.
[The isolated lab](../deploy/testing/README.md) provides that disposable engine.
For your own source, configure the connector and grant a dedicated engine user
read-only access to the reviewed tables first. MetricSpire does not create those
tables or grants.

Start with the [independent service setup](self-hosted.md). Add this section to a
private runtime stored beside `config/local/analysis.yaml`:

```yaml
exploration:
  driver: trino
  connection_env: METRICSPIRE_EXPLORATION_URL
  user: metricspire_reader
  catalog_paths: [analysis.yaml]
  concurrency: 2
  capacity: 20
  result_ttl: 10m
```

The current independent profile still requires its configured online metric
path and policies; this is not a standalone Trino-only service profile.

Keep `query_engine.driver: disabled` when Databricks analytical queries are not
needed. Set the engine URL through your secret store; use HTTPS for a deployed
engine. Unauthenticated loopback HTTP is accepted only for disposable tests.
The catalog's `engine` must match the runtime. See [Trino/Presto limits](trino-presto.md)
before adapting this example: this path supports reviewed single-table DATE
sources; relationships and timestamp fields are rejected. Databricks has its
own [exploration configuration and supported relationships](exploration.md).

Restart the service and check readiness. `catalog_paths` resolves relative to the
runtime file. Editing the file alone does not reload it. A checksum or validation
failure must be fixed before use; it is not a reason to bypass the catalog.

For business aliases and pinned Git provenance, use a private
[DomainRegistry](../config/README.md) instead of listing the same catalog twice.
It records the repository/commit plus each export's path and SHA-256; `validate-domains`
checks the exported bytes. Deployment must separately verify that the reviewed
export came from that commit. MetricSpire does not clone or watch Git. Source
registration without an exported catalog does not enable exploration.

## 3. Verify through the intended user's MCP connection

1. Run `list_namespaces`, then search the relevant metric route and inspect the
   closest definitions. If an existing metric answers the question, use it.
2. For an uncovered question, choose exploration explicitly and run
   `discover_analysis` in namespace `demo`. The example should expose source
   `orders` to an authorized `analyst`; a different role must not gain access.
3. Pass the example `AnalysisQuery` to `plan_analysis`. Inspect the time range,
   grouping, measures and returned limits. A plan is not an executed result.
4. Submit that query once with `submit_analysis`, retain its job ID and poll
   `get_analysis`. Use [the MCP workflow](mcp.md#query-workflow) for connection
   and request details. Ambiguous submission failures must not be blindly retried.
5. Compare the returned dates and exact integer counts with an independently
   written query over the same TPCH table and window. Repeat with a denied reader.
   Real users must not submit SQL or physical table mappings to the MCP tool.

The sample returns up to seven daily groups, not a fixed published result table.
The `customers` count is distinct within each day; summing it does not give
distinct customers across the whole week. No execution result is bundled here.
CI checks real Trino/Presto execution and signed synthetic OIDC/MCP identities;
your connector, enterprise login and business meanings still need acceptance.

## Update the semantics

Review the changed definitions and policy, validate the new export, update any
registry checksum and immutable source revision, and restart from the reviewed
configuration. Rediscover and reconcile the affected query. Accepted jobs pin
their original fingerprints; result jobs are process-local and expire. Keep
company catalogs and physical mappings in ignored/private storage.
