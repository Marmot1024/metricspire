// Package clickhousequery reads bounded, reviewed aggregate snapshots over HTTP.
package clickhousequery

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/model"
)

const EngineName = "clickhouse_online"
const maxWireBytes = 8 << 20

type client struct {
	endpoint, user, password string
	http                     *http.Client
}

func newClient(raw string) (*client, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Hostname() == "" || u.User == nil || u.User.Username() == "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("invalid ClickHouse HTTP connection configuration")
	}
	password, ok := u.User.Password()
	if !ok || password == "" {
		return nil, errors.New("ClickHouse requires a dedicated read credential")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback()))) {
		return nil, errors.New("ClickHouse requires HTTPS except on loopback")
	}
	username := u.User.Username()
	u.User = nil
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // Do not forward deployment credentials through environment proxies.
	transport.MaxConnsPerHost = 4
	transport.MaxIdleConnsPerHost = 4
	return &client{endpoint: u.String(), user: username, password: password, http: &http.Client{Transport: transport, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// Values and SQL travel in multipart bodies, never URL parameters or errors.
// ClickHouse reads format and settings from URL parameters, not multipart fields.
// No retry: an interrupted read must not silently issue duplicate remote work.
func (c *client) query(ctx context.Context, sql string, params map[string]string, bounded bool) ([][]*string, error) {
	idbytes := make([]byte, 16)
	if _, err := rand.Read(idbytes); err != nil {
		return nil, err
	}
	id := "metricspire_" + hex.EncodeToString(idbytes)
	settings := map[string]string{"query_id": id, "default_format": "JSONCompactEachRow", "output_format_json_quote_64bit_integers": "1", "output_format_json_quote_decimals": "1"}
	if bounded {
		for k, v := range map[string]string{"readonly": "1", "max_execution_time": "2", "timeout_before_checking_execution_speed": "0", "timeout_overflow_mode": "throw", "max_rows_to_read": "50000000", "read_overflow_mode": "throw", "max_memory_usage": "268435456", "max_threads": "2", "max_result_rows": "1001", "max_result_bytes": "8388608", "result_overflow_mode": "throw", "cancel_http_readonly_queries_on_client_close": "1", "use_query_cache": "0"} {
			settings[k] = v
		}
	}
	fields := map[string]string{"query": sql}
	for k, v := range params {
		fields["param_"+k] = v
	}
	options := url.Values{}
	for k, v := range settings {
		options.Set(k, v)
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for k, v := range fields {
		if err := form.WriteField(k, v); err != nil {
			return nil, err
		}
	}
	if err := form.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"?"+options.Encode(), &body)
	if err != nil {
		return nil, unavailable()
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.SetBasicAuth(c.user, c.password)
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, unavailable()
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("X-ClickHouse-Exception-Code") != "" {
		return nil, unavailable()
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxWireBytes+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, unavailable()
	}
	if len(raw) > maxWireBytes {
		return nil, problem("online_result_too_large", "ClickHouse response exceeds byte budget")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	out := [][]*string{}
	for {
		var row []*string
		err := decoder.Decode(&row)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, unavailable()
		}
		if len(out) >= 1001 {
			return nil, problem("online_result_too_large", "ClickHouse response exceeds row budget")
		}
		out = append(out, row)
	}
	// HTTP 200 may carry a late exception. Full decoding above rejects any suffix.
	return out, nil
}
func (c *client) close() { c.http.CloseIdleConnections() }
func unavailable() error {
	return problem("online_engine_unavailable", "ClickHouse could not complete the bounded read")
}
func problem(code, message string) *model.Problem {
	return &model.Problem{Code: code, Message: message, Path: "online_query"}
}
func text(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
func quote(v string) string { return "`" + strings.ReplaceAll(v, "`", "``") + "`" }
