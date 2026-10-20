CREATE TABLE metricspire_draft_revisions (
namespace VARCHAR(128) NOT NULL, model_name VARCHAR(128) NOT NULL, revision BIGINT NOT NULL CHECK(revision>0), source JSON NOT NULL,
created_by VARCHAR(255) NOT NULL, created_at DATETIME(6) NOT NULL, PRIMARY KEY(namespace,model_name,revision)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;
