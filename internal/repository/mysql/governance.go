package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/governance"
)

func governanceFailure(op string, err error) error {
	if !errors.Is(err, catalog.ErrConflict) && !errors.Is(err, catalog.ErrNotFound) {
		err = failure(op, err)
	}
	if errors.Is(err, catalog.ErrConflict) {
		return governance.ErrConflict
	}
	if errors.Is(err, catalog.ErrNotFound) {
		return governance.ErrNotFound
	}
	return err
}
func latestImport(ctx context.Context, tx *sql.Tx, ns string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT import_id FROM metricspire_governance_imports WHERE namespace=? AND rolled_back_at IS NULL ORDER BY import_order DESC LIMIT 1`, ns).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", governanceFailure("read import head", err)
	}
	return id, nil
}

func (s *Store) Import(ctx context.Context, id string, in governance.ImportInput) (governance.ImportBatch, error) {
	tx, err := s.begin(ctx, in.Namespace, "")
	if err != nil {
		return governance.ImportBatch{}, governanceFailure("lock governance namespace", err)
	}
	defer tx.Rollback()
	previous, err := latestImport(ctx, tx, in.Namespace)
	if err != nil {
		return governance.ImportBatch{}, err
	}
	if previous != in.ExpectedPreviousImportID {
		return governance.ImportBatch{}, governance.ErrConflict
	}
	stamp := now()
	_, err = tx.ExecContext(ctx, `INSERT INTO metricspire_governance_imports(import_id,namespace,source_fingerprint,previous_import_id,record_count,created_by,created_at) VALUES(?,?,?,NULLIF(?,''),?,?,?)`, id, in.Namespace, in.SourceFingerprint, previous, len(in.Records), in.Actor, stamp)
	if err != nil {
		return governance.ImportBatch{}, governanceFailure("create import", err)
	}
	for _, definition := range in.Records {
		data, err := json.Marshal(definition)
		if err != nil {
			return governance.ImportBatch{}, err
		}
		var current, maximum int64
		err = tx.QueryRowContext(ctx, `SELECT revision FROM metricspire_governance_records WHERE namespace=? AND metric_code=?`, in.Namespace, definition.Code).Scan(&current)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return governance.ImportBatch{}, governanceFailure("read current record", err)
		}
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0) FROM metricspire_governance_record_revisions WHERE namespace=? AND metric_code=?`, in.Namespace, definition.Code).Scan(&maximum); err != nil {
			return governance.ImportBatch{}, governanceFailure("read revision high-water mark", err)
		}
		next := maximum + 1
		if current == 0 {
			_, err = tx.ExecContext(ctx, `INSERT INTO metricspire_governance_records(namespace,metric_code,revision,definition,semantic_model_name,source_import_id,updated_by,updated_at) VALUES(?,?,?,?,NULLIF(?,''),?,?,?)`, in.Namespace, definition.Code, next, data, definition.SemanticModelName, id, in.Actor, stamp)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE metricspire_governance_records SET revision=?,definition=?,semantic_model_name=NULLIF(?,''),source_import_id=?,updated_by=?,updated_at=? WHERE namespace=? AND metric_code=?`, next, data, definition.SemanticModelName, id, in.Actor, stamp, in.Namespace, definition.Code)
		}
		if err != nil {
			return governance.ImportBatch{}, governanceFailure("save governance record", err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO metricspire_governance_record_revisions(namespace,metric_code,revision,definition,semantic_model_name,source_import_id,created_by,created_at) VALUES(?,?,?,?,NULLIF(?,''),?,?,?)`, in.Namespace, definition.Code, next, data, definition.SemanticModelName, id, in.Actor, stamp); err != nil {
			return governance.ImportBatch{}, governanceFailure("save governance revision", err)
		}
		var prior any
		if current > 0 {
			prior = current
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO metricspire_governance_import_items(import_id,metric_code,previous_revision,applied_revision) VALUES(?,?,?,?)`, id, definition.Code, prior, next); err != nil {
			return governance.ImportBatch{}, governanceFailure("save import item", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return governance.ImportBatch{}, governanceFailure("commit import", err)
	}
	return governance.ImportBatch{ID: id, Namespace: in.Namespace, SourceFingerprint: in.SourceFingerprint, PreviousImportID: previous, RecordCount: len(in.Records), CreatedBy: in.Actor, CreatedAt: stamp}, nil
}
func records(rows *sql.Rows, err error) ([]governance.MetricRecord, error) {
	if err != nil {
		return nil, governanceFailure("list governance records", err)
	}
	defer rows.Close()
	out := []governance.MetricRecord{}
	for rows.Next() {
		var r governance.MetricRecord
		var data []byte
		if err = rows.Scan(&r.Namespace, &r.Revision, &r.SourceImportID, &r.UpdatedBy, &r.UpdatedAt, &data); err != nil {
			return nil, governanceFailure("read governance record", err)
		}
		if json.Unmarshal(data, &r.Definition) != nil {
			return nil, errors.New("stored MySQL governance record is invalid")
		}
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		return nil, governanceFailure("list governance records", err)
	}
	return out, nil
}
func (s *Store) List(ctx context.Context, ns, search string, limit int) ([]governance.MetricRecord, error) {
	return records(s.db.QueryContext(ctx, `SELECT namespace,revision,source_import_id,updated_by,updated_at,definition FROM metricspire_governance_records WHERE namespace=? AND (?='' OR LOCATE(?,LOWER(CAST(definition AS CHAR)))>0) ORDER BY metric_code LIMIT ?`, ns, strings.ToLower(search), strings.ToLower(search), limit))
}
func (s *Store) ListByCodes(ctx context.Context, ns string, codes []string) ([]governance.MetricRecord, error) {
	if len(codes) == 0 {
		return []governance.MetricRecord{}, nil
	}
	if len(codes) > governance.MaxImportRecords {
		return nil, governance.ErrInvalid
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(codes)), ",")
	args := []any{ns}
	for _, code := range codes {
		args = append(args, code)
	}
	return records(s.db.QueryContext(ctx, `SELECT namespace,revision,source_import_id,updated_by,updated_at,definition FROM metricspire_governance_records WHERE namespace=? AND metric_code IN (`+placeholders+`) ORDER BY metric_code`, args...))
}

const importColumns = `import_id,namespace,source_fingerprint,COALESCE(previous_import_id,''),record_count,created_by,created_at,COALESCE(rolled_back_by,''),rolled_back_at`

func scanImport(row scanner) (governance.ImportBatch, error) {
	var b governance.ImportBatch
	if err := row.Scan(&b.ID, &b.Namespace, &b.SourceFingerprint, &b.PreviousImportID, &b.RecordCount, &b.CreatedBy, &b.CreatedAt, &b.RolledBackBy, &b.RolledBackAt); err != nil {
		return b, governanceFailure("read import", err)
	}
	return b, nil
}
func (s *Store) GetImport(ctx context.Context, ns, id string) (governance.ImportBatch, error) {
	return scanImport(s.db.QueryRowContext(ctx, `SELECT `+importColumns+` FROM metricspire_governance_imports WHERE namespace=? AND import_id=?`, ns, id))
}
func (s *Store) RollbackImport(ctx context.Context, ns, id, actor string) (governance.ImportBatch, error) {
	tx, err := s.begin(ctx, ns, "")
	if err != nil {
		return governance.ImportBatch{}, governanceFailure("lock rollback namespace", err)
	}
	defer tx.Rollback()
	batch, err := scanImport(tx.QueryRowContext(ctx, `SELECT `+importColumns+` FROM metricspire_governance_imports WHERE namespace=? AND import_id=?`, ns, id))
	if err != nil {
		return batch, err
	}
	latest, err := latestImport(ctx, tx, ns)
	if err != nil {
		return batch, err
	}
	if batch.RolledBackAt != nil || latest != id {
		return batch, governance.ErrConflict
	}
	rows, err := tx.QueryContext(ctx, `SELECT metric_code,previous_revision,applied_revision FROM metricspire_governance_import_items WHERE import_id=? ORDER BY metric_code`, id)
	if err != nil {
		return batch, governanceFailure("read rollback items", err)
	}
	type item struct {
		code     string
		previous sql.NullInt64
		applied  int64
	}
	items := []item{}
	for rows.Next() {
		var it item
		if err = rows.Scan(&it.code, &it.previous, &it.applied); err != nil {
			rows.Close()
			return batch, governanceFailure("read rollback item", err)
		}
		items = append(items, it)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return batch, governanceFailure("read rollback items", err)
	}
	if len(items) != batch.RecordCount {
		return batch, governance.ErrConflict
	}
	stamp := now()
	for _, it := range items {
		var current int64
		if err = tx.QueryRowContext(ctx, `SELECT revision FROM metricspire_governance_records WHERE namespace=? AND metric_code=?`, ns, it.code).Scan(&current); err != nil {
			return batch, governanceFailure("read rollback record", err)
		}
		if current != it.applied {
			return batch, governance.ErrConflict
		}
		if !it.previous.Valid {
			if _, err = tx.ExecContext(ctx, `DELETE FROM metricspire_governance_records WHERE namespace=? AND metric_code=?`, ns, it.code); err != nil {
				return batch, governanceFailure("remove imported record", err)
			}
			continue
		}
		var data []byte
		var modelName sql.NullString
		var sourceID string
		if err = tx.QueryRowContext(ctx, `SELECT definition,semantic_model_name,source_import_id FROM metricspire_governance_record_revisions WHERE namespace=? AND metric_code=? AND revision=?`, ns, it.code, it.previous.Int64).Scan(&data, &modelName, &sourceID); err != nil {
			return batch, governanceFailure("read prior governance revision", err)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE metricspire_governance_records SET revision=?,definition=?,semantic_model_name=?,source_import_id=?,updated_by=?,updated_at=? WHERE namespace=? AND metric_code=?`, it.previous.Int64, data, modelName, sourceID, actor, stamp, ns, it.code); err != nil {
			return batch, governanceFailure("restore governance record", err)
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE metricspire_governance_imports SET rolled_back_by=?,rolled_back_at=? WHERE namespace=? AND import_id=? AND rolled_back_at IS NULL`, actor, stamp, ns, id); err != nil {
		return batch, governanceFailure("mark import rolled back", err)
	}
	if err = tx.Commit(); err != nil {
		return batch, governanceFailure("commit import rollback", err)
	}
	batch.RolledBackBy = actor
	batch.RolledBackAt = &stamp
	return batch, nil
}

var _ governance.Repository = (*Store)(nil)
