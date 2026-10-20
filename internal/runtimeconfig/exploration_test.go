package runtimeconfig_test

import (
	"github.com/marmot1024/metricspire/internal/runtimeconfig"
	"testing"
)

func TestExplorationBudgetsRequireExplicitConfiguration(t *testing.T) {
	valid := runtimeconfig.ExplorationConfig{CatalogPaths: []string{"analysis.json"}, WarehouseID: "warehouse", Concurrency: 2, Capacity: 20, ResultTTL: "1h"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*runtimeconfig.ExplorationConfig){"paths": func(c *runtimeconfig.ExplorationConfig) { c.CatalogPaths = nil }, "duplicate_paths": func(c *runtimeconfig.ExplorationConfig) { c.CatalogPaths = []string{"analysis.json", "analysis.json"} }, "warehouse": func(c *runtimeconfig.ExplorationConfig) { c.WarehouseID = "" }, "concurrency": func(c *runtimeconfig.ExplorationConfig) { c.Concurrency = 0 }, "capacity": func(c *runtimeconfig.ExplorationConfig) { c.Capacity = 1 }, "ttl_missing": func(c *runtimeconfig.ExplorationConfig) { c.ResultTTL = "" }, "ttl_exceeded": func(c *runtimeconfig.ExplorationConfig) { c.ResultTTL = "25h" }} {
		t.Run(name, func(t *testing.T) {
			c := valid
			change(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("invalid exploration budget accepted")
			}
		})
	}
}

func TestExternalExplorationConfiguration(t *testing.T) {
	for _, driver := range []string{"trino", "presto"} {
		c := runtimeconfig.ExplorationConfig{Driver: driver, CatalogPaths: []string{"private.json"}, ConnectionEnv: "METRICSPIRE_EXPLORATION_URL", User: "reader", Concurrency: 1, Capacity: 10, ResultTTL: "10m"}
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		for _, change := range []func(*runtimeconfig.ExplorationConfig){func(c *runtimeconfig.ExplorationConfig) { c.WarehouseID = "wrong" }, func(c *runtimeconfig.ExplorationConfig) { c.ConnectionEnv = "https://secret" }, func(c *runtimeconfig.ExplorationConfig) { c.User = "reader\nadmin" }} {
			copy := c
			change(&copy)
			if err := copy.Validate(); err == nil {
				t.Fatal("mixed external configuration accepted")
			}
		}
	}
}
