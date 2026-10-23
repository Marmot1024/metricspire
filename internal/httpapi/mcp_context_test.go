package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/marmot1024/metricspire/internal/exploration"
	"github.com/marmot1024/metricspire/internal/httpapi"
	"github.com/marmot1024/metricspire/internal/model"
)

// Measure what a fresh MCP client actually receives, including the worst case
// of a host repeating initialize instructions on every tool description.
func TestMCPContextBudget(t *testing.T) {
	for _, online := range []bool{false, true} {
		t.Run(fmt.Sprintf("online=%t", online), func(t *testing.T) {
			checkMCPContextBudget(t, online)
		})
	}
}

func checkMCPContextBudget(t *testing.T, online bool) {
	t.Helper()
	h, _ := newExplorationTransport(t, explorationTransportFixture(t), httpAnalysisEngineFunc(func(context.Context, exploration.Plan) (model.ExecutionSnapshot, error) {
		t.Fatal("metadata discovery executed engine")
		return model.ExecutionSnapshot{}, nil
	}), nil, func(d *httpapi.Dependencies) {
		if online {
			d.Online = &mcpOnlineService{}
			d.OnlineCatalogSearch = d.CatalogSearch
		}
	})
	s := connectRemoteMCP(t, h, "query")
	instructions := s.InitializeResult().Instructions
	if utf8.RuneCountInString(instructions) > 550 {
		t.Fatal("shared instructions exceed context budget")
	}
	tools, err := s.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	characters := 0
	for _, tool := range tools.Tools {
		if strings.Contains(tool.Description, instructions) {
			t.Fatal("instructions duplicated in server tool metadata")
		}
		characters += utf8.RuneCountInString(tool.Description) + utf8.RuneCountInString(instructions)
	}
	expected := 16
	if online {
		expected++
	}
	if len(tools.Tools) != expected {
		t.Fatalf("expected full %d-tool surface, got %d", expected, len(tools.Tools))
	}
	if characters > 12000 {
		t.Fatalf("%d-tool repeated-prefix text budget exceeded: %d", expected, characters)
	}
	encoded, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 40000 {
		t.Fatalf("schema + description budget exceeded: %d", len(encoded))
	}
	t.Logf("instructions_chars=%d tools=%d repeated_prefix_plus_descriptions_chars=%d tools_json_bytes=%d", utf8.RuneCountInString(instructions), len(tools.Tools), characters, len(encoded))
}
