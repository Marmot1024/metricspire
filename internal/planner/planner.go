// Package planner creates deterministic engine-neutral and bound plans.
package planner

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/canonical"
	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/policy"
)

const (
	DefaultLimit         = 1000
	MaximumLimit         = 10000
	MaximumMetrics       = 32
	MaximumDimensions    = 16
	MaximumFilters       = 32
	MaximumFilterValues  = 100
	MaximumTimeRangeDays = 366
)

var physicalNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)
var decimalValuePattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

type manifestIndex struct {
	datasets      map[string]model.Dataset
	entities      map[string]model.Entity
	dimensions    map[string]model.Dimension
	relationships []model.Relationship
	metrics       map[string]model.Metric
}

func BuildLogical(manifest model.SemanticManifest, bundle model.PolicyBundle, context model.RequestContext, query model.SemanticQuery) (model.LogicalPlan, error) {
	return buildLogical(manifest, bundle, context, query, false)
}

// BuildLogicalPreview permits unverified metrics for the protected maintainer
// draft-preview use case. All structural, policy, budget, deprecation,
// dimension, time, and physical-planning checks remain unchanged.
func BuildLogicalPreview(manifest model.SemanticManifest, bundle model.PolicyBundle, context model.RequestContext, query model.SemanticQuery) (model.LogicalPlan, error) {
	return buildLogical(manifest, bundle, context, query, true)
}

func buildLogical(manifest model.SemanticManifest, bundle model.PolicyBundle, context model.RequestContext, query model.SemanticQuery, allowUnverified bool) (model.LogicalPlan, error) {
	if err := compiler.VerifyManifest(manifest); err != nil {
		return model.LogicalPlan{}, err
	}
	if err := compiler.VerifyPolicy(bundle, manifest); err != nil {
		return model.LogicalPlan{}, err
	}
	if query.APIVersion != model.APIVersion {
		return model.LogicalPlan{}, problem("invalid_api_version", "api_version", "must be %q", model.APIVersion)
	}
	if query.Kind != model.KindSemanticQuery {
		return model.LogicalPlan{}, problem("invalid_kind", "kind", "must be %q", model.KindSemanticQuery)
	}
	if len(query.Metrics) == 0 {
		return model.LogicalPlan{}, problem("required", "metrics", "must not be empty")
	}
	if err := validateRequestBudget(query); err != nil {
		return model.LogicalPlan{}, err
	}
	if err := rejectDuplicates(query.Metrics, "metrics"); err != nil {
		return model.LogicalPlan{}, err
	}
	if err := rejectDuplicates(query.GroupBy, "group_by"); err != nil {
		return model.LogicalPlan{}, err
	}
	if _, err := policy.Authorize(context, bundle, manifest.Fingerprint, query); err != nil {
		return model.LogicalPlan{}, err
	}

	index := indexManifest(manifest)
	rootEntity := ""
	for i, name := range query.Metrics {
		metric, exists := index.metrics[name]
		if !exists {
			return model.LogicalPlan{}, problem("unknown_reference", fmt.Sprintf("metrics[%d]", i), "metric %q does not exist", name)
		}
		if !allowUnverified && metric.Verification.Status != model.VerificationVerified {
			return model.LogicalPlan{}, problem("metric_not_verified", fmt.Sprintf("metrics[%d]", i), "metric %q is not verified", name)
		}
		if metric.Deprecated {
			return model.LogicalPlan{}, problem("metric_deprecated", fmt.Sprintf("metrics[%d]", i), "metric %q is deprecated", name)
		}
		if rootEntity == "" {
			rootEntity = metric.Entity
		} else if rootEntity != metric.Entity {
			return model.LogicalPlan{}, problem("cross_entity_query", "metrics", "v0.1 requires all output metrics to share one root entity")
		}
	}

	normalizedQuery := query
	if normalizedQuery.Limit == 0 {
		normalizedQuery.Limit = DefaultLimit
	}
	if normalizedQuery.Limit < 1 || normalizedQuery.Limit > MaximumLimit {
		return model.LogicalPlan{}, problem("limit_exceeded", "limit", "must be between 1 and %d", MaximumLimit)
	}
	if err := validateDimensions(index, normalizedQuery, rootEntity); err != nil {
		return model.LogicalPlan{}, err
	}
	if err := validateFilters(index, normalizedQuery.Filters); err != nil {
		return model.LogicalPlan{}, err
	}
	if err := normalizeTimeSemantics(index, &normalizedQuery, rootEntity); err != nil {
		return model.LogicalPlan{}, err
	}
	if err := validateOrder(normalizedQuery); err != nil {
		return model.LogicalPlan{}, err
	}

	plannedMetrics, metricLineage, err := planMetrics(index, normalizedQuery.Metrics, allowUnverified)
	if err != nil {
		return model.LogicalPlan{}, err
	}
	plannedDimensions, targetEntities := planDimensions(index, normalizedQuery)
	joins, err := planJoins(rootEntity, targetEntities, index.relationships)
	if err != nil {
		return model.LogicalPlan{}, err
	}
	for _, metricName := range normalizedQuery.Metrics {
		metric := index.metrics[metricName]
		for _, dimension := range requestedDimensions(normalizedQuery) {
			if !slices.Contains(metric.AllowedDimensions, dimension) {
				return model.LogicalPlan{}, problem("dimension_not_allowed", "group_by", "dimension %q is not allowed for metric %q", dimension, metricName)
			}
		}
	}

	queryFingerprint, err := canonical.Fingerprint(normalizedQuery)
	if err != nil {
		return model.LogicalPlan{}, fmt.Errorf("fingerprint query: %w", err)
	}
	lineage := buildLineage(index, rootEntity, metricLineage, plannedDimensions, joins)
	plan := model.LogicalPlan{
		APIVersion:          model.APIVersion,
		Kind:                model.KindLogicalPlan,
		ManifestFingerprint: manifest.Fingerprint,
		PolicyFingerprint:   bundle.Fingerprint,
		QueryFingerprint:    queryFingerprint,
		RootEntity:          rootEntity,
		Metrics:             plannedMetrics,
		Dimensions:          plannedDimensions,
		Joins:               joins,
		TimeRange:           normalizedQuery.TimeRange,
		TimeGrouping:        normalizedQuery.TimeGrouping,
		Filters:             normalizedQuery.Filters,
		OrderBy:             normalizedQuery.OrderBy,
		Limit:               normalizedQuery.Limit,
		Lineage:             lineage,
	}
	fingerprint, err := fingerprintWithoutField(plan)
	if err != nil {
		return model.LogicalPlan{}, fmt.Errorf("fingerprint logical plan: %w", err)
	}
	plan.Fingerprint = fingerprint
	return plan, nil
}

func validateRequestBudget(query model.SemanticQuery) error {
	if len(query.Metrics) > MaximumMetrics {
		return problem("budget_exceeded", "metrics", "must contain at most %d metrics", MaximumMetrics)
	}
	if len(requestedDimensions(query)) > MaximumDimensions {
		return problem("budget_exceeded", "dimensions", "must reference at most %d dimensions", MaximumDimensions)
	}
	if len(query.Filters) > MaximumFilters {
		return problem("budget_exceeded", "filters", "must contain at most %d filters", MaximumFilters)
	}
	for i, filter := range query.Filters {
		if len(filter.Values) > MaximumFilterValues {
			return problem("budget_exceeded", fmt.Sprintf("filters[%d].values", i), "must contain at most %d values", MaximumFilterValues)
		}
	}
	return nil
}

func BuildPhysical(manifest model.SemanticManifest, logical model.LogicalPlan, binding model.SourceBinding, capabilities model.EngineCapabilities) (model.PhysicalPlan, error) {
	if err := compiler.VerifyManifest(manifest); err != nil {
		return model.PhysicalPlan{}, err
	}
	if logical.ManifestFingerprint != manifest.Fingerprint {
		return model.PhysicalPlan{}, problem("manifest_mismatch", "logical_plan", "logical plan does not match manifest")
	}
	logicalFingerprint, err := fingerprintWithoutField(logical)
	if err != nil {
		return model.PhysicalPlan{}, fmt.Errorf("verify logical plan fingerprint: %w", err)
	}
	if logicalFingerprint != logical.Fingerprint {
		return model.PhysicalPlan{}, problem("fingerprint_mismatch", "logical_plan.fingerprint", "logical plan content does not match fingerprint")
	}
	if binding.APIVersion != model.APIVersion || binding.Kind != model.KindSourceBinding {
		return model.PhysicalPlan{}, problem("invalid_binding", "binding", "invalid source binding contract")
	}
	if binding.ManifestFingerprint != "" && binding.ManifestFingerprint != manifest.Fingerprint {
		return model.PhysicalPlan{}, problem("manifest_mismatch", "binding.manifest_fingerprint", "does not match manifest")
	}
	binding.ManifestFingerprint = manifest.Fingerprint
	if strings.TrimSpace(binding.Engine) == "" || strings.TrimSpace(capabilities.Engine) == "" || binding.Engine != capabilities.Engine {
		return model.PhysicalPlan{}, problem("capability_mismatch", "engine", "binding and capabilities must name the same engine")
	}
	if capabilities.MaxJoins < len(logical.Joins) {
		return model.PhysicalPlan{}, problem("capability_missing", "joins", "engine supports at most %d joins", capabilities.MaxJoins)
	}
	for _, join := range logical.Joins {
		if !slices.Contains(capabilities.JoinCardinalities, join.Cardinality) {
			return model.PhysicalPlan{}, problem("capability_missing", "joins", "engine does not support %s joins", join.Cardinality)
		}
	}
	if logical.TimeGrouping != nil && !slices.Contains(capabilities.TimeGranularities, logical.TimeGrouping.Granularity) {
		return model.PhysicalPlan{}, problem("capability_missing", "time_grouping.granularity", "engine does not support %s", logical.TimeGrouping.Granularity)
	}
	for _, metric := range logical.Metrics {
		for _, operation := range expressionOperations(metric.Expression) {
			if !slices.Contains(capabilities.ExpressionOps, operation) {
				return model.PhysicalPlan{}, problem("capability_missing", "metrics", "engine does not support expression operation %s", operation)
			}
		}
		for _, operator := range expressionFilterOperators(metric.Expression) {
			if !slices.Contains(capabilities.MetricFilterOperators, operator) {
				return model.PhysicalPlan{}, problem("capability_missing", "metrics", "engine does not support metric filter operator %s", operator)
			}
		}
	}

	index := indexManifest(manifest)
	normalizedBinding, bindingIndex, err := normalizeAndValidateBinding(binding, manifest, logical.Lineage.Datasets)
	if err != nil {
		return model.PhysicalPlan{}, err
	}
	bindingFingerprint, err := canonical.Fingerprint(normalizedBinding)
	if err != nil {
		return model.PhysicalPlan{}, fmt.Errorf("fingerprint binding: %w", err)
	}
	rootEntity := index.entities[logical.RootEntity]
	rootBinding := bindingIndex[rootEntity.Dataset]
	metricFields, err := bindMetricFields(logical.Metrics, index, bindingIndex)
	if err != nil {
		return model.PhysicalPlan{}, err
	}
	physicalDimensions := make([]model.PhysicalDimension, 0, len(logical.Dimensions))
	for _, dimension := range logical.Dimensions {
		entity := index.entities[dimension.Entity]
		datasetBinding := bindingIndex[entity.Dataset]
		fieldBinding, ok := boundField(datasetBinding, dimension.Field)
		if !ok {
			return model.PhysicalPlan{}, problem("binding_missing", "binding.datasets", "field %s.%s is not bound", entity.Dataset, dimension.Field)
		}
		physicalDimensions = append(physicalDimensions, model.PhysicalDimension{
			Name: dimension.Name, Output: dimension.Output, Entity: dimension.Entity,
			Resource: datasetBinding.Resource, Column: fieldBinding.Column, Type: dimension.Type,
			DataType: dimension.DataType, CalendarTimezone: fieldBinding.CalendarTimezone,
		})
	}
	if err := validatePhysicalTimeSemantics(logical.TimeRange, physicalDimensions); err != nil {
		return model.PhysicalPlan{}, err
	}
	physicalJoins := make([]model.PhysicalJoin, 0, len(logical.Joins))
	for _, join := range logical.Joins {
		fromEntity := index.entities[join.FromEntity]
		toEntity := index.entities[join.ToEntity]
		fromBinding := bindingIndex[fromEntity.Dataset]
		toBinding := bindingIndex[toEntity.Dataset]
		fromColumn, fromOK := boundColumn(fromBinding, join.FromField)
		toColumn, toOK := boundColumn(toBinding, join.ToField)
		if !fromOK || !toOK {
			return model.PhysicalPlan{}, problem("binding_missing", "binding.datasets", "join %q fields are not fully bound", join.Name)
		}
		physicalJoins = append(physicalJoins, model.PhysicalJoin{
			Name: join.Name, Cardinality: join.Cardinality, FromEntity: join.FromEntity, ToEntity: join.ToEntity,
			FromResource: fromBinding.Resource, FromColumn: fromColumn,
			ToResource: toBinding.Resource, ToColumn: toColumn,
		})
	}

	plan := model.PhysicalPlan{
		APIVersion:          model.APIVersion,
		Kind:                model.KindPhysicalPlan,
		LogicalFingerprint:  logical.Fingerprint,
		ManifestFingerprint: logical.ManifestFingerprint,
		PolicyFingerprint:   logical.PolicyFingerprint,
		BindingFingerprint:  bindingFingerprint,
		Engine:              binding.Engine,
		Root:                model.PhysicalDataset{Name: rootEntity.Dataset, Entity: logical.RootEntity, Resource: rootBinding.Resource},
		Metrics:             logical.Metrics,
		MetricFields:        metricFields,
		Dimensions:          physicalDimensions,
		Joins:               physicalJoins,
		TimeRange:           logical.TimeRange,
		TimeGrouping:        logical.TimeGrouping,
		Filters:             logical.Filters,
		OrderBy:             logical.OrderBy,
		Limit:               logical.Limit,
		Lineage:             logical.Lineage,
	}
	fingerprint, err := fingerprintWithoutField(plan)
	if err != nil {
		return model.PhysicalPlan{}, fmt.Errorf("fingerprint physical plan: %w", err)
	}
	plan.Fingerprint = fingerprint
	return plan, nil
}

// VerifyPhysical detects mutation between planning and engine execution.
func VerifyPhysical(plan model.PhysicalPlan) error {
	if plan.APIVersion != model.APIVersion || plan.Kind != model.KindPhysicalPlan || plan.Fingerprint == "" {
		return problem("invalid_physical_plan", "physical_plan", "plan is not a compiled MetricSpire artifact")
	}
	fingerprint, err := fingerprintWithoutField(plan)
	if err != nil {
		return fmt.Errorf("verify physical plan fingerprint: %w", err)
	}
	if fingerprint != plan.Fingerprint {
		return problem("fingerprint_mismatch", "physical_plan.fingerprint", "physical plan content does not match fingerprint")
	}
	return nil
}

func indexManifest(manifest model.SemanticManifest) manifestIndex {
	index := manifestIndex{
		datasets: make(map[string]model.Dataset), entities: make(map[string]model.Entity),
		dimensions: make(map[string]model.Dimension), metrics: make(map[string]model.Metric),
		relationships: append([]model.Relationship(nil), manifest.Definitions.Relationships...),
	}
	for _, value := range manifest.Definitions.Datasets {
		index.datasets[value.Name] = value
	}
	for _, value := range manifest.Definitions.Entities {
		index.entities[value.Name] = value
	}
	for _, value := range manifest.Definitions.Dimensions {
		index.dimensions[value.Name] = value
	}
	for _, value := range manifest.Definitions.Metrics {
		index.metrics[value.Name] = value
	}
	return index
}

func validateDimensions(index manifestIndex, query model.SemanticQuery, root string) error {
	for _, name := range requestedDimensions(query) {
		dimension, exists := index.dimensions[name]
		if !exists {
			return problem("unknown_reference", "dimensions", "dimension %q does not exist", name)
		}
		if _, err := findJoinPath(root, dimension.Entity, index.relationships); err != nil {
			return err
		}
	}
	return nil
}

func validateFilters(index manifestIndex, filters []model.Filter) error {
	seen := make(map[string]struct{})
	for i, filter := range filters {
		path := fmt.Sprintf("filters[%d]", i)
		if _, exists := seen[filter.Dimension]; exists {
			return problem("duplicate_value", path+".dimension", "dimension %q has multiple filters", filter.Dimension)
		}
		seen[filter.Dimension] = struct{}{}
		dimension, exists := index.dimensions[filter.Dimension]
		if !exists {
			return problem("unknown_reference", path+".dimension", "dimension %q does not exist", filter.Dimension)
		}
		if filter.Operator != model.FilterEqual && filter.Operator != model.FilterIn {
			return problem("invalid_filter", path+".operator", "must be eq or in")
		}
		if len(filter.Values) == 0 || (filter.Operator == model.FilterEqual && len(filter.Values) != 1) {
			return problem("invalid_filter", path+".values", "operator %s received invalid value count", filter.Operator)
		}
		if err := rejectDuplicates(filter.Values, path+".values"); err != nil {
			return err
		}
		entity := index.entities[dimension.Entity]
		field, _ := datasetField(index.datasets[entity.Dataset], dimension.Field)
		for valueIndex, value := range filter.Values {
			if err := validateScalarValue(value, field.DataType); err != nil {
				return problem("invalid_filter", fmt.Sprintf("%s.values[%d]", path, valueIndex), "%v", err)
			}
		}
	}
	return nil
}

func validateScalarValue(value string, dataType model.DataType) error {
	switch dataType {
	case model.DataTypeString:
		if strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("string contains a control character")
		}
	case model.DataTypeInteger:
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return fmt.Errorf("%q is not an integer", value)
		}
	case model.DataTypeDecimal:
		if !decimalValuePattern.MatchString(value) {
			return fmt.Errorf("%q is not a decimal", value)
		}
	case model.DataTypeBoolean:
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("%q is not a boolean", value)
		}
	case model.DataTypeDate:
		if _, err := time.Parse(time.DateOnly, value); err != nil {
			return fmt.Errorf("%q is not an ISO date", value)
		}
	case model.DataTypeTimestamp:
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
			return fmt.Errorf("%q is not an RFC3339 timestamp", value)
		}
	default:
		return fmt.Errorf("unsupported data type %q", dataType)
	}
	return nil
}

func normalizeTimeSemantics(index manifestIndex, query *model.SemanticQuery, root string) error {
	requiresTime := false
	for _, metricName := range query.Metrics {
		if index.metrics[metricName].TimeDimension != "" {
			requiresTime = true
		}
	}
	if query.TimeRange == nil {
		if requiresTime {
			return problem("time_range_required", "time_range", "time-bound metrics require dimension, RFC3339 start, exclusive end, and the business timezone when the source or grouping is calendar-based")
		}
		if query.TimeGrouping != nil {
			return problem("invalid_time", "time_grouping", "time grouping requires a time range")
		}
		return validateTimeGrouping(index, query)
	}
	rangeValue := *query.TimeRange
	dimension, exists := index.dimensions[rangeValue.Dimension]
	if !exists || dimension.Type != model.DimensionTime {
		return problem("invalid_time", "time_range.dimension", "must reference a time dimension")
	}
	if _, err := findJoinPath(root, dimension.Entity, index.relationships); err != nil {
		return err
	}
	start, err := time.Parse(time.RFC3339Nano, rangeValue.Start)
	if err != nil {
		return problem("invalid_time", "time_range.start", "must be an RFC3339 timestamp with offset")
	}
	end, err := time.Parse(time.RFC3339Nano, rangeValue.End)
	if err != nil {
		return problem("invalid_time", "time_range.end", "must be an RFC3339 timestamp with offset")
	}
	if !start.Before(end) {
		return problem("invalid_time", "time_range", "start must be before exclusive end")
	}
	rangeValue.Timezone = strings.TrimSpace(rangeValue.Timezone)
	if rangeValue.Timezone != "" {
		location, err := time.LoadLocation(rangeValue.Timezone)
		if err != nil {
			return problem("invalid_time", "time_range.timezone", "%q is not a valid IANA timezone", rangeValue.Timezone)
		}
		if end.After(start.In(location).AddDate(0, 0, MaximumTimeRangeDays)) {
			return problem("time_range_exceeded", "time_range", "requested range spans approximately %.1f days; maximum is %d calendar days; split the request into smaller windows", end.Sub(start).Hours()/24, MaximumTimeRangeDays)
		}
	} else if end.Sub(start) > MaximumTimeRangeDays*24*time.Hour {
		return problem("time_range_exceeded", "time_range", "requested range spans %.1f days; maximum is %d days; provide a timezone or split the request into smaller windows", end.Sub(start).Hours()/24, MaximumTimeRangeDays)
	}
	for _, metricName := range query.Metrics {
		if value := index.metrics[metricName].TimeDimension; value != "" && value != rangeValue.Dimension {
			return problem("invalid_time", "time_range.dimension", "metric %q uses time dimension %q", metricName, value)
		}
	}
	rangeValue.Start = start.UTC().Format(time.RFC3339Nano)
	rangeValue.End = end.UTC().Format(time.RFC3339Nano)
	query.TimeRange = &rangeValue
	if query.TimeGrouping != nil && query.TimeGrouping.Dimension != rangeValue.Dimension {
		return problem("invalid_time", "time_grouping.dimension", "must match time_range.dimension")
	}
	if query.TimeGrouping != nil && rangeValue.Timezone != "" && query.TimeGrouping.Timezone != rangeValue.Timezone {
		return problem("invalid_time", "time_grouping.timezone", "must match time_range.timezone")
	}
	return validateTimeGrouping(index, query)
}

func validateTimeGrouping(index manifestIndex, query *model.SemanticQuery) error {
	groupedTimeDimensions := make([]string, 0, 1)
	for _, name := range query.GroupBy {
		if dimension, exists := index.dimensions[name]; exists && dimension.Type == model.DimensionTime {
			groupedTimeDimensions = append(groupedTimeDimensions, name)
		}
	}
	if len(groupedTimeDimensions) > 1 {
		return problem("invalid_time", "group_by", "v0.1 supports one grouped time dimension")
	}
	if len(groupedTimeDimensions) == 0 {
		if query.TimeGrouping != nil {
			return problem("invalid_time", "time_grouping", "time grouping requires its dimension in group_by")
		}
		return nil
	}
	if query.TimeGrouping == nil {
		return problem("invalid_time", "time_grouping", "group_by contains time dimension %q; provide time_grouping with the same dimension, an IANA timezone, and day, week, or month granularity", groupedTimeDimensions[0])
	}
	grouping := *query.TimeGrouping
	if grouping.Dimension != groupedTimeDimensions[0] {
		return problem("invalid_time", "time_grouping.dimension", "must match the grouped time dimension")
	}
	dimension := index.dimensions[grouping.Dimension]
	if !slices.Contains(dimension.TimeGranularities, grouping.Granularity) {
		return problem("invalid_time", "time_grouping.granularity", "dimension %q does not support %q", dimension.Name, grouping.Granularity)
	}
	if grouping.Granularity == model.GrainWeek {
		if grouping.WeekStart != model.WeekStartMonday && grouping.WeekStart != model.WeekStartSunday {
			return problem("invalid_time", "time_grouping.week_start", "weekly grouping requires monday or sunday")
		}
	} else if grouping.WeekStart != "" {
		return problem("invalid_time", "time_grouping.week_start", "week_start is only valid for weekly grouping")
	}
	if grouping.Timezone == "" {
		return problem("invalid_time", "time_grouping.timezone", "IANA business timezone is required")
	}
	if _, err := time.LoadLocation(grouping.Timezone); err != nil {
		return problem("invalid_time", "time_grouping.timezone", "%q is not a valid IANA timezone", grouping.Timezone)
	}
	query.TimeGrouping = &grouping
	return nil
}

func validateOrder(query model.SemanticQuery) error {
	available := make(map[string]struct{})
	for _, value := range query.Metrics {
		available[value] = struct{}{}
	}
	for _, value := range query.GroupBy {
		available[value] = struct{}{}
	}
	seen := make(map[string]struct{})
	for i, order := range query.OrderBy {
		path := fmt.Sprintf("order_by[%d]", i)
		if _, exists := available[order.Field]; !exists {
			return problem("unknown_reference", path+".field", "%q is not an output metric or dimension", order.Field)
		}
		if _, exists := seen[order.Field]; exists {
			return problem("duplicate_value", path+".field", "%q is ordered more than once", order.Field)
		}
		seen[order.Field] = struct{}{}
		if order.Direction != model.SortAscending && order.Direction != model.SortDescending {
			return problem("invalid_order", path+".direction", "must be asc or desc")
		}
	}
	return nil
}

func planMetrics(index manifestIndex, outputs []string, allowUnverified bool) ([]model.PlannedMetric, []string, error) {
	outputSet := make(map[string]bool, len(outputs))
	for _, name := range outputs {
		outputSet[name] = true
	}
	seen := make(map[string]bool)
	result := make([]model.PlannedMetric, 0)
	var add func(string) error
	add = func(name string) error {
		if seen[name] {
			return nil
		}
		metric, exists := index.metrics[name]
		if !exists {
			return problem("unknown_reference", "metrics", "metric %q does not exist", name)
		}
		if !allowUnverified && metric.Verification.Status != model.VerificationVerified {
			return problem("metric_not_verified", "metrics", "dependency metric %q is not verified", name)
		}
		if metric.Deprecated {
			return problem("metric_deprecated", "metrics", "dependency metric %q is deprecated", name)
		}
		dependencies := metricDependencies(metric.Expression)
		for _, dependency := range dependencies {
			if err := add(dependency); err != nil {
				return err
			}
		}
		seen[name] = true
		result = append(result, model.PlannedMetric{
			Name: name, Output: outputSet[name], Kind: metric.Kind, ValueType: metric.ValueType,
			Unit: metric.Unit, Expression: metric.Expression, Dependencies: dependencies,
		})
		return nil
	}
	for _, output := range outputs {
		if err := add(output); err != nil {
			return nil, nil, err
		}
	}
	lineage := make([]string, 0, len(seen))
	for name := range seen {
		lineage = append(lineage, name)
	}
	slices.Sort(lineage)
	return result, lineage, nil
}

func planDimensions(index manifestIndex, query model.SemanticQuery) ([]model.PlannedDimension, []string) {
	outputs := make(map[string]bool, len(query.GroupBy))
	names := append([]string(nil), query.GroupBy...)
	for _, name := range query.GroupBy {
		outputs[name] = true
	}
	auxiliary := make([]string, 0)
	for _, name := range requestedDimensions(query) {
		if !outputs[name] {
			auxiliary = append(auxiliary, name)
		}
	}
	slices.Sort(auxiliary)
	names = append(names, auxiliary...)
	result := make([]model.PlannedDimension, 0, len(names))
	entities := make(map[string]struct{})
	for _, name := range names {
		dimension := index.dimensions[name]
		entity := index.entities[dimension.Entity]
		field, _ := datasetField(index.datasets[entity.Dataset], dimension.Field)
		result = append(result, model.PlannedDimension{
			Name: name, Output: outputs[name], Entity: dimension.Entity, Field: dimension.Field,
			Type: dimension.Type, DataType: field.DataType,
		})
		entities[dimension.Entity] = struct{}{}
	}
	targets := make([]string, 0, len(entities))
	for entity := range entities {
		targets = append(targets, entity)
	}
	slices.Sort(targets)
	return result, targets
}

func planJoins(root string, targets []string, relationships []model.Relationship) ([]model.PlannedJoin, error) {
	seen := make(map[string]bool)
	result := make([]model.PlannedJoin, 0)
	for _, target := range targets {
		path, err := findJoinPath(root, target, relationships)
		if err != nil {
			return nil, err
		}
		for _, relationship := range path {
			if seen[relationship.Name] {
				continue
			}
			seen[relationship.Name] = true
			result = append(result, model.PlannedJoin{
				Name: relationship.Name, FromEntity: relationship.FromEntity, ToEntity: relationship.ToEntity,
				Cardinality: relationship.Cardinality, FromField: relationship.FromField, ToField: relationship.ToField,
			})
		}
	}
	return result, nil
}

func findJoinPath(root, target string, relationships []model.Relationship) ([]model.Relationship, error) {
	if root == target {
		return nil, nil
	}
	type candidate struct {
		entity string
		path   []model.Relationship
	}
	queue := []candidate{{entity: root}}
	bestDepth := map[string]int{root: 0}
	var matches [][]model.Relationship
	shortest := -1
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if shortest >= 0 && len(current.path) >= shortest {
			continue
		}
		next := make([]model.Relationship, 0)
		for _, relationship := range relationships {
			if relationship.FromEntity == current.entity {
				next = append(next, relationship)
			}
		}
		sort.Slice(next, func(i, j int) bool { return next[i].Name < next[j].Name })
		for _, relationship := range next {
			path := append(append([]model.Relationship(nil), current.path...), relationship)
			if relationship.ToEntity == target {
				shortest = len(path)
				matches = append(matches, path)
				continue
			}
			if depth, exists := bestDepth[relationship.ToEntity]; !exists || len(path) <= depth {
				bestDepth[relationship.ToEntity] = len(path)
				queue = append(queue, candidate{relationship.ToEntity, path})
			}
		}
	}
	if len(matches) == 0 {
		return nil, problem("unsafe_join", "dimensions", "entity %q is not reachable from %q", target, root)
	}
	if len(matches) > 1 {
		return nil, problem("ambiguous_join", "dimensions", "more than one shortest join path reaches entity %q", target)
	}
	return matches[0], nil
}

func buildLineage(index manifestIndex, root string, metrics []string, dimensions []model.PlannedDimension, joins []model.PlannedJoin) model.Lineage {
	datasets := map[string]struct{}{index.entities[root].Dataset: {}}
	dimensionNames := make([]string, 0, len(dimensions))
	for _, dimension := range dimensions {
		dimensionNames = append(dimensionNames, dimension.Name)
		datasets[index.entities[dimension.Entity].Dataset] = struct{}{}
	}
	relationshipNames := make([]string, 0, len(joins))
	for _, join := range joins {
		relationshipNames = append(relationshipNames, join.Name)
		datasets[index.entities[join.FromEntity].Dataset] = struct{}{}
		datasets[index.entities[join.ToEntity].Dataset] = struct{}{}
	}
	datasetNames := make([]string, 0, len(datasets))
	for name := range datasets {
		datasetNames = append(datasetNames, name)
	}
	slices.Sort(datasetNames)
	slices.Sort(metrics)
	slices.Sort(dimensionNames)
	slices.Sort(relationshipNames)
	return model.Lineage{Datasets: datasetNames, Metrics: metrics, Dimensions: dimensionNames, Relationships: relationshipNames}
}

func requestedDimensions(query model.SemanticQuery) []string {
	set := make(map[string]struct{})
	for _, value := range query.GroupBy {
		set[value] = struct{}{}
	}
	for _, value := range query.Filters {
		set[value.Dimension] = struct{}{}
	}
	if query.TimeRange != nil {
		set[query.TimeRange.Dimension] = struct{}{}
	}
	if query.TimeGrouping != nil {
		set[query.TimeGrouping.Dimension] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for name := range set {
		result = append(result, name)
	}
	slices.Sort(result)
	return result
}

func metricDependencies(expression model.Expression) []string {
	set := make(map[string]struct{})
	var walk func(model.Expression)
	walk = func(value model.Expression) {
		if value.Op == model.OpMetric {
			set[value.Metric] = struct{}{}
		}
		for _, argument := range value.Args {
			walk(argument)
		}
	}
	walk(expression)
	if len(set) == 0 {
		return nil
	}
	result := make([]string, 0, len(set))
	for name := range set {
		result = append(result, name)
	}
	slices.Sort(result)
	return result
}

func expressionOperations(expression model.Expression) []model.ExpressionOp {
	set := make(map[model.ExpressionOp]struct{})
	var walk func(model.Expression)
	walk = func(value model.Expression) {
		set[value.Op] = struct{}{}
		for _, argument := range value.Args {
			walk(argument)
		}
	}
	walk(expression)
	result := make([]model.ExpressionOp, 0, len(set))
	for operation := range set {
		result = append(result, operation)
	}
	slices.Sort(result)
	return result
}

func expressionFilterOperators(expression model.Expression) []model.MetricFilterOperator {
	set := make(map[model.MetricFilterOperator]struct{})
	var walk func(model.Expression)
	walk = func(value model.Expression) {
		for _, filter := range value.Filters {
			set[filter.Operator] = struct{}{}
		}
		for _, argument := range value.Args {
			walk(argument)
		}
	}
	walk(expression)
	result := make([]model.MetricFilterOperator, 0, len(set))
	for operator := range set {
		result = append(result, operator)
	}
	slices.Sort(result)
	return result
}

func normalizeAndValidateBinding(binding model.SourceBinding, manifest model.SemanticManifest, required []string) (model.SourceBinding, map[string]model.DatasetBinding, error) {
	if !regexp.MustCompile(`^[a-z][a-z0-9_]*$`).MatchString(binding.Metadata.Name) || binding.Metadata.Version == "" {
		return model.SourceBinding{}, nil, problem("invalid_binding", "binding.metadata", "name and version are required")
	}
	result := binding
	result.Datasets = append([]model.DatasetBinding(nil), binding.Datasets...)
	index := make(map[string]model.DatasetBinding)
	manifestIndex := indexManifest(manifest)
	for i := range result.Datasets {
		dataset := &result.Datasets[i]
		path := fmt.Sprintf("binding.datasets[%d]", i)
		if _, exists := manifestIndex.datasets[dataset.Name]; !exists {
			return model.SourceBinding{}, nil, problem("unknown_reference", path+".name", "dataset %q does not exist", dataset.Name)
		}
		if _, exists := index[dataset.Name]; exists {
			return model.SourceBinding{}, nil, problem("duplicate_name", path+".name", "dataset %q is duplicated", dataset.Name)
		}
		if err := validateResource(dataset.Resource, path+".resource"); err != nil {
			return model.SourceBinding{}, nil, err
		}
		dataset.Fields = append([]model.FieldBinding(nil), dataset.Fields...)
		sort.Slice(dataset.Fields, func(a, b int) bool { return dataset.Fields[a].Name < dataset.Fields[b].Name })
		fieldSeen := make(map[string]struct{})
		for fieldIndex, field := range dataset.Fields {
			fieldPath := fmt.Sprintf("%s.fields[%d]", path, fieldIndex)
			if _, exists := fieldSeen[field.Name]; exists {
				return model.SourceBinding{}, nil, problem("duplicate_name", fieldPath+".name", "field %q is duplicated", field.Name)
			}
			fieldSeen[field.Name] = struct{}{}
			if !physicalNamePattern.MatchString(field.Column) {
				return model.SourceBinding{}, nil, problem("invalid_binding", fieldPath+".column", "%q is not a physical identifier", field.Column)
			}
			semanticDataset := manifestIndex.datasets[dataset.Name]
			found := false
			for _, semanticField := range semanticDataset.Fields {
				if semanticField.Name == field.Name {
					found = true
					break
				}
			}
			if !found {
				return model.SourceBinding{}, nil, problem("unknown_reference", fieldPath+".name", "field %q does not exist", field.Name)
			}
			if field.CalendarTimezone != "" {
				semanticField, _ := datasetField(semanticDataset, field.Name)
				if semanticField.DataType != model.DataTypeDate {
					return model.SourceBinding{}, nil, problem("invalid_binding", fieldPath+".calendar_timezone", "is only valid for date fields")
				}
				if _, err := time.LoadLocation(field.CalendarTimezone); err != nil {
					return model.SourceBinding{}, nil, problem("invalid_binding", fieldPath+".calendar_timezone", "%q is not a valid IANA timezone", field.CalendarTimezone)
				}
			}
		}
		index[dataset.Name] = *dataset
	}
	sort.Slice(result.Datasets, func(i, j int) bool { return result.Datasets[i].Name < result.Datasets[j].Name })
	for _, dataset := range required {
		if _, exists := index[dataset]; !exists {
			return model.SourceBinding{}, nil, problem("binding_missing", "binding.datasets", "dataset %q is not bound", dataset)
		}
	}
	return result, index, nil
}

func bindMetricFields(metrics []model.PlannedMetric, index manifestIndex, bindings map[string]model.DatasetBinding) ([]model.PhysicalField, error) {
	logicalFields := make(map[string]struct{})
	var collect func(model.Expression)
	collect = func(expression model.Expression) {
		if expression.Field != "" {
			logicalFields[expression.Field] = struct{}{}
		}
		for _, filter := range expression.Filters {
			logicalFields[filter.Field] = struct{}{}
		}
		for _, argument := range expression.Args {
			collect(argument)
		}
	}
	for _, metric := range metrics {
		collect(metric.Expression)
	}
	names := make([]string, 0, len(logicalFields))
	for name := range logicalFields {
		names = append(names, name)
	}
	slices.Sort(names)
	result := make([]model.PhysicalField, 0, len(names))
	for _, name := range names {
		entityName, fieldName, ok := strings.Cut(name, ".")
		if !ok {
			return nil, problem("invalid_expression", "expression.field", "field is not qualified")
		}
		entity, exists := index.entities[entityName]
		if !exists {
			return nil, problem("unknown_reference", "expression.field", "entity %q does not exist", entityName)
		}
		binding, exists := bindings[entity.Dataset]
		if !exists {
			return nil, problem("binding_missing", "expression.field", "dataset %q is not bound", entity.Dataset)
		}
		column, exists := boundColumn(binding, fieldName)
		if !exists {
			return nil, problem("binding_missing", "expression.field", "field %s.%s is not bound", entity.Dataset, fieldName)
		}
		semanticField, exists := datasetField(index.datasets[entity.Dataset], fieldName)
		if !exists {
			return nil, problem("unknown_reference", "expression.field", "field %s.%s does not exist", entity.Dataset, fieldName)
		}
		result = append(result, model.PhysicalField{
			Entity: entityName, Field: fieldName, Resource: binding.Resource, Column: column, DataType: semanticField.DataType,
		})
	}
	return result, nil
}

func validateResource(resource model.ResourceRef, path string) error {
	switch resource.Kind {
	case model.ResourceTable:
		if resource.Table == "" || resource.URI != "" {
			return problem("invalid_binding", path, "table resource requires table and forbids uri")
		}
		for name, value := range map[string]string{"catalog": resource.Catalog, "schema": resource.Schema, "table": resource.Table} {
			if value != "" && !physicalNamePattern.MatchString(value) {
				return problem("invalid_binding", path+"."+name, "%q is not a physical identifier", value)
			}
		}
	case model.ResourceFile:
		if resource.URI == "" || resource.Table != "" || resource.Catalog != "" || resource.Schema != "" {
			return problem("invalid_binding", path, "file resource requires only uri")
		}
		if strings.ContainsAny(resource.URI, "\r\n") {
			return problem("invalid_binding", path+".uri", "uri contains control characters")
		}
		parsed, err := url.Parse(resource.URI)
		if err != nil || parsed.Scheme == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return problem("invalid_binding", path+".uri", "uri must be absolute and cannot contain credentials, query, or fragment")
		}
	default:
		return problem("invalid_binding", path+".kind", "must be table or file")
	}
	return nil
}

func boundColumn(binding model.DatasetBinding, name string) (string, bool) {
	field, ok := boundField(binding, name)
	return field.Column, ok
}

func boundField(binding model.DatasetBinding, name string) (model.FieldBinding, bool) {
	for _, field := range binding.Fields {
		if field.Name == name {
			return field, true
		}
	}
	return model.FieldBinding{}, false
}

func validatePhysicalTimeSemantics(timeRange *model.TimeRange, dimensions []model.PhysicalDimension) error {
	if timeRange == nil {
		return nil
	}
	var dimension *model.PhysicalDimension
	for index := range dimensions {
		if dimensions[index].Name == timeRange.Dimension {
			dimension = &dimensions[index]
			break
		}
	}
	if dimension == nil || dimension.DataType != model.DataTypeDate {
		return nil
	}
	if dimension.CalendarTimezone == "" {
		return problem("invalid_binding", "binding.datasets.fields.calendar_timezone", "date-backed time dimension %q must declare its physical calendar timezone", dimension.Name)
	}
	if timeRange.Timezone == "" {
		return problem("invalid_time", "time_range.timezone", "date-backed time dimension %q requires timezone %q", dimension.Name, dimension.CalendarTimezone)
	}
	if timeRange.Timezone != dimension.CalendarTimezone {
		return problem("unsupported_timezone", "time_range.timezone", "date-backed time dimension %q uses calendar timezone %q and cannot represent requested timezone %q; use %q or bind a timestamp source", dimension.Name, dimension.CalendarTimezone, timeRange.Timezone, dimension.CalendarTimezone)
	}
	location, _ := time.LoadLocation(dimension.CalendarTimezone)
	for _, boundary := range []struct{ path, raw string }{
		{path: "time_range.start", raw: timeRange.Start},
		{path: "time_range.end", raw: timeRange.End},
	} {
		instant, err := time.Parse(time.RFC3339Nano, boundary.raw)
		if err != nil {
			return problem("invalid_time", boundary.path, "must be an RFC3339 timestamp with offset")
		}
		local := instant.In(location)
		if local.Hour() != 0 || local.Minute() != 0 || local.Second() != 0 || local.Nanosecond() != 0 {
			return problem("invalid_time", boundary.path, "date-backed time dimension %q requires midnight boundaries in timezone %q", dimension.Name, dimension.CalendarTimezone)
		}
	}
	return nil
}

func datasetField(dataset model.Dataset, name string) (model.Field, bool) {
	for _, field := range dataset.Fields {
		if field.Name == name {
			return field, true
		}
	}
	return model.Field{}, false
}

func fingerprintWithoutField(value any) (string, error) {
	data, err := canonical.Marshal(value)
	if err != nil {
		return "", err
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", err
	}
	delete(payload, "fingerprint")
	return canonical.Fingerprint(payload)
}

func rejectDuplicates[T comparable](values []T, path string) error {
	seen := make(map[T]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return problem("duplicate_value", path, "contains duplicate value %v", value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func problem(code, path, format string, arguments ...any) error {
	return &model.Problem{Code: code, Path: path, Message: fmt.Sprintf(format, arguments...)}
}
