// Package oidcauth adapts verified OpenID Connect bearer tokens to MetricSpire principals.
package oidcauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/marmot1024/metricspire/internal/httpapi"
)

const maximumBearerTokenBytes = 16 << 10

type Config struct {
	IssuerURL        string
	ClientID         string
	TenantClaim      string
	RolesClaim       string
	PermissionsClaim string
	HTTPClient       *http.Client
	AllowHTTP        bool
}

type claimsVerifier interface {
	Verify(context.Context, string) (map[string]json.RawMessage, error)
}

type Authenticator struct {
	verifier         claimsVerifier
	tenantClaim      string
	rolesClaim       string
	permissionsClaim string
}

func New(ctx context.Context, config Config) (*Authenticator, error) {
	_, verifier, err := discover(ctx, config)
	if err != nil {
		return nil, err
	}
	return newWithVerifier(config, remoteVerifier{verifier: verifier})
}

func discover(ctx context.Context, config Config) (*oidc.Provider, *oidc.IDTokenVerifier, error) {
	if ctx == nil {
		return nil, nil, errors.New("OIDC initialization context is required")
	}
	issuer, err := url.Parse(strings.TrimSpace(config.IssuerURL))
	if err != nil || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" {
		return nil, nil, errors.New("OIDC issuer URL is invalid")
	}
	if issuer.Scheme != "https" {
		if !config.AllowHTTP || issuer.Scheme != "http" || !isLoopbackHost(issuer.Hostname()) {
			return nil, nil, errors.New("OIDC issuer must use HTTPS; HTTP is allowed only for loopback development")
		}
	}
	if strings.TrimSpace(config.ClientID) == "" {
		return nil, nil, errors.New("OIDC client ID is required")
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, httpClient), issuer.String())
	if err != nil {
		return nil, nil, fmt.Errorf("discover OIDC provider: %w", err)
	}
	verifier := provider.Verifier(&oidc.Config{ClientID: config.ClientID})
	return provider, verifier, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func newWithVerifier(config Config, verifier claimsVerifier) (*Authenticator, error) {
	if verifier == nil {
		return nil, errors.New("OIDC token verifier is required")
	}
	tenantClaim := claimName(config.TenantClaim, "tenant")
	rolesClaim := claimName(config.RolesClaim, "roles")
	permissionsClaim := claimName(config.PermissionsClaim, "permissions")
	if tenantClaim == rolesClaim || tenantClaim == permissionsClaim || rolesClaim == permissionsClaim {
		return nil, errors.New("OIDC tenant, roles, and permissions claims must be distinct")
	}
	return &Authenticator{
		verifier: verifier, tenantClaim: tenantClaim, rolesClaim: rolesClaim, permissionsClaim: permissionsClaim,
	}, nil
}

func (authenticator *Authenticator) Authenticate(ctx context.Context, request *http.Request) (httpapi.Principal, error) {
	rawToken, err := bearerToken(request.Header.Get("Authorization"))
	if err != nil {
		return httpapi.Principal{}, httpapi.ErrUnauthenticated
	}
	claims, err := authenticator.verifier.Verify(ctx, rawToken)
	if err != nil {
		return httpapi.Principal{}, httpapi.ErrUnauthenticated
	}
	principal, err := authenticator.principalFromClaims(claims)
	if err != nil {
		return httpapi.Principal{}, httpapi.ErrUnauthenticated
	}
	return principal, nil
}

func (authenticator *Authenticator) principalFromClaims(claims map[string]json.RawMessage) (httpapi.Principal, error) {
	subject, err := requiredStringClaim(claims, "sub")
	if err != nil {
		return httpapi.Principal{}, err
	}
	tenant, err := requiredStringClaim(claims, authenticator.tenantClaim)
	if err != nil {
		return httpapi.Principal{}, err
	}
	roles, err := stringListClaim(claims, authenticator.rolesClaim)
	if err != nil {
		return httpapi.Principal{}, err
	}
	permissionClaims, err := stringListClaim(claims, authenticator.permissionsClaim)
	if err != nil {
		return httpapi.Principal{}, err
	}
	permissions := make([]httpapi.Permission, 0, len(permissionClaims))
	for _, permission := range permissionClaims {
		switch httpapi.Permission(permission) {
		case httpapi.PermissionManage, httpapi.PermissionQuery:
			permissions = append(permissions, httpapi.Permission(permission))
		}
	}
	return httpapi.Principal{
		Tenant: tenant, Subject: subject, Roles: deduplicate(roles), Permissions: deduplicate(permissions),
	}, nil
}

type remoteVerifier struct {
	verifier *oidc.IDTokenVerifier
}

func (verifier remoteVerifier) Verify(ctx context.Context, rawToken string) (map[string]json.RawMessage, error) {
	token, err := verifier.verifier.Verify(ctx, rawToken)
	if err != nil {
		return nil, err
	}
	claims := make(map[string]json.RawMessage)
	if err := token.Claims(&claims); err != nil {
		return nil, err
	}
	return claims, nil
}

func bearerToken(header string) (string, error) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) > maximumBearerTokenBytes {
		return "", errors.New("invalid bearer token")
	}
	return parts[1], nil
}

func requiredStringClaim(claims map[string]json.RawMessage, name string) (string, error) {
	var value string
	raw, exists := claims[name]
	if !exists || json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("OIDC claim %q must be a non-empty string", name)
	}
	return value, nil
}

func stringListClaim(claims map[string]json.RawMessage, name string) ([]string, error) {
	raw, exists := claims[name]
	if !exists {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		var single string
		if singleErr := json.Unmarshal(raw, &single); singleErr != nil {
			return nil, fmt.Errorf("OIDC claim %q must be a string or string array", name)
		}
		values = strings.Fields(single)
	}
	result := values[:0]
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result, nil
}

func claimName(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

func deduplicate[T ~string](values []T) []T {
	result := make([]T, 0, len(values))
	for _, value := range values {
		if !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	return result
}

var _ httpapi.Authenticator = (*Authenticator)(nil)
