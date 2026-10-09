# Neutral online-serving fixture

These synthetic definitions exercise numeric codes, multiple outputs and a derived metric on precomputed daily sales. They do not certify customer metrics or establish freshness requirements.

The retained grain is one tenant × calendar date × region. `row_key` must identify that grain uniquely. Revenue and order counts are additive at this grain; average order value is computed from their summed components. Never substitute a summed daily DAU for an exact monthly distinct-user count.

The binding targets `serving.daily_sales` using `postgres_online`. A separately reviewed producer must supply the table, SELECT-only role, tenant/snapshot metadata and atomic refresh; the runtime never creates them. The integration test provisions an isolated synthetic equivalent only in a disposable local database. See [the online query contract](../../docs/online-query.md).
