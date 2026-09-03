package lakebase

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type tokenSourceFunc func(context.Context) (string, error)

func (function tokenSourceFunc) Token(ctx context.Context) (string, error) { return function(ctx) }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestCredentialProviderRequestsFreshBoundedDatabaseCredential(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	calls := 0
	provider, err := NewCredentialProvider(CredentialConfig{
		Host:     "https://workspace.example.com",
		Endpoint: "projects/project-1/branches/main/endpoints/primary",
		Tokens: tokenSourceFunc(func(context.Context) (string, error) {
			calls++
			return "workspace-token", nil
		}),
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Method != http.MethodPost || request.URL.Path != "/api/2.0/postgres/credentials" ||
				request.Header.Get("Authorization") != "Bearer workspace-token" {
				t.Fatalf("credential request = %s %s, authorization %q", request.Method, request.URL, request.Header.Get("Authorization"))
			}
			var payload map[string]string
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["endpoint"] != "projects/project-1/branches/main/endpoints/primary" || payload["ttl"] != "3600s" {
				t.Fatalf("credential payload = %#v", payload)
			}
			return jsonResponse(http.StatusOK, `{"token":"database-token","expire_time":"2026-09-03T13:00:00Z"}`), nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider.now = func() time.Time { return now }
	for range 2 {
		password, err := provider.Password(context.Background())
		if err != nil || password != "database-token" {
			t.Fatalf("Password() = %q, %v", password, err)
		}
	}
	if calls != 2 {
		t.Fatalf("workspace token calls = %d, want 2", calls)
	}
}

func TestCredentialProviderFailsClosedWithoutLeakingTokensOrBodies(t *testing.T) {
	const secret = "must-not-leak"
	provider, err := NewCredentialProvider(CredentialConfig{
		Host:     "https://workspace.example.com",
		Endpoint: "projects/project-1/branches/main/endpoints/primary",
		Tokens:   tokenSourceFunc(func(context.Context) (string, error) { return secret, nil }),
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusForbidden, `{"message":"`+secret+`"}`), nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Password(context.Background())
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("Password() error = %v", err)
	}

	provider.tokens = tokenSourceFunc(func(context.Context) (string, error) { return "", errors.New(secret) })
	_, err = provider.Password(context.Background())
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("Password() token-source error = %v", err)
	}
}

func TestCredentialProviderRejectsUnsafeConfigurationAndExpiry(t *testing.T) {
	valid := CredentialConfig{
		Host:     "https://workspace.example.com",
		Endpoint: "projects/project-1/branches/main/endpoints/primary",
		Tokens:   tokenSourceFunc(func(context.Context) (string, error) { return "workspace-token", nil }),
	}
	for name, mutate := range map[string]func(*CredentialConfig){
		"insecure host":   func(config *CredentialConfig) { config.Host = "http://workspace.example.com" },
		"endpoint path":   func(config *CredentialConfig) { config.Endpoint = "projects/project-1/endpoints/primary" },
		"endpoint escape": func(config *CredentialConfig) { config.Endpoint = "projects/../branches/main/endpoints/primary" },
		"missing tokens":  func(config *CredentialConfig) { config.Tokens = nil },
	} {
		t.Run(name, func(t *testing.T) {
			config := valid
			mutate(&config)
			if _, err := NewCredentialProvider(config); err == nil {
				t.Fatal("unsafe Lakebase credential configuration was accepted")
			}
		})
	}

	provider, err := NewCredentialProvider(valid)
	if err != nil {
		t.Fatal(err)
	}
	provider.now = func() time.Time { return time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC) }
	provider.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"token":"database-token","expire_time":"2026-09-03T12:04:59Z"}`), nil
	})}
	if _, err := provider.Password(context.Background()); err == nil {
		t.Fatal("nearly expired Lakebase credential was accepted")
	}
}

func TestCredentialProviderNormalizesAppsWorkspaceHost(t *testing.T) {
	for _, host := range []string{"workspace.example.com", "https://workspace.example.com/"} {
		t.Run(host, func(t *testing.T) {
			provider, err := NewCredentialProvider(CredentialConfig{
				Host: host, Endpoint: "projects/project-1/branches/main/endpoints/primary",
				Tokens: tokenSourceFunc(func(context.Context) (string, error) { return "workspace-token", nil }),
			})
			if err != nil {
				t.Fatal(err)
			}
			if provider.endpoint != "https://workspace.example.com/api/2.0/postgres/credentials" {
				t.Fatalf("credential endpoint = %q", provider.endpoint)
			}
		})
	}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
