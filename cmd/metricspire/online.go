package main

import (
	"fmt"

	"github.com/marmot1024/metricspire/internal/adapter/postgresquery"
	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/runtimeconfig"
)

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
