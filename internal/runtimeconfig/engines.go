package runtimeconfig

import (
	"fmt"
	"regexp"
)

// Connection configuration contains environment variable names, never secrets.
type StorageConfig struct {
	Driver         string `json:"driver,omitempty" yaml:"driver,omitempty"`
	DatabaseURLEnv string `json:"database_url_env,omitempty" yaml:"database_url_env,omitempty"`
}

type QueryEngineConfig struct {
	Driver string `json:"driver,omitempty" yaml:"driver,omitempty"`
}

func (c Config) StorageDriver() string {
	if c.Storage.Driver == "" {
		return "postgres"
	}
	return c.Storage.Driver
}

func (c Config) QueryDriver() string {
	if c.QueryEngine.Driver == "" {
		return "databricks_sql"
	}
	return c.QueryEngine.Driver
}

func (c Config) DatabaseURLEnv() string {
	if c.Storage.DatabaseURLEnv == "" {
		return "METRICSPIRE_DATABASE_URL"
	}
	return c.Storage.DatabaseURLEnv
}

func (c OnlineConfig) DatabaseEnv() string {
	if c.DatabaseURLEnv == "" {
		return "METRICSPIRE_ONLINE_DATABASE_URL"
	}
	return c.DatabaseURLEnv
}

func (c Config) validateEngines() error {
	if c.StorageDriver() != "mysql" && c.StorageDriver() != "postgres" {
		return fmt.Errorf("storage.driver %q is not implemented; supported: postgres, mysql", c.Storage.Driver)
	}
	if c.QueryDriver() != "databricks_sql" && c.QueryDriver() != "disabled" {
		return fmt.Errorf("query_engine.driver %q is not implemented; supported: databricks_sql, disabled", c.QueryDriver())
	}
	envName := regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	if !envName.MatchString(c.DatabaseURLEnv()) {
		return fmt.Errorf("storage.database_url_env must be an environment variable name")
	}
	if c.Online != nil {
		if c.Online.Driver != "" && c.Online.Driver != "postgres_online" && c.Online.Driver != "clickhouse_online" && c.Online.Driver != "doris_online" {
			return fmt.Errorf("online.driver %q is not implemented; supported: postgres_online, clickhouse_online, doris_online", c.Online.Driver)
		}
		if !envName.MatchString(c.Online.DatabaseEnv()) {
			return fmt.Errorf("online.database_url_env must be an environment variable name")
		}
		if c.Online.DatabaseEnv() == c.DatabaseURLEnv() {
			return fmt.Errorf("online and storage require separate credential environment variables")
		}
	}
	if c.QueryDriver() == "disabled" {
		if len(c.Bindings) != 0 || len(c.Policies) != 0 || len(c.ReleasePolicy.TrialNamespaces) != 0 {
			return fmt.Errorf("disabled query engine requires empty top-level bindings, policies and trial namespaces; configure online routes independently")
		}
		if c.Online == nil || len(c.Online.Policies) == 0 {
			return fmt.Errorf("disabled query engine requires online serving with independent policies")
		}
	}
	return nil
}

// Catalog discovery uses exactly the selected execution profile's policies.
func (c Config) CatalogRoutes() ([]PolicyRoute, []BindingRoute) {
	if c.QueryDriver() == "disabled" && c.Online != nil {
		return c.Online.Policies, c.Online.Bindings
	}
	return c.Policies, c.Bindings
}

func (c OnlineConfig) EngineDriver() string {
	if c.Driver == "" {
		return "postgres_online"
	}
	return c.Driver
}
