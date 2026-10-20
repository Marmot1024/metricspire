# Doris aggregate serving

Use `examples/runtime.doris.example.yaml` with PostgreSQL or MySQL metadata and
standard OIDC. The online adapter reads reviewed **internal OLAP base tables**
using Doris' MySQL protocol (certification target: Doris 4.1.0). It does not
implement general analytical queries, exploration, warehouse extraction or a
Doris producer/scheduler. External catalogs and multi-table queries are refused.

Set `METRICSPIRE_DORIS_URL` through your secret provider, using
`doris://user:password@host:9030?tls=true`. Verified TLS is the default;
`tls=false` is allowed only for disposable loopback fixtures. The credential
must be a dedicated user with explicit table SELECT grants on exactly the
configured resources: no inherited roles, global/catalog grants, write, grant,
resource or column privileges. Built-in system-schema reads and normal workload
usage are accepted. Unexpected privilege formats fail closed; revalidate after
Doris upgrades. Caller OIDC or Databricks credentials never reach the engine.

The producer maintains additive daily aggregate components and these non-null
columns on every row:

| Column | Doris type | Meaning |
| --- | --- | --- |
| `_metricspire_tenant` | VARCHAR | Configured tenant |
| `_metricspire_batch_id` | VARCHAR | One immutable batch ID |
| `_metricspire_data_as_of` | DATETIME(3) | Explicit UTC source coverage |
| `_metricspire_data_contract` | VARCHAR(71) | `online-contract` fingerprint |

```sh
metricspire online-contract --source model.yaml --binding doris-binding.yaml
metricspire serve --config runtime.doris.yaml
```

Use the reviewed binding in `examples/online/doris-binding.yaml`. Provision the
physical table separately; every bound field must use a compatible DATE,
VARCHAR/CHAR, BOOLEAN, signed integer, or DECIMAL(precision <=18, scale <=6)
type. Metric components use signed integers or those exact decimals, with SUM
leaves widened to DECIMAL(38,6). Derived arithmetic uses bounded exact rational
calculation and decimal output rounded to 12 places. Integer results must fit
int64. NULL and divide-by-zero remain NULL. Ordering happens before output
rounding. Binary string matching, DATE and typed integer/boolean filters retain
their engine semantics; floating-point columns are refused.

Queries require a positive, calendar-aligned DATE range of at most 31 days,
daily grouping and one reviewed table. There are no joins, user SQL or automatic
engine fallback. Engine hints cap query time at 2 seconds, memory at 256 MiB and
pipeline parallelism at 2. The operator must enforce any additional workload
limits; this adapter does not claim a hard Doris scan-row cap. Returned group
counts above 50 million source rows are refused after execution. At most 1000
output groups, 4 MiB typed output and bounded arithmetic are allowed. Mixed
batches, contract mismatches, invalid/future source time and expired coverage
fail without returning a partial result.

Each query owns its connection and at most four reads run concurrently. The
transport stops at 8 MiB including handshake/TLS overhead; a MySQL packet can
allocate up to 16 MiB before a failed read. Partial/oversized responses have
their socket closed before rows cleanup, preventing tail draining. Client read
and write deadlines are three seconds, connection timeout five seconds; a
request's context and configured online timeout can terminate earlier.

For disposable FE/BE testing, see `deploy/testing/doris-compose.yaml`. It exposes
only a loopback SQL port and uses generated/public fixtures, no host networking,
privileged container or warehouse mount. `tools/doris-ready` requires explicit
`METRICSPIRE_DORIS_DISPOSABLE=1` and the private lab password environment variable;
it waits for the backend before installing the disposable root password.

The opt-in `METRICSPIRE_DORIS_LARGE_TEST=1` acceptance generates ten million
rows inside a disposable Doris database and checks exact sums, counts and the
ratio under the unchanged read hints. Its fixture writer has a separate longer
load timeout; the product read timeout is unchanged. Producer rows must share
one explicit source watermark per batch. A single synthetic run is not a
concurrency or production latency guarantee.
