// Package postgres implements MetricSpire's transactional catalog on standard
// PostgreSQL. It uses no Lakebase-specific API.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/model"
)

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL configuration: %w", err)
	}
	config.MaxConns = 10
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}
	return &Store{pool: pool}, nil
}

func New(pool *pgxpool.Pool) (*Store, error) {
	if pool == nil {
		return nil, errors.New("postgres pool is required")
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Pool() *pgxpool.Pool { return s.pool }
func (s *Store) Close()              { s.pool.Close() }

func (s *Store) SaveDraft(ctx context.Context, input catalog.SaveDraftInput) (catalog.Draft, error) {
	payload, err := json.Marshal(input.Source)
	if err != nil {
		return catalog.Draft{}, fmt.Errorf("encode draft: %w", err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return catalog.Draft{}, fmt.Errorf("begin draft transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	name := input.Source.Metadata.Name
	var current int64
	err = tx.QueryRow(ctx,
		`SELECT revision FROM metricspire_drafts WHERE namespace = $1 AND model_name = $2 FOR UPDATE`,
		input.Namespace, name,
	).Scan(&current)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if input.ExpectedRevision != 0 {
			return catalog.Draft{}, catalog.ErrConflict
		}
		current = 0
	case err != nil:
		return catalog.Draft{}, fmt.Errorf("lock draft: %w", err)
	case current != input.ExpectedRevision:
		return catalog.Draft{}, catalog.ErrConflict
	}
	next := current + 1
	var updatedAt time.Time
	if current == 0 {
		err = tx.QueryRow(ctx, `
INSERT INTO metricspire_drafts (namespace, model_name, revision, source, updated_by)
VALUES ($1, $2, $3, $4::jsonb, $5)
RETURNING updated_at`, input.Namespace, name, next, payload, input.Actor).Scan(&updatedAt)
	} else {
		err = tx.QueryRow(ctx, `
UPDATE metricspire_drafts
SET revision = $3, source = $4::jsonb, updated_by = $5, updated_at = clock_timestamp()
WHERE namespace = $1 AND model_name = $2
RETURNING updated_at`, input.Namespace, name, next, payload, input.Actor).Scan(&updatedAt)
	}
	if err != nil {
		return catalog.Draft{}, classify("save draft", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO metricspire_draft_revisions
    (namespace, model_name, revision, source, created_by, created_at)
VALUES ($1, $2, $3, $4::jsonb, $5, $6)`,
		input.Namespace, name, next, payload, input.Actor, updatedAt,
	); err != nil {
		return catalog.Draft{}, classify("save draft revision", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return catalog.Draft{}, classify("commit draft", err)
	}
	return catalog.Draft{
		Namespace: input.Namespace, Name: name, Revision: next, Source: input.Source,
		UpdatedBy: input.Actor, UpdatedAt: updatedAt.UTC(),
	}, nil
}

func (s *Store) GetDraft(ctx context.Context, namespace, name string) (catalog.Draft, error) {
	var draft catalog.Draft
	var payload []byte
	err := s.pool.QueryRow(ctx, `
SELECT namespace, model_name, revision, source, updated_by, updated_at
FROM metricspire_drafts
WHERE namespace = $1 AND model_name = $2`, namespace, name).Scan(
		&draft.Namespace, &draft.Name, &draft.Revision, &payload, &draft.UpdatedBy, &draft.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return catalog.Draft{}, catalog.ErrNotFound
	}
	if err != nil {
		return catalog.Draft{}, fmt.Errorf("get draft: %w", err)
	}
	if err := json.Unmarshal(payload, &draft.Source); err != nil {
		return catalog.Draft{}, fmt.Errorf("decode stored draft: %w", err)
	}
	draft.UpdatedAt = draft.UpdatedAt.UTC()
	return draft, nil
}

func (s *Store) Publish(ctx context.Context, input catalog.PublishInput) (catalog.Release, error) {
	if err := compiler.VerifyManifest(input.Manifest); err != nil {
		return catalog.Release{}, fmt.Errorf("verify release manifest: %w", err)
	}
	payload, err := json.Marshal(input.Manifest)
	if err != nil {
		return catalog.Release{}, fmt.Errorf("encode release manifest: %w", err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return catalog.Release{}, fmt.Errorf("begin publish transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var revision int64
	var sourcePayload []byte
	if err := tx.QueryRow(ctx,
		`SELECT revision, source FROM metricspire_drafts WHERE namespace = $1 AND model_name = $2 FOR UPDATE`,
		input.Namespace, input.Name,
	).Scan(&revision, &sourcePayload); errors.Is(err, pgx.ErrNoRows) {
		return catalog.Release{}, catalog.ErrNotFound
	} else if err != nil {
		return catalog.Release{}, fmt.Errorf("lock draft for publish: %w", err)
	}
	if revision != input.ExpectedRevision {
		return catalog.Release{}, catalog.ErrConflict
	}
	var storedSource model.SemanticSource
	if err := json.Unmarshal(sourcePayload, &storedSource); err != nil {
		return catalog.Release{}, fmt.Errorf("decode draft for publish: %w", err)
	}
	storedManifest, err := compiler.Compile(storedSource)
	if err != nil {
		return catalog.Release{}, fmt.Errorf("compile draft for publish: %w", err)
	}
	if storedManifest.Fingerprint != input.Manifest.Fingerprint {
		return catalog.Release{}, fmt.Errorf("%w: release manifest does not match draft revision", catalog.ErrConflict)
	}
	var createdAt time.Time
	if err := tx.QueryRow(ctx, `
INSERT INTO metricspire_releases
    (namespace, model_name, release_id, source_revision, manifest_fingerprint, manifest, created_by, note)
VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8)
RETURNING created_at`, input.Namespace, input.Name, input.ReleaseID, input.ExpectedRevision,
		input.Manifest.Fingerprint, payload, input.Actor, input.Note,
	).Scan(&createdAt); err != nil {
		return catalog.Release{}, classify("insert release", err)
	}
	previous, err := lockActive(ctx, tx, input.Namespace, input.Name)
	if err != nil {
		return catalog.Release{}, err
	}
	if previous != input.ExpectedActiveReleaseID {
		return catalog.Release{}, fmt.Errorf("%w: active release changed before publish", catalog.ErrConflict)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO metricspire_active_releases (namespace, model_name, release_id, activated_by)
VALUES ($1, $2, $3, $4)
ON CONFLICT (namespace, model_name) DO UPDATE
SET release_id = EXCLUDED.release_id, activated_by = EXCLUDED.activated_by, activated_at = clock_timestamp()`,
		input.Namespace, input.Name, input.ReleaseID, input.Actor,
	); err != nil {
		return catalog.Release{}, classify("activate published release", err)
	}
	if err := insertEvent(ctx, tx, input.Namespace, input.Name, catalog.EventPublished, previous, input.ReleaseID, input.Actor, input.Note); err != nil {
		return catalog.Release{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return catalog.Release{}, classify("commit publish", err)
	}
	return catalog.Release{
		ID: input.ReleaseID, Namespace: input.Namespace, Name: input.Name,
		SourceRevision: input.ExpectedRevision, ManifestFingerprint: input.Manifest.Fingerprint,
		Manifest: input.Manifest, CreatedBy: input.Actor, Note: input.Note, CreatedAt: createdAt.UTC(),
	}, nil
}

func (s *Store) Activate(ctx context.Context, input catalog.ActivateInput) (catalog.Release, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return catalog.Release{}, fmt.Errorf("begin activation transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	release, err := getRelease(ctx, tx, input.Namespace, input.Name, input.ReleaseID)
	if err != nil {
		return catalog.Release{}, err
	}
	previous, err := lockActive(ctx, tx, input.Namespace, input.Name)
	if err != nil {
		return catalog.Release{}, err
	}
	if previous == "" {
		return catalog.Release{}, catalog.ErrNotFound
	}
	if previous == input.ReleaseID {
		return catalog.Release{}, catalog.ErrConflict
	}
	command, err := tx.Exec(ctx, `
UPDATE metricspire_active_releases
SET release_id = $3, activated_by = $4, activated_at = clock_timestamp()
WHERE namespace = $1 AND model_name = $2`, input.Namespace, input.Name, input.ReleaseID, input.Actor)
	if err != nil {
		return catalog.Release{}, classify("activate release", err)
	}
	if command.RowsAffected() != 1 {
		return catalog.Release{}, catalog.ErrConflict
	}
	if err := insertEvent(ctx, tx, input.Namespace, input.Name, input.Kind, previous, input.ReleaseID, input.Actor, input.Note); err != nil {
		return catalog.Release{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return catalog.Release{}, classify("commit activation", err)
	}
	return release, nil
}

func (s *Store) GetRelease(ctx context.Context, namespace, name, id string) (catalog.Release, error) {
	return getRelease(ctx, s.pool, namespace, name, id)
}

func (s *Store) GetActiveRelease(ctx context.Context, namespace, name string) (catalog.Release, error) {
	return scanRelease(s.pool.QueryRow(ctx, `
SELECT r.release_id, r.namespace, r.model_name, r.source_revision, r.manifest_fingerprint,
       r.manifest, r.created_by, r.note, r.created_at
FROM metricspire_active_releases a
JOIN metricspire_releases r
  ON r.namespace = a.namespace AND r.model_name = a.model_name AND r.release_id = a.release_id
WHERE a.namespace = $1 AND a.model_name = $2`, namespace, name))
}

func (s *Store) ListReleases(ctx context.Context, namespace, name string) ([]catalog.Release, error) {
	rows, err := s.pool.Query(ctx, `
SELECT release_id, namespace, model_name, source_revision, manifest_fingerprint,
       manifest, created_by, note, created_at
FROM metricspire_releases
WHERE namespace = $1 AND model_name = $2
ORDER BY created_at, release_id`, namespace, name)
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	defer rows.Close()
	result := make([]catalog.Release, 0)
	for rows.Next() {
		release, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, release)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	return result, nil
}

func (s *Store) ListEvents(ctx context.Context, namespace, name string) ([]catalog.ReleaseEvent, error) {
	rows, err := s.pool.Query(ctx, `
SELECT event_id, namespace, model_name, event_kind, COALESCE(from_release_id, ''),
       to_release_id, actor, note, created_at
FROM metricspire_release_events
WHERE namespace = $1 AND model_name = $2
ORDER BY event_id`, namespace, name)
	if err != nil {
		return nil, fmt.Errorf("list release events: %w", err)
	}
	defer rows.Close()
	result := make([]catalog.ReleaseEvent, 0)
	for rows.Next() {
		var event catalog.ReleaseEvent
		if err := rows.Scan(
			&event.ID, &event.Namespace, &event.Name, &event.Kind, &event.FromReleaseID,
			&event.ToReleaseID, &event.Actor, &event.Note, &event.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan release event: %w", err)
		}
		event.CreatedAt = event.CreatedAt.UTC()
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list release events: %w", err)
	}
	return result, nil
}

type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type scanner interface {
	Scan(...any) error
}

func getRelease(ctx context.Context, database queryRower, namespace, name, id string) (catalog.Release, error) {
	return scanRelease(database.QueryRow(ctx, `
SELECT release_id, namespace, model_name, source_revision, manifest_fingerprint,
       manifest, created_by, note, created_at
FROM metricspire_releases
WHERE namespace = $1 AND model_name = $2 AND release_id = $3`, namespace, name, id))
}

func scanRelease(row scanner) (catalog.Release, error) {
	var release catalog.Release
	var payload []byte
	if err := row.Scan(
		&release.ID, &release.Namespace, &release.Name, &release.SourceRevision,
		&release.ManifestFingerprint, &payload, &release.CreatedBy, &release.Note, &release.CreatedAt,
	); errors.Is(err, pgx.ErrNoRows) {
		return catalog.Release{}, catalog.ErrNotFound
	} else if err != nil {
		return catalog.Release{}, fmt.Errorf("scan release: %w", err)
	}
	if err := json.Unmarshal(payload, &release.Manifest); err != nil {
		return catalog.Release{}, fmt.Errorf("decode stored release: %w", err)
	}
	release.CreatedAt = release.CreatedAt.UTC()
	return release, nil
}

func lockActive(ctx context.Context, tx pgx.Tx, namespace, name string) (string, error) {
	var previous string
	err := tx.QueryRow(ctx, `
SELECT release_id FROM metricspire_active_releases
WHERE namespace = $1 AND model_name = $2
FOR UPDATE`, namespace, name).Scan(&previous)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("lock active release: %w", err)
	}
	return previous, nil
}

func insertEvent(ctx context.Context, tx pgx.Tx, namespace, name string, kind catalog.EventKind, from, to, actor, note string) error {
	var fromValue any
	if from != "" {
		fromValue = from
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO metricspire_release_events
    (namespace, model_name, event_kind, from_release_id, to_release_id, actor, note)
VALUES ($1, $2, $3, $4, $5, $6, $7)`, namespace, name, kind, fromValue, to, actor, note); err != nil {
		return classify("record release event", err)
	}
	return nil
}

func classify(operation string, err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && (postgresError.Code == "23505" || postgresError.Code == "40001") {
		return fmt.Errorf("%w: %s", catalog.ErrConflict, operation)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

var _ catalog.Repository = (*Store)(nil)
