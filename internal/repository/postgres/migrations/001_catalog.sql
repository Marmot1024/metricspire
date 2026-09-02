CREATE TABLE metricspire_drafts (
    namespace TEXT NOT NULL CHECK (btrim(namespace) <> ''),
    model_name TEXT NOT NULL CHECK (btrim(model_name) <> ''),
    revision BIGINT NOT NULL CHECK (revision > 0),
    source JSONB NOT NULL,
    updated_by TEXT NOT NULL CHECK (btrim(updated_by) <> ''),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (namespace, model_name)
);

CREATE TABLE metricspire_draft_revisions (
    namespace TEXT NOT NULL,
    model_name TEXT NOT NULL,
    revision BIGINT NOT NULL CHECK (revision > 0),
    source JSONB NOT NULL,
    created_by TEXT NOT NULL CHECK (btrim(created_by) <> ''),
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (namespace, model_name, revision)
);

CREATE TABLE metricspire_releases (
    namespace TEXT NOT NULL,
    model_name TEXT NOT NULL,
    release_id TEXT NOT NULL CHECK (btrim(release_id) <> ''),
    source_revision BIGINT NOT NULL CHECK (source_revision > 0),
    manifest_fingerprint TEXT NOT NULL CHECK (manifest_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    manifest JSONB NOT NULL,
    created_by TEXT NOT NULL CHECK (btrim(created_by) <> ''),
    note TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (namespace, model_name, release_id),
    UNIQUE (namespace, model_name, source_revision),
    UNIQUE (namespace, model_name, manifest_fingerprint)
);

CREATE TABLE metricspire_active_releases (
    namespace TEXT NOT NULL,
    model_name TEXT NOT NULL,
    release_id TEXT NOT NULL,
    activated_by TEXT NOT NULL CHECK (btrim(activated_by) <> ''),
    activated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (namespace, model_name),
    FOREIGN KEY (namespace, model_name, release_id)
        REFERENCES metricspire_releases (namespace, model_name, release_id)
        ON DELETE RESTRICT
);

CREATE TABLE metricspire_release_events (
    event_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    namespace TEXT NOT NULL,
    model_name TEXT NOT NULL,
    event_kind TEXT NOT NULL CHECK (event_kind IN ('published', 'rollback')),
    from_release_id TEXT,
    to_release_id TEXT NOT NULL,
    actor TEXT NOT NULL CHECK (btrim(actor) <> ''),
    note TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (namespace, model_name, to_release_id)
        REFERENCES metricspire_releases (namespace, model_name, release_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (namespace, model_name, from_release_id)
        REFERENCES metricspire_releases (namespace, model_name, release_id)
        ON DELETE RESTRICT
);

CREATE INDEX metricspire_releases_created_idx
    ON metricspire_releases (namespace, model_name, created_at, release_id);

CREATE INDEX metricspire_release_events_created_idx
    ON metricspire_release_events (namespace, model_name, event_id);
