package application_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/adapter/databricks"
	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
)

func TestPublishedReleaseToGovernedDatabricksResultAndRollback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := catalog.NewMemoryRepository()
	catalogService, _ := catalog.NewService(repository)
	root := filepath.Join("..", "..", "examples", "orders")
	var source model.SemanticSource
	read(t, filepath.Join(root, "model.yaml"), &source)
	draft, err := catalogService.SaveDraft(ctx, catalog.SaveDraftInput{
		Namespace: "demo", Source: source, Actor: "owner", ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	release1, err := catalogService.Publish(ctx, "demo", source.Metadata.Name, draft.Revision, "owner", "initial")
	if err != nil {
		t.Fatal(err)
	}

	var submitted struct {
		Statement string `json:"statement"`
		RowLimit  int64  `json:"row_limit"`
		ByteLimit int64  `json:"byte_limit"`
		Warehouse string `json:"warehouse_id"`
	}
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(request.Body).Decode(&submitted); err != nil {
			t.Error(err)
		}
		day, segment, average, rate := "2026-09-01T00:00:00.000Z", "enterprise", "42.50", "0.05"
		response := map[string]any{
			"statement_id": "statement-e2e",
			"status":       map[string]any{"state": "SUCCEEDED"},
			"manifest": map[string]any{
				"truncated": false,
				"schema": map[string]any{"columns": []map[string]any{
					{"name": "order_date", "type_name": "TIMESTAMP", "type_text": "TIMESTAMP"},
					{"name": "customer_segment", "type_name": "STRING", "type_text": "STRING"},
					{"name": "average_order_value", "type_name": "DECIMAL", "type_text": "DECIMAL(38,18)"},
					{"name": "refund_rate", "type_name": "DECIMAL", "type_text": "DECIMAL(38,18)"},
				}},
			},
			"result": map[string]any{"data_array": [][]*string{{&day, &segment, &average, &rate}}},
		}
		return jsonResponse(response), nil
	}), Timeout: time.Second}
	client, err := databricks.NewClient(databricks.ClientConfig{
		Host: "https://workspace.test", WarehouseID: "warehouse", TokenSource: databricks.StaticTokenSource("token"),
		HTTPClient: httpClient, ByteLimit: 4096, PollInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, _ := databricks.NewQueryEngine(client)
	queryService, _ := application.NewQueryService(repository, engine)

	var policy model.PolicySource
	var contextValue model.RequestContext
	var query model.SemanticQuery
	var binding model.SourceBinding
	read(t, filepath.Join(root, "policy.yaml"), &policy)
	read(t, filepath.Join(root, "context.json"), &contextValue)
	read(t, filepath.Join(root, "query.json"), &query)
	read(t, filepath.Join(root, "binding.json"), &binding)
	policy.ManifestFingerprint = ""
	binding.ManifestFingerprint = ""
	binding.Engine = databricks.EngineName

	output, err := queryService.ExecuteActive(ctx, application.QueryInput{
		Namespace: "demo", ModelName: source.Metadata.Name, Context: contextValue,
		Query: query, Policy: policy, Binding: binding,
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Release.ID != release1.ID || output.Execution.Job.Status != model.JobSucceeded || len(output.Execution.Result.Rows) != 1 {
		t.Fatalf("query output = %#v", output)
	}
	if submitted.Warehouse != "warehouse" || submitted.RowLimit != int64(query.Limit) || submitted.ByteLimit != 4096 {
		t.Fatalf("submitted request = %#v", submitted)
	}
	if !strings.Contains(submitted.Statement, "FROM `demo`.`public`.`orders`") || strings.Contains(submitted.Statement, ";") {
		t.Fatalf("submitted SQL = %s", submitted.Statement)
	}

	source.Metadata.Version = "1.1.0"
	draft, err = catalogService.SaveDraft(ctx, catalog.SaveDraftInput{
		Namespace: "demo", Source: source, Actor: "owner", ExpectedRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	release2, err := catalogService.Publish(ctx, "demo", source.Metadata.Name, draft.Revision, "owner", "second")
	if err != nil {
		t.Fatal(err)
	}
	if release2.ID == release1.ID {
		t.Fatal("second release reused the first release ID")
	}
	if _, err := catalogService.Rollback(ctx, "demo", source.Metadata.Name, release1.ID, "owner", "restore"); err != nil {
		t.Fatal(err)
	}
	afterRollback, err := queryService.ExecuteActive(ctx, application.QueryInput{
		Namespace: "demo", ModelName: source.Metadata.Name, Context: contextValue,
		Query: query, Policy: policy, Binding: binding,
	})
	if err != nil || afterRollback.Release.ID != release1.ID {
		t.Fatalf("query after rollback = %#v, %v", afterRollback, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func jsonResponse(value any) *http.Response {
	data, _ := json.Marshal(value)
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data)))}
}

func read(t *testing.T, path string, target any) {
	t.Helper()
	if err := contractio.ReadFile(path, target); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
}
