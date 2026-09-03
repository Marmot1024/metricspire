package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const migrationLockID int64 = 6449131086483401

var schemaNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

func ValidateSchemaName(name string) error {
	if !schemaNamePattern.MatchString(name) {
		return fmt.Errorf("postgres schema must match %s", schemaNamePattern.String())
	}
	return nil
}

// EnsureSchema creates the explicitly configured application schema. Lakebase
// App resources grant CREATE on the database, but intentionally do not grant
// CREATE on the shared public schema.
func EnsureSchema(ctx context.Context, pool *pgxpool.Pool, name string) error {
	if pool == nil {
		return fmt.Errorf("postgres pool is required")
	}
	if err := ValidateSchemaName(name); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS `+pgx.Identifier{name}.Sanitize()); err != nil {
		return fmt.Errorf("create postgres schema: %w", err)
	}
	return nil
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return fmt.Errorf("postgres pool is required")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	if _, err := tx.Exec(ctx, `
CREATE TABLE IF NOT EXISTS metricspire_schema_migrations (
    version TEXT PRIMARY KEY,
	checksum TEXT NOT NULL CHECK (checksum ~ '^[0-9a-f]{64}$'),
    applied_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
)`); err != nil {
		return fmt.Errorf("create migration registry: %w", err)
	}
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		contents, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(contents))
		var storedChecksum string
		if err := tx.QueryRow(ctx,
			`SELECT checksum FROM metricspire_schema_migrations WHERE version = $1`, name,
		).Scan(&storedChecksum); err != nil && err != pgx.ErrNoRows {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if storedChecksum != "" {
			if storedChecksum != checksum {
				return fmt.Errorf("migration %s checksum changed after it was applied", name)
			}
			continue
		}
		if _, err := tx.Exec(ctx, string(contents)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO metricspire_schema_migrations (version, checksum) VALUES ($1, $2)`, name, checksum,
		); err != nil {
			return fmt.Errorf("record migration %s: %w", name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}
