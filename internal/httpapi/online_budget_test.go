package httpapi

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/application"
)

type budgetOnlineFunc func(context.Context, application.QueryScope, application.OnlineQuery) (application.OnlineResult, error)

func (f budgetOnlineFunc) Execute(ctx context.Context, s application.QueryScope, q application.OnlineQuery) (application.OnlineResult, error) {
	return f(ctx, s, q)
}

func onlineBudgetHandler(auth Authenticator, service OnlineQueryService) http.Handler {
	s := &Server{config: Config{OnlineTimeout: 60 * time.Millisecond, MaxBodyBytes: DefaultMaxBodyBytes}, deps: Dependencies{Authenticator: auth, Online: service}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return http.HandlerFunc(s.handleOnlineQuery)
}

func TestOnlineHTTPBudgetIncludesAuthentication(t *testing.T) {
	auth := AuthenticatorFunc(func(ctx context.Context, _ *http.Request) (Principal, error) {
		<-ctx.Done()
		return Principal{}, ErrUnauthenticated
	})
	h := onlineBudgetHandler(auth, nil)
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 504 || !strings.Contains(w.Body.String(), `"timeout"`) || w.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("auth timeout misreported: %d %s", w.Code, w.Body.String())
	}
}

func TestOnlineHTTPExecutionGetsRemainingBudgetAndRejectsLateResult(t *testing.T) {
	auth := AuthenticatorFunc(func(ctx context.Context, _ *http.Request) (Principal, error) {
		select {
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			return Principal{}, ctx.Err()
		}
		return Principal{Tenant: "demo", Subject: "test", Permissions: []Permission{PermissionQuery}}, nil
	})
	called := false
	h := onlineBudgetHandler(auth, budgetOnlineFunc(func(ctx context.Context, _ application.QueryScope, _ application.OnlineQuery) (application.OnlineResult, error) {
		called = true
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 45*time.Millisecond {
			t.Fatal("execution got a new budget after authentication")
		}
		<-ctx.Done()
		return application.OnlineResult{}, nil // a misbehaving dependency's late success
	}))
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !called || w.Code != 504 || !strings.Contains(w.Body.String(), `"timeout"`) {
		t.Fatalf("late success delivered: %d %s", w.Code, w.Body.String())
	}
}

func TestOnlineHTTPBudgetBoundsSlowBodyOnRealConnection(t *testing.T) {
	auth := AuthenticatorFunc(func(context.Context, *http.Request) (Principal, error) {
		return Principal{Tenant: "demo", Subject: "test", Permissions: []Permission{PermissionQuery}}, nil
	})
	h := onlineBudgetHandler(auth, budgetOnlineFunc(func(context.Context, application.QueryScope, application.OnlineQuery) (application.OnlineResult, error) {
		t.Error("incomplete body reached execution")
		return application.OnlineResult{}, nil
	}))
	server := httptest.NewServer(h)
	defer server.Close()
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(server.URL, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err = io.WriteString(conn, "POST / HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 100\r\nConnection: close\r\n\r\n{"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 504 || !strings.Contains(string(body), `"timeout"`) {
		t.Fatalf("slow body: %d %s", response.StatusCode, body)
	}
}
