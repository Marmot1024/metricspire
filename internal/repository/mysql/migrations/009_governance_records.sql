CREATE TABLE metricspire_governance_records (
namespace VARCHAR(128) NOT NULL, metric_code VARCHAR(128) NOT NULL, revision BIGINT NOT NULL CHECK(revision>0), definition JSON NOT NULL, semantic_model_name VARCHAR(128),
source_import_id VARCHAR(128) NOT NULL, updated_by VARCHAR(255) NOT NULL, updated_at DATETIME(6) NOT NULL, PRIMARY KEY(namespace,metric_code),
FOREIGN KEY(source_import_id) REFERENCES metricspire_governance_imports(import_id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;
