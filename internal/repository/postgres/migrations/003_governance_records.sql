CREATE TABLE metricspire_governance_imports (
    import_id TEXT PRIMARY KEY CHECK (btrim(import_id) <> ''),
    namespace TEXT NOT NULL CHECK (btrim(namespace) <> ''),
    source_fingerprint TEXT NOT NULL CHECK (source_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    previous_import_id TEXT,
    record_count INTEGER NOT NULL CHECK (record_count > 0),
    created_by TEXT NOT NULL CHECK (btrim(created_by) <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    rolled_back_by TEXT,
    rolled_back_at TIMESTAMPTZ,
    FOREIGN KEY (previous_import_id) REFERENCES metricspire_governance_imports (import_id) ON DELETE RESTRICT,
    CHECK ((rolled_back_by IS NULL) = (rolled_back_at IS NULL))
);

CREATE TABLE metricspire_governance_records (
    namespace TEXT NOT NULL CHECK (btrim(namespace) <> ''),
    metric_code TEXT NOT NULL CHECK (metric_code ~ '^[a-z][a-z0-9_]{0,127}$'),
    revision BIGINT NOT NULL CHECK (revision > 0),
    definition JSONB NOT NULL,
    semantic_model_name TEXT,
    source_import_id TEXT NOT NULL,
    updated_by TEXT NOT NULL CHECK (btrim(updated_by) <> ''),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (namespace, metric_code),
    FOREIGN KEY (source_import_id) REFERENCES metricspire_governance_imports (import_id) ON DELETE RESTRICT,
    CHECK (semantic_model_name IS NULL OR btrim(semantic_model_name) <> '')
);

CREATE TABLE metricspire_governance_record_revisions (
    namespace TEXT NOT NULL,
    metric_code TEXT NOT NULL,
    revision BIGINT NOT NULL CHECK (revision > 0),
    definition JSONB NOT NULL,
    semantic_model_name TEXT,
    source_import_id TEXT NOT NULL,
    created_by TEXT NOT NULL CHECK (btrim(created_by) <> ''),
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (namespace, metric_code, revision),
    FOREIGN KEY (source_import_id) REFERENCES metricspire_governance_imports (import_id) ON DELETE RESTRICT,
    CHECK (semantic_model_name IS NULL OR btrim(semantic_model_name) <> '')
);

CREATE TABLE metricspire_governance_import_items (
    import_id TEXT NOT NULL,
    metric_code TEXT NOT NULL,
    previous_revision BIGINT,
    applied_revision BIGINT NOT NULL CHECK (applied_revision > 0),
    PRIMARY KEY (import_id, metric_code),
    FOREIGN KEY (import_id) REFERENCES metricspire_governance_imports (import_id) ON DELETE RESTRICT,
    CHECK (previous_revision IS NULL OR previous_revision > 0)
);

CREATE INDEX metricspire_governance_records_search_idx
    ON metricspire_governance_records (namespace, metric_code);

CREATE INDEX metricspire_governance_imports_created_idx
    ON metricspire_governance_imports (namespace, created_at, import_id);
