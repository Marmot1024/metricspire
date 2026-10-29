# Maintain and publish a metric

Use this guide to add a metric to a team's existing service. Query users only
need their team's web catalog or MCP connection. Maintainers need the model's
owner, an agreed definition and permission to change that model.

## Choose an editing path

| Change | Use |
| --- | --- |
| Description, owner, or a simple unpublished aggregate over existing fields | Web workbench: load the model's draft, edit, save, validate, review and publish. |
| New dataset, derived expression, or structured population/exclusion variant | Reviewed `SemanticModel` source file and the existing draft API or trusted operator CLI. |
| Temporary question that should not become a maintained metric | [External semantics and exploration](semantic-layer.md). |

The simplified web editor preserves structured definitions and locks their
calculation fields. It does not configure new databases, bindings or result
tables. Publication permission (`model:manage`) is separate from query permission.

## 1. Write the definition

Start with [the neutral daily sales model](../examples/online/model.yaml). It
defines revenue, order count and average order value. Open the file and follow
one metric from its `expression` to the entity and dataset field it uses.
Average order value divides total revenue by total orders; it does not average
daily ratios.

For your own model, copy the file to an ignored private directory and replace
the fixture definitions. Agree on population, exclusions, unit, counting grain,
business date/timezone and owner before connecting fields. Declare important
scope alternatives through [structured definitions](metric-definitions.md).
The fixture's `verified` evidence applies only to its synthetic data; do not
copy that claim onto your business metric.

Validate without connecting to a database, from the checkout root:

```sh
go run ./cmd/metricspire validate --source examples/online/model.yaml
```

Success prints `valid daily_sales` and a fingerprint. This checks the model's
structure, not the correctness of your source data or business definition.

## 2. Save a draft

For an existing deployed service, use the web workbench or the authenticated
[draft API](http-api.md#endpoints). Read the current draft first and save with
its `expected_revision`; a conflict means reload and compare the other change.
The source's `metadata.name` must match the model route. HTTP derives the actor
from the authenticated account.

To learn the lifecycle locally instead, use Go plus Docker Compose and a fresh
disposable PostgreSQL database:

```sh
docker compose -p metricspire-onboarding up -d --wait postgres
export METRICSPIRE_DATABASE_URL='postgres://metricspire:metricspire_dev_only@127.0.0.1:54329/metricspire?sslmode=disable'
go run ./cmd/metricspire migrate
go run ./cmd/metricspire draft-put \
  --namespace demo --source examples/online/model.yaml \
  --actor local-maintainer --expected-revision 0
```

Run from the checkout root. The Compose project owns a separate disposable volume;
port 54329 must be free. Its password is for this local example only. The trusted
CLI connects directly to metadata; it is an operator tool, not a way for a query
user to submit an identity or bypass service permissions. See
[MySQL metadata](mysql-metadata.md) for its separate migration setup.

## 3. Review and publish the exact revision

In the workbench, validate the saved candidate, inspect the review against the
active release and confirm its binding. Check the definition and formula against
an independent result from the intended data. Then publish the saved revision.
The API equivalents are `/validate`, `/review` and `/publish` under the model
route; [the endpoint table](http-api.md#endpoints) describes each operation.

In the disposable local example, the supplied synthetic model is already the
candidate. Review its three formulas, then publish revision 1:

```sh
go run ./cmd/metricspire publish \
  --namespace demo --model daily_sales --revision 1 --actor local-maintainer
go run ./cmd/metricspire release-list --namespace demo --model daily_sales
```

Expect a release ID, `source_revision: 1` and an active release. This is a real
metadata publication; it has not created or queried any business data. For another
save, read the draft revision instead of repeating `--expected-revision 0`.

Published calculation/scope changes require a new metric identity. Do not replace
a published formula or remove its definition. Description-only changes can use
a new model revision. Explicit [rollback](architecture.md#catalog-lifecycle)
reactivates a previous release and preserves history.

## 4. Make the published metric queryable

An operator configures the route once; maintainers should reuse it for compatible
changes. Publication alone does not install it.

| Deployment | What must match the model |
| --- | --- |
| PostgreSQL, ClickHouse or Doris result serving | Independent online policy, physical binding, numeric external codes, precomputed data and a valid snapshot contract. New formulas/metrics require validation of a compatible batch. See [independent setup](self-hosted.md). |
| Databricks analytical execution | Analytical policy/binding plus the intended user's warehouse and source-table permissions. See [service setup](getting-started.md#run-the-http-service). |

Use the intended reader's identity to search the metric, inspect its definition
and run a bounded query. For online serving, search/details use
`route: online_metrics`, then `query_online_metrics`; analytical queries use
`prepare_query → run_query`. See [MCP workflow](mcp.md#query-workflow).

For the supplied daily-sales fixture, with the two producer rows loaded correctly,
`examples/online/query.json` requests the two UTC dates separately. Expected
revenue/order values are `100.000001 / 2` and `50.000002 / 1`; the two-day control
totals are `150.000003 / 3`. These are fixture expectations, not stored live results.
Reconcile returned typed rows, release ID and snapshot. Exercise a denied reader
as a separate check; an administrator's query does not prove reader access.

Stop the local example with `docker compose -p metricspire-onboarding stop`;
this preserves its volume. A production service needs its own deployment and
credentials, not the example database.

## Optional AI assistance

The repository includes a short [metric maintainer skill](../skills/metricspire-maintainer/SKILL.md).
It guides definition, draft/review/publication and result reconciliation; it does
not grant access or install anything into query clients. Copy that folder into
your client's skills directory if you want it. Keep the maintained checkout path
available to the assistant and give it this guide. The ordinary query skill stays
separate. A useful first request is: “Prepare a new metric draft from this agreed
definition and show its review before publishing.”
