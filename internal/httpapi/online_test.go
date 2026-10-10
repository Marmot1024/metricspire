package httpapi_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/marmot1024/metricspire/internal/httpapi"
)

func TestOnlineEndpointIsDisabledByDefaultAndRequiresQueryPermission(t *testing.T) {
	handler, engine, _ := newTestServer(t, httpapi.Config{})
	for _, test := range []struct {
		token  string
		status int
		code   string
	}{{"", 401, "unauthenticated"}, {"manage", 403, "permission_denied"}, {"query", 404, "online_not_enabled"}} {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/namespaces/demo/online-query", bytes.NewBufferString(`{"metric_codes":["10001"]}`))
		request.Header.Set("Authorization", "Bearer "+test.token)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assertProblem(t, response.Result(), test.status, test.code)
	}
	if engine.executionCount() != 0 {
		t.Fatal("disabled endpoint executed the analysis engine")
	}
}
