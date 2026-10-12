package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/adapter/databricks"
	"github.com/marmot1024/metricspire/internal/audit"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/exploration"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/runtimeconfig"
	"go.yaml.in/yaml/v3"
)

func TestAnalysisConfigurationAndOfflineValidation(t *testing.T) {
	var c exploration.Catalog
	if err := contractio.ReadFile(filepath.Join("..", "..", "testdata", "exploration", "catalog.json"), &c); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	yamlData, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "analysis.yaml"), yamlData, 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if err = runValidateAnalysis([]string{"--catalog", filepath.Join(dir, "analysis.yaml")}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Valid       bool   `json:"valid"`
		Fingerprint string `json:"fingerprint"`
	}
	if json.Unmarshal(out.Bytes(), &result) != nil || !result.Valid || result.Fingerprint == "" {
		t.Fatal("invalid validation receipt")
	}
	config := &runtimeconfig.ExplorationConfig{CatalogPaths: []string{"analysis.yaml"}, WarehouseID: "test-exploration", Concurrency: 2, Capacity: 10, ResultTTL: "10m"}
	clientConfig := databricks.ClientConfig{Host: "https://workspace.test", WarehouseID: "other-warehouse", TokenSource: databricks.StaticTokenSource("fixture-token")}
	service, jobs, routes, err := buildServeExploration(dir, config, clientConfig, audit.NewMemoryRecorder())
	if err != nil {
		t.Fatal(err)
	}
	defer jobs.Close()
	if service == nil || routes == nil || clientConfig.WarehouseID != "other-warehouse" {
		t.Fatal("startup overwrote metric engine settings")
	}
	views, err := routes.Discover(context.Background(), "demo", model.RequestContext{Tenant: "demo", Principal: "analyst", Roles: []string{"analyst"}, RequestID: "offline"})
	if err != nil || len(views) != 1 || views[0].Timeout != time.Minute.String() {
		t.Fatal("YAML catalog discovery failed")
	}
	if _, _, _, err = buildServeExploration(dir, &runtimeconfig.ExplorationConfig{}, clientConfig, audit.NewMemoryRecorder()); err == nil {
		t.Fatal("empty exploration configuration accepted")
	}
	s, j, r, err := buildServeExploration(dir, nil, clientConfig, nil)
	if err != nil || s != nil || j != nil || r != nil {
		t.Fatal("disabled exploration must stay disabled")
	}
}
