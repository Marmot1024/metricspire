package servingrefresh

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgresAtomicRefreshReal(t *testing.T) {
	raw := os.Getenv("METRICSPIRE_ONLINE_TEST_DATABASE_URL")
	if raw == "" {
		if os.Getenv("METRICSPIRE_REQUIRE_POSTGRES_TESTS") == "1" {
			t.Fatal("test URL required")
		}
		t.Skip("requires disposable PostgreSQL")
	}
	conn, err := Connect(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	schema := fmt.Sprintf("refresh_test_%d", time.Now().UnixNano())
	q := pgx.Identifier{schema}.Sanitize()
	if _, err = conn.Exec(t.Context(), `CREATE SCHEMA `+q); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
		defer cancel()
		if _, err := conn.Exec(ctx, `DROP SCHEMA `+q+` CASCADE`); err != nil {
			t.Error("cleanup failed")
		}
	}()
	c := testContract()
	c.Resource.Schema = schema
	if _, err = conn.Exec(t.Context(), `CREATE TABLE `+table(c)+` (id text NOT NULL,value numeric,_metricspire_tenant text NOT NULL,_metricspire_batch_id text NOT NULL,_metricspire_data_as_of timestamptz NOT NULL,_metricspire_data_contract text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err = Init(t.Context(), conn, c); err != nil {
		t.Fatal(err)
	}
	if err = Init(t.Context(), conn, c); err != nil {
		t.Fatal(err)
	}
	asof := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	a := artifact(t, c, "a", asof, "{\"id\":\"one\",\"value\":\"9007199254740993.000001\"}\n", 1, pointer("9007199254740993.000001"))
	if r, err := Refresh(t.Context(), conn, c, bytes.NewReader(a), "demo", ""); err != nil || r.Repeated || r.Rows != 1 {
		t.Fatalf("initial %v %v", r, err)
	}
	if r, err := Refresh(t.Context(), conn, c, bytes.NewReader(a), "demo", ""); err != nil || !r.Repeated {
		t.Fatalf("idempotent %v %v", r, err)
	}
	// Other tenant is never deleted by a replacement.
	if _, err = conn.Exec(t.Context(), `INSERT INTO `+table(c)+` VALUES ('other',99,'other','foreign',$1,'foreign')`, asof); err != nil {
		t.Fatal(err)
	}
	snapshot := func(want string) {
		t.Helper()
		var actual string
		if err := conn.QueryRow(t.Context(), `SELECT string_agg(_metricspire_batch_id,',' ORDER BY id) FROM `+table(c)+` WHERE _metricspire_tenant='demo'`).Scan(&actual); err != nil || actual != want {
			t.Fatalf("snapshot %q want %q %v", actual, want, err)
		}
	}
	b := artifact(t, c, "b", asof.Add(time.Second), "{\"id\":\"two\",\"value\":\"2\"}\n", 1, pointer("2"))
	for name, broken := range map[string][]byte{"checksum": bytes.Replace(b, []byte(`"value":"2"`), []byte(`"value":"3"`), 1), "truncated": b[:len(b)-12]} {
		t.Run(name, func(t *testing.T) {
			if _, err := Refresh(t.Context(), conn, c, bytes.NewReader(broken), "demo", "a"); err == nil {
				t.Fatal("invalid artifact accepted")
			}
			snapshot("a")
		})
	}
	if _, err = Refresh(t.Context(), conn, c, bytes.NewReader(b), "demo", "wrong"); err == nil {
		t.Fatal("CAS ignored")
	}
	snapshot("a")
	dup := artifact(t, c, "dup", asof.Add(time.Second), "{\"id\":\"one\",\"value\":\"1\"}\n{\"id\":\"one\",\"value\":\"2\"}\n", 2, pointer("3"))
	if _, err = Refresh(t.Context(), conn, c, bytes.NewReader(dup), "demo", "a"); err == nil {
		t.Fatal("duplicate grain accepted")
	}
	snapshot("a")

	// A silent trigger rewrite must be refused before touching the target.
	if _, err = conn.Exec(t.Context(), `CREATE FUNCTION `+q+`.rewrite_value() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.value=NEW.value+1; RETURN NEW; END $$; CREATE TRIGGER rewrite_value BEFORE INSERT ON `+table(c)+` FOR EACH ROW EXECUTE FUNCTION `+q+`.rewrite_value()`); err != nil {
		t.Fatal(err)
	}
	if _, err = Refresh(t.Context(), conn, c, bytes.NewReader(b), "demo", "a"); err == nil {
		t.Fatal("silent rewrite trigger accepted")
	}
	snapshot("a")
	if _, err = conn.Exec(t.Context(), `DROP TRIGGER rewrite_value ON `+table(c)+`; ALTER TABLE `+table(c)+` ENABLE ROW LEVEL SECURITY`); err != nil {
		t.Fatal(err)
	}
	if _, err = Refresh(t.Context(), conn, c, bytes.NewReader(b), "demo", "a"); err == nil {
		t.Fatal("RLS target accepted")
	}
	snapshot("a")
	if _, err = conn.Exec(t.Context(), `ALTER TABLE `+table(c)+` DISABLE ROW LEVEL SECURITY`); err != nil {
		t.Fatal(err)
	}
	// Reject sub-microsecond source coverage before PostgreSQL can truncate it.
	nanos := bytes.Replace(b, []byte(asof.Add(time.Second).Format(time.RFC3339Nano)), []byte(asof.Add(time.Second+789*time.Nanosecond).Format(time.RFC3339Nano)), 1)
	if _, err = Refresh(t.Context(), conn, c, bytes.NewReader(nanos), "demo", "a"); err == nil {
		t.Fatal("nanosecond watermark accepted")
	}
	snapshot("a")
	// Receipt write fails after target DELETE/INSERT. The whole publication rolls back.
	_, err = conn.Exec(t.Context(), `CREATE FUNCTION `+q+`.reject_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic'; END $$; CREATE TRIGGER reject_insert BEFORE INSERT ON `+control(c, "batches")+` FOR EACH ROW EXECUTE FUNCTION `+q+`.reject_insert()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Refresh(t.Context(), conn, c, bytes.NewReader(b), "demo", "a"); err == nil {
		t.Fatal("trigger failure accepted")
	}
	snapshot("a")
	if _, err = conn.Exec(t.Context(), `DROP TRIGGER reject_insert ON `+control(c, "batches")); err != nil {
		t.Fatal(err)
	}
	if _, err = Refresh(t.Context(), conn, c, bytes.NewReader(b), "demo", "a"); err != nil {
		t.Fatal(err)
	}
	snapshot("b")
	if _, err = Refresh(t.Context(), conn, c, bytes.NewReader(a), "demo", "b"); err == nil {
		t.Fatal("superseded batch replayed")
	}
	snapshot("b")
	reused := artifact(t, c, "b", asof.Add(time.Second), "{\"id\":\"two\",\"value\":\"3\"}\n", 1, pointer("3"))
	if _, err = Refresh(t.Context(), conn, c, bytes.NewReader(reused), "demo", "b"); err == nil {
		t.Fatal("same ID changed content")
	}
	stale := artifact(t, c, "stale", asof, "{\"id\":\"two\",\"value\":\"2\"}\n", 1, pointer("2"))
	if _, err = Refresh(t.Context(), conn, c, bytes.NewReader(stale), "demo", "b"); err == nil {
		t.Fatal("watermark went backwards")
	}
	var other int
	if err = conn.QueryRow(t.Context(), `SELECT count(*) FROM `+table(c)+` WHERE _metricspire_tenant='other'`).Scan(&other); err != nil || other != 1 {
		t.Fatal("other tenant affected")
	}
	// Independent connections racing the same expected head: only one publishes.
	second, err := Connect(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(context.Background())
	results := make(chan error, 2)
	for i, db := range []*pgx.Conn{conn, second} {
		data := artifact(t, c, fmt.Sprintf("race%d", i), asof.Add(2*time.Second), fmt.Sprintf("{\"id\":\"race%d\",\"value\":\"4\"}\n", i), 1, pointer("4"))
		go func(db *pgx.Conn, data []byte) {
			_, err := Refresh(t.Context(), db, c, bytes.NewReader(data), "demo", "b")
			results <- err
		}(db, data)
	}
	successes := 0
	for i := 0; i < 2; i++ {
		if <-results == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatal("concurrent CAS did not have exactly one winner")
	}
	for _, u := range []string{"postgres://u:p@remote/db?sslmode=disable", "postgres://u:p@remote/db?sslmode=require", "postgres://u:p@localhost/db?sslmode=disable&options=unsafe"} {
		if db, err := Connect(t.Context(), u); err == nil {
			db.Close(context.Background())
			t.Fatal("unsafe URL accepted", strings.Contains(u, "unsafe"))
		}
	}
}

func TestRefreshProducerLeastPrivilegesReal(t *testing.T) {
	raw := os.Getenv("METRICSPIRE_ONLINE_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	admin, err := Connect(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	id := fmt.Sprint(time.Now().UnixNano())
	schema, role := "refresh_role_"+id, "refresh_writer_"+id
	qs, qr := pgx.Identifier{schema}.Sanitize(), pgx.Identifier{role}.Sanitize()
	if _, err = admin.Exec(t.Context(), `CREATE SCHEMA `+qs+`; CREATE ROLE `+qr+` NOLOGIN`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, `DROP SCHEMA `+qs+` CASCADE; DROP OWNED BY `+qr+`; DROP ROLE `+qr); err != nil {
			t.Error("producer role cleanup failed")
		}
	}()
	c := testContract()
	c.Resource.Schema = schema
	if _, err = admin.Exec(t.Context(), `CREATE TABLE `+table(c)+` (id text NOT NULL,value numeric,_metricspire_tenant text NOT NULL,_metricspire_batch_id text NOT NULL,_metricspire_data_as_of timestamptz NOT NULL,_metricspire_data_contract text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err = Init(t.Context(), admin, c); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(t.Context(), `GRANT USAGE ON SCHEMA `+qs+` TO `+qr+`; GRANT SELECT,INSERT,DELETE ON `+table(c)+` TO `+qr+`; GRANT SELECT,INSERT ON `+control(c, "batches")+` TO `+qr+`; GRANT SELECT,INSERT ON `+control(c, "heads")+` TO `+qr); err != nil {
		t.Fatal(err)
	}
	var db string
	if err = admin.QueryRow(t.Context(), `SELECT current_database()`).Scan(&db); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(t.Context(), `GRANT TEMP ON DATABASE `+pgx.Identifier{db}.Sanitize()+` TO `+qr); err != nil {
		t.Fatal(err)
	}
	writer, err := Connect(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close(context.Background())
	if _, err = writer.Exec(t.Context(), `SET ROLE `+qr); err != nil {
		t.Fatal(err)
	}
	asof := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	a := artifact(t, c, "one", asof, "{\"id\":\"a\",\"value\":\"1\"}\n", 1, pointer("1"))
	if _, err = Refresh(t.Context(), writer, c, bytes.NewReader(a), "demo", ""); err == nil {
		t.Fatal("missing head UPDATE unexpectedly succeeded")
	}
	if _, err = admin.Exec(t.Context(), `GRANT UPDATE ON `+control(c, "heads")+` TO `+qr); err != nil {
		t.Fatal(err)
	}
	if _, err = Refresh(t.Context(), writer, c, bytes.NewReader(a), "demo", ""); err != nil {
		t.Fatal(err)
	}
	b := artifact(t, c, "two", asof.Add(time.Second), "{\"id\":\"b\",\"value\":\"2\"}\n", 1, pointer("2"))
	if _, err = Refresh(t.Context(), writer, c, bytes.NewReader(b), "demo", "one"); err != nil {
		t.Fatal(err)
	}
	if r, err := Refresh(t.Context(), writer, c, bytes.NewReader(b), "demo", "one"); err != nil || !r.Repeated {
		t.Fatal("least privilege repeat failed", err)
	}
}
