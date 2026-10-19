package runtimeconfig

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/exploration"
)

// Exploration is opt-in and has its own warehouse and execution capacity.
// Host/authentication are shared with the authenticated Databricks deployment.
type ExplorationConfig struct {
	Driver           string                `json:"driver,omitempty" yaml:"driver,omitempty"`
	ReviewedCatalogs []exploration.Catalog `json:"-" yaml:"-"`
	CatalogPaths     []string              `json:"catalog_paths" yaml:"catalog_paths"`
	WarehouseID      string                `json:"warehouse_id" yaml:"warehouse_id"`
	Concurrency      int                   `json:"concurrency" yaml:"concurrency"`
	Capacity         int                   `json:"capacity" yaml:"capacity"`
	ResultTTL        string                `json:"result_ttl" yaml:"result_ttl"`
}

func (c ExplorationConfig) Validate() error {
	if c.Driver != "" && c.Driver != "databricks_sql" {
		return errors.New("exploration.driver is not implemented; supported: databricks_sql")
	}
	if len(c.CatalogPaths)+len(c.ReviewedCatalogs) == 0 || len(c.CatalogPaths)+len(c.ReviewedCatalogs) > 32 || !regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`).MatchString(c.WarehouseID) || c.Concurrency < 1 || c.Concurrency > 32 || c.Capacity < c.Concurrency || c.Capacity > 1000 {
		return errors.New("exploration requires catalog_paths, warehouse_id, concurrency (1-32), and capacity (up to 1000)")
	}
	seen := map[string]bool{}
	for _, p := range c.CatalogPaths {
		if strings.TrimSpace(p) == "" || seen[p] {
			return errors.New("exploration catalog paths must be nonempty and unique")
		}
		seen[p] = true
	}
	ttl, err := ParseDuration(c.ResultTTL, 0)
	if err != nil || ttl <= 0 || ttl > 24*time.Hour {
		return errors.New("exploration.result_ttl must be positive and at most 24h")
	}
	return nil
}
