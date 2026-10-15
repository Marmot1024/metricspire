package runtimeconfig_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/runtimeconfig"
)

func domainRegistry() runtimeconfig.DomainRegistry {
	return runtimeconfig.DomainRegistry{APIVersion: runtimeconfig.APIVersion, Kind: "DomainRegistry", Domains: []runtimeconfig.BusinessDomain{
		{Tenant: "demo", Namespace: "commerce", DisplayName: "Demo Commerce", Aliases: []string{"Orders"}},
	}}
}

func TestDomainAliasesCannotAmbiguouslySelectAnotherBusiness(t *testing.T) {
	r := domainRegistry()
	r.Domains = append(r.Domains, runtimeconfig.BusinessDomain{Tenant: "demo", Namespace: "other", DisplayName: "Other", Aliases: []string{" ORDERS "}})
	if r.Validate() == nil {
		t.Fatal("ambiguous alias accepted")
	}
	r.Domains[1].Tenant = "another"
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	r.Domains[0].SemanticSource = &runtimeconfig.SemanticSourceRef{Repository: "https://token@github.com/example/semantics", Revision: strings.Repeat("a", 40)}
	if r.Validate() == nil {
		t.Fatal("credential-bearing source accepted")
	}
	r.Domains[0].SemanticSource.Repository = "https://github.com/example/semantics"
	r.Domains[0].SemanticSource.Revision = "main"
	if r.Validate() == nil {
		t.Fatal("mutable reference accepted")
	}
}

func TestDomainArtifactsVerifyBytesAndBusinessScope(t *testing.T) {
	data, err := os.ReadFile("../../testdata/acceptance/exploration-calendar/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var scope struct {
		Tenant    string
		Namespace string
	}
	if err := json.Unmarshal(data, &scope); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	artifact := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(artifact, data, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	r := domainRegistry()
	r.Domains[0].Tenant, r.Domains[0].Namespace = scope.Tenant, scope.Namespace
	r.Domains[0].SemanticSource = &runtimeconfig.SemanticSourceRef{Repository: "https://github.com/example/semantics", Revision: strings.Repeat("a", 40), Catalogs: []runtimeconfig.CatalogArtifact{{Path: "catalog.json", SHA256: hex.EncodeToString(sum[:])}}}
	path := filepath.Join(dir, "domains.json")
	write := func() {
		t.Helper()
		b, _ := json.Marshal(r)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	loaded, err := runtimeconfig.LoadDomains(path)
	if err != nil || len(loaded.Catalogs) != 1 {
		t.Fatalf("catalog not loaded: %v", err)
	}
	// Runtime resolves registry/artifacts relative to their own containing file,
	// and retains the verified catalog instead of re-reading mutable paths.
	var config runtimeconfig.Config
	if err := contractio.ReadFile("../../examples/runtime.example.yaml", &config); err != nil {
		t.Fatal(err)
	}
	config.DomainsPath = "domains.json"
	config.Exploration = &runtimeconfig.ExplorationConfig{WarehouseID: "fixture", Concurrency: 1, Capacity: 2, ResultTTL: "1h"}
	b, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	runtimePath := filepath.Join(dir, "runtime.json")
	if err := os.WriteFile(runtimePath, b, 0600); err != nil {
		t.Fatal(err)
	}
	resolved, err := runtimeconfig.Load(runtimePath)
	if err != nil || len(resolved.Domains) != 1 || len(resolved.Exploration.ReviewedCatalogs) != 1 {
		t.Fatalf("runtime did not load pinned artifact: %v", err)
	}
	r.Domains[0].Tenant = "wrong"
	write()
	if _, err := runtimeconfig.LoadDomains(path); err == nil {
		t.Fatal("cross-tenant artifact accepted")
	}
	r.Domains[0].Tenant = scope.Tenant
	write()
	if err := os.WriteFile(artifact, append(data, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeconfig.LoadDomains(path); err == nil {
		t.Fatal("changed artifact accepted")
	}
}

func TestDomainRegistryRejectsAmbiguousOrOversizedInput(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{
		"duplicate.json": `{"api_version":"metricspire.io/v1alpha1","kind":"DomainRegistry","kind":"Other","domains":[]}`,
		"unknown.yaml":   "api_version: metricspire.io/v1alpha1\nkind: DomainRegistry\nunknown: secret\n",
		"oversized.json": strings.Repeat(" ", (1<<20)+1),
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := runtimeconfig.LoadDomains(path); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
	r := domainRegistry()
	r.Domains[0].SemanticSource = &runtimeconfig.SemanticSourceRef{Repository: "https://github.com/example/semantics", Revision: strings.Repeat("a", 40), Catalogs: []runtimeconfig.CatalogArtifact{{Path: "a.json", SHA256: strings.Repeat("a", 64)}, {Path: "./a.json", SHA256: strings.Repeat("b", 64)}}}
	if r.Validate() == nil {
		t.Fatal("duplicate artifact path accepted")
	}
}
