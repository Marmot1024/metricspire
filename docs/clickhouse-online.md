# ClickHouse online aggregates

`online.driver: clickhouse_online` reads reviewed DWS/ADS aggregates through the
existing synchronous online HTTP contract. Metric definitions, releases and audits
remain in PostgreSQL. This driver does not enable ClickHouse exploration or the
analytical MCP route, and never falls back to Databricks.

Start with [the runtime example](../examples/runtime.clickhouse.example.yaml) and
[the neutral binding](../examples/online/clickhouse-binding.yaml). Configure the
result connection in `METRICSPIRE_CLICKHOUSE_URL` as
`https://READER:PASSWORD@clickhouse.example.com:8443/`, URL-encoding credential
components. Keep it in deployment secrets. Only loopback connections permit HTTP;
redirects, caller-configured URL options and custom paths are rejected. SQL and
parameter values travel in request bodies; only fixed format/budget settings and a
random query ID appear in the generated URL. No cloud credential is
required for OIDC + PostgreSQL metadata + ClickHouse serving.

## Reviewed producer contract

The first adapter targets ClickHouse 25.8 with a plain `MergeTree` or
`ReplicatedMergeTree` table. `ResourceRef.schema` is the ClickHouse database;
`catalog` and `uri` stay empty. Distributed, Summing/Replacing/Aggregating engines,
views and alias columns are not supported. Duplicate retained-grain rows are not
deduplicated: the producer must publish one complete, unique-grain batch atomically.
Do not expose an incremental append, in-progress mutation or partially replaced
partition set as a finished batch.

Retain the semantic grain and additive components. A ratio uses sums of its
components, not the sum/average of stored ratios. Supply these non-null columns:

| Column | Required physical type | Meaning |
| --- | --- | --- |
| `_metricspire_tenant` | `String` | Server-reviewed tenant scope |
| `_metricspire_batch_id` | `String` | Nonempty completed batch identifier |
| `_metricspire_data_as_of` | `DateTime64(3, 'UTC')` | Consistent coverage time |
| `_metricspire_data_contract` | `String` | Reviewed calculation/binding fingerprint |

Compute the fingerprint offline, without connecting or synchronizing data:

```sh
metricspire online-contract --source examples/online/model.yaml \
  --binding examples/online/clickhouse-binding.yaml
```

The command returns `data_contract`, `manifest_fingerprint` and the engine. It
hashes the supplied reviewed definitions; it does not publish them or certify the
producer. Use exactly the source/binding deployed for that release. Presentation
changes can reuse compatible batches; calculation, structured scope, source or
numeric-contract changes cannot. A PostgreSQL batch fingerprint is not a
ClickHouse fingerprint.

## Permissions and numeric limits

Use a dedicated credential with **table-level SELECT only**, no grant option,
wildcards or role membership. Grant SELECT on each configured source and on
`system.grants`, `system.role_grants`, `system.tables`, `system.columns` so startup
can inspect the credential and source contracts. This intentionally narrow first
profile rejects role-based grants, even read-only ones. The test administrator is
never an application credential. Keep the reader's settings profile compatible
with the request's bounded read settings.

Source numeric columns support signed/unsigned integers up to 64 bits and
`Decimal(P,S)` with `P <= 18`, `S <= 6`, optionally Nullable. Integer semantic fields
require integer physical columns. Float and wider/higher-scale decimals are
rejected rather than silently rounded. Calendar dimensions use Date; categorical
dimensions use String/LowCardinality(String), signed integers or Bool.

ClickHouse performs exact widened SUMs. At most 1,000 aggregate rows are brought
into the service; derived arithmetic uses bounded arbitrary-precision rationals,
with memoized dependencies and cancellation checks. Decimal outputs round to at
most 12 fractional places, halfway away from zero; ordering uses the exact value
before rounding. NULL propagates, zero division returns NULL, and integer results
must be integral and fit signed 64 bits. The driver fingerprint includes this
numeric contract. This is bounded aggregate finishing, not raw-data processing.

The explicit type and scan limits prevent unchecked wide-decimal overflow;
ClickHouse documents that Decimal128/256 arithmetic does not detect overflow.
[Decimal semantics](https://clickhouse.com/docs/reference/data-types/decimal)

## Failure and resource boundaries

- Whole calendar days, at most 31 days; SUM and simple arithmetic only; no joins,
  distinct counts or metric-level conditional aggregates.
- Existing authenticated allowlist, structured selection, concurrency and durable
  audit rules apply. Tenant-shared access does not inherit Warehouse user rights.
- Remote read budget: 2 seconds, 50 million scanned rows, 256 MiB query memory,
  2 threads, 1,001 result rows and 8 MiB wire response. Overflow throws; no partial
  result is delivered. Final rows remain within the request's limit and 4 MiB.
- Metadata and values come from the same aggregate read. Empty, mixed, stale,
  future or incompatible batches are refused. Permissions or connection failures
  are sanitized and never trigger retries or another engine.
- HTTP context cancellation closes the request. ClickHouse is asked to cancel on
  disconnect and also has its own execution deadline; immediate remote stop is
  not guaranteed for every server/proxy configuration. Test that boundary in your
  deployment. Late HTTP 200 error suffixes are rejected by full response decoding.

## Synthetic acceptance

Use the [isolated lab](../deploy/testing/README.md). Set
`METRICSPIRE_CLICKHOUSE_TEST_URL` to a disposable **loopback administrator** URL
only for tests; `METRICSPIRE_REQUIRE_CLICKHOUSE_TESTS=1` makes missing setup fail.
The startup test also needs the disposable PostgreSQL test URL. Fixtures create
and clean their own databases/users, start the real service with a test OIDC
issuer, and verify results, authorization, selection and persisted audit.
Never load private semantic assets or real customer data into this environment.
