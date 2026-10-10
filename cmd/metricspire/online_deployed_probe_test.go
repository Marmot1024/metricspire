package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/model"
)

func TestOnlineDeployedProbeRejectsIncompleteSuccessAndFalseDenial(t *testing.T) {
	valid := application.OnlineResult{ReleaseID: "release", ManifestFingerprint: "fingerprint", Snapshot: &model.DataSnapshot{BatchID: "batch", DataAsOf: time.Now()}, Result: &model.TypedResult{Rows: [][]any{{"100"}}}}
	for _, test := range []struct {
		name, mode string
		status     int
		body       any
		allowed    bool
	}{
		{"complete", "allowed", 200, valid, true},
		{"missing snapshot", "allowed", 200, application.OnlineResult{Result: valid.Result}, false},
		{"permission", "denied", 403, map[string]string{"code": "permission_denied"}, true},
		{"ingress rejection", "denied", 403, map[string]string{"error_code": "FORBIDDEN"}, false},
		{"invalid user", "denied", 401, nil, false},
		{"unexpected success", "denied", 200, valid, false},
		{"disabled route", "allowed", 404, nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer synthetic-probe-token" || r.Method != http.MethodPost || r.URL.Path != "/api/v1/namespaces/demo/online-query" {
					t.Error("probe request differs from reviewed endpoint")
				}
				w.WriteHeader(test.status)
				_ = json.NewEncoder(w).Encode(test.body)
			}))
			defer server.Close()
			_, err := onlineDeployedRead(t.Context(), server.Client(), server.URL, "demo", "synthetic-probe-token", application.OnlineQuery{MetricCodes: []string{"10001"}}, test.mode)
			if (err == nil) != test.allowed {
				t.Fatalf("probe result: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "synthetic-probe-token") {
				t.Fatal("probe error exposed a token")
			}
		})
	}
}
