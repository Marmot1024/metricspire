package mysql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"
)

// MySQL DDL commits implicitly. Each append-only migration contains ONE DDL
// statement. A crash/failure leaves a dirty marker and requires operator review;
// never pretend the DDL rolled back or automatically adopt an existing table.
//
//go:embed migrations/*.sql
var migrations embed.FS

type migration struct{ name, checksum, sql string }

func migrationList(files fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(files, "migrations")
	if err != nil {
		return nil, err
	}
	out := []migration{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := fs.ReadFile(files, "migrations/"+entry.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{entry.Name(), fmt.Sprintf("%x", sha256.Sum256(data)), string(data)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

type migrationQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func verifyMigrations(ctx context.Context, q migrationQuery, all []migration) (int, error) {
	rows, err := q.QueryContext(ctx, `SELECT version,checksum,dirty FROM metricspire_schema_migrations ORDER BY version`)
	if err != nil {
		return 0, failure("read migrations", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var version, checksum string
		var dirty bool
		if err = rows.Scan(&version, &checksum, &dirty); err != nil {
			return 0, failure("read migration state", err)
		}
		if count >= len(all) || version != all[count].name || checksum != all[count].checksum || dirty {
			return 0, errors.New("MySQL migration checksum, order or dirty state requires operator review")
		}
		count++
	}
	if rows.Err() != nil {
		return 0, failure("read migration state", rows.Err())
	}
	return count, nil
}
func (s *Store) CheckSchema(ctx context.Context) error {
	all, err := migrationList(migrations)
	if err != nil {
		return err
	}
	n, err := verifyMigrations(ctx, s.db, all)
	if err != nil {
		return err
	}
	if n != len(all) {
		return errors.New("MySQL metadata requires explicit migrate before serving")
	}
	return nil
}
func (s *Store) Migrate(ctx context.Context) error { return s.migrate(ctx, migrations) }
func (s *Store) migrate(ctx context.Context, files fs.FS) error {
	all, err := migrationList(files)
	if err != nil {
		return err
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return failure("open migration connection", err)
	}
	defer conn.Close()
	var database string
	if err = conn.QueryRowContext(ctx, `SELECT DATABASE()`).Scan(&database); err != nil {
		return failure("identify migration database", err)
	}
	key := fmt.Sprintf("metricspire:%x", sha256.Sum256([]byte(database)))[:60]
	var locked int
	if err = conn.QueryRowContext(ctx, `SELECT GET_LOCK(?,5)`, key).Scan(&locked); err != nil || locked != 1 {
		return errors.New("MySQL migration lock unavailable")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var released sql.NullInt64
		_ = conn.QueryRowContext(cleanup, `SELECT RELEASE_LOCK(?)`, key).Scan(&released)
	}()
	if _, err = conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS metricspire_schema_migrations(version VARCHAR(128) PRIMARY KEY, checksum CHAR(64) NOT NULL,dirty BOOLEAN NOT NULL,applied_at DATETIME(6)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin`); err != nil {
		return failure("create migration registry", err)
	}
	n, err := verifyMigrations(ctx, conn, all)
	if err != nil {
		return err
	}
	for _, m := range all[n:] {
		if _, err = conn.ExecContext(ctx, `INSERT INTO metricspire_schema_migrations(version,checksum,dirty) VALUES(?,?,TRUE)`, m.name, m.checksum); err != nil {
			return failure("mark migration pending", err)
		}
		if _, err = conn.ExecContext(ctx, m.sql); err != nil {
			return errors.New("MySQL migration " + m.name + " failed; dirty state requires operator review")
		}
		if _, err = conn.ExecContext(ctx, `UPDATE metricspire_schema_migrations SET dirty=FALSE,applied_at=? WHERE version=? AND checksum=? AND dirty=TRUE`, now(), m.name, m.checksum); err != nil {
			return failure("mark migration applied", err)
		}
	}
	return nil
}
