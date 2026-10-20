package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestOnlineExportCLIHasImmutablePrivateArtifact(t *testing.T) {
	output := filepath.Join(t.TempDir(), "batch.ndjson")
	args := []string{"--source", "../../examples/online/model.yaml", "--binding", "../../examples/online/binding.yaml", "--input", "../../examples/online/producer/rows.ndjson", "--reconcile", "../../examples/online/producer/reconcile.json", "--output", output, "--tenant", "demo", "--batch-id", "source-1", "--data-as-of", "2026-09-03T00:00:00Z"}
	var out bytes.Buffer
	if err := runOnlineRefresh("export-online", args, &out, &out); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("artifact is not private")
	}
	if err = runOnlineRefresh("export-online", args, &out, &out); err == nil {
		t.Fatal("immutable artifact replaced")
	}
	after, _ := os.ReadFile(output)
	if !bytes.Equal(before, after) {
		t.Fatal("existing artifact changed")
	}
	entries, err := os.ReadDir(filepath.Dir(output))
	if err != nil || len(entries) != 1 {
		t.Fatal("partial artifact leaked")
	}
}
