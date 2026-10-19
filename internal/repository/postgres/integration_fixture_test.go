package postgres_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/marmot1024/metricspire/internal/repository/postgres"
)

// Each test owns one fresh schema. No migration or cleanup targets public or
// any pre-existing schema. The supplied database must still be disposable.
func isolatedPostgres(t *testing.T) *postgres.Store {
	t.Helper()
	raw := os.Getenv("METRICSPIRE_TEST_DATABASE_URL")
	if raw == "" {
		if os.Getenv("METRICSPIRE_REQUIRE_POSTGRES_TESTS") == "1" {
			t.Fatal("PostgreSQL integration tests are required but METRICSPIRE_TEST_DATABASE_URL is unset")
		}
		t.Skip("set METRICSPIRE_TEST_DATABASE_URL to a disposable loopback PostgreSQL database")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost") {
		t.Fatal("catalog integration tests accept disposable loopback databases only")
	}
	config, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatal("invalid PostgreSQL integration connection configuration")
	}
	admin, err := pgxpool.NewWithConfig(t.Context(), config.Copy())
	if err != nil {
		t.Fatal("could not open integration admin pool")
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("catalog_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatalf("create isolated test schema: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("clean isolated test schema: %v", err)
		}
	})
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal("could not open isolated integration pool")
	}
	t.Cleanup(pool.Close)
	store, err := postgres.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
