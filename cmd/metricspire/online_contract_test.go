package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestOnlineContractOfflineEngineSeparation(t *testing.T) {
	var pgHash string
	for _, binding := range []string{"binding.yaml", "clickhouse-binding.yaml"} {
		var output bytes.Buffer
		err := runOnlineContract([]string{"--source", "../../examples/online/model.yaml", "--binding", "../../examples/online/" + binding}, &output, &output)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]string
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(result["manifest_fingerprint"], "sha256:") {
			t.Fatal("missing manifest")
		}
		if binding == "binding.yaml" {
			pgHash = result["data_contract"]
			if pgHash != "sha256:5948e71014cc14c70371ccf64c25ba9b7c352168eee2c0afc337f41b8818e47d" {
				t.Fatal("PostgreSQL producer contract changed")
			}
		} else if result["engine"] != "clickhouse_online" || result["data_contract"] == pgHash {
			t.Fatal("engine data contracts conflated")
		}
	}
	var out bytes.Buffer
	if err := runOnlineContract(nil, &out, &out); err == nil {
		t.Fatal("missing reviewed definitions accepted")
	}
}
