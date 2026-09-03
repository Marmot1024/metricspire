package oidcauth_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/auth/oidcauth"
	"github.com/marmot1024/metricspire/internal/httpapi"
)

func TestOIDCAuthenticatorVerifiesDiscoveryJWKSAndClaims(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer := newIssuer(t, &privateKey.PublicKey)
	authenticator, err := oidcauth.New(context.Background(), oidcauth.Config{
		IssuerURL: issuer.url, ClientID: "metricspire", HTTPClient: issuer.client,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	token := signToken(t, privateKey, map[string]any{
		"iss": issuer.url, "aud": "metricspire", "sub": "user-123", "tenant": "demo",
		"roles":       []string{"analyst", "analyst"},
		"permissions": []string{"query:execute", "unknown:ignored"},
		"iat":         now.Unix(), "exp": now.Add(time.Minute).Unix(),
	})
	request := httptest.NewRequest(http.MethodGet, "https://metricspire.test/api/v1/catalog/search", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	principal, err := authenticator.Authenticate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if principal.Tenant != "demo" || principal.Subject != "user-123" || len(principal.Roles) != 1 ||
		len(principal.Permissions) != 1 || principal.Permissions[0] != httpapi.PermissionQuery {
		t.Fatalf("principal = %#v", principal)
	}
}

func TestOIDCAuthenticatorRejectsInvalidAudienceExpiredAndMissingTenant(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer := newIssuer(t, &privateKey.PublicKey)
	authenticator, err := oidcauth.New(context.Background(), oidcauth.Config{
		IssuerURL: issuer.url, ClientID: "metricspire", HTTPClient: issuer.client,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tests := []map[string]any{
		{"iss": issuer.url, "aud": "other", "sub": "user", "tenant": "demo", "exp": now.Add(time.Minute).Unix()},
		{"iss": issuer.url, "aud": "metricspire", "sub": "user", "tenant": "demo", "exp": now.Add(-time.Minute).Unix()},
		{"iss": issuer.url, "aud": "metricspire", "sub": "user", "exp": now.Add(time.Minute).Unix()},
	}
	for _, claims := range tests {
		request := httptest.NewRequest(http.MethodGet, "https://metricspire.test/", nil)
		request.Header.Set("Authorization", "Bearer "+signToken(t, privateKey, claims))
		if _, err := authenticator.Authenticate(context.Background(), request); err == nil {
			t.Fatalf("invalid claims were accepted: %#v", claims)
		}
	}
}

func TestOIDCAuthenticatorRequiresHTTPSOutsideTests(t *testing.T) {
	if _, err := oidcauth.New(context.Background(), oidcauth.Config{IssuerURL: "http://issuer.example", ClientID: "client"}); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("HTTP issuer error = %v", err)
	}
	if _, err := oidcauth.New(context.Background(), oidcauth.Config{IssuerURL: "http://issuer.example", ClientID: "client", AllowHTTP: true}); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("non-loopback development issuer error = %v", err)
	}
}

type testIssuer struct {
	url    string
	client *http.Client
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func newIssuer(t *testing.T, publicKey *rsa.PublicKey) testIssuer {
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
			value = map[string]any{"keys": []map[string]any{{
				"kty": "RSA", "kid": "test-key", "use": "sig", "alg": "RS256",
				"n": base64.RawURLEncoding.EncodeToString(publicKey.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(publicKey.E)).Bytes()),
			}}}
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("not found")), Request: request}, nil
		}
		data, _ := json.Marshal(value)
		header := make(http.Header)
		header.Set("Content-Type", "application/json")
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(bytes.NewReader(data)), Request: request}, nil
	})}
	return testIssuer{url: issuerURL, client: client}
}

func signToken(t *testing.T, privateKey *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test-key", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := privateKey.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%s.%s", unsigned, base64.RawURLEncoding.EncodeToString(signature))
}
