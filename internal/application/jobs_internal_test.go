package application

import (
	"testing"

	"github.com/marmot1024/metricspire/internal/model"
)

func TestPublicJobProblemPreservesSanitizedEngineHTTPStatus(t *testing.T) {
	t.Parallel()
	problem := publicJobProblem(model.Problem{
		Code: "engine_http_error", Message: "Databricks returned HTTP 403 (PERMISSION_DENIED)",
	})
	if problem.Code != "engine_http_error" || problem.Message != "Databricks returned HTTP 403 (PERMISSION_DENIED)" {
		t.Fatalf("public problem = %#v", problem)
	}
	generic := publicJobProblem(model.Problem{Code: "engine_failed", Message: "unsafe upstream detail"})
	if generic.Message != "analytical query execution failed" {
		t.Fatalf("generic engine problem = %#v", generic)
	}
}
