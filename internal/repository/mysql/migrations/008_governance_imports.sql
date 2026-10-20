CREATE TABLE metricspire_governance_imports (
import_order BIGINT NOT NULL AUTO_INCREMENT UNIQUE, import_id VARCHAR(128) NOT NULL PRIMARY KEY, namespace VARCHAR(128) NOT NULL, source_fingerprint CHAR(71) NOT NULL, previous_import_id VARCHAR(128), record_count INT NOT NULL CHECK(record_count>0),
created_by VARCHAR(255) NOT NULL, created_at DATETIME(6) NOT NULL, rolled_back_by VARCHAR(255), rolled_back_at DATETIME(6),
FOREIGN KEY(previous_import_id) REFERENCES metricspire_governance_imports(import_id) ON DELETE RESTRICT,
CHECK((rolled_back_by IS NULL)=(rolled_back_at IS NULL)), INDEX(namespace,created_at,import_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;
