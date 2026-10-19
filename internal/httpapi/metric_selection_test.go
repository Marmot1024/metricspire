package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/httpapi"
	"github.com/marmot1024/metricspire/internal/mcpbridge"
	"github.com/marmot1024/metricspire/internal/model"
)

func TestMCPStructuredChoiceCannotBeOmittedOrReusedAfterDefinitionChange(t *testing.T) {
	var deps httpapi.Dependencies
	source := readSource(t)
	for i := range source.Spec.Metrics {
		source.Spec.Metrics[i].Definition = &model.MetricDefinition{Family: "orders", Variant: source.Spec.Metrics[i].Name, Population: "All synthetic orders", Exclusions: []string{}, Numerator: "Reviewed aggregate", Denominator: "As specified in the reviewed formula", CountingUnit: "Order", TimeBasis: "Order creation time"}
	}
	publish := func(expected int64) {
		t.Helper()
		draft, err := deps.Management.SaveDraft(t.Context(), catalog.SaveDraftInput{Namespace: "demo_commerce", Source: source, Actor: "fixture", ExpectedRevision: expected})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := deps.Management.Publish(t.Context(), "demo_commerce", "commerce", draft.Revision, "fixture", "synthetic definition"); err != nil {
			t.Fatal(err)
		}
	}
	h, engine, _ := newTestServerWithDependencies(t, httpapi.Config{}, nil, nil, nil, nil, func(d *httpapi.Dependencies) { deps = *d })
	publish(0)
	session := connectRemoteMCP(t, h, "query")
	query := readQuery(t)
	input := mcpbridge.QueryInput{Namespace: "demo_commerce", Query: query}
	for _, tool := range []string{"prepare_query", "plan_query", "explain_query"} {
		text := callRemoteMCP(t, session, tool, input, true)
		if !strings.Contains(text, "metric_selection_required") {
			t.Fatal(text)
		}
	}
	// Legacy submissions return a job first; planning must fail before engine execution.
	checkFailedJob := func(job application.QueryJobSnapshot) {
		t.Helper()
		finished := awaitJob(t, h, job.Job.ID, "query")
		encoded, _ := json.Marshal(finished)
		if finished.Job.Status != model.JobFailed || !strings.Contains(string(encoded), "metric_selection_required") {
			t.Fatalf("unexpected job outcome: %s", encoded)
		}
	}
	submitted := callRemoteMCP(t, session, "submit_query", input, false)
	var job application.QueryJobSnapshot
	if err := json.Unmarshal([]byte(submitted), &job); err != nil {
		t.Fatal(err)
	}
	checkFailedJob(job)
	response := performJSON(h, http.MethodPost, httpapi.APIPrefix+"/namespaces/demo_commerce/models/commerce/query", "query", query)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("job submission: %s", readBody(t, response))
	}
	decodeResponse(t, response, &job)
	checkFailedJob(job)
	if engine.executionCount() != 0 {
		t.Fatal("missing selection reached the engine")
	}
	text := callRemoteMCP(t, session, "get_metric_details", mcpbridge.MetricDetailsInput{Namespace: input.Namespace, Metrics: query.Metrics}, false)
	var details struct {
		Metrics []mcpbridge.MetricDetail `json:"metrics"`
	}
	if err := json.Unmarshal([]byte(text), &details); err != nil {
		t.Fatal(err)
	}
	input.Query.MetricSelections = map[string]string{}
	for _, m := range details.Metrics {
		if m.Definition == nil || m.Definition.Population == "" {
			t.Fatal("missing structured definition")
		}
		input.Query.MetricSelections[m.Name] = m.ManifestFingerprint
	}
	text = callRemoteMCP(t, session, "prepare_query", input, false)
	var prepared mcpbridge.PreparedQuery
	if err := json.Unmarshal([]byte(text), &prepared); err != nil {
		t.Fatal(err)
	}
	if len(prepared.Query.MetricSelections) != len(query.Metrics) {
		t.Fatal("preparation dropped choices")
	}
	text = callRemoteMCP(t, session, "run_query", fastInput(prepared, 1), false)
	if !strings.Contains(text, `"succeeded"`) || engine.executionCount() != 1 {
		t.Fatal(text)
	}
	source.Spec.Metrics[0].Description += " Additional reviewed explanation."
	publish(1)
	text = callRemoteMCP(t, session, "run_query", fastInput(prepared, 1), true)
	if !strings.Contains(text, "metric_selection_changed") || engine.executionCount() != 1 {
		t.Fatal("old choice executed:", text)
	}
}
