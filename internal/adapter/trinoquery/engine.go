package trinoquery

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/exploration"
	"github.com/marmot1024/metricspire/internal/model"
)

// Config is operator-owned. User is a dedicated read-only engine identity, never
// an identity supplied by an analysis request. Connection contains credentials.
type Config struct{ Driver, Connection, User string }
type Engine struct {
	config   Config
	endpoint *url.URL
	client   *http.Client
}

func New(c Config) (*Engine, error) {
	u, err := url.Parse(c.Connection)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (c.Driver != Trino && c.Driver != Presto) || !identifier.MatchString(c.User) {
		return nil, problem("invalid_configuration", "analysis requires an engine origin and a read-only engine user")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())) && u.User == nil) {
		return nil, problem("invalid_configuration", "analysis requires HTTPS; unauthenticated loopback HTTP is test-only")
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	return &Engine{config: c, endpoint: u, client: &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

var regexpDecimal = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

type page struct {
	ID      string `json:"id"`
	Next    string `json:"nextUri"`
	Columns []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"columns"`
	Data  [][]any         `json:"data"`
	Error json.RawMessage `json:"error"`
}

func (e *Engine) next(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != e.endpoint.Scheme || u.Host != e.endpoint.Host || u.User != nil || u.Fragment != "" || !e.validContinuationQuery(u) || !strings.HasPrefix(u.Path, "/v1/statement/") || strings.Contains(u.Path, "..") {
		return nil, problem("analysis_engine_protocol", "engine returned an untrusted continuation")
	}
	return u, nil
}

// Presto attaches a server-generated slug to queued and executing continuations.
// Keep origin/path pinning and reject all other query parameters.
func (e *Engine) validContinuationQuery(u *url.URL) bool {
	if u.RawQuery == "" {
		return !u.ForceQuery
	}
	if e.config.Driver != Presto || len(u.RawQuery) > 1024 {
		return false
	}
	q, err := url.ParseQuery(u.RawQuery)
	return err == nil && len(q) == 1 && len(q["slug"]) == 1 && q.Get("slug") != ""
}

func (e *Engine) request(ctx context.Context, method string, u *url.URL, sql, session string) (*http.Response, error) {
	v := *u
	v.User = nil
	r, err := http.NewRequestWithContext(ctx, method, v.String(), strings.NewReader(sql))
	if err != nil {
		return nil, err
	}
	prefix := "X-Trino-"
	if e.config.Driver == Presto {
		prefix = "X-Presto-"
	}
	r.Header.Set(prefix+"User", e.config.User)
	r.Header.Set(prefix+"Source", "metricspire")
	r.Header.Set(prefix+"Time-Zone", "UTC")
	if session != "" {
		r.Header.Set(prefix+"Session", session)
	}
	if e.endpoint.User != nil {
		password, _ := e.endpoint.User.Password()
		r.SetBasicAuth(e.endpoint.User.Username(), password)
	}
	return e.client.Do(r)
}
func (e *Engine) cancel(u *url.URL) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := e.request(ctx, http.MethodDelete, u, "", "")
	if err == nil {
		r.Body.Close()
	}
}

func compatible(t string, want model.DataType) bool {
	switch want {
	case model.DataTypeInteger:
		return t == "bigint" || t == "integer" || t == "smallint" || t == "tinyint"
	case model.DataTypeDecimal:
		return strings.HasPrefix(t, "decimal(") || t == "bigint" || t == "integer"
	case model.DataTypeDate:
		return t == "date"
	case model.DataTypeBoolean:
		return t == "boolean"
	case model.DataTypeString:
		return t == "varchar" || strings.HasPrefix(t, "varchar(") || strings.HasPrefix(t, "char(")
	}
	return false
}
func scalar(v any, t model.DataType) (any, error) {
	if v == nil {
		return nil, nil
	}
	bad := func() (any, error) {
		return nil, problem("analysis_engine_protocol", "engine returned an incompatible scalar")
	}
	switch t {
	case model.DataTypeInteger:
		n, ok := v.(json.Number)
		if !ok {
			return bad()
		}
		x, err := strconv.ParseInt(string(n), 10, 64)
		if err != nil {
			return bad()
		}
		return x, nil
	case model.DataTypeDecimal:
		s, ok := v.(string)
		if n, yes := v.(json.Number); yes {
			s = string(n)
			ok = true
		}
		if !ok || len(s) > 80 || !regexpDecimal.MatchString(s) {
			return bad()
		}
		return s, nil
	case model.DataTypeBoolean:
		if b, ok := v.(bool); ok {
			return b, nil
		}
	case model.DataTypeString:
		if s, ok := v.(string); ok {
			return s, nil
		}
	case model.DataTypeDate:
		if s, ok := v.(string); ok {
			if _, err := time.Parse(time.DateOnly, s); err == nil {
				return s, nil
			}
		}
	}
	return bad()
}

func (e *Engine) Execute(parent context.Context, p exploration.Plan) (model.ExecutionSnapshot, error) {
	sql, err := Compile(p, e.config.Driver)
	if err != nil {
		return model.ExecutionSnapshot{}, err
	}
	ctx, cancel := context.WithTimeout(parent, p.Source.Timeout)
	defer cancel()
	started := time.Now().UTC()
	current := *e.endpoint
	current.Path = "/v1/statement"
	method, body := http.MethodPost, sql
	session := "query_max_run_time=" + strconv.FormatInt(p.Source.Timeout.Milliseconds(), 10) + "ms" + ",query_max_execution_time=" + strconv.FormatInt(p.Source.Timeout.Milliseconds(), 10) + "ms" + ",query_max_memory=256MB"
	if e.config.Driver == Trino {
		session += ",query_max_scan_physical_bytes=1GB"
	}
	result := &model.TypedResult{Columns: []model.ResultColumn{}, Rows: [][]any{}}
	queryID := ""
	total, output := 0, 0
	completed := false
	var outstanding *url.URL
	defer func() {
		if !completed && outstanding != nil {
			e.cancel(outstanding)
		}
	}()
	for count := 0; count < 10000; count++ {
		r, err := e.request(ctx, method, &current, body, session)
		if err != nil {
			if ctx.Err() != nil {
				return model.ExecutionSnapshot{}, ctx.Err()
			}
			return model.ExecutionSnapshot{}, problem("analysis_engine_unavailable", "analysis engine request failed")
		}
		data, readErr := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
		r.Body.Close()
		total += len(data)
		if readErr != nil || r.StatusCode != http.StatusOK || len(data) > 1<<20 || total > 8<<20 {
			return model.ExecutionSnapshot{}, problem("analysis_engine_unavailable", "analysis engine response failed its transport budget")
		}
		var pg page
		d := json.NewDecoder(bytes.NewReader(data))
		d.UseNumber()
		if d.Decode(&pg) != nil || d.Decode(new(any)) != io.EOF || pg.ID == "" || (queryID != "" && queryID != pg.ID) {
			return model.ExecutionSnapshot{}, problem("analysis_engine_protocol", "invalid statement page")
		}
		queryID = pg.ID
		if pg.Next != "" {
			next, err := e.next(pg.Next)
			if err != nil {
				return model.ExecutionSnapshot{}, err
			}
			outstanding = next
			current = *next
		} else {
			outstanding = nil
		}
		if len(pg.Error) != 0 && string(pg.Error) != "null" {
			return model.ExecutionSnapshot{}, problem("analysis_engine_failed", "analysis engine rejected the approved query")
		}
		if len(pg.Columns) > 0 {
			if len(pg.Columns) != len(p.Columns) {
				return model.ExecutionSnapshot{}, problem("analysis_engine_protocol", "output schema differs from approved plan")
			}
			for i, c := range pg.Columns {
				want := p.Columns[i]
				if c.Name != want.Name || !compatible(c.Type, want.Type) {
					return model.ExecutionSnapshot{}, problem("analysis_engine_protocol", "output types differ from approved plan")
				}
				if len(result.Columns) > i {
					if result.Columns[i].TypeText != c.Type {
						return model.ExecutionSnapshot{}, problem("analysis_engine_protocol", "output schema changed between pages")
					}
				} else {
					kind := map[model.DataType]string{model.DataTypeInteger: "LONG", model.DataTypeDecimal: "DECIMAL", model.DataTypeString: "STRING", model.DataTypeBoolean: "BOOLEAN", model.DataTypeDate: "DATE"}[want.Type]
					result.Columns = append(result.Columns, model.ResultColumn{Name: c.Name, TypeName: kind, TypeText: c.Type})
				}
			}
		}
		for _, raw := range pg.Data {
			if len(result.Columns) != len(p.Columns) || len(raw) != len(p.Columns) || len(result.Rows) >= p.Query.Limit {
				return model.ExecutionSnapshot{}, problem("analysis_result_too_large", "result exceeds approved schema or row budget")
			}
			row := make([]any, len(raw))
			for i, v := range raw {
				row[i], err = scalar(v, p.Columns[i].Type)
				if err != nil {
					return model.ExecutionSnapshot{}, err
				}
			}
			encoded, _ := json.Marshal(row)
			output += len(encoded)
			if output > 4<<20 {
				return model.ExecutionSnapshot{}, problem("analysis_result_too_large", "result exceeds byte budget")
			}
			result.Rows = append(result.Rows, row)
		}
		if pg.Next == "" {
			if len(result.Columns) != len(p.Columns) {
				return model.ExecutionSnapshot{}, problem("analysis_engine_protocol", "terminal page has no output schema")
			}
			completed = true
			finished := time.Now().UTC()
			return model.ExecutionSnapshot{Job: model.ExecutionJob{ID: queryID, Status: model.JobSucceeded, PhysicalFingerprint: p.Fingerprint, RowLimit: int64(p.Query.Limit), SubmittedAt: started, StartedAt: &started, FinishedAt: &finished}, Result: result}, nil
		}
		method, body, session = http.MethodGet, "", ""
	}
	return model.ExecutionSnapshot{}, problem("analysis_engine_protocol", "statement exceeded page budget")
}
