package main

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// The administrator exists only inside the opt-in disposable loopback fixture.
func clickHouseStartupFixture(t *testing.T, database, contract string) string {
	t.Helper()
	u, err := url.Parse(os.Getenv("METRICSPIRE_CLICKHOUSE_TEST_URL"))
	if err != nil || u.User == nil || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") {
		t.Fatal("ClickHouse fixture requires loopback administrator")
	}
	user, pass := u.User.Username(), ""
	pass, _ = u.User.Password()
	u.User = nil
	execute := func(ctx context.Context, sql string) error {
		var buf bytes.Buffer
		form := multipart.NewWriter(&buf)
		_ = form.WriteField("query", sql)
		_ = form.WriteField("param_contract", contract)
		_ = form.Close()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), &buf)
		req.SetBasicAuth(user, pass)
		req.Header.Set("Content-Type", form.FormDataContentType())
		res, err := (&http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil {
			return context.DeadlineExceeded
		}
		defer res.Body.Close()
		_, _ = io.Copy(io.Discard, res.Body)
		if res.StatusCode != 200 || res.Header.Get("X-ClickHouse-Exception-Code") != "" {
			return context.Canceled
		}
		return nil
	}
	quote := func(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }
	reader := "reader_" + database
	table := quote(database) + ".daily_sales"
	exec := func(sql string) {
		t.Helper()
		if err := execute(t.Context(), sql); err != nil {
			t.Fatal("ClickHouse fixture operation failed")
		}
	}
	exec("CREATE DATABASE " + quote(database))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, sql := range []string{"DROP USER IF EXISTS " + quote(reader), "DROP DATABASE IF EXISTS " + quote(database)} {
			if err := execute(ctx, sql); err != nil {
				t.Error("ClickHouse fixture cleanup failed")
			}
		}
	})
	exec("CREATE TABLE " + table + " (row_key String,day Date,region String,revenue Decimal(18,6),orders Int64,_metricspire_tenant String,_metricspire_batch_id String,_metricspire_data_as_of DateTime64(3,'UTC'),_metricspire_data_contract String) ENGINE=MergeTree ORDER BY (_metricspire_tenant,day,region,row_key)")
	exec("INSERT INTO " + table + " SELECT 'one',toDate('2026-09-01'),'east',toDecimal64(100,6),toInt64(2),'demo','batch-1',now64(3),{contract:String}")
	exec("CREATE USER " + quote(reader) + " IDENTIFIED BY 'synthetic-only'")
	for _, source := range []string{table, "system.grants", "system.role_grants", "system.tables", "system.columns"} {
		exec("GRANT SELECT ON " + source + " TO " + quote(reader))
	}
	readerURL := *u
	readerURL.User = url.UserPassword(reader, "synthetic-only")
	return readerURL.String()
}
