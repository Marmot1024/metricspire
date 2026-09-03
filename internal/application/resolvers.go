package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/model"
)

var ErrResolutionNotFound = errors.New("trusted query configuration not found")

type PolicyConfiguration struct {
	Namespace string
	ModelName string
	Tenant    string
	Policy    model.PolicySource
}

type BindingConfiguration struct {
	Namespace string
	ModelName string
	Binding   model.SourceBinding
}

type ConfiguredPolicyResolver struct {
	policies map[string]model.PolicySource
}

func NewConfiguredPolicyResolver(configurations []PolicyConfiguration) (*ConfiguredPolicyResolver, error) {
	resolver := &ConfiguredPolicyResolver{policies: make(map[string]model.PolicySource, len(configurations))}
	for _, configuration := range configurations {
		if strings.TrimSpace(configuration.Namespace) == "" || strings.TrimSpace(configuration.ModelName) == "" || strings.TrimSpace(configuration.Tenant) == "" {
			return nil, errors.New("policy namespace, model name, and tenant are required")
		}
		if configuration.Policy.APIVersion != model.APIVersion || configuration.Policy.Kind != model.KindPolicySource || configuration.Policy.Tenant != configuration.Tenant {
			return nil, errors.New("policy configuration has an invalid contract or tenant")
		}
		key := policyConfigurationKey(configuration.Namespace, configuration.ModelName, configuration.Tenant)
		if _, exists := resolver.policies[key]; exists {
			return nil, fmt.Errorf("duplicate policy configuration for %s/%s tenant %s", configuration.Namespace, configuration.ModelName, configuration.Tenant)
		}
		resolver.policies[key] = cloneResolverValue(configuration.Policy)
	}
	return resolver, nil
}

func (resolver *ConfiguredPolicyResolver) ResolvePolicy(_ context.Context, scope QueryScope, _ catalog.Release) (model.PolicySource, error) {
	policySource, exists := resolver.policies[policyConfigurationKey(scope.Namespace, scope.ModelName, scope.Context.Tenant)]
	if !exists {
		return model.PolicySource{}, ErrResolutionNotFound
	}
	return cloneResolverValue(policySource), nil
}

type ConfiguredBindingResolver struct {
	bindings map[string]model.SourceBinding
}

func NewConfiguredBindingResolver(configurations []BindingConfiguration) (*ConfiguredBindingResolver, error) {
	resolver := &ConfiguredBindingResolver{bindings: make(map[string]model.SourceBinding, len(configurations))}
	for _, configuration := range configurations {
		if strings.TrimSpace(configuration.Namespace) == "" || strings.TrimSpace(configuration.ModelName) == "" {
			return nil, errors.New("binding namespace and model name are required")
		}
		if configuration.Binding.APIVersion != model.APIVersion || configuration.Binding.Kind != model.KindSourceBinding || strings.TrimSpace(configuration.Binding.Engine) == "" {
			return nil, errors.New("binding configuration has an invalid contract or engine")
		}
		key := bindingConfigurationKey(configuration.Namespace, configuration.ModelName)
		if _, exists := resolver.bindings[key]; exists {
			return nil, fmt.Errorf("duplicate binding configuration for %s/%s", configuration.Namespace, configuration.ModelName)
		}
		resolver.bindings[key] = cloneResolverValue(configuration.Binding)
	}
	return resolver, nil
}

func (resolver *ConfiguredBindingResolver) ResolveBinding(_ context.Context, scope QueryScope, _ catalog.Release) (model.SourceBinding, error) {
	binding, exists := resolver.bindings[bindingConfigurationKey(scope.Namespace, scope.ModelName)]
	if !exists {
		return model.SourceBinding{}, ErrResolutionNotFound
	}
	return cloneResolverValue(binding), nil
}

func policyConfigurationKey(namespace, modelName, tenant string) string {
	return namespace + "\x00" + modelName + "\x00" + tenant
}

func bindingConfigurationKey(namespace, modelName string) string {
	return namespace + "\x00" + modelName
}

func cloneResolverValue[T any](value T) T {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		panic(err)
	}
	return result
}
