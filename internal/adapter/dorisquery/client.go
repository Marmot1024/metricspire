// Package dorisquery reads reviewed aggregate snapshots through Doris' MySQL protocol.
package dorisquery

import (
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"errors"
	"net"
	"net/url"
	"sync"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/marmot1024/metricspire/internal/model"
)

const EngineName = "doris_online"

func problem(code, message string) *model.Problem {
	return &model.Problem{Code: code, Message: message}
}
func unavailable() error {
	return problem("online_engine_unavailable", "Doris could not complete the bounded read")
}
func text(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

type client struct {
	*sql.DB
	connector sqldriver.Connector
	slots     chan struct{}
}

func open(ctx context.Context, raw string) (*client, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "doris" || u.Host == "" || u.User == nil || u.User.Username() == "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("invalid Doris connection configuration")
	}
	password, ok := u.User.Password()
	if !ok || password == "" {
		return nil, errors.New("Doris requires a dedicated read credential")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, errors.New("invalid Doris connection options")
	}
	for k, v := range q {
		if k != "tls" || len(v) != 1 {
			return nil, errors.New("Doris URL supports only tls=true/false")
		}
	}
	tls := q.Get("tls")
	if tls == "" {
		tls = "true"
	}
	ip := net.ParseIP(u.Hostname())
	if tls != "true" && !(tls == "false" && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback()))) {
		return nil, errors.New("Doris requires verified TLS except disposable loopback tests")
	}
	c := driver.NewConfig()
	c.User = u.User.Username()
	c.Passwd = password
	c.Net = "tcp"
	c.Addr = u.Host
	if u.Port() == "" {
		c.Addr = net.JoinHostPort(u.Hostname(), "9030")
	}
	c.TLSConfig = tls
	c.Collation = "utf8mb4_bin"
	c.InterpolateParams = true
	c.MaxAllowedPacket = 8 << 20
	c.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		bounded := &boundedConn{Conn: conn, remaining: 8 << 20}
		if slot, ok := ctx.Value(socketKey{}).(*socketSlot); ok {
			slot.set(bounded)
		}
		return bounded, nil
	}
	c.Timeout = 5 * time.Second
	c.ReadTimeout = 3 * time.Second
	c.WriteTimeout = 3 * time.Second
	c.Params = map[string]string{"time_zone": "'+00:00'"}
	connector, err := driver.NewConnector(c)
	if err != nil {
		return nil, errors.New("invalid Doris connection")
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(4)
	// A fresh connection gives each read its own transport budget and allows
	// immediate socket disposal on a partial result (Close must never drain it).
	db.SetMaxIdleConns(0)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, unavailable()
	}
	return &client{DB: db, connector: connector, slots: make(chan struct{}, 4)}, nil
}

// Read all values as nullable text: no floating-point decoding of DECIMAL or BIGINT.
// Every query has an 8 MiB transport budget, including handshake/TLS overhead.
// The protocol's individual packet allocation can reach 16 MiB before a read
// fails. No partial result is returned and a rejected response is never drained.
func query(ctx context.Context, client *client, statement string, args ...any) ([][]*string, []string, error) {
	select {
	case client.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	defer func() { <-client.slots }()
	// One query per pool: database/sql cannot hand this socket to another waiter.
	// Merely setting MaxIdleConns(0) on a shared pool would allow direct handoff.
	db := sql.OpenDB(client.connector)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	defer db.Close()
	slot := &socketSlot{}
	ctx = context.WithValue(ctx, socketKey{}, slot)
	rows, err := db.QueryContext(ctx, statement, args...)
	if err != nil {
		slot.abort()
		return nil, nil, safeError(ctx)
	}
	complete := false
	defer func() {
		if !complete {
			slot.abort()
		}
		rows.Close()
	}()
	columns, err := rows.Columns()
	if err != nil || len(columns) > 256 {
		return nil, nil, unavailable()
	}
	out := [][]*string{}
	used := 0
	for rows.Next() {
		if len(out) >= 1001 {
			return nil, nil, problem("online_result_too_large", "Doris result exceeds row budget")
		}
		values := make([]sql.NullString, len(columns))
		dest := make([]any, len(values))
		for i := range values {
			dest[i] = &values[i]
		}
		if rows.Scan(dest...) != nil {
			return nil, nil, unavailable()
		}
		row := make([]*string, len(values))
		for i, v := range values {
			if v.Valid {
				copy := v.String
				row[i] = &copy
				used += len(copy)
			} else {
				used++
			}
		}
		if used > 8<<20 {
			return nil, nil, problem("online_result_too_large", "Doris result exceeds byte budget")
		}
		out = append(out, row)
	}
	if rows.Err() != nil {
		return nil, nil, safeError(ctx)
	}
	complete = true
	return out, columns, nil
}
func safeError(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return unavailable()
}
func quote(s string) string { return "`" + s + "`" }

// Bound the underlying transport rather than the driver's write-only packet
// setting. Closing the socket before rows.Close prevents MySQL tail draining.
type boundedConn struct {
	net.Conn
	remaining int64
}

func (c *boundedConn) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		c.Conn.Close()
		return 0, errors.New("Doris transport budget exceeded")
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.Conn.Read(p)
	c.remaining -= int64(n)
	return n, err
}

type socketKey struct{}
type socketSlot struct {
	mu   sync.Mutex
	conn net.Conn
}

func (s *socketSlot) set(c net.Conn) { s.mu.Lock(); s.conn = c; s.mu.Unlock() }
func (s *socketSlot) abort() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		s.conn.Close()
	}
}
