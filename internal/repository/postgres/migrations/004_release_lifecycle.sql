ALTER TABLE metricspire_releases
    ADD COLUMN channel TEXT NOT NULL DEFAULT 'certified'
    CHECK (channel IN ('trial', 'certified'));

UPDATE metricspire_releases
SET channel = 'trial'
WHERE note LIKE 'STAGING TEST RELEASE - business definitions unverified:%';

ALTER TABLE metricspire_release_events
    DROP CONSTRAINT metricspire_release_events_event_kind_check;

ALTER TABLE metricspire_release_events
    ADD CONSTRAINT metricspire_release_events_event_kind_check
    CHECK (event_kind IN ('published', 'rollback', 'deactivated'));

ALTER TABLE metricspire_release_events
    ALTER COLUMN to_release_id DROP NOT NULL;
