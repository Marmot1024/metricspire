CREATE TABLE metricspire_release_events (
event_id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, namespace VARCHAR(128) NOT NULL, model_name VARCHAR(128) NOT NULL, event_kind VARCHAR(16) NOT NULL CHECK(event_kind IN ('published','rollback','deactivated')),
from_release_id VARCHAR(128), to_release_id VARCHAR(128), actor VARCHAR(255) NOT NULL, note TEXT NOT NULL, created_at DATETIME(6) NOT NULL,
FOREIGN KEY(namespace,model_name,from_release_id) REFERENCES metricspire_releases(namespace,model_name,release_id) ON DELETE RESTRICT,
FOREIGN KEY(namespace,model_name,to_release_id) REFERENCES metricspire_releases(namespace,model_name,release_id) ON DELETE RESTRICT, INDEX(namespace,model_name,event_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;
