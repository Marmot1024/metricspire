package clickhousequery

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/adapter/onlinecontract"
	"github.com/marmot1024/metricspire/internal/model"
)

var identifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
var numericType = regexp.MustCompile(`^(U?Int(8|16|32|64)|Decimal\((\d+), (\d+)\))$`)

type Config struct {
	Tenant     string
	Resources  []model.ResourceRef
	MaxDataAge time.Duration
	MaxBytes   int
}
type QueryEngine struct {
	client    *client
	config    Config
	resources map[model.ResourceRef]bool
	now       func() time.Time
}

func NewQueryEngine(ctx context.Context, raw string, config Config) (*QueryEngine, error) {
	if config.Tenant == "" || len(config.Resources) == 0 || config.MaxDataAge <= 0 || config.MaxBytes < 1 || config.MaxBytes > 4<<20 {
		return nil, errors.New("ClickHouse requires reviewed sources, tenant, freshness and byte budgets")
	}
	c, err := newClient(raw)
	if err != nil {
		return nil, err
	}
	e := &QueryEngine{client: c, config: config, resources: map[model.ResourceRef]bool{}, now: time.Now}
	for _, r := range config.Resources {
		if !validResource(r) {
			c.close()
			return nil, errors.New("ClickHouse requires explicit tables outside system databases")
		}
		e.resources[r] = true
	}
	if err := e.validateCredential(ctx); err != nil {
		c.close()
		return nil, err
	}
	for r := range e.resources {
		if _, err := e.sourceColumns(ctx, r); err != nil {
			c.close()
			return nil, err
		}
	}
	return e, nil
}
func validResource(r model.ResourceRef) bool {
	return r.Kind == model.ResourceTable && r.Catalog == "" && r.URI == "" && identifier.MatchString(r.Schema) && identifier.MatchString(r.Table) && r.Schema != "system" && r.Schema != "default" && !strings.EqualFold(r.Schema, "information_schema")
}
func (e *QueryEngine) Close() { e.client.close() }
func (e *QueryEngine) Ready(ctx context.Context) error {
	rows, err := e.client.query(ctx, "SELECT 'ready'", nil, false)
	if err != nil {
		return err
	}
	if len(rows) != 1 || len(rows[0]) != 1 || text(rows[0][0]) != "ready" {
		return unavailable()
	}
	return nil
}
func (e *QueryEngine) Capabilities() model.EngineCapabilities {
	return model.EngineCapabilities{Engine: EngineName, ExpressionOps: []model.ExpressionOp{model.OpSum, model.OpMetric, model.OpLiteral, model.OpAdd, model.OpSubtract, model.OpMultiply, model.OpDivide}, TimeGranularities: []model.TimeGranularity{model.GrainDay}}
}
func (e *QueryEngine) Execute(context.Context, model.PhysicalPlan) (model.ExecutionSnapshot, error) {
	return model.ExecutionSnapshot{}, problem("unsupported_online_query", "trusted data definitions are required")
}
func DataContractFingerprint(m model.SemanticManifest, b model.SourceBinding) (string, error) {
	return onlinecontract.Fingerprint(m, b, EngineName, "clickhouse-online-data-v1-decimal6-result12")
}

// A SELECT-only credential with no role membership is deliberately narrower than
// a generic ClickHouse account. Reject inherited, column-level and grant-option
// write privileges, rather than inferring safety from readonly session settings.
func (e *QueryEngine) validateCredential(ctx context.Context) error {
	fail := func() error {
		return errors.New("ClickHouse online credential requires only SELECT grants, no grant option and no role membership")
	}
	roles, err := e.client.query(ctx, "SELECT toString(count()) FROM system.role_grants WHERE user_name = currentUser()", nil, false)
	if err != nil || len(roles) != 1 || len(roles[0]) != 1 || text(roles[0][0]) != "0" {
		return fail()
	}
	grants, err := e.client.query(ctx, "SELECT toString(access_type), database, table, toString(grant_option), toString(is_partial_revoke), column FROM system.grants WHERE user_name = currentUser()", nil, false)
	if err != nil || len(grants) == 0 {
		return fail()
	}
	// Older ClickHouse versions omit prefix-wildcard information from system.grants.
	// Inspect canonical grants too; never log this trusted metadata.
	shown, err := e.client.query(ctx, "SHOW GRANTS", nil, false)
	if err != nil || len(shown) == 0 {
		return fail()
	}
	for _, row := range shown {
		if len(row) != 1 || strings.Contains(text(row[0]), "*") {
			return fail()
		}
	}
	covered := map[model.ResourceRef]bool{}
	for _, g := range grants {
		if len(g) != 6 || g[5] != nil || text(g[0]) != "SELECT" || text(g[3]) != "0" || text(g[4]) != "0" {
			return fail()
		}
		db, table := text(g[1]), text(g[2])
		allowed := false
		for r := range e.resources {
			if db == r.Schema && table == r.Table {
				allowed = true
				covered[r] = true
			}
		}
		if db == "system" {
			for _, name := range []string{"grants", "role_grants", "tables", "columns"} {
				allowed = allowed || table == name
			}
		}
		if !allowed {
			return fail()
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
	params := map[string]string{"database": r.Schema, "table": r.Table}
	rows, err := e.client.query(ctx, "SELECT engine FROM system.tables WHERE database={database:String} AND name={table:String}", params, false)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 || len(rows[0]) != 1 || (text(rows[0][0]) != "MergeTree" && text(rows[0][0]) != "ReplicatedMergeTree") {
		return nil, problem("unsupported_online_query", "ClickHouse online requires a plain MergeTree or ReplicatedMergeTree table")
	}
	rows, err = e.client.query(ctx, "SELECT name,type,default_kind FROM system.columns WHERE database={database:String} AND table={table:String}", params, false)
	if err != nil {
		return nil, err
	}
	columns := map[string]string{}
	for _, row := range rows {
		if len(row) != 3 || text(row[2]) == "ALIAS" {
			return nil, problem("unsupported_online_query", "alias columns are not supported")
		}
		columns[text(row[0])] = text(row[1])
	}
	for name, want := range map[string]string{"_metricspire_tenant": "String", "_metricspire_batch_id": "String", "_metricspire_data_contract": "String", "_metricspire_data_as_of": "DateTime64(3, 'UTC')"} {
		if columns[name] != want {
			return nil, problem("unsupported_online_query", "ClickHouse snapshot metadata columns have incompatible types")
		}
	}
	return columns, nil
}
func supportedNumeric(s string) bool {
	s = unwrapNullable(s)
	m := numericType.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	if m[3] == "" {
		return true
	}
	p, _ := strconv.Atoi(m[3])
	scale, _ := strconv.Atoi(m[4])
	return p <= 18 && scale <= 6
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
	rows, err := e.client.query(ctx, stmt.sql, stmt.params, true)
	if err != nil {
		return model.ExecutionSnapshot{}, err
	}
	return e.result(ctx, p, stmt, rows, contract)
}

func unwrapNullable(s string) string {
	if strings.HasPrefix(s, "Nullable(") && strings.HasSuffix(s, ")") {
		return s[9 : len(s)-1]
	}
	return s
}
