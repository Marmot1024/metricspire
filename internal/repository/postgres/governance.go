package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/marmot1024/metricspire/internal/governance"
)

func (s *Store) Import(ctx context.Context, id string, input governance.ImportInput) (governance.ImportBatch, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return governance.ImportBatch{}, fmt.Errorf("begin governance import: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "metricspire_governance:"+input.Namespace); err != nil {
		return governance.ImportBatch{}, fmt.Errorf("lock governance namespace: %w", err)
	}
	previous, err := latestActiveGovernanceImport(ctx, tx, input.Namespace)
	if err != nil {
		return governance.ImportBatch{}, err
	}
	if previous != input.ExpectedPreviousImportID {
		return governance.ImportBatch{}, governance.ErrConflict
	}
	var createdAt time.Time
	var previousValue any
	if previous != "" {
		previousValue = previous
	}
	if err := tx.QueryRow(ctx, `
INSERT INTO metricspire_governance_imports
    (import_id, namespace, source_fingerprint, previous_import_id, record_count, created_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING created_at`, id, input.Namespace, input.SourceFingerprint, previousValue, len(input.Records), input.Actor).Scan(&createdAt); err != nil {
		return governance.ImportBatch{}, classifyGovernance("create governance import", err)
	}
	for _, definition := range input.Records {
		payload, err := json.Marshal(definition)
		if err != nil {
			return governance.ImportBatch{}, fmt.Errorf("encode governance record %s: %w", definition.Code, err)
		}
		var current int64
		err = tx.QueryRow(ctx, `
SELECT revision FROM metricspire_governance_records
WHERE namespace = $1 AND metric_code = $2 FOR UPDATE`, input.Namespace, definition.Code).Scan(&current)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			current = 0
		case err != nil:
			return governance.ImportBatch{}, fmt.Errorf("lock governance record %s: %w", definition.Code, err)
		}
		var maximum int64
		if err := tx.QueryRow(ctx, `
SELECT COALESCE(MAX(revision), 0)
FROM metricspire_governance_record_revisions
WHERE namespace = $1 AND metric_code = $2`, input.Namespace, definition.Code).Scan(&maximum); err != nil {
			return governance.ImportBatch{}, fmt.Errorf("read governance revision high-water mark %s: %w", definition.Code, err)
		}
		next := maximum + 1
		var modelValue any
		if definition.SemanticModelName != "" {
			modelValue = definition.SemanticModelName
		}
		if current == 0 {
			_, err = tx.Exec(ctx, `
INSERT INTO metricspire_governance_records
    (namespace, metric_code, revision, definition, semantic_model_name, source_import_id, updated_by, updated_at)
VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8)`, input.Namespace, definition.Code, next, payload, modelValue, id, input.Actor, createdAt)
		} else {
			_, err = tx.Exec(ctx, `
UPDATE metricspire_governance_records
SET revision = $3, definition = $4::jsonb, semantic_model_name = $5,
    source_import_id = $6, updated_by = $7, updated_at = $8
WHERE namespace = $1 AND metric_code = $2`, input.Namespace, definition.Code, next, payload, modelValue, id, input.Actor, createdAt)
		}
		if err != nil {
			return governance.ImportBatch{}, classifyGovernance("save governance record", err)
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO metricspire_governance_record_revisions
    (namespace, metric_code, revision, definition, semantic_model_name, source_import_id, created_by, created_at)
VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8)`, input.Namespace, definition.Code, next, payload, modelValue, id, input.Actor, createdAt); err != nil {
			return governance.ImportBatch{}, classifyGovernance("save governance record revision", err)
		}
		var previousRevision any
		if current > 0 {
			previousRevision = current
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO metricspire_governance_import_items (import_id, metric_code, previous_revision, applied_revision)
VALUES ($1, $2, $3, $4)`, id, definition.Code, previousRevision, next); err != nil {
			return governance.ImportBatch{}, classifyGovernance("record governance import item", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return governance.ImportBatch{}, classifyGovernance("commit governance import", err)
	}
	return governance.ImportBatch{ID: id, Namespace: input.Namespace, SourceFingerprint: input.SourceFingerprint, PreviousImportID: previous, RecordCount: len(input.Records), CreatedBy: input.Actor, CreatedAt: createdAt.UTC()}, nil
}

func (s *Store) List(ctx context.Context, namespace, search string, limit int) ([]governance.MetricRecord, error) {
	pattern := "%" + strings.ToLower(search) + "%"
	rows, err := s.pool.Query(ctx, `
SELECT namespace, revision, source_import_id, updated_by, updated_at, definition
FROM metricspire_governance_records
WHERE namespace = $1 AND ($2 = '%%' OR lower(definition::text) LIKE $2)
ORDER BY metric_code
LIMIT $3`, namespace, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("list governance records: %w", err)
	}
	defer rows.Close()
	result := make([]governance.MetricRecord, 0)
	for rows.Next() {
		var record governance.MetricRecord
		var payload []byte
		if err := rows.Scan(&record.Namespace, &record.Revision, &record.SourceImportID, &record.UpdatedBy, &record.UpdatedAt, &payload); err != nil {
			return nil, fmt.Errorf("scan governance record: %w", err)
		}
		if err := json.Unmarshal(payload, &record.Definition); err != nil {
			return nil, fmt.Errorf("decode governance record: %w", err)
		}
		record.UpdatedAt = record.UpdatedAt.UTC()
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list governance records: %w", err)
	}
	return result, nil
}

func (s *Store) GetImport(ctx context.Context, namespace, id string) (governance.ImportBatch, error) {
	return scanGovernanceImport(s.pool.QueryRow(ctx, `
SELECT import_id, namespace, source_fingerprint, COALESCE(previous_import_id, ''), record_count,
       created_by, created_at, COALESCE(rolled_back_by, ''), rolled_back_at
FROM metricspire_governance_imports
WHERE namespace = $1 AND import_id = $2`, namespace, id))
}

func (s *Store) RollbackImport(ctx context.Context, namespace, id, actor string) (governance.ImportBatch, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return governance.ImportBatch{}, fmt.Errorf("begin governance rollback: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "metricspire_governance:"+namespace); err != nil {
		return governance.ImportBatch{}, fmt.Errorf("lock governance namespace: %w", err)
	}
	batch, err := scanGovernanceImport(tx.QueryRow(ctx, `
SELECT import_id, namespace, source_fingerprint, COALESCE(previous_import_id, ''), record_count,
       created_by, created_at, COALESCE(rolled_back_by, ''), rolled_back_at
FROM metricspire_governance_imports
WHERE namespace = $1 AND import_id = $2 FOR UPDATE`, namespace, id))
	if err != nil {
		return governance.ImportBatch{}, err
	}
	latest, err := latestActiveGovernanceImport(ctx, tx, namespace)
	if err != nil {
		return governance.ImportBatch{}, err
	}
	if batch.RolledBackAt != nil || latest != id {
		return governance.ImportBatch{}, governance.ErrConflict
	}
	rows, err := tx.Query(ctx, `
SELECT metric_code, previous_revision, applied_revision
FROM metricspire_governance_import_items
WHERE import_id = $1 ORDER BY metric_code`, id)
	if err != nil {
		return governance.ImportBatch{}, fmt.Errorf("list governance rollback items: %w", err)
	}
	type item struct {
		code     string
		previous pgtype.Int8
		applied  int64
	}
	items := make([]item, 0, batch.RecordCount)
	for rows.Next() {
		var value item
		if err := rows.Scan(&value.code, &value.previous, &value.applied); err != nil {
			rows.Close()
			return governance.ImportBatch{}, fmt.Errorf("scan governance rollback item: %w", err)
		}
		items = append(items, value)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return governance.ImportBatch{}, fmt.Errorf("read governance rollback items: %w", err)
	}
	if len(items) != batch.RecordCount {
		return governance.ImportBatch{}, fmt.Errorf("%w: governance import item count changed", governance.ErrConflict)
	}
	now := time.Now().UTC()
	for _, value := range items {
		var current int64
		if err := tx.QueryRow(ctx, `
SELECT revision FROM metricspire_governance_records
WHERE namespace = $1 AND metric_code = $2 FOR UPDATE`, namespace, value.code).Scan(&current); err != nil {
			return governance.ImportBatch{}, classifyGovernance("lock governance rollback record", err)
		}
		if current != value.applied {
			return governance.ImportBatch{}, governance.ErrConflict
		}
		if !value.previous.Valid {
			if _, err := tx.Exec(ctx, `DELETE FROM metricspire_governance_records WHERE namespace = $1 AND metric_code = $2`, namespace, value.code); err != nil {
				return governance.ImportBatch{}, fmt.Errorf("delete imported governance record: %w", err)
			}
			continue
		}
		var payload []byte
		var modelName *string
		var sourceImportID string
		if err := tx.QueryRow(ctx, `
SELECT definition, semantic_model_name, source_import_id
FROM metricspire_governance_record_revisions
WHERE namespace = $1 AND metric_code = $2 AND revision = $3`, namespace, value.code, value.previous.Int64).Scan(&payload, &modelName, &sourceImportID); err != nil {
			return governance.ImportBatch{}, classifyGovernance("load prior governance revision", err)
		}
		if _, err := tx.Exec(ctx, `
UPDATE metricspire_governance_records
SET revision = $3, definition = $4::jsonb, semantic_model_name = $5,
    source_import_id = $6, updated_by = $7, updated_at = $8
WHERE namespace = $1 AND metric_code = $2`, namespace, value.code, value.previous.Int64, payload, modelName, sourceImportID, actor, now); err != nil {
			return governance.ImportBatch{}, classifyGovernance("restore governance record", err)
		}
	}
	if err := tx.QueryRow(ctx, `
UPDATE metricspire_governance_imports
SET rolled_back_by = $3, rolled_back_at = $4
WHERE namespace = $1 AND import_id = $2 AND rolled_back_at IS NULL
RETURNING rolled_back_at`, namespace, id, actor, now).Scan(&batch.RolledBackAt); err != nil {
		return governance.ImportBatch{}, classifyGovernance("mark governance import rolled back", err)
	}
	batch.RolledBackBy = actor
	if err := tx.Commit(ctx); err != nil {
		return governance.ImportBatch{}, classifyGovernance("commit governance rollback", err)
	}
	return batch, nil
}

func latestActiveGovernanceImport(ctx context.Context, tx pgx.Tx, namespace string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
SELECT import_id FROM metricspire_governance_imports
WHERE namespace = $1 AND rolled_back_at IS NULL
ORDER BY created_at DESC, import_id DESC LIMIT 1`, namespace).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("find latest governance import: %w", err)
	}
	return id, nil
}

type governanceImportScanner interface {
	Scan(...any) error
}

func scanGovernanceImport(row governanceImportScanner) (governance.ImportBatch, error) {
	var batch governance.ImportBatch
	if err := row.Scan(&batch.ID, &batch.Namespace, &batch.SourceFingerprint, &batch.PreviousImportID, &batch.RecordCount, &batch.CreatedBy, &batch.CreatedAt, &batch.RolledBackBy, &batch.RolledBackAt); errors.Is(err, pgx.ErrNoRows) {
		return governance.ImportBatch{}, governance.ErrNotFound
	} else if err != nil {
		return governance.ImportBatch{}, fmt.Errorf("scan governance import: %w", err)
	}
	batch.CreatedAt = batch.CreatedAt.UTC()
	if batch.RolledBackAt != nil {
		value := batch.RolledBackAt.UTC()
		batch.RolledBackAt = &value
	}
	return batch, nil
}

func classifyGovernance(operation string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return governance.ErrNotFound
	}
	return classify(operation, err)
}

var _ governance.Repository = (*Store)(nil)
