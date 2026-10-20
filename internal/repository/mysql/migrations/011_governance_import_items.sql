CREATE TABLE metricspire_governance_import_items (
import_id VARCHAR(128) NOT NULL, metric_code VARCHAR(128) NOT NULL, previous_revision BIGINT, applied_revision BIGINT NOT NULL CHECK(applied_revision>0), PRIMARY KEY(import_id,metric_code),
FOREIGN KEY(import_id) REFERENCES metricspire_governance_imports(import_id) ON DELETE RESTRICT, CHECK(previous_revision IS NULL OR previous_revision>0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;
