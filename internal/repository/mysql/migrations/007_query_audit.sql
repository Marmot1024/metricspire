CREATE TABLE metricspire_query_audit (
audit_id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, request_id VARCHAR(255) NOT NULL, tenant VARCHAR(128) NOT NULL, principal VARCHAR(255) NOT NULL,
namespace VARCHAR(128) NOT NULL, model_name VARCHAR(128) NOT NULL, release_id VARCHAR(128) NOT NULL, manifest_fingerprint CHAR(71) NOT NULL, logical_fingerprint CHAR(71) NOT NULL, physical_fingerprint CHAR(71) NOT NULL,
job_id VARCHAR(128) NOT NULL, event_kind VARCHAR(32) NOT NULL CHECK(event_kind IN ('query_started','query_succeeded','query_failed','query_cancelled')),
error_code VARCHAR(128) NOT NULL, row_count BIGINT NOT NULL CHECK(row_count>=0), truncated BOOLEAN NOT NULL, occurred_at DATETIME(6) NOT NULL,
UNIQUE(request_id,event_kind), INDEX(namespace,model_name,occurred_at,audit_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;
