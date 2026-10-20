CREATE TABLE metricspire_active_releases (
namespace VARCHAR(128) NOT NULL, model_name VARCHAR(128) NOT NULL, release_id VARCHAR(128) NOT NULL, activated_by VARCHAR(255) NOT NULL, activated_at DATETIME(6) NOT NULL,
PRIMARY KEY(namespace,model_name), FOREIGN KEY(namespace,model_name,release_id) REFERENCES metricspire_releases(namespace,model_name,release_id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;
