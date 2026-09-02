// Package policy evaluates trusted request context against immutable rules.
package policy

import (
	"fmt"
	"slices"
	"strings"

	"github.com/marmot1024/metricspire/internal/model"
)

type Decision struct {
	Allowed      bool
	MatchedRules []string
}

func Authorize(context model.RequestContext, bundle model.PolicyBundle, manifestFingerprint string, query model.SemanticQuery) (Decision, error) {
	if strings.TrimSpace(context.Tenant) == "" || strings.TrimSpace(context.Principal) == "" || strings.TrimSpace(context.RequestID) == "" {
		return Decision{}, denied("trusted request context is incomplete")
	}
	if context.Tenant != bundle.Tenant {
		return Decision{}, denied("tenant does not match policy bundle")
	}
	if manifestFingerprint == "" || manifestFingerprint != bundle.ManifestFingerprint {
		return Decision{}, denied("active manifest does not match policy bundle")
	}

	dimensions := requestedDimensions(query)
	allowedMetrics := make(map[string]bool)
	allowedDimensions := make(map[string]bool)
	matched := make([]string, 0)

	for _, rule := range bundle.Rules {
		if !subjectMatches(context, rule) {
			continue
		}
		matched = append(matched, rule.Name)
		if rule.Effect == model.EffectDeny && (intersects(rule.Metrics, query.Metrics) || intersects(rule.Dimensions, dimensions)) {
			slices.Sort(matched)
			return Decision{MatchedRules: matched}, denied("explicit deny rule %q matched", rule.Name)
		}
		if rule.Effect == model.EffectAllow {
			grant(rule.Metrics, query.Metrics, allowedMetrics)
			grant(rule.Dimensions, dimensions, allowedDimensions)
		}
	}

	slices.Sort(matched)
	for _, metric := range query.Metrics {
		if !allowedMetrics[metric] {
			return Decision{MatchedRules: matched}, denied("metric %q is not allowed", metric)
		}
	}
	for _, dimension := range dimensions {
		if !allowedDimensions[dimension] {
			return Decision{MatchedRules: matched}, denied("dimension %q is not allowed", dimension)
		}
	}
	return Decision{Allowed: true, MatchedRules: matched}, nil
}

func requestedDimensions(query model.SemanticQuery) []string {
	set := make(map[string]struct{})
	for _, dimension := range query.GroupBy {
		set[dimension] = struct{}{}
	}
	for _, filter := range query.Filters {
		set[filter.Dimension] = struct{}{}
	}
	if query.TimeRange != nil {
		set[query.TimeRange.Dimension] = struct{}{}
	}
	if query.TimeGrouping != nil {
		set[query.TimeGrouping.Dimension] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for dimension := range set {
		result = append(result, dimension)
	}
	slices.Sort(result)
	return result
}

func subjectMatches(context model.RequestContext, rule model.PolicyRule) bool {
	if matches(rule.Principals, context.Principal) {
		return true
	}
	for _, role := range context.Roles {
		if matches(rule.Roles, role) {
			return true
		}
	}
	return false
}

func intersects(patterns, requested []string) bool {
	for _, value := range requested {
		if matches(patterns, value) {
			return true
		}
	}
	return false
}

func grant(patterns, requested []string, target map[string]bool) {
	for _, value := range requested {
		if matches(patterns, value) {
			target[value] = true
		}
	}
}

func matches(patterns []string, value string) bool {
	for _, pattern := range patterns {
		if pattern == "*" || pattern == value {
			return true
		}
	}
	return false
}

func denied(format string, arguments ...any) error {
	return &model.Problem{Code: "permission_denied", Message: fmt.Sprintf(format, arguments...)}
}
