package main

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/marmot1024/metricspire/internal/adapter/databricks"
)

func TestDatabricksTokenSourcePrefersOAuthM2MAndRequiresCompleteCredentials(t *testing.T) {
	t.Setenv("DATABRICKS_TOKEN", "personal-token")
	t.Setenv("DATABRICKS_CLIENT_ID", "client")
	t.Setenv("DATABRICKS_CLIENT_SECRET", "secret")
	source, err := databricksTokenSource("https://workspace.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := source.(*databricks.OAuthM2MTokenSource); !ok {
		t.Fatalf("token source = %T, want OAuth M2M", source)
	}

	t.Setenv("DATABRICKS_CLIENT_SECRET", "")
	_, err = databricksTokenSource("https://workspace.test")
	if err == nil || !strings.Contains(err.Error(), "must both be set") {
		t.Fatalf("partial OAuth configuration error = %v", err)
	}
}

func TestCommandContextRejectsNonPositiveTimeout(t *testing.T) {
	ctx, cancel, err := commandContext(context.Background(), 0)
	if ctx != nil || cancel != nil || err == nil {
		t.Fatalf("commandContext() = %#v %#v %v", ctx, cancel, err)
	}
}

func TestLoadStoreConfigurationSupportsStaticPostgresAndLakebaseOAuth(t *testing.T) {
	static, err := loadStoreConfiguration(" postgres://localhost/metricspire ", func(string) string { return "" })
	if err != nil || static.databaseURL != "postgres://localhost/metricspire" || static.passwordProvider != nil {
		t.Fatalf("static store configuration = %#v, %v", static, err)
	}

	values := map[string]string{
		"METRICSPIRE_LAKEBASE_ENDPOINT": "projects/project-1/branches/production/endpoints/primary",
		"DATABRICKS_HOST":               "https://workspace.example.com",
		"DATABRICKS_CLIENT_ID":          "service-principal",
		"DATABRICKS_CLIENT_SECRET":      "must-not-appear-in-url",
		"PGHOST":                        "database.example.com",
		"PGPORT":                        "5432",
		"PGDATABASE":                    "databricks_postgres",
		"PGUSER":                        "service principal@example.com",
		"PGSSLMODE":                     "require",
		"METRICSPIRE_DATABASE_SCHEMA":   "metricspire",
	}
	configured, err := loadStoreConfiguration("", func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(configured.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	username := ""
	if parsed.User != nil {
		username = parsed.User.Username()
	}
	if configured.passwordProvider == nil || parsed.Scheme != "postgres" || parsed.Host != "database.example.com:5432" ||
		parsed.Path != "/databricks_postgres" || username != "service principal@example.com" || parsed.Query().Get("sslmode") != "require" ||
		parsed.Query().Get("search_path") != "metricspire" ||
		strings.Contains(configured.databaseURL, values["DATABRICKS_CLIENT_SECRET"]) {
		t.Fatalf("Lakebase store configuration URL = %q, provider configured = %t", configured.databaseURL, configured.passwordProvider != nil)
	}
}

func TestLoadStoreConfigurationRejectsAmbiguousOrUnsafeLakebaseConfiguration(t *testing.T) {
	valid := map[string]string{
		"METRICSPIRE_LAKEBASE_ENDPOINT": "projects/project-1/branches/production/endpoints/primary",
		"DATABRICKS_HOST":               "https://workspace.example.com",
		"DATABRICKS_CLIENT_ID":          "client",
		"DATABRICKS_CLIENT_SECRET":      "secret",
		"PGHOST":                        "database.example.com", "PGPORT": "5432", "PGDATABASE": "database", "PGUSER": "user", "PGSSLMODE": "require",
	}
	getenv := func(name string) string { return valid[name] }
	if _, err := loadStoreConfiguration("postgres://localhost/other", getenv); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("ambiguous database configuration error = %v", err)
	}
	valid["PGSSLMODE"] = "disable"
	if _, err := loadStoreConfiguration("", getenv); err == nil || !strings.Contains(err.Error(), "PGSSLMODE") {
		t.Fatalf("insecure database configuration error = %v", err)
	}
	valid["PGSSLMODE"] = "require"
	valid["METRICSPIRE_DATABASE_SCHEMA"] = "unsafe-schema"
	if _, err := loadStoreConfiguration("", getenv); err == nil || !strings.Contains(err.Error(), "postgres schema") {
		t.Fatalf("unsafe schema configuration error = %v", err)
	}
	delete(valid, "METRICSPIRE_DATABASE_SCHEMA")
	valid["DATABRICKS_CLIENT_SECRET"] = ""
	if _, err := loadStoreConfiguration("", getenv); err == nil || !strings.Contains(err.Error(), "DATABRICKS_CLIENT_SECRET") {
		t.Fatalf("partial OAuth configuration error = %v", err)
	}
}
