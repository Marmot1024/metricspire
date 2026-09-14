// Package model contains MetricSpire's serializable, engine-neutral contracts.
package model

import "time"

const (
	APIVersion = "metricspire.io/v1alpha1"

	KindSemanticModel    = "SemanticModel"
	KindSemanticManifest = "SemanticManifest"
	KindSourceBinding    = "SourceBinding"
	KindPolicySource     = "PolicySource"
	KindPolicyBundle     = "PolicyBundle"
	KindSemanticQuery    = "SemanticQuery"
	KindLogicalPlan      = "LogicalPlan"
	KindPhysicalPlan     = "PhysicalPlan"
)

type Metadata struct {
	Name    string `json:"name" yaml:"name"`
	Version string `json:"version" yaml:"version"`
}

type SemanticSource struct {
	APIVersion string       `json:"api_version" yaml:"api_version"`
	Kind       string       `json:"kind" yaml:"kind"`
	Metadata   Metadata     `json:"metadata" yaml:"metadata"`
	Spec       SemanticSpec `json:"spec" yaml:"spec"`
}

type SemanticManifest struct {
	APIVersion  string       `json:"api_version" yaml:"api_version"`
	Kind        string       `json:"kind" yaml:"kind"`
	Metadata    Metadata     `json:"metadata" yaml:"metadata"`
	Fingerprint string       `json:"fingerprint" yaml:"fingerprint"`
	Definitions SemanticSpec `json:"definitions" yaml:"definitions"`
}

type SemanticSpec struct {
	Datasets      []Dataset      `json:"datasets" yaml:"datasets"`
	Entities      []Entity       `json:"entities" yaml:"entities"`
	Dimensions    []Dimension    `json:"dimensions" yaml:"dimensions"`
	Relationships []Relationship `json:"relationships" yaml:"relationships"`
	Metrics       []Metric       `json:"metrics" yaml:"metrics"`
}

type Dataset struct {
	Name        string  `json:"name" yaml:"name"`
	Description string  `json:"description,omitempty" yaml:"description,omitempty"`
	Fields      []Field `json:"fields" yaml:"fields"`
}

type Field struct {
	Name     string   `json:"name" yaml:"name"`
	DataType DataType `json:"data_type" yaml:"data_type"`
	Nullable bool     `json:"nullable,omitempty" yaml:"nullable,omitempty"`
}

type DataType string

const (
	DataTypeString    DataType = "string"
	DataTypeInteger   DataType = "integer"
	DataTypeDecimal   DataType = "decimal"
	DataTypeBoolean   DataType = "boolean"
	DataTypeDate      DataType = "date"
	DataTypeTimestamp DataType = "timestamp"
)

type Entity struct {
	Name    string `json:"name" yaml:"name"`
	Dataset string `json:"dataset" yaml:"dataset"`
	Key     string `json:"key" yaml:"key"`
}

type Dimension struct {
	Name              string            `json:"name" yaml:"name"`
	Description       string            `json:"description,omitempty" yaml:"description,omitempty"`
	Entity            string            `json:"entity" yaml:"entity"`
	Field             string            `json:"field" yaml:"field"`
	Type              DimensionType     `json:"type" yaml:"type"`
	TimeGranularities []TimeGranularity `json:"time_granularities,omitempty" yaml:"time_granularities,omitempty"`
}

type DimensionType string

const (
	DimensionCategorical DimensionType = "categorical"
	DimensionTime        DimensionType = "time"
)

type TimeGranularity string

const (
	GrainDay   TimeGranularity = "day"
	GrainWeek  TimeGranularity = "week"
	GrainMonth TimeGranularity = "month"
)

type Relationship struct {
	Name        string      `json:"name" yaml:"name"`
	FromEntity  string      `json:"from_entity" yaml:"from_entity"`
	ToEntity    string      `json:"to_entity" yaml:"to_entity"`
	Cardinality Cardinality `json:"cardinality" yaml:"cardinality"`
	FromField   string      `json:"from_field" yaml:"from_field"`
	ToField     string      `json:"to_field" yaml:"to_field"`
}

type Cardinality string

const CardinalityManyToOne Cardinality = "many_to_one"

type Metric struct {
	Name              string       `json:"name" yaml:"name"`
	DisplayName       string       `json:"display_name,omitempty" yaml:"display_name,omitempty"`
	Description       string       `json:"description,omitempty" yaml:"description,omitempty"`
	Owner             string       `json:"owner,omitempty" yaml:"owner,omitempty"`
	Tags              []string     `json:"tags,omitempty" yaml:"tags,omitempty"`
	UsageExamples     []string     `json:"usage_examples,omitempty" yaml:"usage_examples,omitempty"`
	Deprecated        bool         `json:"deprecated,omitempty" yaml:"deprecated,omitempty"`
	Entity            string       `json:"entity" yaml:"entity"`
	Kind              MetricKind   `json:"kind" yaml:"kind"`
	ValueType         DataType     `json:"value_type" yaml:"value_type"`
	Unit              string       `json:"unit,omitempty" yaml:"unit,omitempty"`
	Expression        Expression   `json:"expression" yaml:"expression"`
	AllowedDimensions []string     `json:"allowed_dimensions" yaml:"allowed_dimensions"`
	TimeDimension     string       `json:"time_dimension,omitempty" yaml:"time_dimension,omitempty"`
	Verification      Verification `json:"verification" yaml:"verification"`
}

type MetricKind string

const (
	MetricAggregate MetricKind = "aggregate"
	MetricRatio     MetricKind = "ratio"
	MetricDerived   MetricKind = "derived"
)

type Expression struct {
	Op      ExpressionOp   `json:"op" yaml:"op"`
	Field   string         `json:"field,omitempty" yaml:"field,omitempty"`
	Metric  string         `json:"metric,omitempty" yaml:"metric,omitempty"`
	Value   string         `json:"value,omitempty" yaml:"value,omitempty"`
	Args    []Expression   `json:"args,omitempty" yaml:"args,omitempty"`
	Filters []MetricFilter `json:"filters,omitempty" yaml:"filters,omitempty"`
}

type ExpressionOp string

const (
	OpSum           ExpressionOp = "sum"
	OpCount         ExpressionOp = "count"
	OpCountDistinct ExpressionOp = "count_distinct"
	OpAverage       ExpressionOp = "avg"
	OpMinimum       ExpressionOp = "min"
	OpMaximum       ExpressionOp = "max"
	OpMetric        ExpressionOp = "metric"
	OpLiteral       ExpressionOp = "literal"
	OpAdd           ExpressionOp = "add"
	OpSubtract      ExpressionOp = "subtract"
	OpMultiply      ExpressionOp = "multiply"
	OpDivide        ExpressionOp = "divide"
)

// MetricFilter is a constrained predicate attached to an aggregate expression.
// It deliberately accepts field names and literal values, never SQL fragments.
type MetricFilter struct {
	Field    string               `json:"field" yaml:"field"`
	Operator MetricFilterOperator `json:"operator" yaml:"operator"`
	Values   []string             `json:"values,omitempty" yaml:"values,omitempty"`
}

type MetricFilterOperator string

const (
	MetricFilterEqual     MetricFilterOperator = "eq"
	MetricFilterNotEqual  MetricFilterOperator = "neq"
	MetricFilterIn        MetricFilterOperator = "in"
	MetricFilterNotIn     MetricFilterOperator = "not_in"
	MetricFilterIsNull    MetricFilterOperator = "is_null"
	MetricFilterIsNotNull MetricFilterOperator = "is_not_null"
)

type Verification struct {
	Status        VerificationStatus `json:"status" yaml:"status"`
	Evidence      []string           `json:"evidence,omitempty" yaml:"evidence,omitempty"`
	OpenQuestions []string           `json:"open_questions,omitempty" yaml:"open_questions,omitempty"`
}

type VerificationStatus string

const (
	VerificationUnverified VerificationStatus = "unverified"
	VerificationVerified   VerificationStatus = "verified"
)

type SourceBinding struct {
	APIVersion          string           `json:"api_version" yaml:"api_version"`
	Kind                string           `json:"kind" yaml:"kind"`
	Metadata            Metadata         `json:"metadata" yaml:"metadata"`
	ManifestFingerprint string           `json:"manifest_fingerprint,omitempty" yaml:"manifest_fingerprint,omitempty"`
	Engine              string           `json:"engine" yaml:"engine"`
	Datasets            []DatasetBinding `json:"datasets" yaml:"datasets"`
}

type DatasetBinding struct {
	Name     string         `json:"name" yaml:"name"`
	Resource ResourceRef    `json:"resource" yaml:"resource"`
	Fields   []FieldBinding `json:"fields" yaml:"fields"`
}

type ResourceRef struct {
	Kind    ResourceKind `json:"kind" yaml:"kind"`
	Catalog string       `json:"catalog,omitempty" yaml:"catalog,omitempty"`
	Schema  string       `json:"schema,omitempty" yaml:"schema,omitempty"`
	Table   string       `json:"table,omitempty" yaml:"table,omitempty"`
	URI     string       `json:"uri,omitempty" yaml:"uri,omitempty"`
}

type ResourceKind string

const (
	ResourceTable ResourceKind = "table"
	ResourceFile  ResourceKind = "file"
)

type FieldBinding struct {
	Name             string `json:"name" yaml:"name"`
	Column           string `json:"column" yaml:"column"`
	CalendarTimezone string `json:"calendar_timezone,omitempty" yaml:"calendar_timezone,omitempty"`
}

type PolicySource struct {
	APIVersion          string       `json:"api_version" yaml:"api_version"`
	Kind                string       `json:"kind" yaml:"kind"`
	Metadata            Metadata     `json:"metadata" yaml:"metadata"`
	ManifestFingerprint string       `json:"manifest_fingerprint,omitempty" yaml:"manifest_fingerprint,omitempty"`
	Tenant              string       `json:"tenant" yaml:"tenant"`
	Rules               []PolicyRule `json:"rules" yaml:"rules"`
}

type PolicyBundle struct {
	APIVersion          string       `json:"api_version" yaml:"api_version"`
	Kind                string       `json:"kind" yaml:"kind"`
	Metadata            Metadata     `json:"metadata" yaml:"metadata"`
	Fingerprint         string       `json:"fingerprint" yaml:"fingerprint"`
	ManifestFingerprint string       `json:"manifest_fingerprint" yaml:"manifest_fingerprint"`
	Tenant              string       `json:"tenant" yaml:"tenant"`
	Rules               []PolicyRule `json:"rules" yaml:"rules"`
}

type PolicyRule struct {
	Name       string       `json:"name" yaml:"name"`
	Effect     PolicyEffect `json:"effect" yaml:"effect"`
	Principals []string     `json:"principals,omitempty" yaml:"principals,omitempty"`
	Roles      []string     `json:"roles,omitempty" yaml:"roles,omitempty"`
	Metrics    []string     `json:"metrics" yaml:"metrics"`
	Dimensions []string     `json:"dimensions" yaml:"dimensions"`
}

type PolicyEffect string

const (
	EffectAllow PolicyEffect = "allow"
	EffectDeny  PolicyEffect = "deny"
)

// RequestContext is trusted input created by an authenticated transport. It is
// intentionally not embedded in SemanticQuery.
type RequestContext struct {
	Tenant    string   `json:"tenant" yaml:"tenant"`
	Principal string   `json:"principal" yaml:"principal"`
	Roles     []string `json:"roles,omitempty" yaml:"roles,omitempty"`
	RequestID string   `json:"request_id" yaml:"request_id"`
}

type SemanticQuery struct {
	APIVersion   string        `json:"api_version" yaml:"api_version"`
	Kind         string        `json:"kind" yaml:"kind"`
	Metrics      []string      `json:"metrics" yaml:"metrics"`
	GroupBy      []string      `json:"group_by,omitempty" yaml:"group_by,omitempty"`
	TimeRange    *TimeRange    `json:"time_range,omitempty" yaml:"time_range,omitempty"`
	TimeGrouping *TimeGrouping `json:"time_grouping,omitempty" yaml:"time_grouping,omitempty"`
	Filters      []Filter      `json:"filters,omitempty" yaml:"filters,omitempty"`
	OrderBy      []OrderBy     `json:"order_by,omitempty" yaml:"order_by,omitempty"`
	Limit        int           `json:"limit,omitempty" yaml:"limit,omitempty"`
}

type TimeRange struct {
	Dimension string `json:"dimension" yaml:"dimension"`
	Start     string `json:"start" yaml:"start"`
	End       string `json:"end" yaml:"end"`
	Timezone  string `json:"timezone,omitempty" yaml:"timezone,omitempty"`
}

type TimeGrouping struct {
	Dimension   string          `json:"dimension" yaml:"dimension"`
	Timezone    string          `json:"timezone" yaml:"timezone"`
	Granularity TimeGranularity `json:"granularity" yaml:"granularity"`
	WeekStart   WeekStart       `json:"week_start,omitempty" yaml:"week_start,omitempty"`
}

type WeekStart string

const (
	WeekStartMonday WeekStart = "monday"
	WeekStartSunday WeekStart = "sunday"
)

type Filter struct {
	Dimension string         `json:"dimension" yaml:"dimension"`
	Operator  FilterOperator `json:"operator" yaml:"operator"`
	Values    []string       `json:"values" yaml:"values"`
}

type FilterOperator string

const (
	FilterEqual FilterOperator = "eq"
	FilterIn    FilterOperator = "in"
)

type OrderBy struct {
	Field     string        `json:"field" yaml:"field"`
	Direction SortDirection `json:"direction" yaml:"direction"`
}

type SortDirection string

const (
	SortAscending  SortDirection = "asc"
	SortDescending SortDirection = "desc"
)

type LogicalPlan struct {
	APIVersion          string             `json:"api_version"`
	Kind                string             `json:"kind"`
	Fingerprint         string             `json:"fingerprint"`
	ManifestFingerprint string             `json:"manifest_fingerprint"`
	PolicyFingerprint   string             `json:"policy_fingerprint"`
	QueryFingerprint    string             `json:"query_fingerprint"`
	RootEntity          string             `json:"root_entity"`
	Metrics             []PlannedMetric    `json:"metrics"`
	Dimensions          []PlannedDimension `json:"dimensions"`
	Joins               []PlannedJoin      `json:"joins"`
	TimeRange           *TimeRange         `json:"time_range,omitempty"`
	TimeGrouping        *TimeGrouping      `json:"time_grouping,omitempty"`
	Filters             []Filter           `json:"filters,omitempty"`
	OrderBy             []OrderBy          `json:"order_by,omitempty"`
	Limit               int                `json:"limit"`
	Lineage             Lineage            `json:"lineage"`
}

type PlannedMetric struct {
	Name         string     `json:"name"`
	Output       bool       `json:"output"`
	Kind         MetricKind `json:"kind"`
	ValueType    DataType   `json:"value_type"`
	Unit         string     `json:"unit,omitempty"`
	Expression   Expression `json:"expression"`
	Dependencies []string   `json:"dependencies,omitempty"`
}

type PlannedDimension struct {
	Name     string        `json:"name"`
	Output   bool          `json:"output"`
	Entity   string        `json:"entity"`
	Field    string        `json:"field"`
	Type     DimensionType `json:"type"`
	DataType DataType      `json:"data_type"`
}

type PlannedJoin struct {
	Name        string      `json:"name"`
	FromEntity  string      `json:"from_entity"`
	ToEntity    string      `json:"to_entity"`
	Cardinality Cardinality `json:"cardinality"`
	FromField   string      `json:"from_field"`
	ToField     string      `json:"to_field"`
}

type Lineage struct {
	Datasets      []string `json:"datasets"`
	Metrics       []string `json:"metrics"`
	Dimensions    []string `json:"dimensions"`
	Relationships []string `json:"relationships"`
}

type EngineCapabilities struct {
	Engine                string                 `json:"engine" yaml:"engine"`
	ExpressionOps         []ExpressionOp         `json:"expression_ops" yaml:"expression_ops"`
	MetricFilterOperators []MetricFilterOperator `json:"metric_filter_operators,omitempty" yaml:"metric_filter_operators,omitempty"`
	JoinCardinalities     []Cardinality          `json:"join_cardinalities" yaml:"join_cardinalities"`
	TimeGranularities     []TimeGranularity      `json:"time_granularities" yaml:"time_granularities"`
	MaxJoins              int                    `json:"max_joins" yaml:"max_joins"`
}

type PhysicalPlan struct {
	APIVersion          string              `json:"api_version"`
	Kind                string              `json:"kind"`
	Fingerprint         string              `json:"fingerprint"`
	LogicalFingerprint  string              `json:"logical_fingerprint"`
	ManifestFingerprint string              `json:"manifest_fingerprint"`
	PolicyFingerprint   string              `json:"policy_fingerprint"`
	BindingFingerprint  string              `json:"binding_fingerprint"`
	Engine              string              `json:"engine"`
	Root                PhysicalDataset     `json:"root"`
	Metrics             []PlannedMetric     `json:"metrics"`
	MetricFields        []PhysicalField     `json:"metric_fields"`
	Dimensions          []PhysicalDimension `json:"dimensions"`
	Joins               []PhysicalJoin      `json:"joins"`
	TimeRange           *TimeRange          `json:"time_range,omitempty"`
	TimeGrouping        *TimeGrouping       `json:"time_grouping,omitempty"`
	Filters             []Filter            `json:"filters,omitempty"`
	OrderBy             []OrderBy           `json:"order_by,omitempty"`
	Limit               int                 `json:"limit"`
	Lineage             Lineage             `json:"lineage"`
}

type PhysicalDataset struct {
	Name     string      `json:"name"`
	Entity   string      `json:"entity"`
	Resource ResourceRef `json:"resource"`
}

type PhysicalField struct {
	Entity   string      `json:"entity"`
	Field    string      `json:"field"`
	Resource ResourceRef `json:"resource"`
	Column   string      `json:"column"`
	DataType DataType    `json:"data_type"`
}

type PhysicalDimension struct {
	Name             string        `json:"name"`
	Output           bool          `json:"output"`
	Entity           string        `json:"entity"`
	Resource         ResourceRef   `json:"resource"`
	Column           string        `json:"column"`
	Type             DimensionType `json:"type"`
	DataType         DataType      `json:"data_type"`
	CalendarTimezone string        `json:"calendar_timezone,omitempty"`
}

type PhysicalJoin struct {
	Name         string      `json:"name"`
	Cardinality  Cardinality `json:"cardinality"`
	FromEntity   string      `json:"from_entity"`
	ToEntity     string      `json:"to_entity"`
	FromResource ResourceRef `json:"from_resource"`
	FromColumn   string      `json:"from_column"`
	ToResource   ResourceRef `json:"to_resource"`
	ToColumn     string      `json:"to_column"`
}

type JobStatus string

const (
	JobPending   JobStatus = "pending"
	JobRunning   JobStatus = "running"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

type ExecutionJob struct {
	ID                  string     `json:"id"`
	Status              JobStatus  `json:"status"`
	PhysicalFingerprint string     `json:"physical_fingerprint"`
	RowLimit            int64      `json:"row_limit"`
	SubmittedAt         time.Time  `json:"submitted_at"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	FinishedAt          *time.Time `json:"finished_at,omitempty"`
	Error               *Problem   `json:"error,omitempty"`
}

type ExecutionSnapshot struct {
	Job    ExecutionJob `json:"job"`
	Result *TypedResult `json:"result,omitempty"`
}

// TypedResult keeps Databricks' exact schema alongside decoded scalar rows.
// Decimal, date, and timestamp values remain strings to avoid precision or
// timezone loss at the service boundary.
type TypedResult struct {
	Columns   []ResultColumn `json:"columns"`
	Rows      [][]any        `json:"rows"`
	Truncated bool           `json:"truncated"`
}

type ResultColumn struct {
	Name     string `json:"name"`
	TypeName string `json:"type_name"`
	TypeText string `json:"type_text"`
}

type Problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
}

func (p *Problem) Error() string {
	if p.Path == "" {
		return p.Code + ": " + p.Message
	}
	return p.Code + " at " + p.Path + ": " + p.Message
}
