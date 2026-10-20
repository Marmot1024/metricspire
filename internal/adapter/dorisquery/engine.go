package dorisquery

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/adapter/onlineaggregate"
	"github.com/marmot1024/metricspire/internal/adapter/onlinecontract"
	"github.com/marmot1024/metricspire/internal/model"
)

var identifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
var integerDisplayType = regexp.MustCompile(`^(tinyint|smallint|int|bigint)\([0-9]{1,3}\)$`)
var decimalType = regexp.MustCompile(`^decimal\((\d+),\s*(\d+)\)$`)

type Config struct {
	Tenant     string
	Resources  []model.ResourceRef
	MaxDataAge time.Duration
	MaxBytes   int
}
type QueryEngine struct {
	db        *client
	config    Config
	resources map[model.ResourceRef]bool
	now       func() time.Time
}

func validResource(r model.ResourceRef) bool {
	return r.Kind == model.ResourceTable && r.Catalog == "" && r.URI == "" && identifier.MatchString(r.Schema) && identifier.MatchString(r.Table) && !strings.EqualFold(r.Schema, "information_schema") && !strings.EqualFold(r.Schema, "mysql")
}
func NewQueryEngine(ctx context.Context, raw string, c Config) (*QueryEngine, error) {
	if c.Tenant == "" || len(c.Resources) == 0 || c.MaxDataAge <= 0 || c.MaxBytes < 1 || c.MaxBytes > 4<<20 {
		return nil, errors.New("Doris requires reviewed sources, tenant, freshness and byte budgets")
	}
	e := &QueryEngine{config: c, resources: map[model.ResourceRef]bool{}, now: time.Now}
	for _, r := range c.Resources {
		if !validResource(r) {
			return nil, errors.New("Doris requires reviewed internal tables outside system databases")
		}
		e.resources[r] = true
	}
	var err error
	e.db, err = open(ctx, raw)
	if err != nil {
		return nil, err
	}
	if err = e.validateCredential(ctx); err != nil {
		e.Close()
		return nil, err
	}
	for r := range e.resources {
		if _, err = e.sourceColumns(ctx, r); err != nil {
			e.Close()
			return nil, err
		}
	}
	return e, nil
}
func (e *QueryEngine) Close()                          { e.db.Close() }
func (e *QueryEngine) Ready(ctx context.Context) error { return e.db.PingContext(ctx) }
func (e *QueryEngine) Capabilities() model.EngineCapabilities {
	return model.EngineCapabilities{Engine: EngineName, ExpressionOps: []model.ExpressionOp{model.OpSum, model.OpMetric, model.OpLiteral, model.OpAdd, model.OpSubtract, model.OpMultiply, model.OpDivide}, TimeGranularities: []model.TimeGranularity{model.GrainDay}}
}
func (e *QueryEngine) Execute(context.Context, model.PhysicalPlan) (model.ExecutionSnapshot, error) {
	return model.ExecutionSnapshot{}, problem("unsupported_online_query", "trusted data definitions are required")
}
func DataContractFingerprint(m model.SemanticManifest, b model.SourceBinding) (string, error) {
	return onlinecontract.Fingerprint(m, b, EngineName, "doris-online-data-v1-decimal6-result12")
}
func (e *QueryEngine) validateCredential(ctx context.Context) error {
	fail := func() error {
		return errors.New("Doris online requires only explicit SELECT table grants and no additional roles or write/grant privileges")
	}
	rows, cols, err := query(ctx, e.db, "SHOW GRANTS")
	if err != nil || len(rows) != 1 {
		return fail()
	}
	values := map[string]string{}
	for i, c := range cols {
		values[c] = strings.TrimSpace(text(rows[0][i]))
	}
	for _, c := range []string{"Roles", "GlobalPrivs", "CatalogPrivs", "DatabasePrivs", "TablePrivs", "ColPrivs", "ResourcePrivs", "WorkloadGroupPrivs"} {
		if _, ok := values[c]; !ok {
			return fail()
		}
	}
	empty := func(s string) bool { return s == "" || s == "NULL" }
	covered := map[model.ResourceRef]bool{}
	for k, v := range values {
		switch k {
		case "UserIdentity", "Comment", "Password":
			continue
		case "DatabasePrivs":
			if empty(v) {
				continue
			}
			for _, entry := range strings.Split(v, ";") {
				entry = strings.TrimSpace(entry)
				if entry != "internal.information_schema: Select_priv" && entry != "internal.mysql: Select_priv" {
					return fail()
				}
			}
		case "TablePrivs":
			if empty(v) {
				continue
			}
			for _, entry := range strings.Split(v, ";") {
				allowed := false
				for r := range e.resources {
					if strings.TrimSpace(entry) == "internal."+r.Schema+"."+r.Table+": Select_priv" {
						allowed = true
						covered[r] = true
					}
				}
				if !allowed {
					return fail()
				}
			}
		case "WorkloadGroupPrivs":
			if !empty(v) && v != "normal: Usage_priv" {
				return fail()
			}
		default:
			if !empty(v) {
				return fail()
			}
		}
	}
	for r := range e.resources {
		if !covered[r] {
			return fail()
		}
	}
	return nil
}
func (e *QueryEngine) sourceColumns(ctx context.Context, r model.ResourceRef) (map[string]string, error) {
	rows, _, err := query(ctx, e.db, "SELECT ENGINE,TABLE_TYPE FROM information_schema.tables WHERE TABLE_CATALOG='internal' AND TABLE_SCHEMA=? AND TABLE_NAME=?", r.Schema, r.Table)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 || len(rows[0]) != 2 || !strings.EqualFold(text(rows[0][0]), "Doris") || text(rows[0][1]) != "BASE TABLE" {
		return nil, problem("unsupported_online_query", "Doris requires an internal OLAP base table")
	}
	rows, _, err = query(ctx, e.db, "SELECT COLUMN_NAME,COLUMN_TYPE,IS_NULLABLE FROM information_schema.columns WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", r.Schema, r.Table)
	if err != nil {
		return nil, err
	}
	columns := map[string]string{}
	nullable := map[string]bool{}
	for _, row := range rows {
		if len(row) != 3 {
			return nil, unavailable()
		}
		columns[text(row[0])] = normalizeType(text(row[1]))
		nullable[text(row[0])] = text(row[2]) != "NO"
	}
	for _, name := range []string{"_metricspire_tenant", "_metricspire_batch_id", "_metricspire_data_contract"} {
		if !strings.HasPrefix(columns[name], "varchar(") || nullable[name] {
			return nil, problem("unsupported_online_query", "Doris snapshot metadata columns must be non-null VARCHAR")
		}
	}
	if columns["_metricspire_data_as_of"] != "datetime(3)" || nullable["_metricspire_data_as_of"] {
		return nil, problem("unsupported_online_query", "Doris snapshot time must be non-null UTC DATETIME(3)")
	}
	return columns, nil
}
func normalizeType(s string) string {
	s = strings.ToLower(s)
	if match := integerDisplayType.FindStringSubmatch(s); match != nil {
		return match[1]
	}
	if strings.HasPrefix(s, "decimalv3(") {
		return "decimal(" + strings.TrimPrefix(s, "decimalv3(")
	}
	return s
}

func supportedNumeric(s string) bool {
	switch s {
	case "tinyint", "smallint", "int", "bigint":
		return true
	}
	m := decimalType.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	p, _ := strconv.Atoi(m[1])
	scale, _ := strconv.Atoi(m[2])
	return p > 0 && p <= 18 && scale <= 6 && scale <= p
}
func (e *QueryEngine) ExecuteBound(ctx context.Context, m model.SemanticManifest, b model.SourceBinding, p model.PhysicalPlan) (model.ExecutionSnapshot, error) {
	if !e.resources[p.Root.Resource] {
		return model.ExecutionSnapshot{}, problem("permission_denied", "online source is not enabled")
	}
	if m.Fingerprint != p.ManifestFingerprint || len(b.Datasets) != 1 || b.Datasets[0].Resource != p.Root.Resource {
		return model.ExecutionSnapshot{}, problem("unsupported_online_query", "plan and data definitions do not match")
	}
	contract, err := DataContractFingerprint(m, b)
	if err != nil {
		return model.ExecutionSnapshot{}, err
	}
	columns, err := e.sourceColumns(ctx, p.Root.Resource)
	if err != nil {
		return model.ExecutionSnapshot{}, err
	}
	stmt, err := compile(p, e.config.Tenant, columns)
	if err != nil {
		return model.ExecutionSnapshot{}, err
	}
	rows, _, err := query(ctx, e.db, stmt.sql, stmt.args...)
	if err != nil {
		return model.ExecutionSnapshot{}, err
	}
	return (onlineaggregate.Config{MaxDataAge: e.config.MaxDataAge, MaxBytes: e.config.MaxBytes, Now: e.now}).Result(ctx, p, stmt.dimensions, stmt.fields, rows, contract)
}
