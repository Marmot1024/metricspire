package oidcauth_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/auth/oidcauth"
	"github.com/marmot1024/metricspire/internal/httpapi"
)

func TestOIDCWebLoginSessionAndLogout(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	expectedNonce := ""
	issuer := newWebIssuer(t, privateKey, &expectedNonce)
	authenticator, err := oidcauth.NewWeb(context.Background(), oidcauth.WebConfig{
		OIDC: oidcauth.Config{
			IssuerURL: issuer.url, ClientID: "metricspire", BearerAudience: "metricspire-api", HTTPClient: issuer.client,
		},
		ClientSecret: "test-secret", RedirectURL: "https://app.test/auth/callback",
		SessionKey: []byte("0123456789abcdef0123456789abcdef"), SessionTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	loginRequest := httptest.NewRequest(http.MethodGet, "https://app.test/auth/login", nil)
	loginResponse := httptest.NewRecorder()
	authenticator.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusFound {
		t.Fatalf("login status = %d, body = %s", loginResponse.Code, loginResponse.Body.String())
	}
	destination, err := url.Parse(loginResponse.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	state := destination.Query().Get("state")
	expectedNonce = destination.Query().Get("nonce")
	if state == "" || expectedNonce == "" || destination.Query().Get("code_challenge") == "" || destination.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("authorization URL = %s", destination)
	}
	flowCookie := cookieNamed(t, loginResponse.Result().Cookies(), "metricspire_oidc_flow")
	if !flowCookie.HttpOnly || !flowCookie.Secure || flowCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("flow cookie = %#v", flowCookie)
	}

	callbackRequest := httptest.NewRequest(http.MethodGet, "https://app.test/auth/callback?code=accepted&state="+url.QueryEscape(state), nil)
	callbackRequest.AddCookie(flowCookie)
	callbackResponse := httptest.NewRecorder()
	authenticator.ServeHTTP(callbackResponse, callbackRequest)
	if callbackResponse.Code != http.StatusFound || callbackResponse.Header().Get("Location") != "/" {
		t.Fatalf("callback = %d location=%q body=%s", callbackResponse.Code, callbackResponse.Header().Get("Location"), callbackResponse.Body.String())
	}
	sessionCookie := cookieNamed(t, callbackResponse.Result().Cookies(), "metricspire_session")
	if !sessionCookie.HttpOnly || !sessionCookie.Secure || sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie = %#v", sessionCookie)
	}

	apiRequest := httptest.NewRequest(http.MethodGet, "https://app.test/api/v1/catalog/search", nil)
	apiRequest.AddCookie(sessionCookie)
	principal, err := authenticator.Authenticate(context.Background(), apiRequest)
	if err != nil {
		t.Fatal(err)
	}
	if principal.Subject != "web-user" || principal.Tenant != "demo" || !principal.Has(httpapi.PermissionQuery) {
		t.Fatalf("session principal = %#v", principal)
	}

	now := time.Now()
	bearerToken := signToken(t, privateKey, map[string]any{
		"iss": issuer.url, "aud": "metricspire-api", "sub": "api-user", "tenant": "demo",
		"roles": []string{"analyst"}, "permissions": []string{"query:execute"},
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	apiRequest = httptest.NewRequest(http.MethodGet, "https://app.test/api/v1/catalog/search", nil)
	apiRequest.Header.Set("Authorization", "Bearer "+bearerToken)
	principal, err = authenticator.Authenticate(context.Background(), apiRequest)
	if err != nil || principal.Subject != "api-user" {
		t.Fatalf("bearer principal = %#v, %v", principal, err)
	}

	tampered := *sessionCookie
	tampered.Value += "x"
	apiRequest = httptest.NewRequest(http.MethodGet, "https://app.test/api/v1/catalog/search", nil)
	apiRequest.AddCookie(&tampered)
	if _, err := authenticator.Authenticate(context.Background(), apiRequest); err == nil {
		t.Fatal("tampered session cookie was accepted")
	}

	logoutRequest := httptest.NewRequest(http.MethodPost, "https://app.test/auth/logout", nil)
	logoutRequest.AddCookie(sessionCookie)
	logoutResponse := httptest.NewRecorder()
	authenticator.ServeHTTP(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusNoContent || cookieNamed(t, logoutResponse.Result().Cookies(), "metricspire_session").MaxAge != -1 {
		t.Fatalf("logout response = %d %#v", logoutResponse.Code, logoutResponse.Result().Cookies())
	}
}

func TestOIDCWebCallbackRejectsStateMismatch(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	nonce := ""
	issuer := newWebIssuer(t, privateKey, &nonce)
	authenticator, err := oidcauth.NewWeb(context.Background(), oidcauth.WebConfig{
		OIDC:        oidcauth.Config{IssuerURL: issuer.url, ClientID: "metricspire", HTTPClient: issuer.client},
		RedirectURL: "https://app.test/auth/callback", SessionKey: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	authenticator.ServeHTTP(login, httptest.NewRequest(http.MethodGet, "https://app.test/auth/login", nil))
	flowCookie := cookieNamed(t, login.Result().Cookies(), "metricspire_oidc_flow")
	callbackRequest := httptest.NewRequest(http.MethodGet, "https://app.test/auth/callback?code=accepted&state=forged", nil)
	callbackRequest.AddCookie(flowCookie)
	callback := httptest.NewRecorder()
	authenticator.ServeHTTP(callback, callbackRequest)
	if callback.Code != http.StatusUnauthorized {
		t.Fatalf("state mismatch status = %d", callback.Code)
	}
}

func TestOIDCWebRejectsInsecureCookieModeForHTTPS(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	nonce := ""
	issuer := newWebIssuer(t, privateKey, &nonce)
	_, err = oidcauth.NewWeb(context.Background(), oidcauth.WebConfig{
		OIDC:        oidcauth.Config{IssuerURL: issuer.url, ClientID: "metricspire", HTTPClient: issuer.client},
		RedirectURL: "https://app.test/auth/callback", SessionKey: []byte("0123456789abcdef0123456789abcdef"),
		InsecureCookies: true,
	})
	if err == nil {
		t.Fatal("HTTPS redirect accepted insecure cookies")
	}
}

func newWebIssuer(t *testing.T, privateKey *rsa.PrivateKey, nonce *string) testIssuer {
	t.Helper()
	const issuerURL = "https://issuer.test"
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var value any
		switch request.URL.Path {
		case "/.well-known/openid-configuration":
			value = map[string]any{
				"issuer": issuerURL, "authorization_endpoint": issuerURL + "/authorize",
				"token_endpoint": issuerURL + "/token", "jwks_uri": issuerURL + "/keys",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			}
		case "/keys":
			publicKey := &privateKey.PublicKey
			value = map[string]any{"keys": []map[string]any{{
				"kty": "RSA", "kid": "test-key", "use": "sig", "alg": "RS256",
				"n": base64.RawURLEncoding.EncodeToString(publicKey.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(publicKey.E)).Bytes()),
			}}}
		case "/token":
			if err := request.ParseForm(); err != nil || request.Form.Get("code") != "accepted" || request.Form.Get("code_verifier") == "" {
				return jsonHTTPResponse(request, http.StatusBadRequest, map[string]any{"error": "invalid_grant"}), nil
			}
			now := time.Now()
			idToken := signToken(t, privateKey, map[string]any{
				"iss": issuerURL, "aud": "metricspire", "sub": "web-user", "tenant": "demo",
				"roles": []string{"analyst"}, "permissions": []string{"query:execute"},
				"nonce": *nonce, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
			})
			return jsonHTTPResponse(request, http.StatusOK, map[string]any{
				"access_token": "opaque-test-token", "token_type": "Bearer", "expires_in": 3600, "id_token": idToken,
			}), nil
		default:
			return jsonHTTPResponse(request, http.StatusNotFound, map[string]any{"error": "not_found"}), nil
		}
		return jsonHTTPResponse(request, http.StatusOK, value), nil
	})}
	return testIssuer{url: issuerURL, client: client}
}

func jsonHTTPResponse(request *http.Request, status int, value any) *http.Response {
	data, _ := json.Marshal(value)
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(bytes.NewReader(data)), Request: request}
}

func cookieNamed(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("cookie %q not found in %#v", name, cookies)
	return nil
}
