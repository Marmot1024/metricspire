package databricks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type StaticTokenSource string

func (s StaticTokenSource) Token(context.Context) (string, error) {
	if strings.TrimSpace(string(s)) == "" {
		return "", errors.New("static Databricks token is empty")
	}
	return string(s), nil
}

type OAuthM2MConfig struct {
	Host         string
	ClientID     string
	ClientSecret string
	HTTPClient   *http.Client
}

type OAuthM2MTokenSource struct {
	endpoint     string
	clientID     string
	clientSecret string
	http         *http.Client

	mu     sync.Mutex
	token  string
	expiry time.Time
	now    func() time.Time
}

func NewOAuthM2MTokenSource(config OAuthM2MConfig) (*OAuthM2MTokenSource, error) {
	host, err := validateHost(config.Host)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.ClientID) == "" || strings.TrimSpace(config.ClientSecret) == "" {
		return nil, errors.New("Databricks OAuth client ID and secret are required")
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	endpoint := host.ResolveReference(&url.URL{Path: "/oidc/v1/token"}).String()
	return &OAuthM2MTokenSource{
		endpoint: endpoint, clientID: config.ClientID, clientSecret: config.ClientSecret,
		http: httpClient, now: time.Now,
	}, nil
}

func (s *OAuthM2MTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.token != "" && now.Add(time.Minute).Before(s.expiry) {
		return s.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "scope": {"all-apis"}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("create Databricks OAuth request: %w", err)
	}
	request.SetBasicAuth(s.clientID, s.clientSecret)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := s.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("request Databricks OAuth token: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read Databricks OAuth response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("Databricks OAuth returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("decode Databricks OAuth response: %w", err)
	}
	if payload.AccessToken == "" || !strings.EqualFold(payload.TokenType, "Bearer") || payload.ExpiresIn < 1 {
		return "", errors.New("Databricks OAuth response omitted a valid bearer token or expiry")
	}
	s.token = payload.AccessToken
	s.expiry = now.Add(time.Duration(payload.ExpiresIn) * time.Second)
	return s.token, nil
}
