package servingrefresh

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/marmot1024/metricspire/internal/model"
)

// Connect accepts only an explicit operator credential, separate from serving.
func Connect(ctx context.Context, raw string) (*pgx.Conn, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" || u.User == nil || u.User.Username() == "" {
		return nil, errors.New("explicit refresh PostgreSQL URL required")
	}
	ip := net.ParseIP(u.Hostname())
	local := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, errors.New("invalid refresh connection options")
	}
	for k, v := range q {
		if k != "sslmode" || len(v) != 1 {
			return nil, errors.New("refresh URL supports only sslmode")
		}
	}
	if q.Get("sslmode") != "verify-full" && !(local && q.Get("sslmode") == "disable") {
		return nil, errors.New("refresh requires verified TLS except disposable loopback tests")
	}
	c, err := pgx.ParseConfig(raw)
	if err != nil {
		return nil, errors.New("invalid refresh connection")
	}
	c.ConnectTimeout = 5 * time.Second
	conn, err := pgx.ConnectConfig(ctx, c)
	if err != nil {
		return nil, errors.New("refresh database unavailable")
	}
	return conn, nil
}
func table(c Contract) string { return pgx.Identifier{c.Resource.Schema, c.Resource.Table}.Sanitize() }
func control(c Contract, name string) string {
	return pgx.Identifier{c.Resource.Schema, "_metricspire_refresh_" + name}.Sanitize()
}

// Init is a separate operator step; serving never installs producer state.
func Init(ctx context.Context, conn *pgx.Conn, c Contract) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return errors.New("refresh initialization unavailable")
	}
	defer rollback(tx)
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "metricspire-refresh-init:"+c.Resource.Schema)
	if err == nil {
		_, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS `+control(c, "batches")+` (resource text NOT NULL,tenant text NOT NULL,batch_id text NOT NULL,receipt jsonb NOT NULL,committed_at timestamptz NOT NULL DEFAULT clock_timestamp(),PRIMARY KEY(resource,tenant,batch_id))`)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS `+control(c, "heads")+` (resource text NOT NULL,tenant text NOT NULL,batch_id text NOT NULL,data_as_of timestamptz NOT NULL,PRIMARY KEY(resource,tenant))`)
	}
	if err != nil {
		return errors.New("refresh state initialization failed; schema must be pre-provisioned")
	}
	if err = tx.Commit(ctx); err != nil {
		return errors.New("refresh initialization commit failed")
	}
	return nil
}
func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

type Receipt struct {
	BatchID  string    `json:"batch_id"`
	Rows     int64     `json:"rows"`
	DataAsOf time.Time `json:"data_as_of"`
	SHA256   string    `json:"sha256"`
	Repeated bool      `json:"repeated"`
}

// Refresh replaces exactly one tenant snapshot in one transaction. Expected is
// mandatory (empty means first publication), preventing concurrent lost updates.
func Refresh(ctx context.Context, conn *pgx.Conn, c Contract, input io.Reader, tenant, expected string) (Receipt, error) {
	fail := func() (Receipt, error) {
		return Receipt{}, errors.New("refresh database operation failed; no publication confirmed")
	}
	br := bufio.NewReaderSize(input, 65536)
	h, err := readHeader(br, c)
	if err != nil {
		return Receipt{}, err
	}
	if h.Tenant != tenant || tenant == "" {
		return Receipt{}, errors.New("batch tenant does not match explicit refresh tenant")
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fail()
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='10min'; SET LOCAL lock_timeout='5s'; SET LOCAL TIME ZONE 'UTC'`); err != nil {
		return fail()
	}
	resource := c.Resource.Schema + "." + c.Resource.Table
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, resource+":"+tenant); err != nil {
		return fail()
	}
	var head string
	var watermark time.Time
	err = tx.QueryRow(ctx, `SELECT batch_id,data_as_of FROM `+control(c, "heads")+` WHERE resource=$1 AND tenant=$2`, resource, tenant).Scan(&head, &watermark)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fail()
	}
	// Repeated batches are verified fully, never used to restore superseded data.
	var previous []byte
	err = tx.QueryRow(ctx, `SELECT receipt FROM `+control(c, "batches")+` WHERE resource=$1 AND tenant=$2 AND batch_id=$3`, resource, tenant, h.BatchID).Scan(&previous)
	repeated := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fail()
	}
	if repeated {
		var old Header
		if json.Unmarshal(previous, &old) != nil || !sameHeader(old, h) {
			return Receipt{}, errors.New("batch ID reused with different content or metadata")
		}
		if head != h.BatchID {
			return Receipt{}, errors.New("batch has been superseded; historical retry cannot overwrite current data")
		}
	} else if expected != head {
		return Receipt{}, errors.New("refresh head changed; review current batch before retry")
	} else if !watermark.IsZero() && !h.DataAsOf.After(watermark) {
		return Receipt{}, errors.New("source watermark must advance")
	}
	if _, err = tx.Exec(ctx, `LOCK TABLE `+table(c)+` IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return fail()
	}
	limits, err := validateTarget(ctx, tx, c)
	if err != nil {
		return Receipt{}, err
	}
	if _, err = tx.Exec(ctx, `CREATE TEMP TABLE metricspire_refresh_stage (LIKE `+table(c)+` INCLUDING CONSTRAINTS) ON COMMIT DROP`); err != nil {
		return fail()
	}
	if _, err = tx.Exec(ctx, `CREATE UNIQUE INDEX ON metricspire_refresh_stage (`+pgx.Identifier{c.Key}.Sanitize()+`)`); err != nil {
		return fail()
	}
	rr := newRows(br, c)
	copy := &copyRows{rows: rr, h: h, limits: limits}
	columns := make([]string, 0, len(c.Columns)+4)
	for _, col := range c.Columns {
		columns = append(columns, col.Name)
	}
	columns = append(columns, "_metricspire_tenant", "_metricspire_batch_id", "_metricspire_data_as_of", "_metricspire_data_contract")
	count, err := tx.CopyFrom(ctx, pgx.Identifier{"metricspire_refresh_stage"}, columns, copy)
	if rr.err != nil {
		return Receipt{}, errors.New("invalid refresh artifact; previous snapshot retained")
	}
	if err != nil {
		return fail()
	}
	if count != h.RowCount {
		return Receipt{}, errors.New("staged row count mismatch")
	}
	if err = rr.verify(h); err != nil {
		return Receipt{}, err
	}
	// Reconcile values after PostgreSQL conversion, detecting rounding or coercion.
	for _, col := range c.Columns {
		if !numeric(col.Type) {
			continue
		}
		var actual *string
		if err = tx.QueryRow(ctx, `SELECT SUM(`+pgx.Identifier{col.Name}.Sanitize()+`)::text FROM metricspire_refresh_stage`).Scan(&actual); err != nil {
			return fail()
		}
		want := h.Totals[col.Name]
		if (actual == nil) != (want == nil) {
			return Receipt{}, errors.New("staged NULL total mismatch")
		}
		if actual != nil {
			a, _ := decimalRat(*actual)
			b, _ := decimalRat(*want)
			if a == nil || b == nil || a.Cmp(b) != 0 {
				return Receipt{}, errors.New("database conversion changed source totals")
			}
		}
	}
	receipt := Receipt{BatchID: h.BatchID, Rows: count, DataAsOf: h.DataAsOf, SHA256: h.SHA256, Repeated: repeated}
	if repeated {
		return receipt, nil
	}
	if _, err = tx.Exec(ctx, `DELETE FROM `+table(c)+` WHERE _metricspire_tenant=$1`, tenant); err != nil {
		return fail()
	}
	names := make([]string, len(columns))
	for i, n := range columns {
		names[i] = pgx.Identifier{n}.Sanitize()
	}
	list := strings.Join(names, ",")
	tag, err := tx.Exec(ctx, `INSERT INTO `+table(c)+` (`+list+`) SELECT `+list+` FROM metricspire_refresh_stage`)
	if err != nil || tag.RowsAffected() != count {
		return fail()
	}
	encoded, _ := json.Marshal(h)
	if _, err = tx.Exec(ctx, `INSERT INTO `+control(c, "batches")+` (resource,tenant,batch_id,receipt) VALUES ($1,$2,$3,$4)`, resource, tenant, h.BatchID, encoded); err != nil {
		return fail()
	}
	if _, err = tx.Exec(ctx, `INSERT INTO `+control(c, "heads")+` (resource,tenant,batch_id,data_as_of) VALUES ($1,$2,$3,$4) ON CONFLICT(resource,tenant) DO UPDATE SET batch_id=excluded.batch_id,data_as_of=excluded.data_as_of`, resource, tenant, h.BatchID, h.DataAsOf); err != nil {
		return fail()
	}
	if err = tx.Commit(ctx); err != nil {
		return Receipt{}, errors.New("refresh commit outcome unknown; retry identical batch to reconcile")
	}
	return receipt, nil
}
func sameHeader(a, b Header) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

type copyRows struct {
	rows   *rows
	h      Header
	limits map[string][2]int
}

func (c *copyRows) Next() bool { return c.rows.next() }
func (c *copyRows) Err() error { return c.rows.err }
func (c *copyRows) Values() ([]any, error) {
	out := make([]any, 0, len(c.rows.c.Columns)+4)
	for _, col := range c.rows.c.Columns {
		v := c.rows.last[col.Name]
		var value any
		if v != nil {
			switch col.Type {
			case model.DataTypeString:
				value = *v
			case model.DataTypeInteger:
				value, _ = strconv.ParseInt(*v, 10, 64)
			case model.DataTypeBoolean:
				value = *v == "true"
			case model.DataTypeDate:
				t, _ := time.Parse(time.DateOnly, *v)
				value = pgtype.Date{Time: t, Valid: true}
			case model.DataTypeDecimal:
				if limit, ok := c.limits[col.Name]; ok {
					parts := strings.Split(strings.TrimPrefix(*v, "-"), ".")
					integer := strings.TrimLeft(parts[0], "0")
					fraction := ""
					if len(parts) == 2 {
						fraction = strings.TrimRight(parts[1], "0")
					}
					if len(integer) > limit[0]-limit[1] || len(fraction) > limit[1] {
						return nil, errors.New("refresh decimal cannot fit target without rounding")
					}
				}
				var n pgtype.Numeric
				if err := n.Scan(*v); err != nil {
					return nil, errors.New("invalid numeric artifact")
				}
				value = n
			}
		}
		out = append(out, value)
	}
	return append(out, c.h.Tenant, c.h.BatchID, c.h.DataAsOf, c.h.Contract), nil
}
func validateTarget(ctx context.Context, tx pgx.Tx, c Contract) (map[string][2]int, error) {
	limits := map[string][2]int{}
	fail := func() error {
		return errors.New("refresh target must be a reviewed base table with exact typed columns and non-null snapshot metadata")
	}
	var safe bool
	if err := tx.QueryRow(ctx, `SELECT relkind='r' AND NOT relrowsecurity AND NOT relforcerowsecurity AND NOT relispartition
 AND NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=pg_class.oid)
 AND NOT EXISTS(SELECT 1 FROM pg_rewrite WHERE ev_class=pg_class.oid)
 AND NOT EXISTS(SELECT 1 FROM pg_inherits WHERE inhrelid=pg_class.oid OR inhparent=pg_class.oid)
 FROM pg_class WHERE oid=to_regclass($1)`, table(c)).Scan(&safe); err != nil || !safe {
		return nil, fail()
	}
	r, err := tx.Query(ctx, `SELECT attname,format_type(atttypid,atttypmod),attnotnull,attgenerated::text FROM pg_attribute WHERE attrelid=to_regclass($1) AND attnum>0 AND NOT attisdropped`, table(c))
	if err != nil {
		return nil, fail()
	}
	defer r.Close()
	types := map[string]string{}
	nonnull := map[string]bool{}
	for r.Next() {
		var n, t, g string
		var nn bool
		if r.Scan(&n, &t, &nn, &g) != nil || g != "" {
			return nil, fail()
		}
		types[n] = t
		nonnull[n] = nn
	}
	if r.Err() != nil || len(types) != len(c.Columns)+4 {
		return nil, fail()
	}
	stringType := func(s string) bool { return s == "text" || strings.HasPrefix(s, "character varying(") }
	for _, col := range c.Columns {
		typ := types[col.Name]
		valid := false
		switch col.Type {
		case model.DataTypeString:
			valid = stringType(typ)
		case model.DataTypeDate:
			valid = typ == "date"
		case model.DataTypeBoolean:
			valid = typ == "boolean"
		case model.DataTypeInteger:
			valid = typ == "bigint"
		case model.DataTypeDecimal:
			valid = typ == "numeric"
			if m := regexp.MustCompile(`^numeric\(([0-9]+),([0-9]+)\)$`).FindStringSubmatch(typ); m != nil {
				p, _ := strconv.Atoi(m[1])
				s, _ := strconv.Atoi(m[2])
				if p > 0 && s >= 0 && s <= p {
					valid = true
					limits[col.Name] = [2]int{p, s}
				}
			}
		}
		if !valid {
			return nil, fail()
		}
	}
	for _, n := range []string{"_metricspire_tenant", "_metricspire_batch_id", "_metricspire_data_contract"} {
		if !stringType(types[n]) || !nonnull[n] {
			return nil, fail()
		}
	}
	if types["_metricspire_data_as_of"] != "timestamp with time zone" || !nonnull["_metricspire_data_as_of"] {
		return nil, fail()
	}
	return limits, nil
}
