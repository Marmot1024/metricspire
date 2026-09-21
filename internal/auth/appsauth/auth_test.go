package appsauth_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/auth/appsauth"
	"github.com/marmot1024/metricspire/internal/executionauth"
	"github.com/marmot1024/metricspire/internal/httpapi"
)

func TestAuthenticatorDefaultsToQueryAndGrantsPublisherManagement(t *testing.T) {
	const token = "short-lived-user-token"
	verifier := currentUserVerifierFunc(func(_ context.Context, actual string) (appsauth.CurrentUser, error) {
		if actual != token {
			t.Fatalf("current-user token = %q", actual)
		}
		return appsauth.CurrentUser{
			Active: true, ID: "123", Username: "analyst@example.com", Emails: []string{"analyst@example.com"},
			Groups: []appsauth.CurrentUserGroup{{ID: "publishers", Display: "Metric publishers"}, {ID: "unmapped", Display: "Other"}},
		}, nil
	})
	authenticator := newAuthenticator(t, verifier)
	request := appsRequest(token)
	principal, err := authenticator.Authenticate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if principal.Tenant != "company" || principal.Subject != "123" || !principal.Has(httpapi.PermissionQuery) || !principal.Has(httpapi.PermissionManage) {
		t.Fatalf("principal = %#v", principal)
	}
	if len(principal.Roles) != 1 || principal.Roles[0] != "analyst" {
		t.Fatalf("roles = %#v", principal.Roles)
	}
	executionContext, err := authenticator.AddToContext(context.Background(), request, principal)
	if err != nil {
		t.Fatal(err)
	}
	actual, ok := executionauth.AccessToken(executionContext)
	if !ok || actual != token {
		t.Fatalf("execution token = %q, %v", actual, ok)
	}
}

func TestAuthenticatorDefaultsNonPublisherToQueryOnly(t *testing.T) {
	verifier := currentUserVerifierFunc(func(context.Context, string) (appsauth.CurrentUser, error) {
		return appsauth.CurrentUser{
			Active: true, ID: "456", Username: "reader@example.com", Emails: []string{"reader@example.com"},
			Groups: []appsauth.CurrentUserGroup{{ID: "other", Display: "Other"}},
		}, nil
	})
	authenticator := newAuthenticator(t, verifier)
	request := appsRequest("token")
	request.Header.Set(appsauth.HeaderUser, "reader@example.com")
	request.Header.Set(appsauth.HeaderPreferredUsername, "reader@example.com")
	request.Header.Set(appsauth.HeaderEmail, "reader@example.com")
	principal, err := authenticator.Authenticate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if principal.Subject != "456" || !principal.Has(httpapi.PermissionQuery) || principal.Has(httpapi.PermissionManage) ||
		len(principal.Roles) != 1 || principal.Roles[0] != "analyst" {
		t.Fatalf("principal = %#v", principal)
	}
}

func TestAuthenticatorCoalescesAndCachesCurrentUserVerification(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	verifier := currentUserVerifierFunc(func(context.Context, string) (appsauth.CurrentUser, error) {
		calls.Add(1)
		<-release
		return appsauth.CurrentUser{Active: true, ID: "123", Username: "analyst@example.com", Emails: []string{"analyst@example.com"}}, nil
	})
	authenticator := newAuthenticatorWithConfig(t, verifier, func(config *appsauth.Config) {
		config.CurrentUserCacheTTL = time.Minute
	})
	var group sync.WaitGroup
	errorsSeen := make(chan error, 2)
	group.Add(2)
	for range 2 {
		go func() {
			defer group.Done()
			_, err := authenticator.Authenticate(context.Background(), appsRequest("same-token"))
			errorsSeen <- err
		}()
	}
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	close(release)
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("current-user calls = %d, want 1", calls.Load())
	}
	if _, err := authenticator.Authenticate(context.Background(), appsRequest("same-token")); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("cached current-user calls = %d, want 1", calls.Load())
	}
}

func TestAuthenticatorDoesNotCacheCurrentUserFailures(t *testing.T) {
	var calls atomic.Int32
	verifier := currentUserVerifierFunc(func(context.Context, string) (appsauth.CurrentUser, error) {
		if calls.Add(1) == 1 {
			return appsauth.CurrentUser{}, errors.New("temporary failure")
		}
		return appsauth.CurrentUser{Active: true, ID: "123", Username: "analyst@example.com", Emails: []string{"analyst@example.com"}}, nil
	})
	authenticator := newAuthenticator(t, verifier)
	if _, err := authenticator.Authenticate(context.Background(), appsRequest("retry-token")); !errors.Is(err, httpapi.ErrUnauthenticated) {
		t.Fatalf("first Authenticate() error = %v", err)
	}
	if _, err := authenticator.Authenticate(context.Background(), appsRequest("retry-token")); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("current-user calls = %d, want 2", calls.Load())
	}
}

func TestAuthenticatorFailsClosedForRuntimeIngressAndIdentityMismatch(t *testing.T) {
	verifier := currentUserVerifierFunc(func(context.Context, string) (appsauth.CurrentUser, error) {
		return appsauth.CurrentUser{Active: true, ID: "123", Username: "analyst@example.com", Emails: []string{"analyst@example.com"}}, nil
	})
	if _, err := appsauth.New(validConfig(), appsauth.Runtime{AppName: "wrong", WorkspaceID: "314", Host: "https://workspace.example.com"}, verifier); err == nil {
		t.Fatal("mismatched runtime identity was accepted")
	}
	authenticator := newAuthenticator(t, verifier)
	for name, mutate := range map[string]func(*http.Request){
		"wrong host":      func(request *http.Request) { request.Header.Set(appsauth.HeaderHost, "evil.example.com") },
		"duplicate token": func(request *http.Request) { request.Header.Add(appsauth.HeaderAccessToken, "second") },
		"wrong identity": func(request *http.Request) {
			request.Header.Set(appsauth.HeaderUser, "someone@example.com")
			request.Header.Set(appsauth.HeaderPreferredUsername, "someone@example.com")
			request.Header.Set(appsauth.HeaderEmail, "someone@example.com")
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := appsRequest("token")
			mutate(request)
			if _, err := authenticator.Authenticate(context.Background(), request); !errors.Is(err, httpapi.ErrUnauthenticated) {
				t.Fatalf("Authenticate() error = %v", err)
			}
		})
	}
}

func TestAuthenticatorAcceptsOpaqueIdPSubjectWhenEmailBindsTokenUser(t *testing.T) {
	verifier := currentUserVerifierFunc(func(context.Context, string) (appsauth.CurrentUser, error) {
		return appsauth.CurrentUser{Active: true, ID: "123", Username: "analyst@example.com", Emails: []string{"analyst@example.com"}}, nil
	})
	request := appsRequest("token")
	request.Header.Set(appsauth.HeaderUser, "opaque-idp-subject")
	principal, err := newAuthenticator(t, verifier).Authenticate(context.Background(), request)
	if err != nil || principal.Subject != "123" {
		t.Fatalf("Authenticate() = %#v, %v", principal, err)
	}
}

func TestCurrentUserFailureDoesNotLeakForwardedToken(t *testing.T) {
	const token = "token-that-must-not-leak"
	verifier := currentUserVerifierFunc(func(context.Context, string) (appsauth.CurrentUser, error) {
		return appsauth.CurrentUser{}, errors.New(token)
	})
	authenticator := newAuthenticator(t, verifier)
	_, err := authenticator.Authenticate(context.Background(), appsRequest(token))
	if !errors.Is(err, httpapi.ErrUnauthenticated) || strings.Contains(err.Error(), token) {
		t.Fatalf("Authenticate() error = %v", err)
	}
	var diagnostic interface{ SafeAuthenticationReason() string }
	if !errors.As(err, &diagnostic) || diagnostic.SafeAuthenticationReason() != "current_user" {
		t.Fatalf("safe authentication diagnostic = %T %v", err, err)
	}
}

func TestDefaultCurrentUserVerifierUsesForwardedTokenAndBoundedMeEndpoint(t *testing.T) {
	const token = "user-token"
	config := validConfig()
	config.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/2.0/preview/scim/v2/Me" ||
			request.URL.Query().Get("attributes") != "active,id,userName,emails,groups" {
			t.Fatalf("current-user request = %s %s", request.Method, request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer "+token {
			t.Fatalf("Authorization = %q", request.Header.Get("Authorization"))
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"active":true,"id":"123","userName":"analyst@example.com","emails":[{"value":"analyst@example.com"}],"groups":[{"value":"publishers","display":"Metric publishers"}]}`)),
		}, nil
	})}
	authenticator, err := appsauth.New(config, appsauth.Runtime{
		AppName: "metricspire", WorkspaceID: "314", Host: "https://workspace.example.com",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := authenticator.Authenticate(context.Background(), appsRequest(token))
	if err != nil || principal.Subject != "123" || !principal.Has(httpapi.PermissionQuery) {
		t.Fatalf("Authenticate() = %#v, %v", principal, err)
	}
}

func TestRuntimeFromEnvironmentRequiresAllManagedValues(t *testing.T) {
	values := map[string]string{
		"DATABRICKS_APP_NAME": "metricspire", "DATABRICKS_WORKSPACE_ID": "314", "DATABRICKS_HOST": "https://workspace.example.com",
	}
	runtime, err := appsauth.RuntimeFromEnvironment(func(name string) string { return values[name] })
	if err != nil || runtime.AppName != "metricspire" || runtime.WorkspaceID != "314" {
		t.Fatalf("RuntimeFromEnvironment() = %#v, %v", runtime, err)
	}
	delete(values, "DATABRICKS_APP_NAME")
	if _, err := appsauth.RuntimeFromEnvironment(func(name string) string { return values[name] }); err == nil {
		t.Fatal("incomplete Apps environment was accepted")
	}
}

func TestAuthenticatorNormalizesAppsWorkspaceHost(t *testing.T) {
	verifier := currentUserVerifierFunc(func(context.Context, string) (appsauth.CurrentUser, error) {
		return appsauth.CurrentUser{}, nil
	})
	for _, host := range []string{"workspace.example.com", "https://workspace.example.com/"} {
		_, err := appsauth.New(validConfig(), appsauth.Runtime{
			AppName: "metricspire", WorkspaceID: "314", Host: host,
		}, verifier)
		if err != nil {
			t.Fatalf("workspace host %q: %v", host, err)
		}
	}
}

type currentUserVerifierFunc func(context.Context, string) (appsauth.CurrentUser, error)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func (function currentUserVerifierFunc) CurrentUser(ctx context.Context, token string) (appsauth.CurrentUser, error) {
	return function(ctx, token)
}

func newAuthenticator(t *testing.T, verifier appsauth.CurrentUserVerifier) *appsauth.Authenticator {
	t.Helper()
	return newAuthenticatorWithConfig(t, verifier, nil)
}

func newAuthenticatorWithConfig(t *testing.T, verifier appsauth.CurrentUserVerifier, configure func(*appsauth.Config)) *appsauth.Authenticator {
	t.Helper()
	config := validConfig()
	if configure != nil {
		configure(&config)
	}
	authenticator, err := appsauth.New(config, appsauth.Runtime{
		AppName: "metricspire", WorkspaceID: "314", Host: "https://workspace.example.com",
	}, verifier)
	if err != nil {
		t.Fatal(err)
	}
	return authenticator
}

func validConfig() appsauth.Config {
	return appsauth.Config{
		ExpectedAppName: "metricspire", ExpectedWorkspaceID: "314",
		ExpectedPublicURL: "https://metricspire-314.aws.databricksapps.com", Tenant: "company",
		QueryRole: "analyst", PublisherGroupID: "publishers",
	}
}

func appsRequest(token string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "https://metricspire-314.aws.databricksapps.com/api/v1/query", nil)
	request.Header.Set(appsauth.HeaderHost, "metricspire-314.aws.databricksapps.com")
	request.Header.Set(appsauth.HeaderUser, "analyst@example.com")
	request.Header.Set(appsauth.HeaderPreferredUsername, "analyst@example.com")
	request.Header.Set(appsauth.HeaderEmail, "analyst@example.com")
	request.Header.Set(appsauth.HeaderAccessToken, token)
	return request
}
