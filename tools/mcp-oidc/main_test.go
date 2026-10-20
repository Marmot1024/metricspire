package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureConnection() connection {
	return connection{URL: "https://metrics.example.com/mcp", Issuer: "https://identity.example.com/realms/demo", ClientID: "public-mcp", Scopes: []string{"metrics", "offline_access"}}
}
func TestOIDCInstallPreservesOtherConnectionsAndRefusesConflicts(t *testing.T) {
	c := fixtureConnection()
	p := filepath.Join(t.TempDir(), "config.toml")
	original := []byte("# retain comment\n[mcp_servers.existing]\nurl = 'https://other.example.com/mcp'\n")
	if err := os.WriteFile(p, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := install(p, c, false); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(after), string(original)) {
		t.Fatal("unrelated config replaced")
	}
	backup, _ := os.ReadFile(p + ".metricspire-oidc.backup")
	if string(backup) != string(original) {
		t.Fatal("backup differs")
	}
	if err := install(p, c, true); err != nil {
		t.Fatal(err)
	}
	c.URL = "https://another.example.com/mcp"
	if err := install(p, c, false); err == nil {
		t.Fatal("conflicting connection replaced")
	}
	same, _ := os.ReadFile(p)
	if string(same) != string(after) {
		t.Fatal("conflict mutated config")
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0600 {
		t.Fatal("config not private")
	}
}
func TestOIDCBundleRejectsCredentialAndTransportOverrides(t *testing.T) {
	for _, change := range []func(*connection){func(c *connection) { c.URL = "http://example.com/mcp" }, func(c *connection) { c.Issuer = "https://user:secret@identity.example.com" }, func(c *connection) { c.ClientID = "" }, func(c *connection) { c.Scopes = []string{"one two"} }, func(c *connection) { c.URL += "?token=secret" }} {
		c := fixtureConnection()
		change(&c)
		if c.validate(false) == nil {
			t.Fatal("unsafe connection accepted")
		}
	}
	for _, entry := range []string{`http_headers = {Authorization='secret'}`, `http_headers_helper = 'custom'`, `command = 'other'`, `enabled = false`} {
		raw := `[mcp_servers.metricspire-oidc]
url = 'https://metrics.example.com/mcp'
` + entry + `
[mcp_servers.metricspire-oidc.oauth]
client_id = 'public-mcp'
`
		if _, err := checkEntry([]byte(raw), fixtureConnection()); err == nil {
			t.Fatal("mixed/conflicting connection accepted")
		}
	}
}
func TestOIDCDiscoveryPinsResourceIssuerAndPKCE(t *testing.T) {
	for _, bad := range []string{"", "resource", "issuer", "pkce", "redirect"} {
		t.Run(bad, func(t *testing.T) {
			c := fixtureConnection()
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if bad == "redirect" {
					http.Redirect(w, r, "https://unreviewed.example.com", 302)
					return
				}
				if strings.Contains(r.URL.Path, "oauth-protected-resource") {
					resource := c.URL
					if bad == "resource" {
						resource += "/wrong"
					}
					json.NewEncoder(w).Encode(map[string]any{"resource": resource, "authorization_servers": []string{c.Issuer}})
					return
				}
				issuer := c.Issuer
				if bad == "issuer" {
					issuer += "/wrong"
				}
				methods := []string{"S256"}
				if bad == "pkce" {
					methods = []string{"plain"}
				}
				json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": server.URL + "/authorize", "token_endpoint": server.URL + "/token", "code_challenge_methods_supported": methods})
			}))
			defer server.Close()
			c.URL = server.URL + "/mcp"
			c.Issuer = server.URL
			err := c.discover(context.Background(), true)
			if (err != nil) != (bad != "") {
				t.Fatalf("discovery %s: %v", bad, err)
			}
		})
	}
}

func TestClientEnvironmentPinsAbsoluteHomeWithoutChangingOtherValues(t *testing.T) {
	original := []string{"CODEX_HOME=relative", "PATH=/bin", "HOME=/original"}
	got := clientEnvironment(original, "/absolute/client")
	if strings.Join(got, "|") != "PATH=/bin|HOME=/original|CODEX_HOME=/absolute/client" || original[0] != "CODEX_HOME=relative" {
		t.Fatal("child environment not isolated")
	}
}
