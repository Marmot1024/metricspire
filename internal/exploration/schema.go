package exploration

import (
	_ "embed"
	"encoding/json"
)

//go:embed query.schema.json
var querySchema []byte

// MCPInputSchema uses an explicit recursive expression schema because the MCP
// SDK cannot infer recursive Go types. Application validation remains decisive.
func MCPInputSchema() map[string]any {
	var query map[string]any
	if err := json.Unmarshal(querySchema, &query); err != nil {
		panic("invalid embedded analysis schema")
	}
	defs := query["$defs"].(map[string]any)
	delete(query, "$defs")
	defs["query"] = query
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"namespace", "query"}, "properties": map[string]any{"namespace": map[string]any{"type": "string"}, "query": map[string]any{"$ref": "#/$defs/query"}}, "$defs": defs}
}
