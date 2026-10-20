CREATE TABLE metricspire_releases (
namespace VARCHAR(128) NOT NULL, model_name VARCHAR(128) NOT NULL, release_id VARCHAR(128) NOT NULL, source_revision BIGINT NOT NULL CHECK(source_revision>0), manifest_fingerprint CHAR(71) NOT NULL,
manifest JSON NOT NULL, channel VARCHAR(16) NOT NULL CHECK(channel IN ('trial','certified')), created_by VARCHAR(255) NOT NULL, note TEXT NOT NULL, created_at DATETIME(6) NOT NULL,
PRIMARY KEY(namespace,model_name,release_id), UNIQUE(namespace,model_name,source_revision), UNIQUE(namespace,model_name,manifest_fingerprint), INDEX(namespace,model_name,created_at,release_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;
