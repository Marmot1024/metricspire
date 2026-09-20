package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/httpapi"
	"github.com/marmot1024/metricspire/internal/runtimeconfig"
)

func TestLoadServeEnvironmentRejectsMissingAndPartialSecrets(t *testing.T) {
	values := map[string]string{}
	getenv := func(name string) string { return values[name] }
	if _, err := loadServeEnvironment(getenv, runtimeconfig.AuthenticationOIDC); err == nil || !strings.Contains(err.Error(), "METRICSPIRE_DATABASE_URL") {
		t.Fatalf("missing database URL error = %v", err)
	}
	values["METRICSPIRE_DATABASE_URL"] = "postgres://localhost/metricspire"
	values["METRICSPIRE_SESSION_KEY"] = base64.StdEncoding.EncodeToString(make([]byte, 32))
	values["DATABRICKS_HOST"] = "https://workspace.example"
	values["DATABRICKS_SQL_WAREHOUSE_ID"] = "warehouse"
	values["DATABRICKS_CLIENT_ID"] = "client"
	if _, err := loadServeEnvironment(getenv, runtimeconfig.AuthenticationOIDC); err == nil || !strings.Contains(err.Error(), "must both be set") {
		t.Fatalf("partial Databricks OAuth error = %v", err)
	}
	values["DATABRICKS_CLIENT_SECRET"] = "secret"
	environment, err := loadServeEnvironment(getenv, runtimeconfig.AuthenticationOIDC)
	if err != nil {
		t.Fatal(err)
	}
	if len(environment.sessionKey) != 32 || environment.databricksHost == "" || environment.warehouseID == "" {
		t.Fatalf("serve environment = %#v", environment)
	}
}

func TestLoadServeEnvironmentForAppsDoesNotAcceptOrRequireServiceCredentialFallback(t *testing.T) {
	values := map[string]string{
		"METRICSPIRE_DATABASE_URL": "postgres://localhost/metricspire",
		"DATABRICKS_HOST":          "https://workspace.example", "DATABRICKS_SQL_WAREHOUSE_ID": "warehouse",
	}
	environment, err := loadServeEnvironment(func(name string) string { return values[name] }, runtimeconfig.AuthenticationDatabricksApps)
	if err != nil {
		t.Fatal(err)
	}
	if len(environment.sessionKey) != 0 || environment.databricksHost == "" || environment.warehouseID == "" {
		t.Fatalf("Apps serve environment = %#v", environment)
	}
}

func TestLoadServeEnvironmentForAppsAcceptsExactlyOneLakebaseConfiguration(t *testing.T) {
	values := map[string]string{
		"METRICSPIRE_LAKEBASE_ENDPOINT": "projects/project-1/branches/production/endpoints/primary",
		"DATABRICKS_HOST":               "https://workspace.example",
		"DATABRICKS_SQL_WAREHOUSE_ID":   "warehouse",
	}
	getenv := func(name string) string { return values[name] }
	environment, err := loadServeEnvironment(getenv, runtimeconfig.AuthenticationDatabricksApps)
	if err != nil {
		t.Fatal(err)
	}
	if environment.databaseURL != "" || environment.databricksHost == "" {
		t.Fatalf("Apps Lakebase environment = %#v", environment)
	}
	values["METRICSPIRE_DATABASE_URL"] = "postgres://localhost/metricspire"
	if _, err := loadServeEnvironment(getenv, runtimeconfig.AuthenticationDatabricksApps); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("ambiguous Apps database configuration error = %v", err)
	}
	delete(values, "METRICSPIRE_DATABASE_URL")
	delete(values, "METRICSPIRE_LAKEBASE_ENDPOINT")
	if _, err := loadServeEnvironment(getenv, runtimeconfig.AuthenticationDatabricksApps); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("missing Apps database configuration error = %v", err)
	}
}

func TestDecodeSessionKeyRequiresExactly32RandomBytes(t *testing.T) {
	for _, size := range []int{0, 16, 31, 33, 64} {
		value := base64.StdEncoding.EncodeToString(make([]byte, size))
		if _, err := decodeSessionKey(value); err == nil {
			t.Fatalf("accepted %d-byte session key", size)
		}
	}
	want := []byte("0123456789abcdef0123456789abcdef")
	for _, value := range []string{
		base64.StdEncoding.EncodeToString(want),
		base64.RawURLEncoding.EncodeToString(want),
	} {
		got, err := decodeSessionKey(value)
		if err != nil || string(got) != string(want) {
			t.Fatalf("decodeSessionKey(%q) = %x, %v", value, got, err)
		}
	}
}

func TestResolveHTTPAddressUsesOnlyAValidTrustedOverride(t *testing.T) {
	for _, test := range []struct {
		name       string
		configured string
		override   string
		want       string
		wantError  bool
	}{
		{name: "configured", configured: "127.0.0.1:8080", want: "127.0.0.1:8080"},
		{name: "override", configured: "127.0.0.1:8080", override: "0.0.0.0:9000", want: "0.0.0.0:9000"},
		{name: "trimmed", configured: "127.0.0.1:8080", override: " [::]:9000 ", want: "[::]:9000"},
		{name: "invalid configured", configured: "8080", wantError: true},
		{name: "invalid override", configured: "127.0.0.1:8080", override: "0.0.0.0", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveHTTPAddress(test.configured, test.override)
			if test.wantError {
				if err == nil {
					t.Fatalf("resolveHTTPAddress() = %q, want error", got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("resolveHTTPAddress() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestRuntimeExampleLoadsDatabricksTrustedRoutes(t *testing.T) {
	configPath := filepath.Join("..", "..", "examples", "runtime.example.yaml")
	config, err := runtimeconfig.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	policies, bindings, err := loadTrustedRoutes(filepath.Dir(configPath), config)
	if err != nil {
		t.Fatal(err)
	}
	if len(policies) != 1 || len(bindings) != 1 || policies[0].Tenant != "acceptance" ||
		bindings[0].Binding.Engine != "databricks_sql" || config.Authentication.OIDC == nil || config.Authentication.OIDC.BearerAudience != "metricspire-api" {
		t.Fatalf("trusted routes = %#v %#v", policies, bindings)
	}
}

func TestDatabricksAppsRuntimeExampleLoadsFailClosedProfile(t *testing.T) {
	config, err := runtimeconfig.Load(filepath.Join("..", "..", "examples", "runtime.databricks-apps.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	apps := config.Authentication.DatabricksApps
	if config.Authentication.Provider != runtimeconfig.AuthenticationDatabricksApps || apps == nil ||
		apps.ExpectedAppName == "" || apps.ExpectedWorkspaceID == "" || apps.QueryRole != "analyst" || apps.PublisherGroupID == "" {
		t.Fatalf("Databricks Apps runtime example = %#v", config.Authentication)
	}
}

func TestConfiguredUIModelsKeepsRouteOrderAndRemovesDuplicates(t *testing.T) {
	routes := []runtimeconfig.BindingRoute{
		{Namespace: "acceptance", ModelName: "tpch_orders"},
		{Namespace: "demo", ModelName: "commerce"},
		{Namespace: "acceptance", ModelName: "tpch_orders"},
	}
	want := []httpapi.UIModelRoute{
		{Namespace: "acceptance", ModelName: "tpch_orders"},
		{Namespace: "demo", ModelName: "commerce"},
	}
	got := configuredUIModels(routes)
	if len(got) != len(want) {
		t.Fatalf("configuredUIModels() = %#v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("configuredUIModels()[%d] = %#v, want %#v", index, got[index], want[index])
		}
	}
}

func TestMCPAuthorizationServerFollowsAuthenticationProvider(t *testing.T) {
	oidc := runtimeconfig.Config{Authentication: runtimeconfig.AuthenticationConfig{
		Provider: runtimeconfig.AuthenticationOIDC,
		OIDC:     &runtimeconfig.OIDCConfig{IssuerURL: "https://identity.example.com/"},
	}}
	if got := mcpAuthorizationServer(oidc, serveEnvironment{databricksHost: "https://workspace.example.com"}); got != "https://identity.example.com/" {
		t.Fatalf("OIDC authorization server = %q", got)
	}
	apps := runtimeconfig.Config{Authentication: runtimeconfig.AuthenticationConfig{Provider: runtimeconfig.AuthenticationDatabricksApps}}
	if got := mcpAuthorizationServer(apps, serveEnvironment{databricksHost: "https://workspace.example.com/"}); got != "https://workspace.example.com/oidc" {
		t.Fatalf("Apps authorization server = %q", got)
	}
}

func TestTrialReleasesRequireConfiguredNamespacesAndDeploymentAcknowledgement(t *testing.T) {
	config := runtimeconfig.Config{
		ReleasePolicy: runtimeconfig.ReleasePolicyConfig{TrialNamespaces: []string{"matchingstory"}},
	}
	value := ""
	getenv := func(string) string { return value }
	if _, err := enabledTrialNamespaces(config, getenv); err == nil {
		t.Fatal("configured trial namespaces were accepted without deployment acknowledgement")
	}
	value = "approved-trial"
	if enabled, err := enabledTrialNamespaces(config, getenv); err != nil || len(enabled) != 1 || enabled[0] != "matchingstory" {
		t.Fatalf("enabled trial namespaces=%v, %v", enabled, err)
	}
	config.ReleasePolicy.TrialNamespaces = nil
	if _, err := enabledTrialNamespaces(config, getenv); err == nil {
		t.Fatal("deployment acknowledgement without configured namespaces was accepted")
	}
}

func TestServeStartsOIDCLoginAndStopsGracefullyWithPostgres(t *testing.T) {
	databaseURL := os.Getenv("METRICSPIRE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("METRICSPIRE_TEST_DATABASE_URL is not set")
	}
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/authorize",
				"token_endpoint": issuer.URL + "/token", "jwks_uri": issuer.URL + "/keys",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		default:
			http.NotFound(response, request)
		}
	}))
	defer issuer.Close()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	config := runtimeconfig.Config{
		APIVersion: runtimeconfig.APIVersion, Kind: runtimeconfig.Kind,
		HTTP: runtimeconfig.HTTPConfig{
			Address: "127.0.0.1:0", PublicURL: "http://127.0.0.1:3000",
			ControlTimeout: "2s", QueryTimeout: "2s",
		},
		Authentication: runtimeconfig.AuthenticationConfig{
			Provider: runtimeconfig.AuthenticationOIDC,
			OIDC: &runtimeconfig.OIDCConfig{
				IssuerURL: issuer.URL, ClientID: "metricspire-test", BearerAudience: "metricspire-api", DevelopmentAllowInsecureHTTP: true,
			},
		},
		Policies: []runtimeconfig.PolicyRoute{{
			Namespace: "acceptance", ModelName: "tpch_orders", Tenant: "acceptance",
			Path: filepath.Join(root, "testdata", "acceptance", "databricks-tpch", "policy.yaml"),
		}},
		Bindings: []runtimeconfig.BindingRoute{{
			Namespace: "acceptance", ModelName: "tpch_orders",
			Path: filepath.Join(root, "testdata", "acceptance", "databricks-tpch", "binding.json"),
		}},
	}
	configPath := filepath.Join(t.TempDir(), "runtime.json")
	if err := contractio.WriteJSON(configPath, config); err != nil {
		t.Fatal(err)
	}
	t.Setenv("METRICSPIRE_DATABASE_URL", databaseURL)
	t.Setenv("METRICSPIRE_SESSION_KEY", base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	t.Setenv("METRICSPIRE_OIDC_CLIENT_SECRET", "")
	t.Setenv("DATABRICKS_HOST", "https://workspace.example")
	t.Setenv("DATABRICKS_SQL_WAREHOUSE_ID", "warehouse")
	t.Setenv("DATABRICKS_CLIENT_ID", "")
	t.Setenv("DATABRICKS_CLIENT_SECRET", "")
	t.Setenv("DATABRICKS_TOKEN", "local-test-token")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	statuses := make(chan map[string]string, 2)
	decodeErrors := make(chan error, 1)
	go func() {
		decoder := json.NewDecoder(reader)
		for range 2 {
			var status map[string]string
			if err := decoder.Decode(&status); err != nil {
				decodeErrors <- err
				return
			}
			statuses <- status
		}
	}()
	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- runServe(ctx, []string{"--config", configPath}, writer, io.Discard)
		_ = writer.Close()
	}()

	var started map[string]string
	select {
	case started = <-statuses:
	case err := <-decodeErrors:
		t.Fatal(err)
	case err := <-serveErrors:
		t.Fatalf("serve exited before listening: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not start")
	}
	if started["status"] != "listening" || started["address"] == "" {
		t.Fatalf("listening status = %#v", started)
	}
	baseURL := "http://" + started["address"]
	response, err := http.Get(baseURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(data), "MetricSpire") {
		t.Fatalf("root response = %d %q, %v", response.StatusCode, data, readErr)
	}
	for _, path := range []string{"/health/live", "/health/ready"} {
		response, err = http.Get(baseURL + path)
		if err != nil {
			t.Fatal(err)
		}
		data, readErr = io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil || response.StatusCode != http.StatusOK || !bytes.Contains(data, []byte("status")) {
			t.Fatalf("%s response = %d %q, %v", path, response.StatusCode, data, readErr)
		}
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err = client.Get(baseURL + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	location := response.Header.Get("Location")
	if response.StatusCode != http.StatusFound || !strings.HasPrefix(location, issuer.URL+"/authorize?") ||
		!strings.Contains(location, "code_challenge_method=S256") {
		t.Fatalf("OIDC login response = %d location=%q", response.StatusCode, location)
	}

	cancel()
	select {
	case stopped := <-statuses:
		if stopped["status"] != "stopped" {
			t.Fatalf("stopped status = %#v", stopped)
		}
	case err := <-decodeErrors:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not report graceful stop")
	}
	select {
	case err := <-serveErrors:
		if err != nil {
			t.Fatalf("serve error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop")
	}
}
