// Package mysql implements the catalog and audit contracts on MySQL 8.4.
// It stores metric metadata, never DWS/ADS business rows.
package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/marmot1024/metricspire/internal/audit"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/model"
)

type Store struct{ db *sql.DB }

func Open(ctx context.Context, raw string) (*Store, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "mysql" || u.Host == "" || u.User == nil || u.User.Username() == "" || u.Fragment != "" || len(strings.Trim(u.Path, "/")) == 0 || strings.Contains(strings.TrimPrefix(u.Path, "/"), "/") {
		return nil, errors.New("invalid MySQL metadata URL")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, errors.New("invalid MySQL metadata options")
	}
	for k, v := range query {
		if k != "tls" || len(v) != 1 {
			return nil, errors.New("MySQL metadata URL supports only tls=true/false")
		}
	}
	tls := query.Get("tls")
	if tls == "" {
		tls = "true"
	}
	ip := net.ParseIP(u.Hostname())
	if tls != "true" && !(tls == "false" && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback()))) {
		return nil, errors.New("MySQL metadata requires verified TLS except disposable loopback tests")
	}
	c := driver.NewConfig()
	c.User = u.User.Username()
	c.Passwd, _ = u.User.Password()
	c.Net = "tcp"
	c.Addr = u.Host
	if u.Port() == "" {
		c.Addr = net.JoinHostPort(u.Hostname(), "3306")
	}
	c.DBName = strings.TrimPrefix(u.Path, "/")
	c.TLSConfig = tls
	c.ParseTime = true
	c.Loc = time.UTC
	c.Collation = "utf8mb4_bin"
	c.Timeout = 5 * time.Second
	c.ReadTimeout = 30 * time.Second
	c.WriteTimeout = 30 * time.Second
	c.Params = map[string]string{"time_zone": "'+00:00'", "sql_mode": "'STRICT_ALL_TABLES,NO_ZERO_DATE,NO_ZERO_IN_DATE,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION'"}
	connector, err := driver.NewConnector(c)
	if err != nil {
		return nil, errors.New("invalid MySQL metadata connection")
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, errors.New("connect MySQL metadata failed")
	}
	return &Store{db: db}, nil
}
func (s *Store) Close()                          { s.db.Close() }
func (s *Store) DB() *sql.DB                     { return s.db }
func (s *Store) Ready(ctx context.Context) error { return s.db.PingContext(ctx) }
func failure(op string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return catalog.ErrNotFound
	}
	var me *driver.MySQLError
	if errors.As(err, &me) && (me.Number == 1062 || me.Number == 1205 || me.Number == 1213) {
		return catalog.ErrConflict
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errors.New("MySQL metadata " + op + " failed")
}
func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// Stable model locks serialize first publication as well as updates. All
// publication, rollback and deactivation operations acquire the same lock.
func (s *Store) begin(ctx context.Context, ns, name string) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, failure("begin transaction", err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO metricspire_model_locks(namespace,model_name) VALUES(?,?) ON DUPLICATE KEY UPDATE model_name=model_name`, ns, name); err != nil {
		tx.Rollback()
		return nil, failure("lock model", err)
	}
	var locked string
	if err = tx.QueryRowContext(ctx, `SELECT model_name FROM metricspire_model_locks WHERE namespace=? AND model_name=? FOR UPDATE`, ns, name).Scan(&locked); err != nil {
		tx.Rollback()
		return nil, failure("lock model", err)
	}
	return tx, nil
}
func (s *Store) Record(ctx context.Context, e audit.QueryEvent) error {
	if err := e.Validate(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO metricspire_query_audit(request_id,tenant,principal,namespace,model_name,release_id,manifest_fingerprint,logical_fingerprint,physical_fingerprint,job_id,event_kind,error_code,row_count,truncated,occurred_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, e.RequestID, e.Tenant, e.Principal, e.Namespace, e.ModelName, e.ReleaseID, e.ManifestFingerprint, e.LogicalFingerprint, e.PhysicalFingerprint, e.JobID, e.Kind, e.ErrorCode, e.RowCount, e.Truncated, e.OccurredAt.UTC())
	if err != nil {
		return failure("record audit", err)
	}
	return nil
}
func (s *Store) SaveDraft(ctx context.Context, in catalog.SaveDraftInput) (catalog.Draft, error) {
	payload, err := json.Marshal(in.Source)
	if err != nil {
		return catalog.Draft{}, err
	}
	name := in.Source.Metadata.Name
	tx, err := s.begin(ctx, in.Namespace, name)
	if err != nil {
		return catalog.Draft{}, err
	}
	defer tx.Rollback()
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM metricspire_drafts WHERE namespace=? AND model_name=?`, in.Namespace, name).Scan(&revision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return catalog.Draft{}, failure("read draft", err)
	}
	if revision != in.ExpectedRevision {
		return catalog.Draft{}, catalog.ErrConflict
	}
	stamp := now()
	revision++
	if revision == 1 {
		_, err = tx.ExecContext(ctx, `INSERT INTO metricspire_drafts(namespace,model_name,revision,source,updated_by,updated_at) VALUES(?,?,?,?,?,?)`, in.Namespace, name, revision, payload, in.Actor, stamp)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE metricspire_drafts SET revision=?,source=?,updated_by=?,updated_at=? WHERE namespace=? AND model_name=?`, revision, payload, in.Actor, stamp, in.Namespace, name)
	}
	if err != nil {
		return catalog.Draft{}, failure("save draft", err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO metricspire_draft_revisions(namespace,model_name,revision,source,created_by,created_at) VALUES(?,?,?,?,?,?)`, in.Namespace, name, revision, payload, in.Actor, stamp); err != nil {
		return catalog.Draft{}, failure("save revision", err)
	}
	if err = tx.Commit(); err != nil {
		return catalog.Draft{}, failure("commit draft", err)
	}
	return catalog.Draft{Namespace: in.Namespace, Name: name, Revision: revision, Source: in.Source, UpdatedBy: in.Actor, UpdatedAt: stamp}, nil
}
func (s *Store) GetDraft(ctx context.Context, ns, name string) (catalog.Draft, error) {
	var d catalog.Draft
	var data []byte
	if err := s.db.QueryRowContext(ctx, `SELECT namespace,model_name,revision,source,updated_by,updated_at FROM metricspire_drafts WHERE namespace=? AND model_name=?`, ns, name).Scan(&d.Namespace, &d.Name, &d.Revision, &data, &d.UpdatedBy, &d.UpdatedAt); err != nil {
		return d, failure("get draft", err)
	}
	if err := json.Unmarshal(data, &d.Source); err != nil {
		return catalog.Draft{}, errors.New("stored MySQL draft is invalid")
	}
	return d, nil
}

type scanner interface{ Scan(...any) error }
type rowQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

const releaseColumns = `release_id,namespace,model_name,source_revision,manifest_fingerprint,manifest,channel,created_by,note,created_at`

func scanRelease(row scanner) (catalog.Release, error) {
	var r catalog.Release
	var data []byte
	if err := row.Scan(&r.ID, &r.Namespace, &r.Name, &r.SourceRevision, &r.ManifestFingerprint, &data, &r.Channel, &r.CreatedBy, &r.Note, &r.CreatedAt); err != nil {
		return r, failure("read release", err)
	}
	if json.Unmarshal(data, &r.Manifest) != nil {
		return catalog.Release{}, errors.New("stored MySQL release is invalid")
	}
	return r, nil
}
func getRelease(ctx context.Context, q rowQuery, ns, name, id string) (catalog.Release, error) {
	return scanRelease(q.QueryRowContext(ctx, `SELECT `+releaseColumns+` FROM metricspire_releases WHERE namespace=? AND model_name=? AND release_id=?`, ns, name, id))
}
func active(ctx context.Context, tx *sql.Tx, ns, name string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT release_id FROM metricspire_active_releases WHERE namespace=? AND model_name=?`, ns, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", failure("read active release", err)
	}
	return id, nil
}
func event(ctx context.Context, tx *sql.Tx, ns, name string, kind catalog.EventKind, from, to, actor, note string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO metricspire_release_events(namespace,model_name,event_kind,from_release_id,to_release_id,actor,note,created_at) VALUES(?,?,?,NULLIF(?,''),NULLIF(?,''),?,?,?)`, ns, name, kind, from, to, actor, note, now())
	if err != nil {
		return failure("record release event", err)
	}
	return nil
}
func setActive(ctx context.Context, tx *sql.Tx, ns, name, id, actor string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO metricspire_active_releases(namespace,model_name,release_id,activated_by,activated_at) VALUES(?,?,?,?,?) ON DUPLICATE KEY UPDATE release_id=?,activated_by=?,activated_at=?`, ns, name, id, actor, now(), id, actor, now())
	if err != nil {
		return failure("activate release", err)
	}
	return nil
}
func (s *Store) Publish(ctx context.Context, in catalog.PublishInput) (catalog.Release, error) {
	if err := compiler.VerifyManifest(in.Manifest); err != nil {
		return catalog.Release{}, err
	}
	data, err := json.Marshal(in.Manifest)
	if err != nil {
		return catalog.Release{}, err
	}
	tx, err := s.begin(ctx, in.Namespace, in.Name)
	if err != nil {
		return catalog.Release{}, err
	}
	defer tx.Rollback()
	var revision int64
	var source []byte
	if err = tx.QueryRowContext(ctx, `SELECT revision,source FROM metricspire_drafts WHERE namespace=? AND model_name=?`, in.Namespace, in.Name).Scan(&revision, &source); err != nil {
		return catalog.Release{}, failure("read publication draft", err)
	}
	if revision != in.ExpectedRevision {
		return catalog.Release{}, catalog.ErrConflict
	}
	var src model.SemanticSource
	if json.Unmarshal(source, &src) != nil {
		return catalog.Release{}, errors.New("stored MySQL draft is invalid")
	}
	compiled, err := compiler.Compile(src)
	if err != nil {
		return catalog.Release{}, err
	}
	if compiled.Fingerprint != in.Manifest.Fingerprint {
		return catalog.Release{}, catalog.ErrConflict
	}
	previous, err := active(ctx, tx, in.Namespace, in.Name)
	if err != nil {
		return catalog.Release{}, err
	}
	if previous != in.ExpectedActiveReleaseID {
		return catalog.Release{}, catalog.ErrConflict
	}
	stamp := now()
	_, err = tx.ExecContext(ctx, `INSERT INTO metricspire_releases(namespace,model_name,release_id,source_revision,manifest_fingerprint,manifest,channel,created_by,note,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, in.Namespace, in.Name, in.ReleaseID, in.ExpectedRevision, in.Manifest.Fingerprint, data, in.Channel, in.Actor, in.Note, stamp)
	if err != nil {
		return catalog.Release{}, failure("insert release", err)
	}
	if err = setActive(ctx, tx, in.Namespace, in.Name, in.ReleaseID, in.Actor); err != nil {
		return catalog.Release{}, err
	}
	if err = event(ctx, tx, in.Namespace, in.Name, catalog.EventPublished, previous, in.ReleaseID, in.Actor, in.Note); err != nil {
		return catalog.Release{}, err
	}
	if err = tx.Commit(); err != nil {
		return catalog.Release{}, failure("commit publication", err)
	}
	return catalog.Release{ID: in.ReleaseID, Namespace: in.Namespace, Name: in.Name, SourceRevision: in.ExpectedRevision, ManifestFingerprint: in.Manifest.Fingerprint, Manifest: in.Manifest, Channel: in.Channel, CreatedBy: in.Actor, Note: in.Note, CreatedAt: stamp}, nil
}
func (s *Store) Activate(ctx context.Context, in catalog.ActivateInput) (catalog.Release, error) {
	tx, err := s.begin(ctx, in.Namespace, in.Name)
	if err != nil {
		return catalog.Release{}, err
	}
	defer tx.Rollback()
	r, err := getRelease(ctx, tx, in.Namespace, in.Name, in.ReleaseID)
	if err != nil {
		return r, err
	}
	previous, err := active(ctx, tx, in.Namespace, in.Name)
	if err != nil {
		return r, err
	}
	if previous == in.ReleaseID {
		return r, catalog.ErrConflict
	}
	if err = setActive(ctx, tx, in.Namespace, in.Name, in.ReleaseID, in.Actor); err != nil {
		return r, err
	}
	if err = event(ctx, tx, in.Namespace, in.Name, in.Kind, previous, in.ReleaseID, in.Actor, in.Note); err != nil {
		return r, err
	}
	if err = tx.Commit(); err != nil {
		return r, failure("commit activation", err)
	}
	return r, nil
}
func (s *Store) Deactivate(ctx context.Context, in catalog.DeactivateInput) (catalog.Release, error) {
	tx, err := s.begin(ctx, in.Namespace, in.Name)
	if err != nil {
		return catalog.Release{}, err
	}
	defer tx.Rollback()
	previous, err := active(ctx, tx, in.Namespace, in.Name)
	if err != nil {
		return catalog.Release{}, err
	}
	if previous == "" {
		return catalog.Release{}, catalog.ErrNotFound
	}
	r, err := getRelease(ctx, tx, in.Namespace, in.Name, previous)
	if err != nil {
		return r, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM metricspire_active_releases WHERE namespace=? AND model_name=?`, in.Namespace, in.Name); err != nil {
		return r, failure("deactivate release", err)
	}
	if err = event(ctx, tx, in.Namespace, in.Name, catalog.EventDeactivated, previous, "", in.Actor, in.Note); err != nil {
		return r, err
	}
	if err = tx.Commit(); err != nil {
		return r, failure("commit deactivation", err)
	}
	return r, nil
}
func (s *Store) GetRelease(ctx context.Context, ns, name, id string) (catalog.Release, error) {
	return getRelease(ctx, s.db, ns, name, id)
}
func (s *Store) GetActiveRelease(ctx context.Context, ns, name string) (catalog.Release, error) {
	// A single statement reads a consistent pointer and immutable release.
	return scanRelease(s.db.QueryRowContext(ctx, `SELECT r.release_id,r.namespace,r.model_name,r.source_revision,r.manifest_fingerprint,r.manifest,r.channel,r.created_by,r.note,r.created_at FROM metricspire_active_releases a JOIN metricspire_releases r ON r.namespace=a.namespace AND r.model_name=a.model_name AND r.release_id=a.release_id WHERE a.namespace=? AND a.model_name=?`, ns, name))
}
func releases(rows *sql.Rows, err error) ([]catalog.Release, error) {
	if err != nil {
		return nil, failure("list releases", err)
	}
	defer rows.Close()
	out := []catalog.Release{}
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		return nil, failure("list releases", err)
	}
	return out, nil
}
func (s *Store) ListReleases(ctx context.Context, ns, name string) ([]catalog.Release, error) {
	return releases(s.db.QueryContext(ctx, `SELECT `+releaseColumns+` FROM metricspire_releases WHERE namespace=? AND model_name=? ORDER BY created_at,release_id`, ns, name))
}
func (s *Store) ListActiveReleases(ctx context.Context, ns string) ([]catalog.Release, error) {
	return releases(s.db.QueryContext(ctx, `SELECT r.release_id,r.namespace,r.model_name,r.source_revision,r.manifest_fingerprint,r.manifest,r.channel,r.created_by,r.note,r.created_at FROM metricspire_active_releases a JOIN metricspire_releases r ON r.namespace=a.namespace AND r.model_name=a.model_name AND r.release_id=a.release_id WHERE a.namespace=? ORDER BY r.model_name`, ns))
}
func (s *Store) ListEvents(ctx context.Context, ns, name string) ([]catalog.ReleaseEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT event_id,namespace,model_name,event_kind,COALESCE(from_release_id,''),COALESCE(to_release_id,''),actor,note,created_at FROM metricspire_release_events WHERE namespace=? AND model_name=? ORDER BY event_id`, ns, name)
	if err != nil {
		return nil, failure("list events", err)
	}
	defer rows.Close()
	out := []catalog.ReleaseEvent{}
	for rows.Next() {
		var e catalog.ReleaseEvent
		if err = rows.Scan(&e.ID, &e.Namespace, &e.Name, &e.Kind, &e.FromReleaseID, &e.ToReleaseID, &e.Actor, &e.Note, &e.CreatedAt); err != nil {
			return nil, failure("read event", err)
		}
		out = append(out, e)
	}
	if err = rows.Err(); err != nil {
		return nil, failure("list events", err)
	}
	return out, nil
}

var _ catalog.Repository = (*Store)(nil)
var _ audit.Recorder = (*Store)(nil)
