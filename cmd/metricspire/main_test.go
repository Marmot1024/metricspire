package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

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
	if strings.TrimSpace(stdout.String()) != "metricspire "+version {
		t.Fatalf("version output = %q", stdout.String())
	}
	if err := run([]string{"unsafe-sql"}, &stdout, &stderr); err == nil {
		t.Fatal("unknown command was accepted")
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
