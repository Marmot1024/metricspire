package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
)

func dorisStartupFixture(t *testing.T, db, contract string) string {
	t.Helper()
	u, err := url.Parse(os.Getenv("METRICSPIRE_DORIS_TEST_URL"))
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatal("loopback Doris fixture required")
	}
	c := driver.NewConfig()
	c.User = u.User.Username()
	c.Passwd, _ = u.User.Password()
	c.Net = "tcp"
	c.Addr = u.Host
	c.InterpolateParams = true
	c.Params = map[string]string{"time_zone": "'+00:00'"}
	connector, err := driver.NewConnector(c)
	if err != nil {
		t.Fatal("invalid fixture")
	}
	admin := sql.OpenDB(connector)
	t.Cleanup(func() { admin.Close() })
	exec := func(s string, args ...any) {
		t.Helper()
		if _, err := admin.ExecContext(t.Context(), s, args...); err != nil {
			t.Fatal("Doris fixture failed", err)
		}
	}
	user := fmt.Sprintf("startup_reader_%d", time.Now().UnixNano())
	exec("CREATE DATABASE `" + db + "`")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, q := range []string{"DROP USER IF EXISTS `" + user + "`", "DROP DATABASE IF EXISTS `" + db + "`"} {
			if _, err := admin.ExecContext(ctx, q); err != nil {
				t.Error("Doris fixture cleanup failed")
			}
		}
	})
	table := "`" + db + "`.daily_sales"
	exec("CREATE TABLE " + table + "(row_key VARCHAR(64) NOT NULL,day DATE NOT NULL,region VARCHAR(64),revenue DECIMAL(18,6),orders BIGINT,_metricspire_tenant VARCHAR(64) NOT NULL,_metricspire_batch_id VARCHAR(128) NOT NULL,_metricspire_data_as_of DATETIME(3) NOT NULL,_metricspire_data_contract VARCHAR(71) NOT NULL) ENGINE=OLAP DUPLICATE KEY(row_key) DISTRIBUTED BY HASH(row_key) BUCKETS 1 PROPERTIES('replication_num'='1')")
	exec("INSERT INTO "+table+" VALUES('one','2026-09-01','east',100,2,'demo','batch-1',NOW(3),?)", contract)
	exec("CREATE USER `" + user + "` IDENTIFIED BY 'synthetic-only'")
	exec("GRANT SELECT_PRIV ON " + table + " TO `" + user + "`")
	reader := *u
	reader.User = url.UserPassword(user, "synthetic-only")
	return reader.String()
}
