package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/adapter/postgresquery"
	"github.com/marmot1024/metricspire/internal/repository/mysql"
)

func TestServeMySQLMetadataPostgresResultsNativeMCP(t *testing.T) {
	if os.Getenv("METRICSPIRE_MYSQL_TEST_URL") == "" {
		if os.Getenv("METRICSPIRE_REQUIRE_MYSQL_TESTS") == "1" {
			t.Fatal("disposable MySQL URL required")
		}
		t.Skip("requires disposable MySQL and PostgreSQL")
	}
	testServeOnlineEngine(t, "", false, true, true, postgresquery.EngineName, "mysql")
}

func mysqlStartupFixture(t *testing.T) (*mysql.Store, *url.URL) {
	t.Helper()
	raw := os.Getenv("METRICSPIRE_MYSQL_TEST_URL")
	u, err := url.Parse(raw)
	if err != nil || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") {
		t.Fatal("disposable loopback MySQL required")
	}
	admin, err := mysql.Open(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	name := fmt.Sprintf("metricspire_start_%d", time.Now().UnixNano())
	if _, err = admin.DB().ExecContext(t.Context(), "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin"); err != nil {
		t.Fatal("create fixture database failed")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.DB().ExecContext(ctx, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error("cleanup fixture database failed")
		}
	})
	u.Path = "/" + name
	store, err := mysql.Open(t.Context(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err = store.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return store, u
}
