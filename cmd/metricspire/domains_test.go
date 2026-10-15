package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marmot1024/metricspire/internal/adapter/databricks"
	"github.com/marmot1024/metricspire/internal/audit"
	"github.com/marmot1024/metricspire/internal/exploration"
	"github.com/marmot1024/metricspire/internal/runtimeconfig"
)

func TestDomainCLIAndVerifiedArtifactsReachExplorationStartup(t *testing.T) {
	data, err := os.ReadFile("../../testdata/exploration/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalog exploration.Catalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	dir := t.TempDir()
	artifact := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(artifact, data, 0600); err != nil {
		t.Fatal(err)
	}
	r := runtimeconfig.DomainRegistry{APIVersion: runtimeconfig.APIVersion, Kind: "DomainRegistry", Domains: []runtimeconfig.BusinessDomain{{Tenant: catalog.Tenant, Namespace: catalog.Namespace, DisplayName: "Demo", SemanticSource: &runtimeconfig.SemanticSourceRef{Repository: "https://github.com/example/semantics", Revision: strings.Repeat("a", 40), Catalogs: []runtimeconfig.CatalogArtifact{{Path: "catalog.json", SHA256: hex.EncodeToString(sum[:])}}}}}}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "domains.json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if err := run([]string{"validate-domains", "--config", path}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "valid domain registry: 1") || strings.Contains(out.String(), "github.com") {
		t.Fatal("invalid or excessive validation output")
	}
	loaded, err := runtimeconfig.LoadDomains(path)
	if err != nil {
		t.Fatal(err)
	}
	// Mutating the file after validation must not change startup's decoded bytes.
	if err := os.WriteFile(artifact, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	c := &runtimeconfig.ExplorationConfig{ReviewedCatalogs: loaded.Catalogs, WarehouseID: "fixture", Concurrency: 1, Capacity: 2, ResultTTL: "1h"}
	s, j, routes, err := buildServeExploration(dir, c, databricks.ClientConfig{Host: "https://workspace.test", WarehouseID: "fixture", TokenSource: databricks.StaticTokenSource("fixture-token")}, audit.NewMemoryRecorder())
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if s == nil || routes == nil {
		t.Fatal("verified catalog not wired to exploration")
	}
}
