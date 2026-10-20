package main

import (
	"os"
	"path/filepath"

	"github.com/marmot1024/metricspire/internal/adapter/databricks"
	"github.com/marmot1024/metricspire/internal/adapter/trinoquery"
	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/audit"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/exploration"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/runtimeconfig"
)

func buildServeExploration(base string, c *runtimeconfig.ExplorationConfig, clientConfig databricks.ClientConfig, recorder audit.Recorder) (*application.AnalysisService, *application.AnalysisJobs, *application.ConfiguredAnalysisCatalogs, error) {
	if c == nil {
		return nil, nil, nil, nil
	}
	if err := c.Validate(); err != nil {
		return nil, nil, nil, err
	}
	catalogs := append([]exploration.Catalog(nil), c.ReviewedCatalogs...)
	for _, p := range c.CatalogPaths {
		if !filepath.IsAbs(p) {
			p = filepath.Join(base, p)
		}
		var catalog exploration.Catalog
		if err := contractio.ReadFile(p, &catalog); err != nil {
			return nil, nil, nil, err
		}
		catalogs = append(catalogs, catalog)
	}
	for _, catalog := range catalogs {
		if catalog.EngineName() != c.EngineDriver() {
			return nil, nil, nil, &model.Problem{Code: "engine_mismatch", Message: "analysis catalog engine differs from deployment driver"}
		}
		if c.EngineDriver() != "databricks_sql" {
			if err := trinoquery.ValidateCatalog(catalog, c.EngineDriver()); err != nil {
				return nil, nil, nil, err
			}
		}
	}
	routes, err := application.NewConfiguredAnalysisCatalogs(catalogs)
	if err != nil {
		return nil, nil, nil, err
	}
	var engine application.AnalysisEngine
	if c.EngineDriver() == "databricks_sql" {
		clientConfig.WarehouseID = c.WarehouseID
		client, e := databricks.NewClient(clientConfig)
		if e != nil {
			return nil, nil, nil, e
		}
		engine, err = databricks.NewAnalysisEngine(client)
	} else {
		engine, err = trinoquery.New(trinoquery.Config{Driver: c.EngineDriver(), Connection: os.Getenv(c.ConnectionEnv), User: c.User})
	}
	if err != nil {
		return nil, nil, nil, err
	}
	service, err := application.NewAnalysisService(nil, routes, engine, recorder)
	if err != nil {
		return nil, nil, nil, err
	}
	ttl, err := runtimeconfig.ParseDuration(c.ResultTTL, 0)
	if err != nil {
		return nil, nil, nil, err
	}
	jobs, err := application.NewAnalysisJobs(service, c.Concurrency, c.Capacity, ttl)
	if err != nil {
		return nil, nil, nil, err
	}
	return service, jobs, routes, nil
}
