package main

import (
	"context"
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
