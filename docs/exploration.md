# Governed temporary analysis

Exploration uses an approved external semantic catalog to query detail rows.
It does not require registering each result formula as a metric. Existing
`SemanticQuery` and the online metric service keep their own contracts.

See [team configuration](../config/README.md) for business aliases, private Git
source provenance and checksum-verified exported catalogs.

## External semantics

The operator exports reviewed warehouse semantics into an `AnalysisCatalog`
JSON or YAML file: datasets and typed fields, logical attributes, approved
relationships, source population filters, business calendars, role grants,
attribute policy and execution budgets. Files load and validate at startup;
accepted jobs pin the semantic, policy and plan fingerprints. Changed files
require a reload/restart. Business definitions and bindings stay outside Git.

A directory of arbitrary YAML or prose is not executable configuration. Export
it explicitly; do not guess field mappings, joins or business defaults. The
neutral [catalog](../testdata/exploration/catalog.json) shows the required format.
Source `dimensions` lists usable logical attributes, including measure inputs.
Every source must expose a root DATE attribute for mandatory bounded time ranges.
Attributes marked `internal_only: true` can define intermediate counting units
and predicates, but cannot be returned as values. Counts are allowed; aliases,
buckets and numeric aggregates retain the restriction. Discovery exposes this
rule and planning rejects violations before submission. Physical-column aliases
must use the same restriction, including across dataset aliases. This does not provide
statistical privacy or a minimum cohort size.
`timeout` is a Go duration: YAML accepts `1m`; JSON uses nanoseconds.

```bash
metricspire validate-analysis --catalog reviewed-analysis.yaml
```

Optional runtime configuration, relative to the runtime file:

```yaml
exploration:
  catalog_paths: [reviewed-analysis.yaml]
  warehouse_id: '<existing-warehouse-id>'
  concurrency: 2
  capacity: 100
  result_ttl: 1h
```

Exploration uses its own warehouse and capacity, the deployment's existing
host/authentication, and the durable audit recorder. No login or credential
replacement is added. Source roles and attribute policy must be exported
explicitly; rights on an existing metric do not grant all raw fields.
Warehouse permissions remain authoritative.

## Temporary query

`AnalysisQuery` selects a logical `source` and registered attributes. Required
`time_range` uses a root DATE field, local-midnight RFC3339 bounds and its
business timezone, with inclusive start and exclusive end. Week grouping
starts on Monday; its dimension must be in the first-stage `group_by`. The [schema](../internal/exploration/query.schema.json) and
[request](../testdata/exploration/direct-query.json) describe the operators:

- Row `where` and conditional measures; `count`, `count_distinct`, `sum`,
  `avg`, `min`, `max`.
- Ordered typed expressions: arithmetic, comparisons, boolean/null checks,
  `weekday` (Monday 0), `case` buckets.
- Optional `summarize`: a second aggregation over the first stage's outputs.
- Final `share`: partition total computed before the final limit; division
  by zero returns NULL.

First-stage grouping defines the counting unit. Entity plus week yields
entity-weeks; a changing label can split that unit. Maintain the label anchor
and population/denominator in the semantic definition. Ask only about unresolved
choices that materially affect results; carry a user's answer through the
analysis. Common business workflows belong in external semantics.

## Approved associations

The planner follows explicit directed paths, at most eight joins, with complete
composite keys. Targets must match at most one row. Target aggregation prevents
fanout; duplicate matches reject the query. Missing matches preserve NULL or
reject according to configuration. Root-row conditions define the population before match checks. Conditions on
joined attributes run after cardinality checks, so ambiguous labels cannot be
hidden by filtering an arbitrarily selected value.
No arbitrary caller-provided JOIN.

Exact DATE snapshots support `same_day`, `week_start`, and `week_end`, anchored
to the root business date. Each week uses its own snapshot, including across
four-week summaries. Never substitute today's label or a latest available row.
Static dimensions can form multiple hops. Valid-from/to intervals, latest-row
matching, whole-batch snapshots, many-to-many associations, more than two
aggregate stages and non-DATE root ranges are not supported in this version.

## HTTP and MCP

All operations require the existing authenticated query permission:

| HTTP under `/api/v1/namespaces/{namespace}/analysis` | MCP tool |
| --- | --- |
| `GET /sources` | `discover_analysis` |
| `POST /plan` | `plan_analysis` |
| `POST /jobs` (202) | `submit_analysis` |
| `GET /jobs/{job}` | `get_analysis` |
| `POST /jobs/{job}/cancel` | `cancel_analysis` |

Hosted MCP exposes these tools only when exploration is configured. The stdio
bridge exposes the contract; an unconfigured server rejects those requests.
Existing metrics that correctly answer an online request use their existing
path; choose exploration explicitly for unsupported grain/formulas. Permission
or timeout failures never trigger an automatic route change.

Requests contain no SQL, physical bindings, identity, policy or executable plan.
Public discovery/plans/results omit those internals. Tokens reach only engine
execution. Admission/completion audit failures withhold results. Jobs are owned
by tenant, namespace and principal; accepted work survives request disconnect.
Cancellation requests propagate to the warehouse; the worker publishes a terminal
state after completion audit. Once that outcome is sealed, late cancellation does
not change it. Shutdown drains workers and their audit cleanup before closing
storage. Concurrency exhaustion returns 429. Completed results share a 32 MiB
encoded-result budget; oldest results can expire before their TTL under pressure.
Missing, expired and foreign jobs all return 404. Planning rejects requests whose
combined literals, filters and join bindings exceed 1,000 parameters.
This is a bounded single-process job manager: no restart recovery or durable
queue. Time/row/byte/operator bounds are not a scan-byte or cost guarantee.

## Verification boundary

Local independent numerical oracles cover staged activity and purchase analyses,
composite keys, multi-hop dimensions, snapshot anchors, missing/duplicate matches,
row conditions and shares before limits. HTTP/MCP tests cover direct sources
without metrics, identity, field permissions, typed results and hidden bindings.

Opt-in `TestAnalysisRealMCPDatabricks` executes read-only SELECTs against the
public TPC-DS calendar through local HTTP/MCP and a real Databricks warehouse:
staged formulas/buckets/conditional counts/shares, temporal plus static joins,
NULL preservation and relationship rejection. Local identity/audit fixtures
are used; this does not establish deployed OAuth, durable-audit integration,
company-business acceptance, production cost or SLA. Reuse an existing CLI
profile for that test; never print its credential or force a login.

```bash
METRICSPIRE_RUN_REAL_EXPLORATION=1 \
METRICSPIRE_EXPLORATION_PROFILE='<existing-profile>' \
METRICSPIRE_EXPLORATION_HOST='<existing-host>' \
METRICSPIRE_EXPLORATION_WAREHOUSE='<running-warehouse-id>' \
go test ./internal/httpapi -run '^TestAnalysisRealMCPDatabricks$' -count=1 -v
```

Deployment acceptance additionally requires loading the reviewed business
catalog, discovering only granted sources/fields, and reconciling results at
the requested grain against an independently reviewed warehouse query. A local
test run does not enable exploration on an already deployed MCP server.
