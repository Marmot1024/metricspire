package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestContractRegistryIsValidJSONAndContainsPublicContracts(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "contracts", "metricspire.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Definitions map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("contract registry is not valid JSON: %v", err)
	}
	for _, name := range []string{
		"SemanticModel", "SemanticManifest", "SourceBinding", "PolicySource", "PolicyBundle",
		"SemanticQuery", "RequestContext", "EngineCapabilities", "LogicalPlan", "PhysicalPlan", "ExecutionJob", "Problem",
	} {
		if len(schema.Definitions[name]) == 0 {
			t.Errorf("contract registry is missing %s", name)
		}
	}
}

func TestProblemErrorIncludesStableCodeAndPath(t *testing.T) {
	t.Parallel()
	problem := &Problem{Code: "invalid", Message: "bad value", Path: "query.limit"}
	if got := problem.Error(); got != "invalid at query.limit: bad value" {
		t.Fatalf("Error() = %q", got)
	}
	problem.Path = ""
	if got := problem.Error(); got != "invalid: bad value" {
		t.Fatalf("Error() = %q", got)
	}
}
