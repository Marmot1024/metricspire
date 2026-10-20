package mysql

import (
	"testing"
	"testing/fstest"
)

func TestMigrationInstallUpgradeAndFailure(t *testing.T) {
	t.Run("upgrade", func(t *testing.T) {
		s := isolatedMySQL(t)
		if s.CheckSchema(t.Context()) == nil {
			t.Fatal("unmigrated store accepted")
		}
		all, err := migrationList(migrations)
		if err != nil {
			t.Fatal(err)
		}
		prefix := fstest.MapFS{}
		for _, m := range all[:3] {
			prefix["migrations/"+m.name] = &fstest.MapFile{Data: []byte(m.sql)}
		}
		if err = s.migrate(t.Context(), prefix); err != nil {
			t.Fatal(err)
		}
		if s.CheckSchema(t.Context()) == nil {
			t.Fatal("partial schema accepted")
		}
		if err = s.Migrate(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err = s.Migrate(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err = s.CheckSchema(t.Context()); err != nil {
			t.Fatal(err)
		}
		if _, err = s.DB().ExecContext(t.Context(), `UPDATE metricspire_schema_migrations SET checksum=REPEAT('0',64) WHERE version=?`, all[0].name); err != nil {
			t.Fatal(err)
		}
		if s.Migrate(t.Context()) == nil || s.CheckSchema(t.Context()) == nil {
			t.Fatal("changed checksum accepted")
		}
	})
	t.Run("dirty", func(t *testing.T) {
		s := isolatedMySQL(t)
		files := fstest.MapFS{"migrations/001_broken.sql": &fstest.MapFile{Data: []byte("CREATE TABLE broken (not_valid)")}}
		if s.migrate(t.Context(), files) == nil {
			t.Fatal("bad DDL accepted")
		}
		var dirty bool
		if err := s.DB().QueryRowContext(t.Context(), `SELECT dirty FROM metricspire_schema_migrations`).Scan(&dirty); err != nil || !dirty {
			t.Fatal("dirty marker missing", err)
		}
		if s.migrate(t.Context(), files) == nil {
			t.Fatal("dirty migration retried")
		}
	})
}

func TestUnsafeMySQLURLs(t *testing.T) {
	for _, raw := range []string{"mysql://u:p@remote/db?tls=false", "mysql://u:p@remote/db?tls=skip-verify", "mysql://u:p@remote/db?multiStatements=true", "mysql://u:p@remote/db?tls=true&tls=false", "mysql://u:p@remote/db/more", "mysql://remote/db"} {
		if _, err := Open(t.Context(), raw); err == nil {
			t.Fatal("unsafe URL accepted")
		}
	}
}
