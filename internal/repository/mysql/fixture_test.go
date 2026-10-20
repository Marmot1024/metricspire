package mysql

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"
)

// Own one generated database per test. Never alter the configured database.
func isolatedMySQL(t *testing.T) *Store {
	t.Helper()
	raw := os.Getenv("METRICSPIRE_MYSQL_TEST_URL")
	if raw == "" {
		if os.Getenv("METRICSPIRE_REQUIRE_MYSQL_TESTS") == "1" {
			t.Fatal("disposable MySQL URL required")
		}
		t.Skip("requires disposable loopback MySQL 8.4")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatal("loopback test database required")
	}
	admin, err := Open(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	name := fmt.Sprintf("metricspire_test_%d", time.Now().UnixNano())
	if _, err = admin.DB().ExecContext(t.Context(), "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin"); err != nil {
		t.Fatal("create isolated database failed")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.DB().ExecContext(ctx, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error("cleanup isolated database failed")
		}
	})
	u.Path = "/" + name
	store, err := Open(t.Context(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store
}
