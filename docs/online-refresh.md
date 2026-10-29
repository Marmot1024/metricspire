# Offline aggregate refresh

MetricSpire queries precomputed daily DWS/ADS tables; it does not automatically
extract a warehouse or synchronize real data. This operator tool publishes a
validated **PostgreSQL** tenant snapshot from a local typed file. MySQL remains
an independent metadata store; ClickHouse/Doris operators currently use their
own ETL and published online data contracts.

The warehouse job must retain the metric's reviewed grain, exclusions and
population, produce a stable unique entity key, and supply independently
computed row count and SUM controls for **every integer/decimal column**.
The exporter cannot certify business meaning or prove that controls were
computed independently. Never sum ratios; export their additive components.

## 1. Provision separately

Review `examples/online/model.yaml`, its PostgreSQL `binding.yaml`, and the
neutral [fixture DDL](../examples/online/producer/schema.sql). Provision that serving table
and schema before running the producer. Run the DDL only in a fresh disposable
database; it intentionally refuses an existing schema. Target columns must match the binding
exactly: string → text/varchar, integer → bigint, decimal → numeric, boolean →
boolean, date → date, plus the four non-null snapshot columns. A tenant snapshot
must have a unique entity key. Partitioned/inherited tables, triggers (including foreign-key triggers), rules,
row-level security, generated fields, arbitrary
SQL and schema inference are outside this first loader.

Use separate operator and query credentials. The initializer needs schema DDL
permission; the refresh account needs SELECT/INSERT/DELETE on the reviewed
target, SELECT/INSERT on `_metricspire_refresh_batches`, SELECT/INSERT/UPDATE
on `_metricspire_refresh_heads`, schema USAGE, and temporary-table permission
on the serving database. Serving keeps its read-only role and
never creates producer state. Producer state belongs in the serving database,
separate from the MetricSpire metadata database.

Set `METRICSPIRE_REFRESH_DATABASE_URL` through your secret provider. Use an
explicit `postgres://user:password@host/database?sslmode=verify-full` URL. Only disposable loopback
fixtures may use `sslmode=disable`; other connection options are refused.
Do not pass passwords in CLI arguments or commit environment files.

```sh
metricspire init-online-refresh --source model.yaml --binding online-binding.yaml
```

## 2. Export a batch

The input is UTF-8 NDJSON: each row contains **all physical bound columns**;
values are strings (including integers and decimals), or JSON null for fields
explicitly marked nullable in the reviewed model. No floats,
scientific notation, duplicate/missing keys or extra columns are accepted.
Decimals support at most 38 digits and 18 fractional digits; strings 4096 bytes;
rows 64 KiB; batches 50 million rows. Null controls mean all source values were
null. An empty snapshot is refused to prevent an accidental total deletion.

Neutral inputs are in `examples/online/producer`. Replace the example watermark
with the **UTC upstream coverage time at microsecond precision or coarser**, never the export or load clock:

```sh
metricspire export-online --source model.yaml --binding online-binding.yaml \
  --input rows.ndjson --reconcile reconcile.json --output batch.ndjson \
  --tenant demo --batch-id source-run-001 --data-as-of 2026-09-03T00:00:00Z
```

The two-pass export validates source controls, hashes canonical rows, and issues
a private immutable artifact by atomic creation. It refuses an existing output.
Source and artifact files can contain private data; keep them outside Git and
container build contexts. The checksum detects corruption, not authenticity:
transport artifacts through a trusted, access-controlled operator channel.

## 3. Publish and retry

```sh
metricspire refresh-online --source model.yaml --binding online-binding.yaml \
  --input batch.ndjson --tenant demo --expected-batch ''
# A later batch must name the currently reviewed head:
metricspire refresh-online --source model.yaml --binding online-binding.yaml \
  --input next-batch.ndjson --tenant demo --expected-batch source-run-001
```

The loader serializes each table/tenant, checks the expected head and advancing
watermark, COPYs to temporary staging, validates typed values, key uniqueness,
checksum, row count and numeric totals after database conversion, then replaces
that tenant's rows and commits its receipt/head in one transaction. Other tenants
are retained. A failure before commit preserves the previous snapshot. On an
uncertain commit outcome, retry the identical artifact: a current identical batch
returns `repeated: true`; the same ID with changed content is refused; a historical
superseded batch cannot restore old data. A correction uses a new batch ID and a
reviewed advancing coverage watermark. Keep artifact and receipt history under
your retention policy; this tool does not delete history automatically.

The online reader compares the producer data contract and coverage time with
its configured freshness limit. Loading old source data does not make it fresh.
The loader is a command, not a scheduler: run it after successful upstream ETL,
record its receipt, alert on failure, and retain the old artifact for investigation.
Synthetic tests and large generated batches do not certify production DWS/ADS,
company business definitions, upstream extraction, or a production SLA.
