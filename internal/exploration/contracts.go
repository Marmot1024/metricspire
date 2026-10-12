// Package exploration describes bounded, governed temporary analyses. It does
// not accept SQL, physical resources, identities, or client-selected policies.
package exploration

import (
	"time"

	"github.com/marmot1024/metricspire/internal/model"
)

const Kind = "AnalysisQuery"

type Query struct {
	APIVersion   string              `json:"api_version" yaml:"api_version"`
	Kind         string              `json:"kind" yaml:"kind"`
	Source       string              `json:"source" yaml:"source"`
	GroupBy      []string            `json:"group_by,omitempty" yaml:"group_by,omitempty"`
	TimeRange    *model.TimeRange    `json:"time_range" yaml:"time_range"`
	TimeGrouping *model.TimeGrouping `json:"time_grouping,omitempty" yaml:"time_grouping,omitempty"`
	Filters      []model.Filter      `json:"filters,omitempty" yaml:"filters,omitempty"`
	Where        *Expression         `json:"where,omitempty" yaml:"where,omitempty"`
	Measures     []Measure           `json:"measures" yaml:"measures"`
	Calculations []Calculation       `json:"calculations,omitempty" yaml:"calculations,omitempty"`
	Summarize    *Summary            `json:"summarize,omitempty" yaml:"summarize,omitempty"`
	OrderBy      []model.OrderBy     `json:"order_by,omitempty" yaml:"order_by,omitempty"`
	Limit        int                 `json:"limit" yaml:"limit"`
}

type Measure struct {
	Name  string      `json:"name" yaml:"name"`
	Op    string      `json:"op" yaml:"op"`                           // count, count_distinct, sum, avg, min, max
	Field string      `json:"field,omitempty" yaml:"field,omitempty"` // registered dimension or prior-stage output
	Where *Expression `json:"where,omitempty" yaml:"where,omitempty"`
}

type Calculation struct {
	Name       string     `json:"name" yaml:"name"`
	Expression Expression `json:"expression" yaml:"expression"`
}

type Summary struct {
	GroupBy      []string      `json:"group_by,omitempty" yaml:"group_by,omitempty"`
	Measures     []Measure     `json:"measures" yaml:"measures"`
	Calculations []Calculation `json:"calculations,omitempty" yaml:"calculations,omitempty"`
}

// CASE uses condition/value pairs followed by one ELSE argument. Share is a
// final-stage window ratio; its partition keys must be final grouping keys.
type Expression struct {
	Op          string         `json:"op" yaml:"op"`
	Ref         string         `json:"ref,omitempty" yaml:"ref,omitempty"`
	Type        model.DataType `json:"type,omitempty" yaml:"type,omitempty"`
	Value       string         `json:"value,omitempty" yaml:"value,omitempty"`
	Args        []Expression   `json:"args,omitempty" yaml:"args,omitempty"`
	PartitionBy []string       `json:"partition_by,omitempty" yaml:"partition_by,omitempty"`
}

// Source is trusted server configuration, never a public request field.
// AnchorMetric must define COUNT(field) over approved primitive rows. A
// distinct-count or preaggregated metric cannot define an analysis row set.
type Source struct {
	Name          string        `json:"name" yaml:"name"`
	Description   string        `json:"description,omitempty" yaml:"description,omitempty"`
	AnchorMetric  string        `json:"anchor_metric" yaml:"anchor_metric"`
	Dataset       string        `json:"dataset,omitempty" yaml:"dataset,omitempty"`
	FixedFilter   *Expression   `json:"fixed_filter,omitempty" yaml:"fixed_filter,omitempty"`
	Dimensions    []string      `json:"dimensions" yaml:"dimensions"`
	Roles         []string      `json:"roles" yaml:"roles"`
	MaxWindowDays int           `json:"max_window_days" yaml:"max_window_days"`
	MaxRows       int           `json:"max_rows" yaml:"max_rows"`
	Timeout       time.Duration `json:"timeout" yaml:"timeout"`
}

type Column struct {
	Name string         `json:"name" yaml:"name"`
	Type model.DataType `json:"type" yaml:"type"`
}

// Plan pins the existing authorized physical plan and the temporary workflow.
// Fingerprints provide integrity/traceability, not proof of authorization;
// callers must obtain plans through the trusted application service.
type Plan struct {
	Fingerprint  string             `json:"fingerprint" yaml:"fingerprint"`
	Source       Source             `json:"source" yaml:"source"`
	Query        Query              `json:"query" yaml:"query"`
	Base         model.PhysicalPlan `json:"base" yaml:"base"`
	Columns      []Column           `json:"columns" yaml:"columns"`
	Semantics    *SemanticVersion   `json:"external_semantics,omitempty" yaml:"external_semantics,omitempty"`
	Relations    []BoundRelation    `json:"relations,omitempty" yaml:"relations,omitempty"`
	InternalOnly []string           `json:"internal_only,omitempty" yaml:"internal_only,omitempty"`
}
