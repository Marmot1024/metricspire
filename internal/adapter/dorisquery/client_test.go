package dorisquery

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestTransportBudgetStopsBeforeTail(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	c := &boundedConn{Conn: a, remaining: 4}
	done := make(chan error, 1)
	go func() { _, err := b.Write([]byte("1234never-read-tail")); done <- err }()
	buf := make([]byte, 100)
	n, err := c.Read(buf)
	if err != nil || n != 4 {
		t.Fatalf("first read %d %v", n, err)
	}
	if _, err = c.Read(buf); err == nil {
		t.Fatal("budget accepted tail")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("tail was consumed")
		}
	case <-time.After(time.Second):
		t.Fatal("peer not closed")
	}
}

// Fake database driver models mysqlRows.Close draining until the socket closes.
// This exercises the query cleanup order without relying on a cooperating FE.
type tailConnector struct {
	closed   atomic.Bool
	tailRead atomic.Bool
}

func (c *tailConnector) Connect(ctx context.Context) (driver.Conn, error) {
	ctx.Value(socketKey{}).(*socketSlot).set(&tailSocket{c: c})
	return &tailConn{c}, nil
}
func (c *tailConnector) Driver() driver.Driver { return tailDriver{} }

type tailDriver struct{}

func (tailDriver) Open(string) (driver.Conn, error) { return nil, errors.New("unused") }

type tailSocket struct {
	net.Conn
	c *tailConnector
}

func (s *tailSocket) Close() error { s.c.closed.Store(true); return nil }

type tailConn struct{ c *tailConnector }

func (c *tailConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (c *tailConn) Close() error                        { return nil }
func (c *tailConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (c *tailConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &tailRows{c: c.c}, nil
}

type tailRows struct {
	c     *tailConnector
	count int
}

func (r *tailRows) Columns() []string { return []string{"value"} }
func (r *tailRows) Next(v []driver.Value) error {
	if r.count >= 1002 {
		return io.EOF
	}
	r.count++
	v[0] = "1"
	return nil
}
func (r *tailRows) Close() error {
	if !r.c.closed.Load() {
		r.c.tailRead.Store(true)
	}
	return nil
}
func TestRowBudgetAbortsSocketBeforeDriverDrain(t *testing.T) {
	connector := &tailConnector{}
	db := &client{DB: sql.OpenDB(connector), connector: connector, slots: make(chan struct{}, 4)}
	defer db.Close()
	db.SetMaxIdleConns(0)
	_, _, err := query(t.Context(), db, "synthetic")
	requireProblem(t, err, "online_result_too_large")
	if !connector.closed.Load() || connector.tailRead.Load() {
		t.Fatal("partial result drained before socket disposal")
	}
}

func TestQueuedQueriesEachHaveOwnSocket(t *testing.T) {
	connector := &countConnector{}
	db := &client{DB: sql.OpenDB(connector), connector: connector, slots: make(chan struct{}, 4)}
	defer db.Close()
	done := make(chan error, 12)
	for i := 0; i < 12; i++ {
		go func() { _, _, err := query(t.Context(), db, "synthetic"); done <- err }()
	}
	for i := 0; i < 12; i++ {
		if err := <-done; err == nil {
			t.Fatal("overflow accepted")
		}
	}
	if connector.connected.Load() != 12 || connector.closed.Load() != 12 || connector.drained.Load() != 0 {
		t.Fatalf("connections=%d closed=%d drained=%d", connector.connected.Load(), connector.closed.Load(), connector.drained.Load())
	}
}

type countConnector struct{ connected, closed, drained atomic.Int64 }

func (c *countConnector) Driver() driver.Driver { return tailDriver{} }
func (c *countConnector) Connect(ctx context.Context) (driver.Conn, error) {
	slot, ok := ctx.Value(socketKey{}).(*socketSlot)
	if !ok {
		return nil, errors.New("connection missing owning query")
	}
	socket := &countSocket{owner: c}
	slot.set(socket)
	c.connected.Add(1)
	return &countConn{socket}, nil
}

type countSocket struct {
	net.Conn
	owner  *countConnector
	closed atomic.Bool
}

func (s *countSocket) Close() error {
	if s.closed.CompareAndSwap(false, true) {
		s.owner.closed.Add(1)
	}
	return nil
}

type countConn struct{ s *countSocket }

func (c *countConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (c *countConn) Close() error                        { return nil }
func (c *countConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (c *countConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &countRows{s: c.s}, nil
}

type countRows struct {
	s *countSocket
	n int
}

func (r *countRows) Columns() []string { return []string{"value"} }
func (r *countRows) Next(v []driver.Value) error {
	if r.n == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	r.n++
	v[0] = "1"
	return nil
}
func (r *countRows) Close() error {
	if !r.s.closed.Load() {
		r.s.owner.drained.Add(1)
	}
	return nil
}
