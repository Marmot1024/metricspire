package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
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

const realAcceptanceFingerprint = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

// TestPhase2RealAcceptance is intentionally opt-in. Unlike the fast test suite,
// it writes a unique namespace to a disposable PostgreSQL database and executes
// read-only statements on a real Databricks SQL warehouse. Its inputs must be
// fixed and reviewable so a mock response can never be mistaken for acceptance.
func TestPhase2RealAcceptance(t *testing.T) {
	if os.Getenv("METRICSPIRE_RUN_REAL_ACCEPTANCE") != "1" {
		t.Skip("set METRICSPIRE_RUN_REAL_ACCEPTANCE=1 to run the real Phase 2 acceptance")
	}
	required := []string{
		"METRICSPIRE_TEST_DATABASE_URL",
		"DATABRICKS_HOST",
		"DATABRICKS_SQL_WAREHOUSE_ID",
		"METRICSPIRE_TEST_DATABRICKS_MODEL",
		"METRICSPIRE_TEST_DATABRICKS_CONTEXT",
		"METRICSPIRE_TEST_DATABRICKS_QUERY",
		"METRICSPIRE_TEST_DATABRICKS_POLICY",
		"METRICSPIRE_TEST_DATABRICKS_BINDING",
		"METRICSPIRE_TEST_DATABRICKS_EXPECTED",
	}
	requireRealDatabricksEnvironment(t, required...)

	databaseURL := os.Getenv("METRICSPIRE_TEST_DATABASE_URL")
	t.Setenv("METRICSPIRE_DATABASE_URL", databaseURL)
	modelPath := os.Getenv("METRICSPIRE_TEST_DATABRICKS_MODEL")
	contextPath := os.Getenv("METRICSPIRE_TEST_DATABRICKS_CONTEXT")
	queryPath := os.Getenv("METRICSPIRE_TEST_DATABRICKS_QUERY")
	policyPath := os.Getenv("METRICSPIRE_TEST_DATABRICKS_POLICY")
	bindingPath := os.Getenv("METRICSPIRE_TEST_DATABRICKS_BINDING")
	expectedPath := os.Getenv("METRICSPIRE_TEST_DATABRICKS_EXPECTED")
	var source model.SemanticSource
	if err := contractio.ReadFile(modelPath, &source); err != nil {
		t.Fatalf("read fixed Databricks semantic model: %v", err)
	}
	modelName := strings.TrimSpace(source.Metadata.Name)
	if modelName == "" {
		t.Fatal("fixed Databricks semantic model must have a name")
	}
	var binding model.SourceBinding
	if err := contractio.ReadFile(bindingPath, &binding); err != nil {
		t.Fatalf("read fixed Databricks binding: %v", err)
	}
	if binding.Engine != databricks.EngineName {
		t.Fatalf("fixed binding engine = %q, want %q", binding.Engine, databricks.EngineName)
	}
	var expected model.TypedResult
	if err := contractio.ReadFile(expectedPath, &expected); err != nil {
		t.Fatalf("read fixed expected result: %v", err)
	}
	if len(expected.Columns) == 0 || len(expected.Rows) == 0 {
		t.Fatal("fixed expected result must contain at least one column and one row")
	}

	namespace := fmt.Sprintf("acceptance_%d", time.Now().UTC().UnixNano())
	runRealCommand(t, "migrate")
	firstDraftJSON := runRealCommand(t, "draft-put", "--namespace", namespace, "--source", modelPath, "--actor", "phase2-acceptance")
	var firstDraft catalog.Draft
	decodeRealOutput(t, firstDraftJSON, &firstDraft)
	firstReleaseJSON := runRealCommand(t, "publish", "--namespace", namespace, "--model", modelName, "--revision", fmt.Sprint(firstDraft.Revision), "--actor", "phase2-acceptance", "--note", "real acceptance v1")
	var firstRelease catalog.Release
	decodeRealOutput(t, firstReleaseJSON, &firstRelease)

	queryArguments := []string{
		"query-active", "--namespace", namespace, "--model", modelName,
		"--context", contextPath, "--query", queryPath, "--policy", policyPath,
		"--binding", bindingPath, "--timeout", "5m",
	}
	firstQuery := runRealQuery(t, queryArguments...)
	if firstQuery.Release.ID != firstRelease.ID {
		t.Fatalf("first query release = %q, want %q", firstQuery.Release.ID, firstRelease.ID)
	}
	assertExpectedRealResult(t, firstQuery.Execution.Result, expected)
	statement, err := databricks.Compile(firstQuery.Physical)
	if err != nil {
		t.Fatalf("compile accepted physical plan: %v", err)
	}
	if statement.RowLimit != int64(firstQuery.Physical.Limit) || len(statement.Parameters) == 0 || strings.Contains(statement.SQL, ";") {
		t.Fatalf("generated statement did not preserve the governed query contract")
	}

	source.Metadata.Version = "1.1.0"
	source.Spec.Metrics[0].Description = "Real acceptance wording revision."
	secondModelPath := filepath.Join(t.TempDir(), "model-v2.json")
	if err := contractio.WriteJSON(secondModelPath, source); err != nil {
		t.Fatal(err)
	}
	secondDraftJSON := runRealCommand(t, "draft-put", "--namespace", namespace, "--source", secondModelPath, "--actor", "phase2-acceptance", "--expected-revision", fmt.Sprint(firstDraft.Revision))
	var secondDraft catalog.Draft
	decodeRealOutput(t, secondDraftJSON, &secondDraft)
	secondReleaseJSON := runRealCommand(t, "publish", "--namespace", namespace, "--model", modelName, "--revision", fmt.Sprint(secondDraft.Revision), "--actor", "phase2-acceptance", "--note", "real acceptance v2")
	var secondRelease catalog.Release
	decodeRealOutput(t, secondReleaseJSON, &secondRelease)
	if secondRelease.ID == firstRelease.ID {
		t.Fatal("second publication reused the first immutable release ID")
	}
	secondQuery := runRealQuery(t, queryArguments...)
	if secondQuery.Release.ID != secondRelease.ID {
		t.Fatalf("second query release = %q, want %q", secondQuery.Release.ID, secondRelease.ID)
	}
	assertExpectedRealResult(t, secondQuery.Execution.Result, expected)

	runRealCommand(t, "rollback", "--namespace", namespace, "--model", modelName, "--release", firstRelease.ID, "--actor", "phase2-acceptance", "--note", "real acceptance rollback")
	rolledBackQuery := runRealQuery(t, queryArguments...)
	if rolledBackQuery.Release.ID != firstRelease.ID {
		t.Fatalf("query after rollback release = %q, want %q", rolledBackQuery.Release.ID, firstRelease.ID)
	}
	assertExpectedRealResult(t, rolledBackQuery.Execution.Result, expected)
	eventsJSON := runRealCommand(t, "release-events", "--namespace", namespace, "--model", modelName)
	var events []catalog.ReleaseEvent
	decodeRealOutput(t, eventsJSON, &events)
	if len(events) != 3 || events[0].Kind != catalog.EventPublished || events[1].Kind != catalog.EventPublished || events[2].Kind != catalog.EventRollback {
		t.Fatalf("release events = %#v", events)
	}
}

// TestPhase2RealDatabricksExecutionControls is separate because its explicit
// slow query and oversized response are exceptional operations. Enabling the
// ordinary acceptance test must never trigger those statements implicitly.
func TestPhase2RealDatabricksExecutionControls(t *testing.T) {
	if os.Getenv("METRICSPIRE_RUN_REAL_DATABRICKS_CONTROLS") != "1" {
		t.Skip("set METRICSPIRE_RUN_REAL_DATABRICKS_CONTROLS=1 to run exceptional Databricks controls")
	}
	requireRealDatabricksEnvironment(t,
		"DATABRICKS_HOST",
		"DATABRICKS_SQL_WAREHOUSE_ID",
		"METRICSPIRE_TEST_DATABRICKS_SLOW_SQL",
	)
	testRealDatabricksExecutionControls(t)
}

func requireRealDatabricksEnvironment(t *testing.T, required ...string) {
	t.Helper()
	missing := make([]string, 0)
	for _, name := range required {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			missing = append(missing, name)
		}
	}
	if (os.Getenv("DATABRICKS_CLIENT_ID") == "") != (os.Getenv("DATABRICKS_CLIENT_SECRET") == "") {
		t.Fatal("DATABRICKS_CLIENT_ID and DATABRICKS_CLIENT_SECRET must both be set")
	}
	profile := strings.TrimSpace(os.Getenv("METRICSPIRE_TEST_DATABRICKS_PROFILE"))
	if os.Getenv("DATABRICKS_CLIENT_ID") == "" && os.Getenv("DATABRICKS_TOKEN") == "" && profile == "" {
		missing = append(missing, "DATABRICKS_CLIENT_ID+DATABRICKS_CLIENT_SECRET, DATABRICKS_TOKEN, or METRICSPIRE_TEST_DATABRICKS_PROFILE")
	}
	if len(missing) != 0 {
		t.Fatalf("real acceptance is enabled but required inputs are missing: %s", strings.Join(missing, ", "))
	}
	if os.Getenv("DATABRICKS_CLIENT_ID") == "" && os.Getenv("DATABRICKS_TOKEN") == "" {
		tokenContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		token, err := databricksProfileAccessToken(tokenContext, profile)
		if err != nil {
			t.Fatalf("load temporary Databricks token from profile %q: %v", profile, err)
		}
		t.Setenv("DATABRICKS_TOKEN", token)
	}
}

func testRealDatabricksExecutionControls(t *testing.T) {
	tokens, err := databricksTokenSource(os.Getenv("DATABRICKS_HOST"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := databricks.NewClient(databricks.ClientConfig{
		Host: os.Getenv("DATABRICKS_HOST"), WarehouseID: os.Getenv("DATABRICKS_SQL_WAREHOUSE_ID"),
		TokenSource: tokens, ByteLimit: 1 << 20, PollInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("row limit", func(t *testing.T) {
		snapshot, executeErr := client.Execute(context.Background(), databricks.Statement{
			SQL:                 "SELECT value FROM VALUES (1), (2) AS t(value) ORDER BY value",
			PhysicalFingerprint: realAcceptanceFingerprint, RowLimit: 1,
		})
		if executeErr != nil {
			assertRealProblem(t, executeErr, "result_truncated")
			return
		}
		if snapshot.Job.RowLimit != 1 || snapshot.Result == nil || len(snapshot.Result.Rows) > 1 {
			t.Fatalf("row-limited snapshot = %#v", snapshot)
		}
	})

	t.Run("byte limit", func(t *testing.T) {
		limitedClient, clientErr := databricks.NewClient(databricks.ClientConfig{
			Host: os.Getenv("DATABRICKS_HOST"), WarehouseID: os.Getenv("DATABRICKS_SQL_WAREHOUSE_ID"),
			TokenSource: tokens, ByteLimit: 1024, PollInterval: 100 * time.Millisecond,
		})
		if clientErr != nil {
			t.Fatal(clientErr)
		}
		snapshot, executeErr := limitedClient.Execute(context.Background(), databricks.Statement{
			SQL:                 "SELECT repeat('x', 65536) AS payload",
			PhysicalFingerprint: realAcceptanceFingerprint, RowLimit: 1,
		})
		if executeErr != nil {
			var problem *model.Problem
			if !errors.As(executeErr, &problem) || (problem.Code != "result_truncated" && problem.Code != "engine_response_too_large") {
				t.Fatalf("byte-limit error = %v", executeErr)
			}
			return
		}
		if snapshot.Result == nil || len(snapshot.Result.Rows) != 1 || len(snapshot.Result.Rows[0]) != 1 {
			t.Fatalf("byte-limited snapshot = %#v", snapshot)
		}
		value, ok := snapshot.Result.Rows[0][0].(string)
		if !ok || len(value) > 1024 {
			t.Fatalf("byte limit returned %d bytes without a truncation error", len(value))
		}
	})

	slowSQL := strings.TrimSpace(os.Getenv("METRICSPIRE_TEST_DATABRICKS_SLOW_SQL"))
	if !strings.HasPrefix(strings.ToUpper(slowSQL), "SELECT") || strings.Contains(slowSQL, ";") {
		t.Fatal("METRICSPIRE_TEST_DATABRICKS_SLOW_SQL must be one read-only SELECT without a semicolon")
	}
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		snapshot, submitErr := client.Submit(ctx, databricks.Statement{
			SQL: slowSQL, PhysicalFingerprint: realAcceptanceFingerprint, RowLimit: 1,
		})
		if submitErr != nil {
			t.Fatal(submitErr)
		}
		if realJobTerminal(snapshot.Job.Status) {
			t.Fatalf("slow SQL completed before cancellation with status %s", snapshot.Job.Status)
		}
		if err := client.Cancel(ctx, snapshot.Job.ID); err != nil {
			t.Fatal(err)
		}
		cancelled := awaitRealJob(t, ctx, client, snapshot.Job)
		if cancelled.Status != model.JobCancelled {
			t.Fatalf("cancelled job status = %s", cancelled.Status)
		}
	})

	t.Run("timeout triggers cancellation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		snapshot, executeErr := client.Execute(ctx, databricks.Statement{
			SQL: slowSQL, PhysicalFingerprint: realAcceptanceFingerprint, RowLimit: 1,
		})
		if !errors.Is(executeErr, context.DeadlineExceeded) {
			t.Fatalf("timeout error = %v", executeErr)
		}
		if snapshot.Job.ID == "" {
			t.Fatal("timeout ended before a cancellable remote statement ID was received")
		}
		pollCtx, pollCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer pollCancel()
		cancelled := awaitRealJob(t, pollCtx, client, snapshot.Job)
		if cancelled.Status != model.JobCancelled {
			t.Fatalf("timed-out job status = %s", cancelled.Status)
		}
	})
}

func runRealQuery(t *testing.T, arguments ...string) application.QueryOutput {
	t.Helper()
	data := runRealCommand(t, arguments...)
	var output application.QueryOutput
	decodeRealOutput(t, data, &output)
	if output.Execution.Job.Status != model.JobSucceeded || output.Execution.Result == nil {
		t.Fatalf("real query did not return a successful typed result: %#v", output.Execution.Job)
	}
	return output
}

func runRealCommand(t *testing.T, arguments ...string) []byte {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := runContext(context.Background(), arguments, &stdout, &stderr); err != nil {
		t.Fatalf("metricspire %v: %v\nstderr: %s", arguments, err, stderr.String())
	}
	return stdout.Bytes()
}

func decodeRealOutput(t *testing.T, data []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("decode command output: %v", err)
	}
}

func assertExpectedRealResult(t *testing.T, actual *model.TypedResult, expected model.TypedResult) {
	t.Helper()
	actualJSON, _ := json.Marshal(actual)
	expectedJSON, _ := json.Marshal(expected)
	if actual == nil || !bytes.Equal(actualJSON, expectedJSON) {
		t.Fatalf("real Databricks result mismatch\nactual: %s\nexpected: %s", actualJSON, expectedJSON)
	}
}

func assertRealProblem(t *testing.T, err error, code string) {
	t.Helper()
	var problem *model.Problem
	if !errors.As(err, &problem) || problem.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
}

func awaitRealJob(t *testing.T, ctx context.Context, client *databricks.Client, job model.ExecutionJob) model.ExecutionJob {
	t.Helper()
	for {
		if realJobTerminal(job.Status) {
			return job
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for Databricks job %s: %v", job.ID, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
		snapshot, err := client.Get(ctx, job)
		if err != nil {
			t.Fatalf("get Databricks job %s: %v", job.ID, err)
		}
		job = snapshot.Job
	}
}

func realJobTerminal(status model.JobStatus) bool {
	return status == model.JobSucceeded || status == model.JobFailed || status == model.JobCancelled
}

// databricksProfileAccessToken is test-only glue for local acceptance. Product
// commands remain independent of the Databricks CLI and use OAuth M2M or an
// explicitly supplied token. The short-lived token is never printed or saved.
func databricksProfileAccessToken(ctx context.Context, profile string) (string, error) {
	command := exec.CommandContext(ctx, "databricks", "auth", "token", "--profile", profile)
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("databricks CLI token command failed: %w", err)
	}
	var response struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return "", fmt.Errorf("decode databricks CLI token response: %w", err)
	}
	if strings.TrimSpace(response.AccessToken) == "" {
		return "", errors.New("databricks CLI returned an empty access token")
	}
	return response.AccessToken, nil
}
