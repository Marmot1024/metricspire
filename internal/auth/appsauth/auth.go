// Package appsauth adapts Databricks Apps managed-ingress identity and user
// authorization to MetricSpire's provider-neutral HTTP boundary.
package appsauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/executionauth"
	"github.com/marmot1024/metricspire/internal/httpapi"
)

const (
	HeaderAccessToken       = "X-Forwarded-Access-Token" // #nosec G101 -- managed-ingress header name, not a credential value.
	HeaderEmail             = "X-Forwarded-Email"
	HeaderHost              = "X-Forwarded-Host"
	HeaderPreferredUsername = "X-Forwarded-Preferred-Username"
	HeaderUser              = "X-Forwarded-User"

	maximumIdentityHeaderBytes = 4 << 10
	maximumCurrentUserBytes    = 256 << 10
)

type Runtime struct {
	AppName     string
	WorkspaceID string
	Host        string
}

func RuntimeFromEnvironment(getenv func(string) string) (Runtime, error) {
	if getenv == nil {
		return Runtime{}, errors.New("environment reader is required")
	}
	runtime := Runtime{
		AppName:     strings.TrimSpace(getenv("DATABRICKS_APP_NAME")),
		WorkspaceID: strings.TrimSpace(getenv("DATABRICKS_WORKSPACE_ID")),
		Host:        strings.TrimSpace(getenv("DATABRICKS_HOST")),
	}
	if runtime.AppName == "" || runtime.WorkspaceID == "" || runtime.Host == "" {
		return Runtime{}, errors.New("Databricks Apps runtime identity is incomplete")
	}
	return runtime, nil
}

type Config struct {
	ExpectedAppName     string
	ExpectedWorkspaceID string
	ExpectedPublicURL   string
	Tenant              string
	QueryRole           string
	PublisherGroupID    string
	HTTPClient          *http.Client
}

type CurrentUser struct {
	Active   bool
	ID       string
	Username string
	Emails   []string
	Groups   []CurrentUserGroup
}

type CurrentUserGroup struct {
	ID      string
	Display string
}

type CurrentUserVerifier interface {
	CurrentUser(context.Context, string) (CurrentUser, error)
}

type Authenticator struct {
	tenant           string
	publicHost       string
	queryRole        string
	publisherGroupID string
	currentUsers     CurrentUserVerifier
}

type authenticationError struct{ reason string }

func (err authenticationError) Error() string                    { return "Databricks Apps authentication rejected" }
func (err authenticationError) Unwrap() error                    { return httpapi.ErrUnauthenticated }
func (err authenticationError) SafeAuthenticationReason() string { return err.reason }

func rejected(reason string) error { return authenticationError{reason: reason} }

// New verifies an explicit expected Apps identity against the runtime-provided
// app name, workspace ID, and workspace host. Merely setting forwarded headers
// outside that guarded runtime is insufficient to enable this adapter.
func New(config Config, runtime Runtime, verifier CurrentUserVerifier) (*Authenticator, error) {
	appName := strings.TrimSpace(config.ExpectedAppName)
	workspaceID := strings.TrimSpace(config.ExpectedWorkspaceID)
	tenant := strings.TrimSpace(config.Tenant)
	queryRole := strings.TrimSpace(config.QueryRole)
	publisherGroupID := strings.TrimSpace(config.PublisherGroupID)
	if appName == "" || workspaceID == "" || tenant == "" || queryRole == "" || publisherGroupID == "" {
		return nil, errors.New("expected app name, workspace ID, MetricSpire tenant, query role, and publisher group ID are required")
	}
	if queryRole != config.QueryRole || len(queryRole) > 128 {
		return nil, errors.New("Databricks Apps query role must be a bounded string without surrounding whitespace")
	}
	if publisherGroupID != config.PublisherGroupID || len(publisherGroupID) > 256 {
		return nil, errors.New("Databricks Apps publisher group ID must be a bounded string without surrounding whitespace")
	}
	if strings.TrimSpace(runtime.AppName) != appName || strings.TrimSpace(runtime.WorkspaceID) != workspaceID {
		return nil, errors.New("Databricks Apps runtime identity does not match the expected app and workspace")
	}
	workspaceHost, err := validateHTTPSOrigin(runtime.Host)
	if err != nil {
		return nil, fmt.Errorf("validate Databricks Apps workspace host: %w", err)
	}
	publicURL, err := url.Parse(strings.TrimSpace(config.ExpectedPublicURL))
	if err != nil || publicURL.Scheme != "https" || publicURL.Host == "" || publicURL.User != nil || publicURL.Path != "" || publicURL.RawQuery != "" || publicURL.Fragment != "" {
		return nil, errors.New("expected Databricks App public URL must be an HTTPS origin")
	}
	if verifier == nil {
		verifier = &currentUserClient{host: workspaceHost, http: configuredHTTPClient(config.HTTPClient)}
	}
	return &Authenticator{
		tenant: tenant, publicHost: strings.ToLower(publicURL.Host), queryRole: queryRole,
		publisherGroupID: publisherGroupID, currentUsers: verifier,
	}, nil
}

func (authenticator *Authenticator) Authenticate(ctx context.Context, request *http.Request) (httpapi.Principal, error) {
	if err := authenticator.validateIngress(request); err != nil {
		return httpapi.Principal{}, rejected("managed_ingress")
	}
	token, err := forwardedAccessToken(request)
	if err != nil {
		return httpapi.Principal{}, rejected("forwarded_token")
	}
	// Deliberately verify once per authenticated API request. This keeps user
	// status and publisher membership fail-closed without adding a token cache;
	// optimize only after deployed latency and availability are measured.
	current, err := authenticator.currentUsers.CurrentUser(ctx, token)
	if err != nil || !current.Active || strings.TrimSpace(current.ID) == "" || strings.TrimSpace(current.Username) == "" {
		return httpapi.Principal{}, rejected("current_user")
	}
	if err := validateForwardedIdentity(request, current); err != nil {
		return httpapi.Principal{}, rejected("forwarded_identity")
	}
	principal := httpapi.Principal{
		Tenant: authenticator.tenant, Subject: current.ID,
		Roles: []string{authenticator.queryRole}, Permissions: []httpapi.Permission{httpapi.PermissionQuery},
	}
	for _, group := range current.Groups {
		if group.ID == authenticator.publisherGroupID {
			principal.Permissions = append(principal.Permissions, httpapi.PermissionManage)
			break
		}
	}
	return principal, nil
}

// AddToContext places the forwarded user token only in the current query's
// execution context. NewServer calls this after Authenticate on the same
// request; explain, plan, catalog, management, and job-status routes never call
// it.
func (authenticator *Authenticator) AddToContext(ctx context.Context, request *http.Request, principal httpapi.Principal) (context.Context, error) {
	if strings.TrimSpace(principal.Subject) == "" || principal.Tenant != authenticator.tenant || authenticator.validateIngress(request) != nil {
		return nil, httpapi.ErrUnauthenticated
	}
	token, err := forwardedAccessToken(request)
	if err != nil {
		return nil, httpapi.ErrUnauthenticated
	}
	result, err := executionauth.WithAccessToken(ctx, token)
	if err != nil {
		return nil, httpapi.ErrUnauthenticated
	}
	return result, nil
}

func (authenticator *Authenticator) validateIngress(request *http.Request) error {
	if request == nil {
		return errors.New("request is required")
	}
	host, err := oneHeader(request, HeaderHost, true, maximumIdentityHeaderBytes)
	if err != nil || !strings.EqualFold(host, authenticator.publicHost) {
		return errors.New("forwarded host does not match the expected Databricks App")
	}
	return nil
}

type currentUserClient struct {
	host *url.URL
	http *http.Client
}

func (client *currentUserClient) CurrentUser(ctx context.Context, token string) (CurrentUser, error) {
	endpoint := client.host.ResolveReference(&url.URL{
		Path:     "/api/2.0/preview/scim/v2/Me",
		RawQuery: "attributes=active%2Cid%2CuserName%2Cemails%2Cgroups",
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return CurrentUser{}, errors.New("create current-user request")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "metricspire/0.1")
	response, err := client.http.Do(request)
	if err != nil {
		return CurrentUser{}, errors.New("verify Databricks current user")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maximumCurrentUserBytes+1))
	if err != nil || len(data) > maximumCurrentUserBytes {
		return CurrentUser{}, errors.New("read Databricks current-user response")
	}
	if response.StatusCode != http.StatusOK {
		return CurrentUser{}, fmt.Errorf("Databricks current-user verification returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		Active   bool   `json:"active"`
		ID       string `json:"id"`
		Username string `json:"userName"`
		Emails   []struct {
			Value string `json:"value"`
		} `json:"emails"`
		Groups []struct {
			Value   string `json:"value"`
			Display string `json:"display"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return CurrentUser{}, errors.New("decode Databricks current-user response")
	}
	current := CurrentUser{Active: payload.Active, ID: strings.TrimSpace(payload.ID), Username: strings.TrimSpace(payload.Username)}
	for _, email := range payload.Emails {
		if value := strings.TrimSpace(email.Value); value != "" {
			current.Emails = append(current.Emails, value)
		}
	}
	for _, group := range payload.Groups {
		if value := strings.TrimSpace(group.Value); value != "" {
			current.Groups = append(current.Groups, CurrentUserGroup{ID: value, Display: strings.TrimSpace(group.Display)})
		}
	}
	return current, nil
}

func forwardedAccessToken(request *http.Request) (string, error) {
	token, err := oneHeader(request, HeaderAccessToken, true, 16<<10)
	if err != nil {
		return "", err
	}
	if _, err := executionauth.WithAccessToken(context.Background(), token); err != nil {
		return "", err
	}
	return token, nil
}

func validateForwardedIdentity(request *http.Request, current CurrentUser) error {
	forwardedUser, err := oneHeader(request, HeaderUser, true, maximumIdentityHeaderBytes)
	if err != nil {
		return errors.New("forwarded user is unavailable")
	}
	username, err := oneHeader(request, HeaderPreferredUsername, false, maximumIdentityHeaderBytes)
	if err != nil {
		return errors.New("forwarded username is invalid")
	}
	email, err := oneHeader(request, HeaderEmail, false, maximumIdentityHeaderBytes)
	if err != nil {
		return errors.New("forwarded email is invalid")
	}
	// X-Forwarded-User is an IdP identifier and is not guaranteed to equal the
	// workspace SCIM ID. The verified token's current-user response is the
	// authoritative principal; require at least one proxy-controlled identity
	// attribute to bind those two views of the user.
	if !matchesCurrentUser(forwardedUser, current) &&
		(username == "" || !strings.EqualFold(username, current.Username)) &&
		(email == "" || !matchesEmail(email, current.Emails)) {
		return errors.New("forwarded identity does not match current user")
	}
	return nil
}

func matchesCurrentUser(value string, current CurrentUser) bool {
	if value == current.ID || strings.EqualFold(value, current.Username) {
		return true
	}
	return matchesEmail(value, current.Emails)
}

func matchesEmail(value string, emails []string) bool {
	for _, email := range emails {
		if strings.EqualFold(value, email) {
			return true
		}
	}
	return false
}

func oneHeader(request *http.Request, name string, required bool, maximumBytes int) (string, error) {
	values := request.Header.Values(name)
	if len(values) == 0 && !required {
		return "", nil
	}
	if len(values) != 1 {
		return "", fmt.Errorf("%s must occur exactly once", name)
	}
	value := values[0]
	if value == "" || value != strings.TrimSpace(value) || len(value) > maximumBytes {
		return "", fmt.Errorf("%s has an invalid value", name)
	}
	for index := range len(value) {
		if value[index] <= 0x1f || value[index] == 0x7f {
			return "", fmt.Errorf("%s contains control characters", name)
		}
	}
	return value, nil
}

func validateHTTPSOrigin(value string) (*url.URL, error) {
	value = strings.TrimSpace(value)
	if value != "" && !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("URL must be an HTTPS origin")
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed, nil
}

func configuredHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: 5 * time.Second}
}

var (
	_ httpapi.Authenticator               = (*Authenticator)(nil)
	_ httpapi.ExecutionCredentialProvider = (*Authenticator)(nil)
)
