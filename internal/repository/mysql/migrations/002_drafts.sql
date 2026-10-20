CREATE TABLE metricspire_drafts (
namespace VARCHAR(128) NOT NULL, model_name VARCHAR(128) NOT NULL, revision BIGINT NOT NULL CHECK(revision>0), source JSON NOT NULL,
updated_by VARCHAR(255) NOT NULL, updated_at DATETIME(6) NOT NULL, PRIMARY KEY(namespace,model_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;
