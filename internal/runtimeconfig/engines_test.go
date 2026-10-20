package runtimeconfig_test

import (
	"testing"

	"github.com/marmot1024/metricspire/internal/runtimeconfig"
)

func TestPortableProfileAndUnsupportedDrivers(t *testing.T) {
	base, err := runtimeconfig.Load("../../examples/runtime.postgres.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*runtimeconfig.Config)
	}{
		{"metadata mysql", func(c *runtimeconfig.Config) { c.Storage.Driver = "mysql" }},
		{"clickhouse", func(c *runtimeconfig.Config) { c.QueryEngine.Driver = "clickhouse" }},
		{"doris", func(c *runtimeconfig.Config) { c.QueryEngine.Driver = "doris" }},
		{"online mysql", func(c *runtimeconfig.Config) { c.Online.Driver = "mysql" }},
		{"trino", func(c *runtimeconfig.Config) { c.Exploration = &runtimeconfig.ExplorationConfig{Driver: "trino"} }},
		{"embedded URL", func(c *runtimeconfig.Config) { c.Storage.DatabaseURLEnv = "postgres://localhost/db" }},
		{"same credential name", func(c *runtimeconfig.Config) { c.Online.DatabaseURLEnv = c.DatabaseURLEnv() }},
		{"implicit policy", func(c *runtimeconfig.Config) { c.Online.Policies = nil }},
		{"unused route", func(c *runtimeconfig.Config) { c.Bindings = c.Online.Bindings }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			online := *base.Online
			c.Online = &online
			tc.change(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("invalid profile accepted")
			}
		})
	}
}

func TestClickHouseServingKeepsMetadataAndAnalyticalRolesSeparate(t *testing.T) {
	c, err := runtimeconfig.Load("../../examples/runtime.clickhouse.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if c.Storage.Driver != "postgres" || c.QueryEngine.Driver != "disabled" || c.Online.EngineDriver() != "clickhouse_online" {
		t.Fatal("runtime roles conflated")
	}
	c.Online.Driver = "doris"
	if err := c.Validate(); err == nil {
		t.Fatal("unimplemented result driver accepted")
	}
}
