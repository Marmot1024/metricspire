-- Neutral fixture only. Run as an operator in a fresh disposable PostgreSQL DB.
-- Intentionally fails if the schema exists; never replaces existing tables.
BEGIN;
CREATE SCHEMA serving;
CREATE TABLE serving.daily_sales (
    row_key text NOT NULL,
    day date NOT NULL,
    region text NOT NULL,
    revenue numeric(38, 6) NOT NULL,
    orders bigint NOT NULL,
    _metricspire_tenant text NOT NULL,
    _metricspire_batch_id text NOT NULL,
    _metricspire_data_as_of timestamptz NOT NULL,
    _metricspire_data_contract text NOT NULL,
    PRIMARY KEY (_metricspire_tenant, row_key)
);
COMMIT;
