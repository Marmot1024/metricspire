package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marmot1024/metricspire/internal/model"
)

func TestMCPDomainDetailDoesNotExposeBindingReferences(t *testing.T) {
	backend := &remoteMCPBackend{request: httptest.NewRequest(http.MethodPost, "/api/v1/mcp", nil)}
	for _, test := range []struct {
		code, path string
		public     bool
	}{
		{"unknown_reference", "dimensions", true},
		{"unknown_reference", "metrics[0]", true},
		{"unknown_reference", "filters[1].dimension", true},
		{"unknown_reference", "order_by[0].field", true},
		{"unknown_reference", "binding.datasets[0].name", false},
		{"unknown_reference", "expression.field", false},
		{"binding_missing", "binding.datasets", false},
		{"capability_missing", "metrics", true},
		{"capability_missing", "binding.datasets", false},
		{"metric_route_ambiguous", "metrics", true},
	} {
		t.Run(test.code+"/"+test.path, func(t *testing.T) {
			problem := &model.Problem{Code: test.code, Path: test.path, Message: "reference-detail-marker"}
			output := backend.safeError(problem).Error()
			if got := strings.Contains(output, "reference-detail-marker"); got != test.public {
				t.Fatalf("detail visibility = %v, want %v", got, test.public)
			}
		})
	}
}
