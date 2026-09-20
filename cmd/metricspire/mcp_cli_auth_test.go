package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/httpapi"
)

// These opt-in tests reuse the user's Databricks CLI OAuth login. They never
// print, persist, or accept a token in an environment variable or command
// argument. The query test uses only the existing bounded TPCH fixture.
func TestMCPRemoteStagingCLIAuth(t *testing.T) {
	mode := os.Getenv("METRICSPIRE_RUN_MCP_CLI_ACCEPTANCE")
	if mode != "staging-auth-only" && mode != "staging-read-only" {
		t.Skip("set METRICSPIRE_RUN_MCP_CLI_ACCEPTANCE=staging-auth-only for the staging App")
	}
	origin, profile := stagingCLIInputs(t)
	authorization := stagingCLIHelperHeader(t, profile)

	client := &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, origin+"/api/v1/ui/context", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", authorization)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("staging App identity request failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("staging App identity status=%d, want 200 (no browser redirect)", response.StatusCode)
	}
	var identity httpapi.UIContext
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<16)).Decode(&identity); err != nil {
		t.Fatalf("decode staging App identity: %v", err)
	}
	if identity.AuthenticationProfile != "databricks_apps" || strings.TrimSpace(identity.DisplayName) == "" || !slices.Contains(identity.Permissions, httpapi.PermissionQuery) {
		t.Fatal("staging App did not establish a Databricks user with query permission")
	}

	const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"auth-probe","version":"1"}}}`
	initializeRequest := func(t *testing.T) *http.Request {
		t.Helper()
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, origin+"/api/v1/mcp", bytes.NewBufferString(initialize))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		return request
	}
	t.Run("MCP accepts the user token", func(t *testing.T) {
		request := initializeRequest(t)
		request.Header.Set("Authorization", authorization)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("authenticated MCP initialize status=%d, want 200", response.StatusCode)
		}
	})
	for _, probe := range []struct {
		name    string
		headers http.Header
	}{
		{name: "missing token with a forged session", headers: http.Header{"Mcp-Session-Id": {"forged-session"}}},
		{name: "invalid bearer", headers: http.Header{"Authorization": {"Bearer invalid-staging-probe-token"}}},
		{name: "spoofed forwarded identity without bearer", headers: http.Header{
			"X-Forwarded-Access-Token": {"forged-user-token"}, "X-Forwarded-User": {"attacker"}, "X-Forwarded-Email": {"attacker@example.invalid"},
		}},
	} {
		t.Run(probe.name, func(t *testing.T) {
			request := initializeRequest(t)
			for name, values := range probe.headers {
				request.Header[name] = values
			}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusUnauthorized {
				t.Fatalf("MCP rejected-credential probe status=%d, want 401 without a redirect", response.StatusCode)
			}
		})
	}
	t.Run("forged forwarded identity cannot replace the bearer user", func(t *testing.T) {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, origin+"/api/v1/ui/context", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", authorization)
		request.Header.Set("X-Forwarded-Access-Token", "forged-user-token")
		request.Header.Set("X-Forwarded-User", "attacker")
		request.Header.Set("X-Forwarded-Email", "attacker@example.invalid")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return // Fail-closed is also safe if the ingress rejects spoofed headers.
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("spoofed-header probe status=%d, want identity unchanged or rejection", response.StatusCode)
		}
		var actual httpapi.UIContext
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<16)).Decode(&actual); err != nil {
			t.Fatal(err)
		}
		if actual.DisplayName != identity.DisplayName || actual.AuthenticationProfile != identity.AuthenticationProfile || !reflect.DeepEqual(actual.Permissions, identity.Permissions) {
			t.Fatal("spoofed forwarded headers changed the authenticated user or permissions")
		}
	})
	t.Log("CLI user reached MCP; missing/invalid credentials and forged forwarded identity did not grant access")
}

func TestMCPRemoteStagingCLIQuery(t *testing.T) {
	if os.Getenv("METRICSPIRE_RUN_MCP_CLI_ACCEPTANCE") != "staging-read-only" {
		t.Skip("set METRICSPIRE_RUN_MCP_CLI_ACCEPTANCE=staging-read-only for the fixed read-only query")
	}
	origin, profile := stagingCLIInputs(t)
	token := stagingCLIToken(t, profile)
	session := mcpHTTP(t, origin+"/api/v1/mcp", token)
	verifyMCPStagingAcceptance(t, session, "Databricks CLI user -> remote HTTP")
}

func stagingCLIInputs(t *testing.T) (string, string) {
	t.Helper()
	origin := strings.TrimSuffix(strings.TrimSpace(os.Getenv("METRICSPIRE_MCP_TEST_URL")), "/")
	profile := strings.TrimSpace(os.Getenv("METRICSPIRE_MCP_TEST_CLI_PROFILE"))
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" ||
		!strings.HasSuffix(parsed.Hostname(), ".databricksapps.com") || !strings.Contains(parsed.Hostname(), "-staging-") || profile == "" || strings.HasPrefix(profile, "-") {
		t.Fatal("an HTTPS staging Databricks App origin and explicit CLI profile are required")
	}
	return origin, profile
}

func stagingCLIToken(t *testing.T, profile string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	token, err := databricksProfileAccessToken(ctx, profile)
	if err != nil {
		t.Fatal("Databricks CLI user token unavailable; run databricks auth login for the staging workspace and retry")
	}
	return token
}

func stagingCLIHelperHeader(t *testing.T, profile string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", filepath.Join("..", "..", "tools", "databricks_mcp_headers.py"), "--profile", profile)
	output, err := command.Output()
	if err != nil {
		t.Fatal("local Databricks CLI header helper failed; confirm the staging CLI profile is logged in")
	}
	var headers map[string]string
	if err := json.Unmarshal(output, &headers); err != nil || len(headers) != 1 || !strings.HasPrefix(headers["Authorization"], "Bearer ") {
		t.Fatal("local Databricks CLI header helper returned invalid headers")
	}
	return headers["Authorization"]
}
