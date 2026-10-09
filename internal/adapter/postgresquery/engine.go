package postgresquery

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/marmot1024/metricspire/internal/model"
)

type Config struct {
	Tenant     string
	Resources  []model.ResourceRef
	MaxDataAge time.Duration
	MaxBytes   int
}

type QueryEngine struct {
	pool      *pgxpool.Pool
	config    Config
	resources map[model.ResourceRef]bool
	now       func() time.Time
}

func NewQueryEngine(pool *pgxpool.Pool, config Config) (*QueryEngine, error) {
	if pool == nil || config.Tenant == "" || len(config.Resources) == 0 || config.MaxDataAge <= 0 || config.MaxBytes < 1 || config.MaxBytes > 4<<20 {
		return nil, errors.New("online PostgreSQL requires a separate pool, tenant, reviewed sources, freshness and byte budgets")
	}
	resources := map[model.ResourceRef]bool{}
	for _, r := range config.Resources {
		if r.Kind != model.ResourceTable || r.Catalog != "" || r.URI != "" || !identifier.MatchString(r.Schema) || !identifier.MatchString(r.Table) || r.Schema == "public" || r.Schema == "metricspire" || strings.HasPrefix(r.Schema, "pg_") || r.Schema == "information_schema" {
			return nil, errors.New("online sources must be reviewed tables outside the control and system schemas")
		}
		resources[r] = true
	}
	return &QueryEngine{pool: pool, config: config, resources: resources, now: time.Now}, nil
}

func (e *QueryEngine) Capabilities() model.EngineCapabilities { return Capabilities() }

func (e *QueryEngine) Execute(ctx context.Context, plan model.PhysicalPlan) (model.ExecutionSnapshot, error) {
	if !e.resources[plan.Root.Resource] {
		return model.ExecutionSnapshot{}, reject("permission_denied", "online source is not enabled")
	}
	stmt, err := compile(plan, e.config.Tenant)
	if err != nil {
		return model.ExecutionSnapshot{}, err
	}
	tx, err := e.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return model.ExecutionSnapshot{}, safeDatabaseError(ctx, err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='2s'`); err != nil {
		return model.ExecutionSnapshot{}, safeDatabaseError(ctx, err)
	}
	if _, err = tx.Exec(ctx, `SET LOCAL TIME ZONE 'UTC'`); err != nil {
		return model.ExecutionSnapshot{}, safeDatabaseError(ctx, err)
	}
	rows, err := tx.Query(ctx, stmt.SQL, stmt.Args...)
	if err != nil {
		return model.ExecutionSnapshot{}, safeDatabaseError(ctx, err)
	}
	defer rows.Close()
	result := &model.TypedResult{Columns: stmt.Columns, Rows: [][]any{}}
	var snapshot *model.DataSnapshot
	bytesUsed := 0
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return model.ExecutionSnapshot{}, safeDatabaseError(ctx, err)
		}
		if len(values) != len(stmt.Columns)+10 {
			return model.ExecutionSnapshot{}, reject("online_data_unavailable", "online result shape is invalid")
		}
		meta := values[len(stmt.Columns):]
		text := func(i int) string { s, _ := meta[i].(string); return s }
		count, err := strconv.ParseInt(text(0), 10, 64)
		if err != nil || count == 0 || text(1) != text(0) || text(2) != text(0) || text(3) != text(0) || text(4) == "" || text(4) != text(5) || text(6) != text(7) || text(8) != text(9) || text(8) != plan.ManifestFingerprint {
			return model.ExecutionSnapshot{}, reject("online_data_unavailable", "online snapshot is empty, incomplete or incompatible with this release")
		}
		asOf, err := time.Parse("2006-01-02 15:04:05.999999999Z07:00", text(6))
		if err != nil { // PostgreSQL may use an hours-only UTC offset.
			asOf, err = time.Parse("2006-01-02 15:04:05.999999999-07", text(6))
		}
		if err != nil || asOf.After(e.now().Add(time.Second)) || e.now().Sub(asOf) > e.config.MaxDataAge {
			return model.ExecutionSnapshot{}, reject("online_data_stale", "online data is stale or has an invalid coverage time")
		}
		if snapshot == nil {
			snapshot = &model.DataSnapshot{BatchID: text(4), DataAsOf: asOf}
		} else if snapshot.BatchID != text(4) || !snapshot.DataAsOf.Equal(asOf) {
			return model.ExecutionSnapshot{}, reject("online_data_unavailable", "online result spans multiple data batches")
		}
		if len(result.Rows) >= plan.Limit {
			return model.ExecutionSnapshot{}, reject("online_result_too_large", "result exceeds the online row budget; narrow the request")
		}
		row := make([]any, len(stmt.Columns))
		for i, t := range stmt.Types {
			if values[i] == nil {
				continue
			}
			v, ok := values[i].(string)
			if !ok {
				return model.ExecutionSnapshot{}, reject("online_data_unavailable", "invalid online scalar")
			}
			switch t {
			case model.DataTypeInteger:
				row[i], err = strconv.ParseInt(v, 10, 64)
			case model.DataTypeBoolean:
				row[i], err = strconv.ParseBool(v)
			default:
				row[i] = v
			}
			if err != nil {
				return model.ExecutionSnapshot{}, reject("online_data_unavailable", "invalid online scalar")
			}
		}
		encoded, _ := json.Marshal(row)
		bytesUsed += len(encoded)
		if bytesUsed > e.config.MaxBytes {
			return model.ExecutionSnapshot{}, reject("online_result_too_large", "result exceeds the online byte budget")
		}
		result.Rows = append(result.Rows, row)
	}
	if err = rows.Err(); err != nil {
		return model.ExecutionSnapshot{}, safeDatabaseError(ctx, err)
	}
	if snapshot == nil {
		return model.ExecutionSnapshot{}, reject("online_data_unavailable", "no online data covers this request")
	}
	if err = tx.Commit(ctx); err != nil {
		return model.ExecutionSnapshot{}, safeDatabaseError(ctx, err)
	}
	now := e.now().UTC()
	return model.ExecutionSnapshot{Job: model.ExecutionJob{Status: model.JobSucceeded, SubmittedAt: now, FinishedAt: &now, PhysicalFingerprint: plan.Fingerprint}, Result: result, DataSnapshot: snapshot}, nil
}

func safeDatabaseError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return reject("online_engine_unavailable", "online data source could not complete the read")
}
