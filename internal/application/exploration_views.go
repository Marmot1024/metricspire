package application

import (
	"context"
	"slices"
	"time"

	"github.com/marmot1024/metricspire/internal/canonical"
	"github.com/marmot1024/metricspire/internal/exploration"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/policy"
)

// Public views deliberately exclude physical bindings, policies and internal
// releases. They are shared by HTTP and MCP, including completed job results.
type AnalysisAttributeView struct {
	Name             string                 `json:"name"`
	Description      string                 `json:"description,omitempty"`
	Type             model.DataType         `json:"type"`
	CalendarTimezone string                 `json:"calendar_timezone,omitempty"`
	TimeRangeAllowed bool                   `json:"time_range_allowed,omitempty"`
	InternalOnly     bool                   `json:"internal_only,omitempty"`
	Relations        []AnalysisRelationView `json:"relations,omitempty"`
}

type AnalysisRelationView struct {
	Name           string `json:"name"`
	SnapshotAnchor string `json:"snapshot_anchor,omitempty"`
	Missing        string `json:"missing"`
	Duplicate      string `json:"duplicate"`
}

type AnalysisSourceView struct {
	Namespace     string                      `json:"namespace"`
	Source        string                      `json:"source"`
	Description   string                      `json:"description,omitempty"`
	Semantics     exploration.SemanticVersion `json:"semantics"`
	Attributes    []AnalysisAttributeView     `json:"attributes"`
	MaxWindowDays int                         `json:"max_window_days"`
	MaxRows       int                         `json:"max_rows"`
	Timeout       string                      `json:"timeout"`
}

type AnalysisPlanView struct {
	Fingerprint string                       `json:"fingerprint"`
	Semantics   *exploration.SemanticVersion `json:"semantics,omitempty"`
	Source      string                       `json:"source"`
	Query       exploration.Query            `json:"query"`
	Columns     []exploration.Column         `json:"columns"`
	Relations   []AnalysisRelationView       `json:"relations,omitempty"`
	Timeout     string                       `json:"timeout"`
}

type AnalysisJobView struct {
	ID          string             `json:"id"`
	Status      model.JobStatus    `json:"status"`
	Fingerprint string             `json:"fingerprint"`
	SubmittedAt time.Time          `json:"submitted_at"`
	FinishedAt  *time.Time         `json:"finished_at,omitempty"`
	ErrorCode   string             `json:"error_code,omitempty"`
	Plan        *AnalysisPlanView  `json:"plan,omitempty"`
	Result      *model.TypedResult `json:"result,omitempty"`
}

func analysisRelationView(r exploration.BoundRelation) AnalysisRelationView {
	v := AnalysisRelationView{Name: r.Name, Missing: r.Missing, Duplicate: "error"}
	if r.Snapshot != nil {
		v.SnapshotAnchor = r.Snapshot.Anchor
	}
	return v
}

func PublicAnalysisPlan(p exploration.Plan) AnalysisPlanView {
	v := AnalysisPlanView{Fingerprint: p.Fingerprint, Semantics: p.Semantics, Source: p.Source.Name, Query: p.Query, Columns: p.Columns, Timeout: p.Source.Timeout.String()}
	for _, r := range p.Relations {
		v.Relations = append(v.Relations, analysisRelationView(r))
	}
	return cloneResolverValue(v)
}

func PublicAnalysisJob(s AnalysisJobSnapshot) AnalysisJobView {
	v := AnalysisJobView{ID: s.ID, Status: s.Status, Fingerprint: s.Fingerprint, SubmittedAt: s.SubmittedAt, FinishedAt: s.FinishedAt, ErrorCode: s.ErrorCode}
	if s.Output != nil {
		p := PublicAnalysisPlan(s.Output.Plan)
		v.Plan = &p
		v.Result = s.Output.Execution.Result
	}
	return cloneResolverValue(v)
}

// Discover exposes only sources granted by role and attributes granted by
// the same policy evaluator used by planning. No physical table inventory.
func (r *ConfiguredAnalysisCatalogs) Discover(ctx context.Context, namespace string, rc model.RequestContext) ([]AnalysisSourceView, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := []AnalysisSourceView{}
	for _, c := range r.catalogs {
		if c.Namespace != namespace || c.Tenant != rc.Tenant {
			continue
		}
		fp, _ := canonical.Fingerprint(c)
		pfp, _ := canonical.Fingerprint(c.Policy)
		bundle := model.PolicyBundle{Tenant: c.Tenant, ManifestFingerprint: fp, Rules: c.Policy.Rules}
		fields := map[string]exploration.BoundField{}
		for _, d := range c.Datasets {
			for _, f := range d.Fields {
				fields[d.Name+"."+f.Name] = f
			}
		}
		for _, s := range c.Sources {
			allowed := false
			for _, role := range rc.Roles {
				if slices.Contains(s.Roles, role) {
					allowed = true
				}
			}
			if !allowed {
				continue
			}
			v := AnalysisSourceView{Namespace: namespace, Source: s.Name, Description: s.Description, Semantics: exploration.SemanticVersion{Name: c.Metadata.Name, Version: c.Metadata.Version, Fingerprint: fp, PolicyFingerprint: pfp}, MaxWindowDays: s.MaxWindowDays, MaxRows: s.MaxRows, Timeout: s.Timeout.String(), Attributes: []AnalysisAttributeView{}}
			for _, a := range c.Attributes {
				if !slices.Contains(s.Dimensions, a.Name) {
					continue
				}
				if _, err := policy.Authorize(rc, bundle, fp, model.SemanticQuery{GroupBy: []string{a.Name}}); err != nil {
					continue
				}
				f := fields[a.Dataset+"."+a.Field]
				av := AnalysisAttributeView{Name: a.Name, Description: a.Description, Type: f.Type, CalendarTimezone: f.CalendarTimezone, TimeRangeAllowed: a.Dataset == s.Dataset && f.Type == model.DataTypeDate, InternalOnly: a.InternalOnly}
				target := a.Dataset
				path := []AnalysisRelationView{}
				reachable := target == s.Dataset
				for steps := 0; !reachable && steps < 8; steps++ {
					found := false
					for _, rel := range c.Relationships {
						if rel.To == target {
							br := exploration.BoundRelation{Name: rel.Name, Missing: rel.Missing, Snapshot: rel.Snapshot}
							path = append(path, analysisRelationView(br))
							target = rel.From
							found = true
							break
						}
					}
					if !found {
						break
					}
					reachable = target == s.Dataset
				}
				if !reachable {
					continue
				}
				slices.Reverse(path)
				av.Relations = path
				v.Attributes = append(v.Attributes, av)
			}
			// An accessible root date is required to construct any bounded query.
			usable := false
			for _, a := range v.Attributes {
				usable = usable || a.TimeRangeAllowed
			}
			if usable {
				out = append(out, v)
			}
		}
	}
	return out, nil
}

func (r *ConfiguredAnalysisCatalogs) Namespaces(ctx context.Context, rc model.RequestContext) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, c := range r.catalogs {
		if c.Tenant != rc.Tenant || seen[c.Namespace] {
			continue
		}
		seen[c.Namespace] = true
		v, err := r.Discover(ctx, c.Namespace, rc)
		if err != nil {
			return nil, err
		}
		if len(v) > 0 {
			out = append(out, c.Namespace)
		}
	}
	slices.Sort(out)
	return out, nil
}
