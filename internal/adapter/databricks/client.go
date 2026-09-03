package databricks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	pathpkg "path"
	"strconv"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/executionauth"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/planner"
)

const (
	defaultByteLimit  int64 = 4 << 20
	maximumSQLBytes         = 1 << 20
	maximumParameters       = 1000
	responseOverhead  int64 = 1 << 20
)

var ErrRequestAccessTokenRequired = errors.New("request-scoped Databricks access token is required")

type TokenSource interface {
	Token(context.Context) (string, error)
}

type ClientConfig struct {
	Host                string
	WarehouseID         string
	TokenSource         TokenSource
	RequireRequestToken bool
	HTTPClient          *http.Client
	ByteLimit           int64
	PollInterval        time.Duration
}

type Client struct {
	host                *url.URL
	warehouseID         string
	tokens              TokenSource
	requireRequestToken bool
	http                *http.Client
	byteLimit           int64
	pollInterval        time.Duration
	now                 func() time.Time
}

func NewClient(config ClientConfig) (*Client, error) {
	host, err := validateHost(config.Host)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.WarehouseID) == "" {
		return nil, errors.New("Databricks warehouse ID is required")
	}
	if config.TokenSource == nil && !config.RequireRequestToken {
		return nil, errors.New("Databricks token source is required")
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	byteLimit := config.ByteLimit
	if byteLimit == 0 {
		byteLimit = defaultByteLimit
	}
	if byteLimit < 1 || byteLimit > 24<<20 {
		return nil, errors.New("Databricks byte limit must be between 1 byte and 24 MiB")
	}
	pollInterval := config.PollInterval
	if pollInterval == 0 {
		pollInterval = 500 * time.Millisecond
	}
	if pollInterval < 10*time.Millisecond {
		return nil, errors.New("Databricks poll interval must be at least 10ms")
	}
	return &Client{
		host: host, warehouseID: config.WarehouseID, tokens: config.TokenSource,
		requireRequestToken: config.RequireRequestToken,
		http:                httpClient, byteLimit: byteLimit, pollInterval: pollInterval, now: time.Now,
	}, nil
}

func (c *Client) Submit(ctx context.Context, statement Statement) (model.ExecutionSnapshot, error) {
	if err := validateStatement(statement); err != nil {
		return model.ExecutionSnapshot{}, err
	}
	request := executeRequest{
		Statement: statement.SQL, WarehouseID: c.warehouseID, Parameters: statement.Parameters,
		WaitTimeout: "0s", Format: "JSON_ARRAY", Disposition: "INLINE",
		RowLimit: statement.RowLimit, ByteLimit: c.byteLimit,
	}
	var response statementResponse
	responseBytes, err := c.doJSON(ctx, http.MethodPost, "/api/2.0/sql/statements", request, &response)
	if err != nil {
		return model.ExecutionSnapshot{}, err
	}
	if response.StatementID == "" {
		return model.ExecutionSnapshot{}, problem("invalid_engine_response", "databricks.statement_id", "response omitted statement ID")
	}
	job := model.ExecutionJob{
		ID: response.StatementID, PhysicalFingerprint: statement.PhysicalFingerprint,
		RowLimit: statement.RowLimit, SubmittedAt: c.now().UTC(),
	}
	return c.snapshot(ctx, job, response, responseBytes)
}

func (c *Client) Get(ctx context.Context, job model.ExecutionJob) (model.ExecutionSnapshot, error) {
	if strings.TrimSpace(job.ID) == "" || strings.TrimSpace(job.PhysicalFingerprint) == "" {
		return model.ExecutionSnapshot{}, errors.New("job ID and physical fingerprint are required")
	}
	if job.RowLimit < 1 || job.RowLimit > int64(planner.MaximumLimit) {
		return model.ExecutionSnapshot{}, fmt.Errorf("job row limit must be between 1 and %d", planner.MaximumLimit)
	}
	var response statementResponse
	path := "/api/2.0/sql/statements/" + url.PathEscape(job.ID)
	responseBytes, err := c.doJSON(ctx, http.MethodGet, path, nil, &response)
	if err != nil {
		return model.ExecutionSnapshot{}, err
	}
	if response.StatementID == "" {
		response.StatementID = job.ID
	}
	return c.snapshot(ctx, job, response, responseBytes)
}

func (c *Client) Cancel(ctx context.Context, statementID string) error {
	if strings.TrimSpace(statementID) == "" {
		return errors.New("statement ID is required")
	}
	path := "/api/2.0/sql/statements/" + url.PathEscape(statementID) + "/cancel"
	_, err := c.doJSON(ctx, http.MethodPost, path, struct{}{}, nil)
	return err
}

func (c *Client) Execute(ctx context.Context, statement Statement) (model.ExecutionSnapshot, error) {
	snapshot, err := c.Submit(ctx, statement)
	if err != nil {
		return model.ExecutionSnapshot{}, err
	}
	for !terminal(snapshot.Job.Status) {
		select {
		case <-ctx.Done():
			c.cancelBestEffort(ctx, snapshot.Job.ID)
			return snapshot, ctx.Err()
		case <-time.After(c.pollInterval):
		}
		nextSnapshot, getErr := c.Get(ctx, snapshot.Job)
		if getErr != nil {
			if ctx.Err() != nil {
				c.cancelBestEffort(ctx, snapshot.Job.ID)
				return snapshot, ctx.Err()
			}
			return snapshot, getErr
		}
		snapshot = nextSnapshot
	}
	if snapshot.Job.Status != model.JobSucceeded {
		if snapshot.Job.Error != nil {
			return snapshot, snapshot.Job.Error
		}
		return snapshot, problem("engine_failed", "databricks.status", "statement ended with status %s", snapshot.Job.Status)
	}
	if snapshot.Result == nil {
		return snapshot, problem("result_unavailable", "databricks.result", "successful statement returned no inline result")
	}
	return snapshot, nil
}

func (c *Client) snapshot(ctx context.Context, job model.ExecutionJob, response statementResponse, responseBytes int64) (model.ExecutionSnapshot, error) {
	status, err := mapStatus(response.Status.State)
	if err != nil {
		return model.ExecutionSnapshot{}, err
	}
	job.ID = response.StatementID
	job.Status = status
	now := c.now().UTC()
	if (status == model.JobRunning || terminal(status)) && job.StartedAt == nil {
		job.StartedAt = &now
	}
	if terminal(status) {
		job.FinishedAt = &now
	}
	if response.Status.Error != nil {
		job.Error = &model.Problem{
			Code:    "engine_" + strings.ToLower(response.Status.Error.ErrorCode),
			Message: response.Status.Error.Message,
			Path:    "databricks.status",
		}
	}
	if status != model.JobSucceeded {
		return model.ExecutionSnapshot{Job: job}, nil
	}
	if response.Manifest.Truncated {
		return model.ExecutionSnapshot{Job: job}, problem("result_truncated", "databricks.manifest", "result exceeded the configured row or byte limit")
	}
	if strings.EqualFold(response.Status.State, "CLOSED") || len(response.Manifest.Schema.Columns) == 0 {
		return model.ExecutionSnapshot{Job: job}, problem("result_unavailable", "databricks.result", "successful statement result is no longer available")
	}
	result, err := c.decodeResult(ctx, response.Manifest, response.Result, int(job.RowLimit), responseBytes)
	if err != nil {
		return model.ExecutionSnapshot{Job: job}, err
	}
	return model.ExecutionSnapshot{Job: job, Result: &result}, nil
}

func (c *Client) decodeResult(ctx context.Context, manifest resultManifest, first resultChunk, rowLimit int, responseBytes int64) (model.TypedResult, error) {
	responseBudget := c.byteLimit + responseOverhead
	if responseBytes > responseBudget {
		return model.TypedResult{}, problem("engine_response_too_large", "databricks.result", "cumulative result responses exceeded %d bytes", responseBudget)
	}
	columns := make([]model.ResultColumn, len(manifest.Schema.Columns))
	for i, column := range manifest.Schema.Columns {
		columns[i] = model.ResultColumn{Name: column.Name, TypeName: column.TypeName, TypeText: column.TypeText}
	}
	rawRows := append([][]*string(nil), first.DataArray...)
	if len(rawRows) > rowLimit {
		return model.TypedResult{}, problem("engine_limit_exceeded", "databricks.result", "result exceeded the service row limit")
	}
	next := first.NextChunkInternalLink
	visited := make(map[string]struct{})
	for next != "" {
		if !strings.HasPrefix(next, "/api/2.0/sql/statements/") || strings.Contains(next, "://") {
			return model.TypedResult{}, problem("invalid_engine_response", "databricks.result", "unsafe next chunk link")
		}
		if _, exists := visited[next]; exists {
			return model.TypedResult{}, problem("invalid_engine_response", "databricks.result", "next chunk link formed a cycle")
		}
		visited[next] = struct{}{}
		if len(visited) > rowLimit+1 {
			return model.TypedResult{}, problem("invalid_engine_response", "databricks.result", "result returned too many chunks")
		}
		var chunk resultChunk
		chunkBytes, err := c.doJSON(ctx, http.MethodGet, next, nil, &chunk)
		if err != nil {
			return model.TypedResult{}, err
		}
		responseBytes += chunkBytes
		if responseBytes > responseBudget {
			return model.TypedResult{}, problem("engine_response_too_large", "databricks.result", "cumulative result responses exceeded %d bytes", responseBudget)
		}
		rawRows = append(rawRows, chunk.DataArray...)
		if len(rawRows) > rowLimit {
			return model.TypedResult{}, problem("engine_limit_exceeded", "databricks.result", "result exceeded the service row limit")
		}
		next = chunk.NextChunkInternalLink
	}
	rows := make([][]any, len(rawRows))
	for rowIndex, raw := range rawRows {
		if len(raw) != len(columns) {
			return model.TypedResult{}, problem("invalid_engine_response", "databricks.result", "row %d has %d values for %d columns", rowIndex, len(raw), len(columns))
		}
		rows[rowIndex] = make([]any, len(raw))
		for columnIndex, value := range raw {
			decoded, err := decodeValue(value, columns[columnIndex].TypeName)
			if err != nil {
				return model.TypedResult{}, problem("invalid_engine_response", "databricks.result", "row %d column %d: %v", rowIndex, columnIndex, err)
			}
			rows[rowIndex][columnIndex] = decoded
		}
	}
	return model.TypedResult{Columns: columns, Rows: rows, Truncated: false}, nil
}

func (c *Client) cancelBestEffort(executionContext context.Context, statementID string) {
	base := executionauth.PropagateAccessToken(context.Background(), executionContext)
	cancelContext, cancel := context.WithTimeout(base, 5*time.Second)
	defer cancel()
	_ = c.Cancel(cancelContext, statementID)
}

func decodeValue(value *string, typeName string) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch strings.ToUpper(typeName) {
	case "BYTE", "SHORT", "INT", "LONG":
		return strconv.ParseInt(*value, 10, 64)
	case "FLOAT", "DOUBLE":
		return strconv.ParseFloat(*value, 64)
	case "BOOLEAN":
		return strconv.ParseBool(*value)
	case "DECIMAL", "DATE", "TIMESTAMP", "STRING", "CHAR", "BINARY", "INTERVAL":
		return *value, nil
	case "NULL":
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported result type %q", typeName)
	}
}

func (c *Client) doJSON(ctx context.Context, method, path string, input, output any) (int64, error) {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return 0, fmt.Errorf("encode Databricks request: %w", err)
		}
		body = bytes.NewReader(data)
	}
	reference, err := url.Parse(path)
	if err != nil || reference.IsAbs() || reference.Host != "" || !strings.HasPrefix(reference.Path, "/api/") || pathpkg.Clean(reference.Path) != reference.Path {
		return 0, errors.New("Databricks API path must be a safe relative API path")
	}
	endpoint := c.host.ResolveReference(reference).String()
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return 0, fmt.Errorf("create Databricks request: %w", err)
	}
	token, present := executionauth.AccessToken(ctx)
	if !present {
		if c.requireRequestToken {
			return 0, ErrRequestAccessTokenRequired
		}
		var err error
		token, err = c.tokens.Token(ctx)
		if err != nil {
			return 0, fmt.Errorf("get Databricks access token: %w", err)
		}
		if strings.TrimSpace(token) == "" {
			return 0, errors.New("Databricks token source returned an empty token")
		}
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "metricspire/0.2")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return 0, fmt.Errorf("call Databricks: %w", err)
	}
	defer response.Body.Close()
	limit := c.byteLimit + responseOverhead
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return 0, fmt.Errorf("read Databricks response: %w", err)
	}
	if int64(len(data)) > limit {
		return int64(len(data)), problem("engine_response_too_large", "databricks.response", "response exceeded %d bytes", limit)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var apiError struct {
			ErrorCode string `json:"error_code"`
		}
		_ = json.Unmarshal(data, &apiError)
		return int64(len(data)), problem(
			"engine_http_error", "databricks.response", "Databricks returned HTTP %d (%s)",
			response.StatusCode, safeDatabricksErrorCode(apiError.ErrorCode),
		)
	}
	if output == nil || len(data) == 0 {
		return int64(len(data)), nil
	}
	if err := json.Unmarshal(data, output); err != nil {
		return int64(len(data)), problem("invalid_engine_response", "databricks.response", "decode JSON: %v", err)
	}
	return int64(len(data)), nil
}

func safeDatabricksErrorCode(value string) string {
	value = strings.TrimSpace(value)
	if len(value) < 1 || len(value) > 64 {
		return "unknown_error"
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return "unknown_error"
		}
	}
	return value
}

func validateStatement(statement Statement) error {
	if statement.PhysicalFingerprint == "" {
		return errors.New("physical fingerprint is required")
	}
	if statement.RowLimit < 1 || statement.RowLimit > int64(planner.MaximumLimit) {
		return fmt.Errorf("row limit must be between 1 and %d", planner.MaximumLimit)
	}
	if len(statement.SQL) == 0 || len(statement.SQL) > maximumSQLBytes {
		return fmt.Errorf("generated SQL must be between 1 and %d bytes", maximumSQLBytes)
	}
	if len(statement.Parameters) > maximumParameters {
		return fmt.Errorf("generated statement must contain at most %d parameters", maximumParameters)
	}
	if !strings.HasPrefix(strings.TrimSpace(statement.SQL), "SELECT") || strings.Contains(statement.SQL, ";") {
		return errors.New("only one generated SELECT statement is allowed")
	}
	return nil
}

func terminal(status model.JobStatus) bool {
	return status == model.JobSucceeded || status == model.JobFailed || status == model.JobCancelled
}

func mapStatus(state string) (model.JobStatus, error) {
	switch strings.ToUpper(state) {
	case "PENDING":
		return model.JobPending, nil
	case "RUNNING":
		return model.JobRunning, nil
	case "SUCCEEDED", "CLOSED":
		return model.JobSucceeded, nil
	case "FAILED":
		return model.JobFailed, nil
	case "CANCELED", "CANCELLED":
		return model.JobCancelled, nil
	default:
		return "", problem("invalid_engine_response", "databricks.status.state", "unknown state %q", state)
	}
}

func validateHost(value string) (*url.URL, error) {
	value = strings.TrimSpace(value)
	if value != "" && !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("Databricks host must be an absolute URL without credentials, query, or fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, errors.New("Databricks host must not include an API path")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost")) {
		return nil, errors.New("Databricks host must use HTTPS")
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed, nil
}

type executeRequest struct {
	Statement   string      `json:"statement"`
	WarehouseID string      `json:"warehouse_id"`
	Parameters  []Parameter `json:"parameters,omitempty"`
	WaitTimeout string      `json:"wait_timeout"`
	Format      string      `json:"format"`
	Disposition string      `json:"disposition"`
	RowLimit    int64       `json:"row_limit"`
	ByteLimit   int64       `json:"byte_limit"`
}

type statementResponse struct {
	StatementID string          `json:"statement_id"`
	Status      statementStatus `json:"status"`
	Manifest    resultManifest  `json:"manifest"`
	Result      resultChunk     `json:"result"`
}

type statementStatus struct {
	State string          `json:"state"`
	Error *statementError `json:"error,omitempty"`
}

type statementError struct {
	ErrorCode string `json:"error_code"`
	Message   string `json:"message"`
	SQLState  string `json:"sql_state,omitempty"`
}

type resultManifest struct {
	Schema    resultSchema `json:"schema"`
	Truncated bool         `json:"truncated"`
}

type resultSchema struct {
	Columns []resultColumn `json:"columns"`
}

type resultColumn struct {
	Name     string `json:"name"`
	TypeName string `json:"type_name"`
	TypeText string `json:"type_text"`
}

type resultChunk struct {
	DataArray             [][]*string `json:"data_array"`
	NextChunkInternalLink string      `json:"next_chunk_internal_link"`
}
