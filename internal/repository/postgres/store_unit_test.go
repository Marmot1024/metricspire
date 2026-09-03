package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type passwordProviderFunc func(context.Context) (string, error)

func (function passwordProviderFunc) Password(ctx context.Context) (string, error) {
	return function(ctx)
}

func TestPoolConfigRefreshesPasswordBeforeEachPhysicalConnection(t *testing.T) {
	calls := 0
	config, err := poolConfig("postgres://role@database.example/metricspire?sslmode=require", passwordProviderFunc(func(context.Context) (string, error) {
		calls++
		return "short-lived-credential", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		connection := config.ConnConfig.Copy()
		if err := config.BeforeConnect(context.Background(), connection); err != nil {
			t.Fatal(err)
		}
		if connection.Password != "short-lived-credential" {
			t.Fatalf("connection password was not refreshed")
		}
	}
	if calls != 2 {
		t.Fatalf("password provider calls = %d, want 2", calls)
	}
}

func TestPoolConfigDoesNotExposeCredentialProviderFailures(t *testing.T) {
	const secret = "credential-must-not-leak"
	config, err := poolConfig("postgres://role@database.example/metricspire?sslmode=require", passwordProviderFunc(func(context.Context) (string, error) {
		return "", errors.New(secret)
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := config.BeforeConnect(context.Background(), &pgx.ConnConfig{}); err == nil || err.Error() != "refresh PostgreSQL credential" || strings.Contains(err.Error(), secret) {
		t.Fatalf("BeforeConnect() error = %v", err)
	}
}
