CREATE TABLE metricspire_model_locks (
namespace VARCHAR(128) NOT NULL, model_name VARCHAR(128) NOT NULL, PRIMARY KEY(namespace,model_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;
