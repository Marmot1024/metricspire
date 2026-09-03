package oidcauth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/marmot1024/metricspire/internal/httpapi"
	"golang.org/x/oauth2"
)

const (
	flowCookieName     = "metricspire_oidc_flow"
	sessionCookieName  = "metricspire_session"
	flowLifetime       = 10 * time.Minute
	defaultSessionTTL  = 8 * time.Hour
	maximumCookieBytes = 4096
)

type WebConfig struct {
	OIDC            Config
	ClientSecret    string
	RedirectURL     string
	SessionKey      []byte
	SessionTTL      time.Duration
	InsecureCookies bool
	PostLoginPath   string
}

type WebAuthenticator struct {
	bearer        *Authenticator
	verifier      *oidc.IDTokenVerifier
	oauth         oauth2.Config
	httpClient    *http.Client
	flowCodec     cookieCodec
	sessionCodec  cookieCodec
	sessionTTL    time.Duration
	secureCookies bool
	postLoginPath string
}

type flowState struct {
	State     string `json:"state"`
	Nonce     string `json:"nonce"`
	Verifier  string `json:"verifier"`
	ExpiresAt int64  `json:"expires_at"`
}

type sessionState struct {
	Tenant      string               `json:"tenant"`
	Subject     string               `json:"subject"`
	Roles       []string             `json:"roles,omitempty"`
	Permissions []httpapi.Permission `json:"permissions,omitempty"`
	ExpiresAt   int64                `json:"expires_at"`
}

func NewWeb(ctx context.Context, config WebConfig) (*WebAuthenticator, error) {
	if strings.TrimSpace(config.OIDC.ClientID) == "" {
		return nil, errors.New("OIDC client ID is required")
	}
	provider, err := discover(ctx, config.OIDC)
	if err != nil {
		return nil, err
	}
	verifier := provider.Verifier(&oidc.Config{ClientID: config.OIDC.ClientID})
	redirect, err := url.Parse(strings.TrimSpace(config.RedirectURL))
	if err != nil || redirect.Host == "" || redirect.User != nil || redirect.RawQuery != "" || redirect.Fragment != "" || redirect.Path != "/auth/callback" {
		return nil, errors.New("OIDC redirect URL must be an absolute /auth/callback URL")
	}
	if redirect.Scheme != "https" {
		if !config.OIDC.AllowHTTP || redirect.Scheme != "http" || !isLoopbackHost(redirect.Hostname()) {
			return nil, errors.New("OIDC redirect URL must use HTTPS; HTTP is allowed only for loopback development")
		}
	}
	if config.InsecureCookies != (redirect.Scheme == "http") {
		return nil, errors.New("insecure OIDC cookies are allowed only and required for loopback HTTP development")
	}
	flowCodec, err := newCookieCodec(config.SessionKey, flowCookieName)
	if err != nil {
		return nil, err
	}
	sessionCodec, err := newCookieCodec(config.SessionKey, sessionCookieName)
	if err != nil {
		return nil, err
	}
	sessionTTL := config.SessionTTL
	if sessionTTL == 0 {
		sessionTTL = defaultSessionTTL
	}
	if sessionTTL < time.Minute || sessionTTL > 24*time.Hour {
		return nil, errors.New("OIDC session TTL must be between one minute and 24 hours")
	}
	postLoginPath := strings.TrimSpace(config.PostLoginPath)
	if postLoginPath == "" {
		postLoginPath = "/"
	}
	if !strings.HasPrefix(postLoginPath, "/") || strings.HasPrefix(postLoginPath, "//") {
		return nil, errors.New("OIDC post-login path must be an absolute local path")
	}
	bearer, err := newWithVerifier(config.OIDC, remoteVerifier{
		verifier: provider.Verifier(&oidc.Config{ClientID: bearerAudience(config.OIDC)}),
	})
	if err != nil {
		return nil, err
	}
	httpClient := config.OIDC.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &WebAuthenticator{
		bearer: bearer, verifier: verifier,
		oauth: oauth2.Config{
			ClientID: config.OIDC.ClientID, ClientSecret: config.ClientSecret,
			RedirectURL: redirect.String(), Endpoint: provider.Endpoint(),
			Scopes: []string{oidc.ScopeOpenID, oidc.ScopeProfile, oidc.ScopeEmail},
		},
		httpClient: httpClient, flowCodec: flowCodec, sessionCodec: sessionCodec,
		sessionTTL: sessionTTL, secureCookies: !config.InsecureCookies, postLoginPath: postLoginPath,
	}, nil
}

func (authenticator *WebAuthenticator) Authenticate(ctx context.Context, request *http.Request) (httpapi.Principal, error) {
	if request.Header.Get("Authorization") != "" {
		return authenticator.bearer.Authenticate(ctx, request)
	}
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil || len(cookie.Value) > maximumCookieBytes {
		return httpapi.Principal{}, httpapi.ErrUnauthenticated
	}
	var session sessionState
	if err := authenticator.sessionCodec.Open(cookie.Value, &session); err != nil || time.Now().Unix() >= session.ExpiresAt {
		return httpapi.Principal{}, httpapi.ErrUnauthenticated
	}
	if strings.TrimSpace(session.Tenant) == "" || strings.TrimSpace(session.Subject) == "" {
		return httpapi.Principal{}, httpapi.ErrUnauthenticated
	}
	return httpapi.Principal{
		Tenant: session.Tenant, Subject: session.Subject,
		Roles: append([]string(nil), session.Roles...), Permissions: append([]httpapi.Permission(nil), session.Permissions...),
	}, nil
}

func (authenticator *WebAuthenticator) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/auth/login":
		authenticator.handleLogin(response, request)
	case "/auth/callback":
		authenticator.handleCallback(response, request)
	case "/auth/logout":
		authenticator.handleLogout(response, request)
	default:
		writeAuthProblem(response, request, http.StatusNotFound, "not_found", "authentication endpoint not found")
	}
}

func (authenticator *WebAuthenticator) handleLogin(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", http.MethodGet)
		writeAuthProblem(response, request, http.StatusMethodNotAllowed, "method_not_allowed", "login requires GET")
		return
	}
	state, err := randomToken(32)
	if err != nil {
		writeAuthProblem(response, request, http.StatusInternalServerError, "authentication_failed", "could not initialize login")
		return
	}
	nonce, err := randomToken(32)
	if err != nil {
		writeAuthProblem(response, request, http.StatusInternalServerError, "authentication_failed", "could not initialize login")
		return
	}
	verifier := oauth2.GenerateVerifier()
	flow := flowState{State: state, Nonce: nonce, Verifier: verifier, ExpiresAt: time.Now().Add(flowLifetime).Unix()}
	value, err := authenticator.flowCodec.Seal(flow)
	if err != nil {
		writeAuthProblem(response, request, http.StatusInternalServerError, "authentication_failed", "could not initialize login")
		return
	}
	authenticator.setCookie(response, flowCookieName, value, int(flowLifetime.Seconds()))
	destination := authenticator.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
	http.Redirect(response, request, destination, http.StatusFound)
}

func (authenticator *WebAuthenticator) handleCallback(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", http.MethodGet)
		writeAuthProblem(response, request, http.StatusMethodNotAllowed, "method_not_allowed", "callback requires GET")
		return
	}
	authenticator.clearCookie(response, flowCookieName)
	if request.URL.Query().Get("error") != "" {
		writeAuthProblem(response, request, http.StatusUnauthorized, "unauthenticated", "identity provider rejected login")
		return
	}
	cookie, err := request.Cookie(flowCookieName)
	if err != nil || len(cookie.Value) > maximumCookieBytes {
		writeAuthProblem(response, request, http.StatusUnauthorized, "unauthenticated", "login flow cookie is missing")
		return
	}
	var flow flowState
	if err := authenticator.flowCodec.Open(cookie.Value, &flow); err != nil || time.Now().Unix() >= flow.ExpiresAt ||
		subtle.ConstantTimeCompare([]byte(flow.State), []byte(request.URL.Query().Get("state"))) != 1 {
		writeAuthProblem(response, request, http.StatusUnauthorized, "unauthenticated", "login state is invalid or expired")
		return
	}
	ctx := oidc.ClientContext(request.Context(), authenticator.httpClient)
	token, err := authenticator.oauth.Exchange(ctx, request.URL.Query().Get("code"), oauth2.VerifierOption(flow.Verifier))
	if err != nil {
		writeAuthProblem(response, request, http.StatusUnauthorized, "unauthenticated", "authorization code exchange failed")
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		writeAuthProblem(response, request, http.StatusUnauthorized, "unauthenticated", "identity provider returned no ID token")
		return
	}
	idToken, err := authenticator.verifier.Verify(ctx, rawIDToken)
	if err != nil || subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(flow.Nonce)) != 1 {
		writeAuthProblem(response, request, http.StatusUnauthorized, "unauthenticated", "ID token validation failed")
		return
	}
	claims := make(map[string]json.RawMessage)
	if err := idToken.Claims(&claims); err != nil {
		writeAuthProblem(response, request, http.StatusUnauthorized, "unauthenticated", "ID token claims are invalid")
		return
	}
	principal, err := authenticator.bearer.principalFromClaims(claims)
	if err != nil {
		writeAuthProblem(response, request, http.StatusUnauthorized, "unauthenticated", "required identity claims are missing")
		return
	}
	expiresAt := time.Now().Add(authenticator.sessionTTL)
	if idToken.Expiry.Before(expiresAt) {
		expiresAt = idToken.Expiry
	}
	session := sessionState{
		Tenant: principal.Tenant, Subject: principal.Subject,
		Roles: principal.Roles, Permissions: principal.Permissions, ExpiresAt: expiresAt.Unix(),
	}
	value, err := authenticator.sessionCodec.Seal(session)
	if err != nil {
		writeAuthProblem(response, request, http.StatusInternalServerError, "authentication_failed", "could not create session")
		return
	}
	authenticator.setCookie(response, sessionCookieName, value, int(time.Until(expiresAt).Seconds()))
	http.Redirect(response, request, authenticator.postLoginPath, http.StatusFound)
}

func (authenticator *WebAuthenticator) handleLogout(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writeAuthProblem(response, request, http.StatusMethodNotAllowed, "method_not_allowed", "logout requires POST")
		return
	}
	authenticator.clearCookie(response, sessionCookieName)
	authenticator.clearCookie(response, flowCookieName)
	response.WriteHeader(http.StatusNoContent)
}

func (authenticator *WebAuthenticator) setCookie(response http.ResponseWriter, name, value string, maxAge int) {
	// #nosec G124 -- NewWeb enforces Secure cookies except for explicit loopback HTTP development.
	http.SetCookie(response, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: authenticator.secureCookies, SameSite: http.SameSiteLaxMode,
	})
}

func (authenticator *WebAuthenticator) clearCookie(response http.ResponseWriter, name string) {
	// #nosec G124 -- NewWeb enforces Secure cookies except for explicit loopback HTTP development.
	http.SetCookie(response, &http.Cookie{
		Name: name, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0),
		HttpOnly: true, Secure: authenticator.secureCookies, SameSite: http.SameSiteLaxMode,
	})
}

type cookieCodec struct {
	aead cipher.AEAD
	aad  []byte
}

func newCookieCodec(key []byte, name string) (cookieCodec, error) {
	if len(key) != 32 {
		return cookieCodec{}, errors.New("OIDC session key must be exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return cookieCodec{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return cookieCodec{}, err
	}
	return cookieCodec{aead: aead, aad: []byte(name)}, nil
}

func (codec cookieCodec) Seal(value any) (string, error) {
	plaintext, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, codec.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := codec.aead.Seal(nonce, nonce, plaintext, codec.aad)
	encoded := base64.RawURLEncoding.EncodeToString(sealed)
	if len(encoded) > maximumCookieBytes {
		return "", errors.New("encrypted OIDC cookie exceeds browser limit")
	}
	return encoded, nil
}

func (codec cookieCodec) Open(value string, target any) error {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(data) < codec.aead.NonceSize() {
		return errors.New("invalid encrypted OIDC cookie")
	}
	nonce, ciphertext := data[:codec.aead.NonceSize()], data[codec.aead.NonceSize():]
	plaintext, err := codec.aead.Open(nil, nonce, ciphertext, codec.aad)
	if err != nil {
		return errors.New("invalid encrypted OIDC cookie")
	}
	if err := json.Unmarshal(plaintext, target); err != nil {
		return errors.New("invalid encrypted OIDC cookie")
	}
	return nil
}

func randomToken(size int) (string, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func writeAuthProblem(response http.ResponseWriter, request *http.Request, status int, code, detail string) {
	response.Header().Set("Content-Type", "application/problem+json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(httpapi.Problem{
		Type: "https://metricspire.io/problems/" + code, Title: http.StatusText(status),
		Status: status, Detail: detail, Code: code, RequestID: httpapi.RequestID(request),
	})
}

var _ httpapi.Authenticator = (*WebAuthenticator)(nil)
var _ http.Handler = (*WebAuthenticator)(nil)
