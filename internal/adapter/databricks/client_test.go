package databricks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/model"
)

func TestClientExecutesAsyncStatementAndDecodesTypedChunks(t *testing.T) {
	t.Parallel()
	var statusCalls atomic.Int64
	httpClient := testHTTPClient(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/api/2.0/sql/statements":
			var body executeRequest
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.WaitTimeout != "0s" || body.RowLimit != 10 || body.ByteLimit != 1024 || body.WarehouseID != "warehouse" {
				t.Errorf("execute request = %#v", body)
			}
			return jsonResponse(http.StatusOK, statementResponse{StatementID: "statement-1", Status: statementStatus{State: "PENDING"}}), nil
		case request.Method == http.MethodGet && request.URL.Path == "/api/2.0/sql/statements/statement-1":
			if statusCalls.Add(1) == 1 {
				return jsonResponse(http.StatusOK, statementResponse{StatementID: "statement-1", Status: statementStatus{State: "RUNNING"}}), nil
			}
			one, amount, yes, label := "1", "12.3400", "true", "alpha"
			return jsonResponse(http.StatusOK, statementResponse{
				StatementID: "statement-1", Status: statementStatus{State: "SUCCEEDED"},
				Manifest: resultManifest{Schema: resultSchema{Columns: []resultColumn{
					{Name: "id", TypeName: "LONG", TypeText: "BIGINT"},
					{Name: "amount", TypeName: "DECIMAL", TypeText: "DECIMAL(18,4)"},
					{Name: "enabled", TypeName: "BOOLEAN", TypeText: "BOOLEAN"},
					{Name: "label", TypeName: "STRING", TypeText: "STRING"},
				}}},
				Result: resultChunk{
					DataArray:             [][]*string{{&one, &amount, &yes, &label}},
					NextChunkInternalLink: "/api/2.0/sql/statements/statement-1/result/chunks/1?row_offset=1",
				},
			}), nil
		case request.Method == http.MethodGet && request.URL.Path == "/api/2.0/sql/statements/statement-1/result/chunks/1":
			if request.URL.Query().Get("row_offset") != "1" {
				t.Errorf("chunk query = %s", request.URL.RawQuery)
			}
			two, amount, no := "2", "0.0100", "false"
			return jsonResponse(http.StatusOK, resultChunk{DataArray: [][]*string{{&two, &amount, &no, nil}}}), nil
		default:
			return jsonResponse(http.StatusNotFound, map[string]string{"message": "not found"}), nil
		}
	})
	client, err := NewClient(ClientConfig{
		Host: "https://workspace.test", WarehouseID: "warehouse", TokenSource: StaticTokenSource("test-token"),
		HTTPClient: httpClient, ByteLimit: 1024, PollInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.Execute(context.Background(), Statement{
		SQL: "SELECT 1", PhysicalFingerprint: "sha256:plan", RowLimit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Job.Status != model.JobSucceeded || snapshot.Job.RowLimit != 10 || snapshot.Result == nil || len(snapshot.Result.Rows) != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.Result.Rows[0][0] != int64(1) || snapshot.Result.Rows[0][1] != "12.3400" || snapshot.Result.Rows[0][2] != true {
		t.Fatalf("typed first row = %#v", snapshot.Result.Rows[0])
	}
	if snapshot.Result.Rows[1][3] != nil {
		t.Fatalf("null value = %#v", snapshot.Result.Rows[1][3])
	}
}

func TestClientRejectsTruncatedResultsAndRetainsFailedJob(t *testing.T) {
	t.Parallel()
	responses := []statementResponse{
		{StatementID: "truncated", Status: statementStatus{State: "SUCCEEDED"}, Manifest: resultManifest{Truncated: true}},
		{StatementID: "failed", Status: statementStatus{
			State: "FAILED", Error: &statementError{ErrorCode: "BAD_REQUEST", Message: "query rejected"},
		}},
	}
	var calls atomic.Int64
	client := mustClient(t, testHTTPClient(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, responses[int(calls.Add(1))-1]), nil
	}))
	_, err := client.Submit(context.Background(), Statement{SQL: "SELECT 1", PhysicalFingerprint: "p", RowLimit: 1})
	assertProblemCode(t, err, "result_truncated")
	failed, err := client.Submit(context.Background(), Statement{SQL: "SELECT 1", PhysicalFingerprint: "p", RowLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if failed.Job.ID != "failed" || failed.Job.Status != model.JobFailed || failed.Job.Error == nil || failed.Job.Error.Code != "engine_bad_request" {
		t.Fatalf("failed snapshot = %#v", failed)
	}
}

func TestExecuteCancelsRemoteStatementWhenContextEnds(t *testing.T) {
	t.Parallel()
	cancelled := make(chan struct{}, 1)
	httpClient := testHTTPClient(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/2.0/sql/statements":
			return jsonResponse(http.StatusOK, statementResponse{StatementID: "slow", Status: statementStatus{State: "PENDING"}}), nil
		case "/api/2.0/sql/statements/slow/cancel":
			cancelled <- struct{}{}
			return jsonResponse(http.StatusOK, struct{}{}), nil
		default:
			return jsonResponse(http.StatusNotFound, struct{}{}), nil
		}
	})
	client, err := NewClient(ClientConfig{
		Host: "https://workspace.test", WarehouseID: "warehouse", TokenSource: StaticTokenSource("token"),
		HTTPClient: httpClient, ByteLimit: 1024, PollInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	_, err = client.Execute(ctx, Statement{SQL: "SELECT 1", PhysicalFingerprint: "p", RowLimit: 1})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute() error = %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("remote cancel was not called")
	}
}

func TestExecuteCancelsWhenContextEndsDuringStatusRequest(t *testing.T) {
	t.Parallel()
	cancelled := make(chan struct{}, 1)
	httpClient := testHTTPClient(func(request *http.Request) (*http.Response, error) {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/api/2.0/sql/statements":
			return jsonResponse(http.StatusOK, statementResponse{StatementID: "blocked", Status: statementStatus{State: "PENDING"}}), nil
		case request.Method == http.MethodGet && request.URL.Path == "/api/2.0/sql/statements/blocked":
			<-request.Context().Done()
			return nil, request.Context().Err()
		case request.Method == http.MethodPost && request.URL.Path == "/api/2.0/sql/statements/blocked/cancel":
			cancelled <- struct{}{}
			return jsonResponse(http.StatusOK, struct{}{}), nil
		default:
			return jsonResponse(http.StatusNotFound, struct{}{}), nil
		}
	})
	client, err := NewClient(ClientConfig{
		Host: "https://workspace.test", WarehouseID: "warehouse", TokenSource: StaticTokenSource("token"),
		HTTPClient: httpClient, ByteLimit: 1024, PollInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = client.Execute(ctx, Statement{SQL: "SELECT 1", PhysicalFingerprint: "p", RowLimit: 1})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute() error = %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("remote cancel was not called after the status request ended")
	}
}

func TestDecodeResultRejectsChunkCyclesAndExcessRows(t *testing.T) {
	t.Parallel()
	manifest := resultManifest{Schema: resultSchema{Columns: []resultColumn{{Name: "id", TypeName: "LONG"}}}}
	one, two := "1", "2"

	t.Run("excess rows", func(t *testing.T) {
		client := mustClient(t, testHTTPClient(func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusInternalServerError, struct{}{}), nil
		}))
		_, err := client.decodeResult(context.Background(), manifest, resultChunk{DataArray: [][]*string{{&one}, {&two}}}, 1, 0)
		assertProblemCode(t, err, "engine_limit_exceeded")
	})

	t.Run("cycle", func(t *testing.T) {
		link := "/api/2.0/sql/statements/s/result/chunks/1"
		client := mustClient(t, testHTTPClient(func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, resultChunk{DataArray: [][]*string{{&two}}, NextChunkInternalLink: link}), nil
		}))
		_, err := client.decodeResult(context.Background(), manifest, resultChunk{DataArray: [][]*string{{&one}}, NextChunkInternalLink: link}, 10, 0)
		assertProblemCode(t, err, "invalid_engine_response")
	})
}

func TestClientEnforcesThisStatementRowLimit(t *testing.T) {
	t.Parallel()
	one, two := "1", "2"
	client := mustClient(t, testHTTPClient(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, statementResponse{
			StatementID: "too-many-rows", Status: statementStatus{State: "SUCCEEDED"},
			Manifest: resultManifest{Schema: resultSchema{Columns: []resultColumn{{Name: "id", TypeName: "LONG", TypeText: "BIGINT"}}}},
			Result:   resultChunk{DataArray: [][]*string{{&one}, {&two}}},
		}), nil
	}))
	snapshot, err := client.Submit(context.Background(), Statement{
		SQL: "SELECT 1", PhysicalFingerprint: "p", RowLimit: 1,
	})
	assertProblemCode(t, err, "engine_limit_exceeded")
	if snapshot.Job.RowLimit != 1 {
		t.Fatalf("job row limit = %d, want 1", snapshot.Job.RowLimit)
	}
}

func TestClientRejectsCumulativeChunkResponseBytes(t *testing.T) {
	t.Parallel()
	large := strings.Repeat("x", 600<<10)
	chunkPath := "/api/2.0/sql/statements/large/result/chunks/1"
	httpClient := testHTTPClient(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/2.0/sql/statements":
			return jsonResponse(http.StatusOK, statementResponse{
				StatementID: "large", Status: statementStatus{State: "SUCCEEDED"},
				Manifest: resultManifest{Schema: resultSchema{Columns: []resultColumn{{Name: "value", TypeName: "STRING", TypeText: "STRING"}}}},
				Result: resultChunk{
					DataArray: [][]*string{{&large}}, NextChunkInternalLink: chunkPath,
				},
			}), nil
		case chunkPath:
			return jsonResponse(http.StatusOK, resultChunk{DataArray: [][]*string{{&large}}}), nil
		default:
			return jsonResponse(http.StatusNotFound, struct{}{}), nil
		}
	})
	client, err := NewClient(ClientConfig{
		Host: "https://workspace.test", WarehouseID: "warehouse", TokenSource: StaticTokenSource("token"),
		HTTPClient: httpClient, ByteLimit: 1, PollInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Submit(context.Background(), Statement{
		SQL: "SELECT 1", PhysicalFingerprint: "p", RowLimit: 2,
	})
	assertProblemCode(t, err, "engine_response_too_large")
}

func TestStatementAdmissionBudgets(t *testing.T) {
	t.Parallel()
	base := Statement{SQL: "SELECT 1", PhysicalFingerprint: "p", RowLimit: 1}
	tooManyParameters := base
	tooManyParameters.Parameters = make([]Parameter, maximumParameters+1)
	if err := validateStatement(tooManyParameters); err == nil || !strings.Contains(err.Error(), "parameters") {
		t.Fatalf("parameter budget error = %v", err)
	}
	tooMuchSQL := base
	tooMuchSQL.SQL = "SELECT " + strings.Repeat("x", maximumSQLBytes)
	if err := validateStatement(tooMuchSQL); err == nil || !strings.Contains(err.Error(), "SQL") {
		t.Fatalf("SQL budget error = %v", err)
	}
}

func TestOAuthM2MUsesBasicAuthFormAndCachesToken(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	httpClient := testHTTPClient(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		clientID, secret, ok := request.BasicAuth()
		if !ok || clientID != "client-id" || secret != "client-secret" {
			t.Errorf("basic auth = %q %q %v", clientID, secret, ok)
		}
		data, _ := io.ReadAll(request.Body)
		form, _ := url.ParseQuery(string(data))
		if form.Get("grant_type") != "client_credentials" || form.Get("scope") != "all-apis" {
			t.Errorf("OAuth form = %#v", form)
		}
		return jsonResponse(http.StatusOK, map[string]any{"access_token": "oauth-token", "token_type": "Bearer", "expires_in": 3600}), nil
	})
	source, err := NewOAuthM2MTokenSource(OAuthM2MConfig{
		Host: "https://workspace.test", ClientID: "client-id", ClientSecret: "client-secret", HTTPClient: httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		token, err := source.Token(context.Background())
		if err != nil || token != "oauth-token" {
			t.Fatalf("Token() = %q, %v", token, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("OAuth calls = %d", calls.Load())
	}
}

func TestOAuthErrorDoesNotExposeClientSecret(t *testing.T) {
	t.Parallel()
	httpClient := testHTTPClient(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusUnauthorized, map[string]string{"message": "secret-value"}), nil
	})
	source, err := NewOAuthM2MTokenSource(OAuthM2MConfig{
		Host: "https://workspace.test", ClientID: "id", ClientSecret: "secret-value", HTTPClient: httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Token(context.Background())
	if err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("OAuth error leaked secret: %v", err)
	}
}

func mustClient(t *testing.T, httpClient *http.Client) *Client {
	t.Helper()
	client, err := NewClient(ClientConfig{
		Host: "https://workspace.test", WarehouseID: "warehouse", TokenSource: StaticTokenSource("token"),
		HTTPClient: httpClient, ByteLimit: 1024, PollInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func testHTTPClient(function roundTripFunc) *http.Client {
	return &http.Client{Transport: function, Timeout: time.Second}
}

func jsonResponse(status int, value any) *http.Response {
	data, _ := json.Marshal(value)
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(data))),
	}
}

func assertProblemCode(t *testing.T, err error, code string) {
	t.Helper()
	var problem *model.Problem
	if !errors.As(err, &problem) || problem.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
}
