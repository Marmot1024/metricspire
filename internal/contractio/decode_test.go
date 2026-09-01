package contractio

import (
	"strings"
	"testing"

	"github.com/marmot1024/metricspire/internal/model"
)

func TestDecodeJSONRejectsDuplicateAndUnknownFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data string
		want string
	}{
		{"duplicate", `{"tenant":"a","tenant":"b","principal":"p","request_id":"r"}`, "duplicate JSON key"},
		{"unknown", `{"tenant":"a","principal":"p","request_id":"r","context":{"admin":true}}`, "unknown field"},
		{"multiple", `{} {}`, "multiple JSON values"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var context model.RequestContext
			err := DecodeJSON([]byte(test.data), &context)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DecodeJSON() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestDecodeYAMLRejectsDuplicateUnknownAndMultipleDocuments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data string
		want string
	}{
		{"duplicate", "tenant: a\ntenant: b\nprincipal: p\nrequest_id: r\n", "already defined"},
		{"unknown", "tenant: a\nprincipal: p\nrequest_id: r\nadmin: true\n", "field admin not found"},
		{"multiple", "tenant: a\nprincipal: p\nrequest_id: r\n---\ntenant: b\n", "multiple YAML documents"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var context model.RequestContext
			err := DecodeYAML([]byte(test.data), &context)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DecodeYAML() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func FuzzDecodeJSON(f *testing.F) {
	f.Add([]byte(`{"tenant":"demo","principal":"p","request_id":"r"}`))
	f.Add([]byte(`{"tenant":"a","tenant":"b"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var context model.RequestContext
		_ = DecodeJSON(data, &context)
	})
}
