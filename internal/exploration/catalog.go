package exploration

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/canonical"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/policy"
)

// Catalog is trusted external semantic configuration. It contains no metric
// registry: temporary analysis starts from an explicitly approved row source.
// Transports must never accept this object from an analysis request.
type Catalog struct {
	Engine        string             `json:"engine,omitempty" yaml:"engine,omitempty"`
	APIVersion    string             `json:"api_version" yaml:"api_version"`
	Kind          string             `json:"kind" yaml:"kind"`
	Metadata      model.Metadata     `json:"metadata" yaml:"metadata"`
	Namespace     string             `json:"namespace" yaml:"namespace"`
	Tenant        string             `json:"tenant" yaml:"tenant"`
	Datasets      []Dataset          `json:"datasets" yaml:"datasets"`
	Attributes    []Attribute        `json:"attributes" yaml:"attributes"`
	Relationships []Relation         `json:"relationships,omitempty" yaml:"relationships,omitempty"`
	Sources       []Source           `json:"sources" yaml:"sources"`
	Policy        model.PolicySource `json:"policy" yaml:"policy"`
}

type Dataset struct {
	Name        string            `json:"name" yaml:"name"`
	Description string            `json:"description,omitempty" yaml:"description,omitempty"`
	Resource    model.ResourceRef `json:"resource" yaml:"resource"`
	Fields      []BoundField      `json:"fields" yaml:"fields"`
}

type BoundField struct {
	Name             string         `json:"name" yaml:"name"`
	Column           string         `json:"column" yaml:"column"`
	Type             model.DataType `json:"type" yaml:"type"`
	CalendarTimezone string         `json:"calendar_timezone,omitempty" yaml:"calendar_timezone,omitempty"`
}

type Attribute struct {
	Name         string `json:"name" yaml:"name"`
	Description  string `json:"description,omitempty" yaml:"description,omitempty"`
	Dataset      string `json:"dataset" yaml:"dataset"`
	Field        string `json:"field" yaml:"field"`
	InternalOnly bool   `json:"internal_only,omitempty" yaml:"internal_only,omitempty"`
}

type Relation struct {
	Name     string        `json:"name" yaml:"name"`
	From     string        `json:"from" yaml:"from"`
	To       string        `json:"to" yaml:"to"`
	Keys     []JoinKey     `json:"keys" yaml:"keys"`
	Snapshot *SnapshotRule `json:"snapshot,omitempty" yaml:"snapshot,omitempty"`
	Missing  string        `json:"missing" yaml:"missing"` // preserve_null or error
}

type JoinKey struct {
	From string `json:"from" yaml:"from"`
	To   string `json:"to" yaml:"to"`
}

// SnapshotRule is an exact calendar-date match. It is not "latest available".
type SnapshotRule struct {
	From   string `json:"from" yaml:"from"`
	To     string `json:"to" yaml:"to"`
	Anchor string `json:"anchor" yaml:"anchor"` // same_day, week_start, week_end
}

type BoundRelation struct {
	Name     string            `json:"name" yaml:"name"`
	From     string            `json:"from" yaml:"from"`
	To       string            `json:"to" yaml:"to"`
	Resource model.ResourceRef `json:"resource" yaml:"resource"`
	Keys     []JoinKey         `json:"keys" yaml:"keys"`                             // physical columns, produced by trusted planning
	Snapshot *SnapshotRule     `json:"snapshot,omitempty" yaml:"snapshot,omitempty"` // physical columns
	Missing  string            `json:"missing" yaml:"missing"`
}

type SemanticVersion struct {
	Name              string `json:"name" yaml:"name"`
	Version           string `json:"version" yaml:"version"`
	Fingerprint       string `json:"fingerprint" yaml:"fingerprint"`
	PolicyFingerprint string `json:"policy_fingerprint" yaml:"policy_fingerprint"`
}

var physicalIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)

func validType(t model.DataType) bool {
	return slices.Contains([]model.DataType{model.DataTypeString, model.DataTypeInteger, model.DataTypeDecimal, model.DataTypeBoolean, model.DataTypeDate, model.DataTypeTimestamp}, t)
}

// ValidateCatalog rejects ambiguous routes and unbound fields at startup.
func (c Catalog) EngineName() string {
	if c.Engine == "" {
		return "databricks_sql"
	}
	return c.Engine
}

func ValidateCatalog(c Catalog) error {
	if c.EngineName() != "databricks_sql" && c.EngineName() != "trino" && c.EngineName() != "presto" {
		return invalid("catalog.engine", "unsupported analysis engine")
	}
	if c.APIVersion != model.APIVersion || c.Kind != "AnalysisCatalog" || !namePattern.MatchString(c.Metadata.Name) || c.Metadata.Version == "" || c.Namespace == "" || c.Tenant == "" || len(c.Datasets) == 0 || len(c.Sources) == 0 || len(c.Datasets) > 64 || len(c.Attributes) > 256 || len(c.Relationships) > 32 {
		return invalid("catalog", "invalid external analysis catalog")
	}
	datasets := map[string]Dataset{}
	fields := map[string]map[string]BoundField{}
	for _, d := range c.Datasets {
		if !namePattern.MatchString(d.Name) || datasets[d.Name].Name != "" || len(d.Fields) == 0 || len(d.Fields) > 128 || d.Resource.Kind != model.ResourceTable || d.Resource.Table == "" || d.Resource.URI != "" {
			return invalid("catalog.datasets", "invalid or duplicate dataset")
		}
		for _, v := range []string{d.Resource.Catalog, d.Resource.Schema, d.Resource.Table} {
			if v != "" && !physicalIdentifier.MatchString(v) {
				return invalid("catalog.datasets.resource", "invalid physical identifier")
			}
		}
		fields[d.Name] = map[string]BoundField{}
		physicalColumns := map[string]bool{}
		for _, f := range d.Fields {
			if !namePattern.MatchString(f.Name) || fields[d.Name][f.Name].Name != "" || !physicalIdentifier.MatchString(f.Column) || strings.EqualFold(f.Column, "_analysis_matches") || physicalColumns[strings.ToLower(f.Column)] || !validType(f.Type) {
				return invalid("catalog.datasets.fields", "invalid or duplicate field")
			}
			if f.Type == model.DataTypeDate {
				if _, err := time.LoadLocation(f.CalendarTimezone); err != nil || f.CalendarTimezone == "" {
					return invalid("catalog.datasets.fields", "DATE fields require a business calendar timezone")
				}
			} else if f.CalendarTimezone != "" {
				return invalid("catalog.datasets.fields", "calendar timezone applies to DATE fields only")
			}
			fields[d.Name][f.Name] = f
			physicalColumns[strings.ToLower(f.Column)] = true
		}
		datasets[d.Name] = d
	}
	attributes := map[string]Attribute{}
	classifications := map[string]bool{}
	for _, a := range c.Attributes {
		if !namePattern.MatchString(a.Name) || attributes[a.Name].Name != "" || fields[a.Dataset][a.Field].Name == "" {
			return invalid("catalog.attributes", "invalid or duplicate attribute")
		}
		r := datasets[a.Dataset].Resource
		key := strings.ToLower(strings.Join([]string{r.Catalog, r.Schema, r.Table, fields[a.Dataset][a.Field].Column}, "."))
		if restricted, exists := classifications[key]; exists && restricted != a.InternalOnly {
			return invalid("catalog.attributes", "aliases of a physical column must share internal_only classification")
		}
		classifications[key] = a.InternalOnly
		attributes[a.Name] = a
	}
	to := map[string]bool{}
	names := map[string]bool{}
	for _, r := range c.Relationships {
		if !namePattern.MatchString(r.Name) || names[r.Name] || r.From == r.To || datasets[r.From].Name == "" || datasets[r.To].Name == "" || to[r.To] || len(r.Keys) == 0 || len(r.Keys) > 8 || r.Missing != "preserve_null" && r.Missing != "error" {
			return invalid("catalog.relationships", "relationships must form unambiguous directed paths with explicit missing handling")
		}
		names[r.Name] = true
		to[r.To] = true
		seenFrom := map[string]bool{}
		seenTo := map[string]bool{}
		for _, k := range r.Keys {
			f, t := fields[r.From][k.From], fields[r.To][k.To]
			if f.Name == "" || t.Name == "" || f.Type != t.Type || seenFrom[k.From] || seenTo[k.To] {
				return invalid("catalog.relationships.keys", "keys must be unique registered fields of the same type")
			}
			if f.Type == model.DataTypeDate && f.CalendarTimezone != t.CalendarTimezone {
				return invalid("catalog.relationships.keys", "calendar key timezones disagree")
			}
			seenFrom[k.From] = true
			seenTo[k.To] = true
		}
		if s := r.Snapshot; s != nil {
			f, t := fields[r.From][s.From], fields[r.To][s.To]
			if f.Type != model.DataTypeDate || t.Type != model.DataTypeDate || f.CalendarTimezone != t.CalendarTimezone || !slices.Contains([]string{"same_day", "week_start", "week_end"}, s.Anchor) || seenFrom[s.From] || seenTo[s.To] {
				return invalid("catalog.relationships.snapshot", "invalid exact-date snapshot rule")
			}
		}
	}
	// A single incoming edge avoids choosing a join path by naming heuristics.
	for dataset := range datasets {
		visited := map[string]bool{}
		current := dataset
		for {
			if visited[current] {
				return invalid("catalog.relationships", "relationship cycle")
			}
			visited[current] = true
			found := false
			for _, r := range c.Relationships {
				if r.To == current {
					current = r.From
					found = true
					break
				}
			}
			if !found {
				break
			}
		}
	}
	sources := map[string]bool{}
	for _, s := range c.Sources {
		if !namePattern.MatchString(s.Name) || sources[s.Name] || datasets[s.Dataset].Name == "" || s.AnchorMetric != "" || len(s.Dimensions) == 0 || len(s.Roles) == 0 || s.MaxRows < 1 || s.MaxRows > 10000 || s.MaxWindowDays < 1 || s.MaxWindowDays > 366 || s.Timeout <= 0 || s.Timeout > 15*time.Minute {
			return invalid("catalog.sources", "direct sources require a registered dataset, not an anchor metric")
		}
		sources[s.Name] = true
		seen := map[string]bool{}
		hasCalendar := false
		for _, n := range s.Dimensions {
			if attributes[n].Name == "" || seen[n] {
				return invalid("catalog.sources.dimensions", "unregistered source attribute")
			}
			seen[n] = true
			a := attributes[n]
			if a.Dataset == s.Dataset && fields[a.Dataset][a.Field].Type == model.DataTypeDate {
				hasCalendar = true
			}
			target := attributes[n].Dataset
			for steps := 0; target != s.Dataset; steps++ {
				if steps >= 8 {
					return invalid("catalog.sources.dimensions", "attribute exceeds approved join depth")
				}
				found := false
				for _, rel := range c.Relationships {
					if rel.To == target {
						if rel.Snapshot != nil && rel.From != s.Dataset {
							return invalid("catalog.relationships.snapshot", "snapshots must originate at the source business date")
						}
						target = rel.From
						found = true
						break
					}
				}
				if !found {
					return invalid("catalog.sources.dimensions", "attribute dataset is unreachable from source")
				}
			}
		}
		if !hasCalendar {
			return invalid("catalog.sources.dimensions", "source requires an approved root DATE attribute for bounded time ranges")
		}
		for _, role := range s.Roles {
			if role == "" || len(role) > 128 {
				return invalid("catalog.sources.roles", "source roles must be bounded nonempty strings")
			}
		}
		if s.FixedFilter != nil {
			if err := boundedExpression(*s.FixedFilter); err != nil {
				return err
			}
			types := map[string]model.DataType{}
			for _, n := range s.Dimensions {
				a := attributes[n]
				types[n] = fields[a.Dataset][a.Field].Type
			}
			typ, err := expressionType(*s.FixedFilter, types, nil, false)
			if err != nil || typ != model.DataTypeBoolean {
				return invalid("catalog.sources.fixed_filter", "fixed filter must be boolean over approved attributes")
			}
		}
	}
	if c.Policy.APIVersion != model.APIVersion || c.Policy.Kind != model.KindPolicySource || c.Policy.Tenant != c.Tenant || c.Policy.Metadata.Name == "" || c.Policy.Metadata.Version == "" || len(c.Policy.Rules) == 0 {
		return invalid("catalog.policy", "trusted attribute policy is required")
	}
	policyNames := map[string]bool{}
	for _, r := range c.Policy.Rules {
		if policyNames[r.Name] || r.Name == "" || r.Effect != model.EffectAllow && r.Effect != model.EffectDeny {
			return invalid("catalog.policy", "invalid policy rule")
		}
		policyNames[r.Name] = true
		for _, d := range r.Dimensions {
			if d != "*" && attributes[d].Name == "" {
				return invalid("catalog.policy", "policy references unknown attribute")
			}
		}
	}
	return nil
}

func boundedExpression(e Expression) error {
	nodes := 0
	var check func(Expression, int) error
	check = func(e Expression, depth int) error {
		nodes++
		if depth > 12 || nodes > 512 {
			return invalid("expression", "expression budget exceeded")
		}
		for _, a := range e.Args {
			if err := check(a, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return check(e, 0)
}

func expressionReferences(e *Expression) []string {
	if e == nil {
		return nil
	}
	names := map[string]bool{}
	var visit func(Expression)
	visit = func(e Expression) {
		if e.Op == "ref" {
			names[e.Ref] = true
		}
		for _, a := range e.Args {
			visit(a)
		}
	}
	visit(*e)
	out := []string{}
	for n := range names {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// PlanCatalog authorizes source membership plus every requested attribute,
// binds the reviewed external catalog, and pins its version. No metric lookup.
func PlanCatalog(c Catalog, q Query, rc model.RequestContext) (Plan, error) {
	if err := ValidateCatalog(c); err != nil {
		return Plan{}, err
	}
	if rc.Tenant != c.Tenant {
		return Plan{}, invalid("catalog", "tenant mismatch")
	}
	var source Source
	for _, s := range c.Sources {
		if s.Name == q.Source {
			source = s
			break
		}
	}
	if err := ValidateRequest(q, source, rc); err != nil {
		return Plan{}, err
	}
	fingerprint, err := canonical.Fingerprint(c)
	if err != nil {
		return Plan{}, err
	}
	policyFingerprint, err := canonical.Fingerprint(c.Policy)
	if err != nil {
		return Plan{}, err
	}
	bundle := model.PolicyBundle{APIVersion: model.APIVersion, Kind: model.KindPolicyBundle, Metadata: c.Policy.Metadata, ManifestFingerprint: fingerprint, Tenant: c.Tenant, Rules: c.Policy.Rules}
	if _, err = policy.Authorize(rc, bundle, fingerprint, model.SemanticQuery{GroupBy: References(q)}); err != nil {
		return Plan{}, err
	}
	datasets := map[string]Dataset{}
	fields := map[string]map[string]BoundField{}
	attrs := map[string]Attribute{}
	for _, d := range c.Datasets {
		datasets[d.Name] = d
		fields[d.Name] = map[string]BoundField{}
		for _, f := range d.Fields {
			fields[d.Name][f.Name] = f
		}
	}
	for _, a := range c.Attributes {
		attrs[a.Name] = a
	}
	needed := References(q)
	for _, ref := range expressionReferences(source.FixedFilter) {
		if !slices.Contains(needed, ref) {
			needed = append(needed, ref)
		}
	}
	slices.Sort(needed)
	if len(needed) > 64 {
		return Plan{}, invalid("attributes", "raw attribute budget exceeded")
	}
	joined := map[string]bool{source.Dataset: true}
	bound := []BoundRelation{}
	var addPath func(string) error
	addPath = func(target string) error {
		if joined[target] {
			return nil
		}
		var rel Relation
		for _, r := range c.Relationships {
			if r.To == target {
				rel = r
				break
			}
		}
		if rel.Name == "" {
			return invalid("catalog.relationships", "attribute dataset is unreachable from approved source")
		}
		if err := addPath(rel.From); err != nil {
			return err
		}
		if len(bound) >= 8 {
			return invalid("catalog.relationships", "join budget exceeded")
		}
		br := BoundRelation{Name: rel.Name, From: rel.From, To: rel.To, Resource: datasets[rel.To].Resource, Missing: rel.Missing}
		for _, k := range rel.Keys {
			br.Keys = append(br.Keys, JoinKey{From: fields[rel.From][k.From].Column, To: fields[rel.To][k.To].Column})
		}
		if snap := rel.Snapshot; snap != nil {
			if rel.From != source.Dataset || snap.From != attrs[q.TimeRange.Dimension].Field {
				return invalid("relationships.snapshot", "snapshot must be anchored to the source business date")
			}
			br.Snapshot = &SnapshotRule{From: fields[rel.From][snap.From].Column, To: fields[rel.To][snap.To].Column, Anchor: snap.Anchor}
		}
		bound = append(bound, br)
		joined[target] = true
		return nil
	}
	physicalDims := []model.PhysicalDimension{}
	types := map[string]model.DataType{}
	for _, ref := range needed {
		a, ok := attrs[ref]
		if !ok || !slices.Contains(source.Dimensions, ref) {
			return Plan{}, invalid("attributes", "reference is not approved for source: "+ref)
		}
		if err := addPath(a.Dataset); err != nil {
			return Plan{}, err
		}
		f := fields[a.Dataset][a.Field]
		kind := model.DimensionCategorical
		if f.Type == model.DataTypeDate {
			kind = model.DimensionTime
		}
		physicalDims = append(physicalDims, model.PhysicalDimension{Name: ref, Entity: a.Dataset, Resource: datasets[a.Dataset].Resource, Column: f.Column, Type: kind, DataType: f.Type, CalendarTimezone: f.CalendarTimezone})
		types[ref] = f.Type
	}
	timeAttr := attrs[q.TimeRange.Dimension]
	calendar := fields[timeAttr.Dataset][timeAttr.Field]
	if timeAttr.Dataset != source.Dataset || calendar.Type != model.DataTypeDate || calendar.CalendarTimezone != q.TimeRange.Timezone {
		return Plan{}, invalid("time_range", "range must use the root DATE field's business timezone")
	}
	loc, _ := time.LoadLocation(calendar.CalendarTimezone)
	start, _ := time.Parse(time.RFC3339Nano, q.TimeRange.Start)
	end, _ := time.Parse(time.RFC3339Nano, q.TimeRange.End)
	for _, t := range []time.Time{start.In(loc), end.In(loc)} {
		h, m, s := t.Clock()
		if h != 0 || m != 0 || s != 0 || t.Nanosecond() != 0 {
			return Plan{}, invalid("time_range", "DATE range boundaries must be local midnight")
		}
	}
	if tg := q.TimeGrouping; tg != nil {
		if tg.Dimension != q.TimeRange.Dimension || tg.Timezone != q.TimeRange.Timezone || !slices.Contains([]model.TimeGranularity{model.GrainDay, model.GrainWeek, model.GrainMonth}, tg.Granularity) || tg.Granularity == model.GrainWeek && tg.WeekStart != model.WeekStartMonday || tg.Granularity != model.GrainWeek && tg.WeekStart != "" {
			return Plan{}, invalid("time_grouping", "invalid calendar grouping")
		}
	}
	for _, e := range []*Expression{q.Where, source.FixedFilter} {
		if e != nil {
			typ, err := expressionType(*e, types, nil, false)
			if err != nil {
				return Plan{}, err
			}
			if typ != model.DataTypeBoolean {
				return Plan{}, invalid("where", "row filter must be boolean")
			}
		}
	}
	for _, f := range q.Filters {
		if f.Operator != model.FilterEqual && f.Operator != model.FilterIn || len(f.Values) == 0 || f.Operator == model.FilterEqual && len(f.Values) != 1 {
			return Plan{}, invalid("filters", "invalid equality filter")
		}
		for _, v := range f.Values {
			if !validFilterValue(v, types[f.Dimension]) {
				return Plan{}, invalid("filters", "filter value does not match its registered type")
			}
		}
	}
	tr := *q.TimeRange
	tr.Start = start.UTC().Format(time.RFC3339Nano)
	tr.End = end.UTC().Format(time.RFC3339Nano)
	q.TimeRange = &tr
	root := datasets[source.Dataset]
	base := model.PhysicalPlan{APIVersion: model.APIVersion, Kind: model.KindPhysicalPlan, Engine: c.EngineName(), ManifestFingerprint: fingerprint, PolicyFingerprint: policyFingerprint, BindingFingerprint: fingerprint, Root: model.PhysicalDataset{Name: root.Name, Entity: root.Name, Resource: root.Resource}, Dimensions: physicalDims, TimeRange: q.TimeRange, TimeGrouping: q.TimeGrouping, Filters: q.Filters, Limit: q.Limit}
	base.LogicalFingerprint, err = canonical.Fingerprint(q)
	if err != nil {
		return Plan{}, err
	}
	base.Fingerprint, err = physicalFingerprint(base)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{Source: source, Query: q, Base: base, Semantics: &SemanticVersion{Name: c.Metadata.Name, Version: c.Metadata.Version, Fingerprint: fingerprint, PolicyFingerprint: policyFingerprint}, Relations: bound}
	cols, err := validateBase(q, source, base, bound)
	if err != nil {
		return Plan{}, err
	}
	p.Columns = cols
	for _, ref := range needed {
		if attrs[ref].InternalOnly {
			p.InternalOnly = append(p.InternalOnly, ref)
		}
	}
	if err := validateOutputAccess(q, p.InternalOnly); err != nil {
		return Plan{}, err
	}
	return detachPlan(p)
}

func validFilterValue(v string, t model.DataType) bool {
	if t == model.DataTypeDate {
		_, err := time.Parse(time.DateOnly, v)
		return err == nil
	}
	if t == model.DataTypeTimestamp {
		_, err := time.Parse(time.RFC3339Nano, v)
		return err == nil
	}
	_, err := expressionType(Expression{Op: "literal", Type: t, Value: v}, nil, nil, false)
	return err == nil
}

func physicalFingerprint(p model.PhysicalPlan) (string, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	var values map[string]any
	if err = json.Unmarshal(data, &values); err != nil {
		return "", err
	}
	delete(values, "fingerprint")
	return canonical.Fingerprint(values)
}

func detachPlan(p Plan) (Plan, error) {
	if err := validateParameterBudget(p); err != nil {
		return Plan{}, err
	}
	data, err := json.Marshal(p)
	if err != nil {
		return Plan{}, err
	}
	var copy Plan
	if err = json.Unmarshal(data, &copy); err != nil {
		return Plan{}, err
	}
	copy.Fingerprint = ""
	copy.Fingerprint, err = canonical.Fingerprint(copy)
	return copy, err
}

func SourceIn(c Catalog, name string) (Source, bool) {
	for _, s := range c.Sources {
		if s.Name == name {
			return s, true
		}
	}
	return Source{}, false
}

func (c Catalog) String() string {
	return fmt.Sprintf("AnalysisCatalog(%s@%s)", c.Metadata.Name, c.Metadata.Version)
}
