package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
)

func TestVersionAndUnknownCommand(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := run([]string{"version"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout.String()) != "metricspire 0.1.0-dev" {
		t.Fatalf("version output = %q", stdout.String())
	}
	if err := run([]string{"unsafe-sql"}, &stdout, &stderr); err == nil {
		t.Fatal("unknown command was accepted")
	}
}

func TestCLIPhase2RejectsMissingOperationalInputsBeforeConnecting(t *testing.T) {
	t.Setenv("METRICSPIRE_DATABASE_URL", "")
	t.Setenv("METRICSPIRE_LAKEBASE_ENDPOINT", "")
	t.Setenv("DATABRICKS_HOST", "")
	t.Setenv("DATABRICKS_SQL_WAREHOUSE_ID", "")
	t.Setenv("DATABRICKS_CLIENT_ID", "")
	t.Setenv("DATABRICKS_CLIENT_SECRET", "")
	t.Setenv("DATABRICKS_TOKEN", "")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	commands := [][]string{
		{"migrate"},
		{"draft-put"},
		{"publish"},
		{"rollback"},
		{"release-list"},
		{"release-events"},
		{"query-active"},
	}
	for _, command := range commands {
		if err := run(command, &stdout, &stderr); err == nil {
			t.Fatalf("metricspire %v accepted missing inputs", command)
		}
	}
}

func TestCLIPhase2CatalogWorkflowWithPostgres(t *testing.T) {
	databaseURL := os.Getenv("METRICSPIRE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("METRICSPIRE_TEST_DATABASE_URL is not set")
	}
	t.Setenv("METRICSPIRE_DATABASE_URL", databaseURL)
	namespace := fmt.Sprintf("cli_%d", time.Now().UnixNano())
	example := filepath.Join("..", "..", "examples", "orders", "model.yaml")
	updatedPath := filepath.Join(t.TempDir(), "model.json")

	runCommand := func(arguments ...string) []byte {
		t.Helper()
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if err := run(arguments, &stdout, &stderr); err != nil {
			t.Fatalf("metricspire %v: %v\nstderr: %s", arguments, err, stderr.String())
		}
		return stdout.Bytes()
	}

	runCommand("migrate")
	firstDraft := runCommand("draft-put", "--namespace", namespace, "--source", example, "--actor", "cli-test")
	var draft catalog.Draft
	if err := json.Unmarshal(firstDraft, &draft); err != nil || draft.Revision != 1 {
		t.Fatalf("first draft = %s, %v", firstDraft, err)
	}
	firstReleaseJSON := runCommand("publish", "--namespace", namespace, "--model", "commerce", "--revision", "1", "--actor", "cli-test")
	var first catalog.Release
	if err := json.Unmarshal(firstReleaseJSON, &first); err != nil || first.ID == "" {
		t.Fatalf("first release = %s, %v", firstReleaseJSON, err)
	}

	var source model.SemanticSource
	if err := contractio.ReadFile(example, &source); err != nil {
		t.Fatal(err)
	}
	source.Metadata.Version = "1.1.0"
	if err := contractio.WriteJSON(updatedPath, source); err != nil {
		t.Fatal(err)
	}
	runCommand("draft-put", "--namespace", namespace, "--source", updatedPath, "--actor", "cli-test", "--expected-revision", "1")
	runCommand("publish", "--namespace", namespace, "--model", "commerce", "--revision", "2", "--actor", "cli-test")
	releases := runCommand("release-list", "--namespace", namespace, "--model", "commerce")
	if !bytes.Contains(releases, []byte(`"active": true`)) {
		t.Fatalf("release list = %s", releases)
	}
	runCommand("rollback", "--namespace", namespace, "--model", "commerce", "--release", first.ID, "--actor", "cli-test")
	events := runCommand("release-events", "--namespace", namespace, "--model", "commerce")
	if !bytes.Contains(events, []byte(`"kind": "rollback"`)) {
		t.Fatalf("release events = %s", events)
	}
}

func TestCLIPhase1Workflow(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	example := filepath.Join("..", "..", "examples", "orders")
	manifestPath := filepath.Join(directory, "manifest.json")
	policyPath := filepath.Join(directory, "policy.json")
	logicalPath := filepath.Join(directory, "logical.json")
	physicalPath := filepath.Join(directory, "physical.json")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	commands := [][]string{
		{"validate", "--source", filepath.Join(example, "model.yaml")},
		{"compile", "--source", filepath.Join(example, "model.yaml"), "--out", manifestPath},
		{"compile-policy", "--source", filepath.Join(example, "policy.yaml"), "--manifest", manifestPath, "--out", policyPath},
		{
			"plan", "--manifest", manifestPath, "--policy", policyPath,
			"--context", filepath.Join(example, "context.json"), "--query", filepath.Join(example, "query.json"),
			"--binding", filepath.Join(example, "binding.json"), "--capabilities", filepath.Join(example, "capabilities.json"),
			"--logical-out", logicalPath, "--physical-out", physicalPath,
		},
	}
	for _, command := range commands {
		if err := run(command, &stdout, &stderr); err != nil {
			t.Fatalf("metricspire %v: %v\nstderr: %s", command, err, stderr.String())
		}
	}
	var logical model.LogicalPlan
	var physical model.PhysicalPlan
	if err := contractio.ReadFile(logicalPath, &logical); err != nil {
		t.Fatal(err)
	}
	if err := contractio.ReadFile(physicalPath, &physical); err != nil {
		t.Fatal(err)
	}
	if logical.Fingerprint == "" || physical.LogicalFingerprint != logical.Fingerprint {
		t.Fatalf("CLI outputs are not linked: %#v %#v", logical, physical)
	}
}
