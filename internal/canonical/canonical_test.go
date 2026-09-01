package canonical

import (
	"bytes"
	"testing"
)

func TestFingerprintIsIndependentOfMapInsertionOrder(t *testing.T) {
	t.Parallel()
	first := map[string]any{"z": 1, "a": []string{"x", "y"}}
	second := make(map[string]any)
	second["a"] = []string{"x", "y"}
	second["z"] = 1
	left, err := Fingerprint(first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := Fingerprint(second)
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("fingerprints differ: %s %s", left, right)
	}
	pretty, err := MarshalIndent(first)
	if err != nil || !bytes.Contains(pretty, []byte("\n")) {
		t.Fatalf("MarshalIndent() = %q, %v", pretty, err)
	}
}
