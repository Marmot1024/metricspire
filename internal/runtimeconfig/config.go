// Package runtimeconfig defines the non-secret deployment configuration.
package runtimeconfig

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/contractio"
)

const (
	APIVersion = "metricspire.io/v1alpha1"
	Kind       = "RuntimeConfig"

	AuthenticationOIDC           = "oidc"
	AuthenticationDatabricksApps = "databricks_apps"
)

type Config struct {
	APIVersion     string               `json:"api_version" yaml:"api_version"`
	Kind           string               `json:"kind" yaml:"kind"`
	HTTP           HTTPConfig           `json:"http" yaml:"http"`
	Authentication AuthenticationConfig `json:"authentication" yaml:"authentication"`
	ReleasePolicy  ReleasePolicyConfig  `json:"release_policy,omitempty" yaml:"release_policy,omitempty"`
	Policies       []PolicyRoute        `json:"policies" yaml:"policies"`
	Bindings       []BindingRoute       `json:"bindings" yaml:"bindings"`
}

type ReleasePolicyConfig struct {
	TrialNamespaces []string `json:"trial_namespaces,omitempty" yaml:"trial_namespaces,omitempty"`
}

type AuthenticationConfig struct {
	Provider       string                `json:"provider" yaml:"provider"`
	OIDC           *OIDCConfig           `json:"oidc,omitempty" yaml:"oidc,omitempty"`
	DatabricksApps *DatabricksAppsConfig `json:"databricks_apps,omitempty" yaml:"databricks_apps,omitempty"`
}

type HTTPConfig struct {
	Address        string `json:"address" yaml:"address"`
	PublicURL      string `json:"public_url" yaml:"public_url"`
	MaxBodyBytes   int64  `json:"max_body_bytes,omitempty" yaml:"max_body_bytes,omitempty"`
	ControlTimeout string `json:"control_timeout,omitempty" yaml:"control_timeout,omitempty"`
	QueryTimeout   string `json:"query_timeout,omitempty" yaml:"query_timeout,omitempty"`
}

type OIDCConfig struct {
	IssuerURL                    string `json:"issuer_url" yaml:"issuer_url"`
	ClientID                     string `json:"client_id" yaml:"client_id"`
	BearerAudience               string `json:"bearer_audience" yaml:"bearer_audience"`
	SessionTTL                   string `json:"session_ttl,omitempty" yaml:"session_ttl,omitempty"`
	TenantClaim                  string `json:"tenant_claim,omitempty" yaml:"tenant_claim,omitempty"`
	RolesClaim                   string `json:"roles_claim,omitempty" yaml:"roles_claim,omitempty"`
	PermissionsClaim             string `json:"permissions_claim,omitempty" yaml:"permissions_claim,omitempty"`
	DevelopmentAllowInsecureHTTP bool   `json:"development_allow_insecure_http,omitempty" yaml:"development_allow_insecure_http,omitempty"`
}

type DatabricksAppsConfig struct {
	ExpectedAppName     string `json:"expected_app_name" yaml:"expected_app_name"`
	ExpectedWorkspaceID string `json:"expected_workspace_id" yaml:"expected_workspace_id"`
	Tenant              string `json:"tenant" yaml:"tenant"`
	QueryRole           string `json:"query_role" yaml:"query_role"`
	PublisherGroupID    string `json:"publisher_group_id" yaml:"publisher_group_id"`
}

type PolicyRoute struct {
	Namespace string `json:"namespace" yaml:"namespace"`
	ModelName string `json:"model_name" yaml:"model_name"`
	Tenant    string `json:"tenant" yaml:"tenant"`
	Path      string `json:"path" yaml:"path"`
}

type BindingRoute struct {
	Namespace string `json:"namespace" yaml:"namespace"`
	ModelName string `json:"model_name" yaml:"model_name"`
	Path      string `json:"path" yaml:"path"`
}

func Load(path string) (Config, error) {
	var config Config
	if err := contractio.ReadFile(path, &config); err != nil {
		return Config{}, err
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (config Config) Validate() error {
	if config.APIVersion != APIVersion || config.Kind != Kind {
		return errors.New("runtime config has an invalid api_version or kind")
	}
	if strings.TrimSpace(config.HTTP.Address) == "" {
		return errors.New("http.address is required")
	}
	publicURL, err := url.Parse(strings.TrimSpace(config.HTTP.PublicURL))
	if err != nil || publicURL.Host == "" || publicURL.User != nil || publicURL.RawQuery != "" || publicURL.Fragment != "" || publicURL.Path != "" {
		return errors.New("http.public_url must be an absolute origin without path, query, or fragment")
	}
	allowHTTP := config.Authentication.OIDC != nil && config.Authentication.OIDC.DevelopmentAllowInsecureHTTP
	if publicURL.Scheme != "https" {
		if !allowHTTP || publicURL.Scheme != "http" || !isLoopbackHost(publicURL.Hostname()) {
			return errors.New("http.public_url must use HTTPS; HTTP is allowed only for explicit loopback development")
		}
	}
	if err := config.validateAuthentication(); err != nil {
		return err
	}
	if config.HTTP.MaxBodyBytes < 0 {
		return errors.New("http.max_body_bytes cannot be negative")
	}
	if _, err := ParseDuration(config.HTTP.ControlTimeout, 15*time.Second); err != nil {
		return fmt.Errorf("http.control_timeout: %w", err)
	}
	if _, err := ParseDuration(config.HTTP.QueryTimeout, 2*time.Minute); err != nil {
		return fmt.Errorf("http.query_timeout: %w", err)
	}
	if len(config.Policies) == 0 || len(config.Bindings) == 0 {
		return errors.New("at least one policy and binding route are required")
	}
	seenPolicies := make(map[string]struct{})
	for _, route := range config.Policies {
		if empty(route.Namespace, route.ModelName, route.Tenant, route.Path) {
			return errors.New("policy route namespace, model_name, tenant, and path are required")
		}
		key := route.Namespace + "\x00" + route.ModelName + "\x00" + route.Tenant
		if _, exists := seenPolicies[key]; exists {
			return fmt.Errorf("duplicate policy route for %s/%s tenant %s", route.Namespace, route.ModelName, route.Tenant)
		}
		seenPolicies[key] = struct{}{}
	}
	seenBindings := make(map[string]struct{})
	knownNamespaces := make(map[string]struct{})
	for _, route := range config.Bindings {
		if empty(route.Namespace, route.ModelName, route.Path) {
			return errors.New("binding route namespace, model_name, and path are required")
		}
		key := route.Namespace + "\x00" + route.ModelName
		if _, exists := seenBindings[key]; exists {
			return fmt.Errorf("duplicate binding route for %s/%s", route.Namespace, route.ModelName)
		}
		seenBindings[key] = struct{}{}
		knownNamespaces[route.Namespace] = struct{}{}
	}
	seenTrialNamespaces := make(map[string]struct{}, len(config.ReleasePolicy.TrialNamespaces))
	for _, namespace := range config.ReleasePolicy.TrialNamespaces {
		namespace = strings.TrimSpace(namespace)
		if namespace == "" {
			return errors.New("release_policy.trial_namespaces cannot contain an empty namespace")
		}
		if _, exists := seenTrialNamespaces[namespace]; exists {
			return fmt.Errorf("duplicate trial release namespace %s", namespace)
		}
		if _, exists := knownNamespaces[namespace]; !exists {
			return fmt.Errorf("trial release namespace %s has no binding route", namespace)
		}
		seenTrialNamespaces[namespace] = struct{}{}
	}
	return nil
}

func (config Config) validateAuthentication() error {
	switch strings.TrimSpace(config.Authentication.Provider) {
	case AuthenticationOIDC:
		if config.Authentication.OIDC == nil || config.Authentication.DatabricksApps != nil {
			return errors.New("authentication.provider oidc requires only authentication.oidc")
		}
		return validateOIDC(*config.Authentication.OIDC)
	case AuthenticationDatabricksApps:
		if config.Authentication.DatabricksApps == nil || config.Authentication.OIDC != nil {
			return errors.New("authentication.provider databricks_apps requires only authentication.databricks_apps")
		}
		return validateDatabricksApps(*config.Authentication.DatabricksApps)
	default:
		return errors.New("authentication.provider must be oidc or databricks_apps")
	}
}

func validateOIDC(config OIDCConfig) error {
	issuerURL, err := url.Parse(strings.TrimSpace(config.IssuerURL))
	if err != nil || issuerURL.Host == "" || issuerURL.User != nil || issuerURL.RawQuery != "" || issuerURL.Fragment != "" {
		return errors.New("oidc.issuer_url is invalid")
	}
	if issuerURL.Scheme != "https" {
		if !config.DevelopmentAllowInsecureHTTP || issuerURL.Scheme != "http" || !isLoopbackHost(issuerURL.Hostname()) {
			return errors.New("oidc.issuer_url must use HTTPS; HTTP is allowed only for loopback development")
		}
	}
	if strings.TrimSpace(config.ClientID) == "" {
		return errors.New("oidc.client_id is required")
	}
	if strings.TrimSpace(config.BearerAudience) == "" {
		return errors.New("oidc.bearer_audience is required and must identify the API resource")
	}
	sessionTTL, err := ParseDuration(config.SessionTTL, 8*time.Hour)
	if err != nil || sessionTTL < time.Minute || sessionTTL > 24*time.Hour {
		return errors.New("oidc.session_ttl must be between one minute and 24 hours")
	}
	return nil
}

func validateDatabricksApps(config DatabricksAppsConfig) error {
	if empty(config.ExpectedAppName, config.ExpectedWorkspaceID, config.Tenant, config.QueryRole, config.PublisherGroupID) {
		return errors.New("databricks_apps expected_app_name, expected_workspace_id, tenant, query_role, and publisher_group_id are required")
	}
	if config.QueryRole != strings.TrimSpace(config.QueryRole) || len(config.QueryRole) > 128 {
		return errors.New("databricks_apps query_role must be a bounded string without surrounding whitespace")
	}
	if config.PublisherGroupID != strings.TrimSpace(config.PublisherGroupID) || len(config.PublisherGroupID) > 256 {
		return errors.New("databricks_apps publisher_group_id must be a bounded string without surrounding whitespace")
	}
	return nil
}

func ParseDuration(value string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, errors.New("must be a positive Go duration")
	}
	return duration, nil
}

func isLoopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}

func empty(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}
	return false
}
