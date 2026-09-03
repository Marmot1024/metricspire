CREATE TABLE metricspire_query_audit (
    audit_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    request_id TEXT NOT NULL CHECK (btrim(request_id) <> ''),
    tenant TEXT NOT NULL CHECK (btrim(tenant) <> ''),
    principal TEXT NOT NULL CHECK (btrim(principal) <> ''),
    namespace TEXT NOT NULL CHECK (btrim(namespace) <> ''),
    model_name TEXT NOT NULL CHECK (btrim(model_name) <> ''),
    release_id TEXT NOT NULL CHECK (btrim(release_id) <> ''),
    manifest_fingerprint TEXT NOT NULL CHECK (manifest_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    logical_fingerprint TEXT NOT NULL CHECK (logical_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    physical_fingerprint TEXT NOT NULL CHECK (physical_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    job_id TEXT NOT NULL DEFAULT '',
    event_kind TEXT NOT NULL CHECK (event_kind IN ('query_started', 'query_succeeded', 'query_failed', 'query_cancelled')),
    error_code TEXT NOT NULL DEFAULT '',
    row_count INTEGER NOT NULL DEFAULT 0 CHECK (row_count >= 0),
    truncated BOOLEAN NOT NULL DEFAULT FALSE,
    occurred_at TIMESTAMPTZ NOT NULL
);

CREATE UNIQUE INDEX metricspire_query_audit_request_kind_idx
    ON metricspire_query_audit (request_id, event_kind);

CREATE INDEX metricspire_query_audit_model_time_idx
    ON metricspire_query_audit (namespace, model_name, occurred_at, audit_id);
