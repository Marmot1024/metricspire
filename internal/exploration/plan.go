package exploration

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/marmot1024/metricspire/internal/canonical"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/planner"
)

var namePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
var decimalPattern = regexp.MustCompile(`^-?[0-9]{1,20}(\.[0-9]{1,18})?$`)

func invalid(path, message string) error {
	return &model.Problem{Code: "invalid_analysis", Path: path, Message: message}
}

// References collects every raw dimension used in grouping, filtering or
// aggregation. All must pass source authorization and attribute policy checks.
func References(q Query) []string {
	set := map[string]bool{}
	add := func(s string) {
		if s != "" {
			set[s] = true
		}
	}
	for _, s := range q.GroupBy {
		add(s)
	}
	if q.TimeRange != nil {
		add(q.TimeRange.Dimension)
	}
	if q.TimeGrouping != nil {
		add(q.TimeGrouping.Dimension)
	}
	for _, f := range q.Filters {
		add(f.Dimension)
	}
	var visit func(Expression)
	visit = func(e Expression) {
		if e.Op == "ref" {
			add(e.Ref)
		}
		for _, a := range e.Args {
			visit(a)
		}
	}
	for _, m := range q.Measures {
		add(m.Field)
		if m.Where != nil {
			visit(*m.Where)
		}
	}
	if q.Where != nil {
		visit(*q.Where)
	}
	result := make([]string, 0, len(set))
	for s := range set {
		result = append(result, s)
	}
	slices.Sort(result)
	return result
}

// ValidateRequest must run before recursively collecting references or
// invoking shared planning. Bounds limit both the workload and expression tree.
func ValidateRequest(q Query, s Source, rc model.RequestContext) error {
	if q.APIVersion != model.APIVersion || q.Kind != Kind {
		return invalid("query", "unsupported analysis contract")
	}
	if !namePattern.MatchString(s.Name) || q.Source != s.Name || (s.Dataset == "") == (s.AnchorMetric == "") || s.AnchorMetric != "" && !namePattern.MatchString(s.AnchorMetric) || s.Dataset != "" && !namePattern.MatchString(s.Dataset) {
		return invalid("source", "an approved source is required")
	}
	if s.MaxRows < 1 || s.MaxRows > 10000 || s.MaxWindowDays < 1 || s.MaxWindowDays > 366 || s.Timeout <= 0 || s.Timeout > 15*time.Minute || len(s.Dimensions) == 0 || len(s.Roles) == 0 {
		return invalid("source", "source budgets, dimensions, and roles are required")
	}
	if rc.Tenant == "" || rc.Principal == "" || rc.RequestID == "" {
		return invalid("context", "trusted tenant, principal and request ID are required")
	}
	allowed := false
	for _, role := range rc.Roles {
		if slices.Contains(s.Roles, role) {
			allowed = true
		}
	}
	if !allowed {
		return &model.Problem{Code: "analysis_forbidden", Path: "source", Message: "principal has no analysis source role"}
	}
	if q.Limit < 1 || q.Limit > s.MaxRows {
		return invalid("limit", "limit exceeds the source budget")
	}
	if q.TimeRange == nil {
		return invalid("time_range", "an absolute half-open range is required")
	}
	a, ae := time.Parse(time.RFC3339Nano, q.TimeRange.Start)
	b, be := time.Parse(time.RFC3339Nano, q.TimeRange.End)
	loc, ze := time.LoadLocation(q.TimeRange.Timezone)
	if ae != nil || be != nil || ze != nil || q.TimeRange.Timezone == "" || !a.Before(b) || b.After(a.In(loc).AddDate(0, 0, s.MaxWindowDays)) {
		return invalid("time_range", "invalid range or source window budget exceeded")
	}
	if q.TimeGrouping != nil && !slices.Contains(q.GroupBy, q.TimeGrouping.Dimension) {
		return invalid("time_grouping", "time grouping dimension must be a first-stage group")
	}
	if q.TimeGrouping != nil && q.TimeGrouping.WeekStart == model.WeekStartSunday {
		return invalid("time_grouping", "initial analysis contract uses Monday weeks")
	}
	if len(q.Filters) > 32 || len(q.OrderBy) > 32 {
		return invalid("query", "too many filters or order fields")
	}
	for _, f := range q.Filters {
		if len(f.Values) > 100 {
			return invalid("filters", "too many filter values")
		}
	}
	count := 0
	var bounded func(Expression, int) error
	bounded = func(e Expression, depth int) error {
		count++
		if depth > 12 || count > 512 {
			return invalid("expression", "expression budget exceeded")
		}
		for _, a := range e.Args {
			if err := bounded(a, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	check := func(groups []string, ms []Measure, cs []Calculation) error {
		if len(groups) > 16 || len(ms) == 0 || len(ms) > 32 || len(cs) > 16 {
			return invalid("stage", "stage exceeds the column budget")
		}
		for _, m := range ms {
			if m.Where != nil {
				if err := bounded(*m.Where, 0); err != nil {
					return err
				}
			}
		}
		for _, c := range cs {
			if err := bounded(c.Expression, 0); err != nil {
				return err
			}
		}
		return nil
	}
	if err := check(q.GroupBy, q.Measures, q.Calculations); err != nil {
		return err
	}
	if q.Summarize != nil {
		if err := check(q.Summarize.GroupBy, q.Summarize.Measures, q.Summarize.Calculations); err != nil {
			return err
		}
	}
	for _, e := range []*Expression{q.Where, s.FixedFilter} {
		if e != nil {
			if err := bounded(*e, 0); err != nil {
				return err
			}
		}
	}
	for _, ref := range References(q) {
		if !slices.Contains(s.Dimensions, ref) {
			return invalid("dimensions", "reference is outside the approved source dimensions: "+ref)
		}
	}
	return nil
}

func Prepare(q Query, s Source, base model.PhysicalPlan, rc model.RequestContext) (Plan, error) {
	if s.Dataset != "" {
		return Plan{}, invalid("source", "direct sources require external catalog planning")
	}
	if err := ValidateRequest(q, s, rc); err != nil {
		return Plan{}, err
	}
	cols, err := validateBase(q, s, base)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{Source: s, Query: q, Base: base, Columns: cols}
	if err := validateParameterBudget(p); err != nil {
		return Plan{}, err
	}
	// Detach slices and pointers from caller-owned request and resolver objects.
	data, err := json.Marshal(p)
	if err != nil {
		return Plan{}, err
	}
	var detached Plan
	if err = json.Unmarshal(data, &detached); err != nil {
		return Plan{}, err
	}
	p = detached
	p.Fingerprint, err = canonical.Fingerprint(p)
	return p, err
}

func Verify(p Plan) error {
	expected := p.Fingerprint
	p.Fingerprint = ""
	actual, err := canonical.Fingerprint(p)
	if err != nil {
		return err
	}
	if expected == "" || expected != actual {
		return invalid("plan", "analysis fingerprint mismatch")
	}
	// Recheck structural budgets here, not authorization. This prevents a
	// corrupted internal plan from bypassing depth limits during compilation.
	if err := ValidateRequest(p.Query, p.Source, model.RequestContext{Tenant: "internal", Principal: "internal", RequestID: "internal", Roles: p.Source.Roles}); err != nil {
		return err
	}
	if p.Source.Dataset != "" && (p.Semantics == nil || p.Semantics.Fingerprint != p.Base.ManifestFingerprint || p.Semantics.PolicyFingerprint != p.Base.PolicyFingerprint || p.Semantics.Version == "") {
		return invalid("plan", "missing pinned external semantics")
	}
	if p.Source.Dataset == "" && (p.Semantics != nil || len(p.Relations) != 0) {
		return invalid("plan", "legacy plans cannot add external relationships")
	}
	cols, err := validateBase(p.Query, p.Source, p.Base, p.Relations)
	if err != nil {
		return err
	}
	if !slices.Equal(cols, p.Columns) {
		return invalid("plan.columns", "output schema mismatch")
	}
	if len(p.InternalOnly) > 64 {
		return invalid("plan.internal_only", "internal attribute budget exceeded")
	}
	for _, ref := range p.InternalOnly {
		if !slices.Contains(p.Source.Dimensions, ref) {
			return invalid("plan.internal_only", "unknown internal attribute")
		}
	}
	if err := validateParameterBudget(p); err != nil {
		return err
	}
	return validateOutputAccess(p.Query, p.InternalOnly)
}

func validateBase(q Query, s Source, base model.PhysicalPlan, bound ...[]BoundRelation) ([]Column, error) {
	if err := planner.VerifyPhysical(base); err != nil {
		return nil, err
	}
	if len(base.Joins) != 0 || s.Dataset == "" && len(base.Metrics) != 1 || s.Dataset != "" && (len(base.Metrics) != 0 || base.Root.Name != s.Dataset) {
		return nil, invalid("source", "source binding does not match its approved analysis contract")
	}
	if s.Dataset == "" {
		m := base.Metrics[0]
		if m.Name != s.AnchorMetric || m.Expression.Op != model.OpCount || m.Expression.Field == "" || len(m.Expression.Args) != 0 {
			return nil, invalid("source", "source anchor must be a primitive COUNT(field)")
		}
	}
	if q.TimeRange == nil || base.TimeRange == nil || *q.TimeRange != *base.TimeRange {
		return nil, invalid("time_range", "authorized range mismatch")
	}
	if (q.TimeGrouping == nil) != (base.TimeGrouping == nil) || (q.TimeGrouping != nil && *q.TimeGrouping != *base.TimeGrouping) {
		return nil, invalid("time_grouping", "authorized grouping mismatch")
	}
	f1, _ := canonical.Fingerprint(q.Filters)
	f2, _ := canonical.Fingerprint(base.Filters)
	if f1 != f2 {
		return nil, invalid("filters", "authorized filters mismatch")
	}
	types := map[string]model.DataType{}
	resources := map[string]model.ResourceRef{base.Root.Entity: base.Root.Resource}
	if len(bound) > 0 {
		if len(bound[0]) > 8 {
			return nil, invalid("relations", "join budget exceeded")
		}
		for _, r := range bound[0] {
			if _, ok := resources[r.From]; !ok {
				return nil, invalid("relations", "unavailable parent")
			}
			if _, ok := resources[r.To]; ok {
				return nil, invalid("relations", "repeated target")
			}
			if len(r.Keys) == 0 || len(r.Keys) > 8 || r.Missing != "preserve_null" && r.Missing != "error" {
				return nil, invalid("relations", "invalid relation")
			}
			for _, k := range r.Keys {
				if !physicalIdentifier.MatchString(k.From) || !physicalIdentifier.MatchString(k.To) {
					return nil, invalid("relations", "invalid bound key")
				}
			}
			if snap := r.Snapshot; snap != nil {
				if !physicalIdentifier.MatchString(snap.From) || !physicalIdentifier.MatchString(snap.To) || !slices.Contains([]string{"same_day", "week_start", "week_end"}, snap.Anchor) {
					return nil, invalid("relations", "invalid bound snapshot")
				}
			}
			resources[r.To] = r.Resource
		}
	}
	for _, d := range base.Dimensions {
		if resource, ok := resources[d.Entity]; !ok || d.Resource != resource || !physicalIdentifier.MatchString(d.Column) {
			return nil, invalid("dimensions", "dimensions must belong to the approved dataset")
		}
		types[d.Name] = d.DataType
	}
	for _, e := range []*Expression{q.Where, s.FixedFilter} {
		if e != nil {
			typ, err := expressionType(*e, types, nil, false)
			if err != nil {
				return nil, err
			}
			if typ != model.DataTypeBoolean {
				return nil, invalid("where", "row predicate must be boolean")
			}
		}
	}
	for _, ref := range References(q) {
		if !slices.Contains(s.Dimensions, ref) || types[ref] == "" {
			return nil, invalid("dimensions", "missing approved dimension: "+ref)
		}
	}
	d, ok := types[q.TimeRange.Dimension]
	if !ok || d != model.DataTypeDate {
		return nil, invalid("time_range", "initial analysis requires a calendar DATE dimension")
	}
	for _, field := range base.MetricFields {
		if field.Entity != base.Root.Entity || field.Resource != base.Root.Resource {
			return nil, invalid("source", "anchor fields must belong to the approved dataset")
		}
	}
	cols, err := stage(q.GroupBy, q.Measures, q.Calculations, types, q.Summarize == nil)
	if err != nil {
		return nil, err
	}
	if q.Summarize != nil {
		types = map[string]model.DataType{}
		for _, c := range cols {
			types[c.Name] = c.Type
		}
		cols, err = stage(q.Summarize.GroupBy, q.Summarize.Measures, q.Summarize.Calculations, types, true)
		if err != nil {
			return nil, err
		}
	}
	for _, order := range q.OrderBy {
		if order.Direction != model.SortAscending && order.Direction != model.SortDescending {
			return nil, invalid("order_by", "invalid order direction")
		}
		found := false
		for _, c := range cols {
			if c.Name == order.Field {
				found = true
			}
		}
		if !found {
			return nil, invalid("order_by", "order field is not a final output")
		}
	}
	return cols, nil
}

func numeric(t model.DataType) bool { return t == model.DataTypeInteger || t == model.DataTypeDecimal }

func stage(groups []string, ms []Measure, cs []Calculation, in map[string]model.DataType, final bool) ([]Column, error) {
	out := map[string]model.DataType{}
	cols := []Column{}
	add := func(n string, t model.DataType) error {
		if !namePattern.MatchString(n) || out[n] != "" {
			return invalid("stage", "invalid or duplicate output: "+n)
		}
		out[n] = t
		cols = append(cols, Column{n, t})
		return nil
	}
	for _, g := range groups {
		if in[g] == "" {
			return nil, invalid("group_by", "unknown group: "+g)
		}
		if err := add(g, in[g]); err != nil {
			return nil, err
		}
	}
	for _, m := range ms {
		t := in[m.Field]
		if m.Field == "" && m.Op != "count" || m.Field != "" && t == "" {
			return nil, invalid("measure.field", "unknown aggregate field")
		}
		if m.Where != nil {
			wt, err := expressionType(*m.Where, in, nil, false)
			if err != nil {
				return nil, err
			}
			if wt != model.DataTypeBoolean {
				return nil, invalid("measure.where", "condition must be boolean")
			}
		}
		switch m.Op {
		case "count", "count_distinct":
			t = model.DataTypeInteger
		case "sum", "avg":
			if !numeric(t) {
				return nil, invalid("measure", "sum/avg require numeric inputs")
			}
			if m.Op == "avg" {
				t = model.DataTypeDecimal
			}
		case "min", "max":
			if !numeric(t) && t != model.DataTypeDate {
				return nil, invalid("measure", "min/max require numeric or date input")
			}
		default:
			return nil, invalid("measure.op", "unsupported aggregate")
		}
		if err := add(m.Name, t); err != nil {
			return nil, err
		}
	}
	windowOutputs := map[string]bool{}
	for _, c := range cs {
		// A window expression cannot feed another calculation: SQL window
		// nesting/reaggregation would otherwise change its denominator.
		var usesWindow func(Expression) bool
		usesWindow = func(e Expression) bool {
			if windowOutputs[e.Ref] {
				return true
			}
			for _, a := range e.Args {
				if usesWindow(a) {
					return true
				}
			}
			return false
		}
		if usesWindow(c.Expression) {
			return nil, invalid("calculation", "window outputs cannot feed later calculations")
		}
		t, err := expressionType(c.Expression, out, groups, final)
		if err != nil {
			return nil, err
		}
		if err = add(c.Name, t); err != nil {
			return nil, err
		}
		windowOutputs[c.Name] = containsShare(c.Expression)
	}
	return cols, nil
}

func containsShare(e Expression) bool {
	if e.Op == "share" {
		return true
	}
	for _, a := range e.Args {
		if containsShare(a) {
			return true
		}
	}
	return false
}

func expressionType(e Expression, refs map[string]model.DataType, groups []string, final bool) (model.DataType, error) {
	bad := func() (model.DataType, error) {
		return "", invalid("expression", "invalid expression shape or types: "+e.Op)
	}
	if e.Op != "ref" && e.Ref != "" || e.Op != "literal" && (e.Type != "" || e.Value != "") || e.Op != "share" && len(e.PartitionBy) > 0 {
		return bad()
	}
	if e.Op == "ref" {
		if len(e.Args) != 0 || refs[e.Ref] == "" {
			return bad()
		}
		return refs[e.Ref], nil
	}
	if e.Op == "literal" {
		if len(e.Args) != 0 {
			return bad()
		}
		switch e.Type {
		case model.DataTypeInteger:
			if _, err := strconv.ParseInt(e.Value, 10, 64); err != nil {
				return bad()
			}
		case model.DataTypeDecimal:
			if !decimalPattern.MatchString(e.Value) {
				return bad()
			}
		case model.DataTypeString:
			if len(e.Value) > 1024 {
				return bad()
			}
		case model.DataTypeBoolean:
			if e.Value != "true" && e.Value != "false" {
				return bad()
			}
		case model.DataTypeDate:
			if _, err := time.Parse(time.DateOnly, e.Value); err != nil {
				return bad()
			}
		case model.DataTypeTimestamp:
			if _, err := time.Parse(time.RFC3339Nano, e.Value); err != nil {
				return bad()
			}
		default:
			return bad()
		}
		return e.Type, nil
	}
	args := make([]model.DataType, len(e.Args))
	for i, a := range e.Args {
		if e.Op == "share" && containsShare(a) {
			return bad()
		}
		t, err := expressionType(a, refs, groups, final)
		if err != nil {
			return "", err
		}
		args[i] = t
	}
	compatible := func(a, b model.DataType) bool { return a == b || numeric(a) && numeric(b) }
	switch e.Op {
	case "add", "subtract", "multiply", "divide":
		if len(args) != 2 || !numeric(args[0]) || !numeric(args[1]) {
			return bad()
		}
		if e.Op == "divide" || args[0] == model.DataTypeDecimal || args[1] == model.DataTypeDecimal {
			return model.DataTypeDecimal, nil
		}
		return model.DataTypeInteger, nil
	case "eq", "neq", "lt", "lte", "gt", "gte":
		if len(args) != 2 || !compatible(args[0], args[1]) {
			return bad()
		}
		return model.DataTypeBoolean, nil
	case "and", "or":
		if len(args) < 2 {
			return bad()
		}
		for _, t := range args {
			if t != model.DataTypeBoolean {
				return bad()
			}
		}
		return model.DataTypeBoolean, nil
	case "not":
		if len(args) != 1 || args[0] != model.DataTypeBoolean {
			return bad()
		}
		return model.DataTypeBoolean, nil
	case "is_null", "is_not_null":
		if len(args) != 1 {
			return bad()
		}
		return model.DataTypeBoolean, nil
	case "weekday":
		if len(args) != 1 || args[0] != model.DataTypeDate {
			return bad()
		}
		return model.DataTypeInteger, nil
	case "case":
		if len(args) < 3 || len(args)%2 != 1 {
			return bad()
		}
		t := args[len(args)-1]
		for i := 0; i < len(args)-1; i += 2 {
			if args[i] != model.DataTypeBoolean || !compatible(t, args[i+1]) {
				return bad()
			}
			if args[i+1] == model.DataTypeDecimal {
				t = model.DataTypeDecimal
			}
		}
		return t, nil
	case "share":
		if !final || len(args) != 1 || !numeric(args[0]) {
			return bad()
		}
		seen := map[string]bool{}
		for _, g := range e.PartitionBy {
			if !slices.Contains(groups, g) || seen[g] {
				return bad()
			}
			seen[g] = true
		}
		return model.DataTypeDecimal, nil
	}
	return bad()
}

func (p Plan) String() string { return fmt.Sprintf("AnalysisPlan(%s)", p.Fingerprint) }
