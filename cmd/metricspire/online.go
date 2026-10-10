package main

import (
	"fmt"
	"strings"

	"github.com/marmot1024/metricspire/internal/adapter/postgresquery"
	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/runtimeconfig"
)

// Reuse the existing policy contract/evaluator, without extending authentication
// or constructing a second user directory. Analytical policies are unchanged.
func loadOnlinePolicies(base string, config runtimeconfig.OnlineConfig) ([]application.PolicyConfiguration, error) {
	policies := make([]application.PolicyConfiguration, 0, len(config.Policies))
	for _, route := range config.Policies {
		var source model.PolicySource
		if err := contractio.ReadFile(resolveConfigPath(base, route.Path), &source); err != nil {
			return nil, err
		}
		for _, rule := range source.Rules {
			if rule.Effect != model.EffectAllow {
				continue
			}
			// Principals and roles match with OR, not AND. A broad role would
			// bypass an explicit reader list, so this small online gate forbids it.
			if len(rule.Roles) != 0 || !explicitOnlineValues(rule.Principals) || !explicitOnlineValues(rule.Metrics) || !explicitOnlineValues(rule.Dimensions) {
				return nil, fmt.Errorf("online policy %s/%s allow rules require explicit principals, metrics and dimensions without roles or wildcards", route.Namespace, route.ModelName)
			}
		}
		policies = append(policies, application.PolicyConfiguration{Namespace: route.Namespace, ModelName: route.ModelName, Tenant: route.Tenant, Policy: source})
	}
	if _, err := application.NewConfiguredPolicyResolver(policies); err != nil {
		return nil, err
	}
	return policies, nil
}

func explicitOnlineValues(values []string) bool {
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) || value == "*" {
			return false
		}
	}
	return true
}

// Online bindings do not replace the analytical bindings used by UI/MCP/jobs.
func loadOnlineBindings(base string, config runtimeconfig.OnlineConfig) ([]application.BindingConfiguration, error) {
	bindings := make([]application.BindingConfiguration, 0, len(config.Bindings))
	allowed := map[model.ResourceRef]bool{}
	for _, resource := range config.Resources {
		allowed[resource] = true
	}
	for _, route := range config.Bindings {
		var binding model.SourceBinding
		if err := contractio.ReadFile(resolveConfigPath(base, route.Path), &binding); err != nil {
			return nil, err
		}
		if binding.Engine != postgresquery.EngineName || len(binding.Datasets) != 1 || !allowed[binding.Datasets[0].Resource] {
			return nil, fmt.Errorf("online binding %s/%s requires one enabled PostgreSQL source", route.Namespace, route.ModelName)
		}
		bindings = append(bindings, application.BindingConfiguration{Namespace: route.Namespace, ModelName: route.ModelName, Binding: binding})
	}
	if _, err := application.NewConfiguredBindingResolver(bindings); err != nil {
		return nil, err
	}
	return bindings, nil
}
