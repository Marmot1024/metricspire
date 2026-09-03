// Package lakebase adapts Databricks Lakebase OAuth database credentials to
// the provider-neutral PostgreSQL password boundary.
package lakebase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maximumCredentialResponseBytes = 1 << 20

type TokenSource interface {
	Token(context.Context) (string, error)
}

type CredentialConfig struct {
	Host       string
	Endpoint   string
	Tokens     TokenSource
	HTTPClient *http.Client
}

type CredentialProvider struct {
	endpoint string
	resource string
	tokens   TokenSource
	http     *http.Client
	now      func() time.Time
}

func NewCredentialProvider(config CredentialConfig) (*CredentialProvider, error) {
	host, err := validateHost(config.Host)
	if err != nil {
		return nil, err
	}
	resource, err := validateEndpoint(config.Endpoint)
	if err != nil {
		return nil, err
	}
	if config.Tokens == nil {
		return nil, errors.New("Lakebase workspace token source is required")
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &CredentialProvider{
		endpoint: host.ResolveReference(&url.URL{Path: "/api/2.0/postgres/credentials"}).String(),
		resource: resource, tokens: config.Tokens, http: client, now: time.Now,
	}, nil
}

// Password obtains a fresh one-hour database credential. The PostgreSQL pool
// calls it only when opening a physical connection, so no second token cache or
// refresh goroutine is needed.
func (provider *CredentialProvider) Password(ctx context.Context) (string, error) {
	workspaceToken, err := provider.tokens.Token(ctx)
	if err != nil {
		return "", errors.New("get Lakebase workspace token")
	}
	if err := validateToken(workspaceToken); err != nil {
		return "", errors.New("Lakebase workspace token source returned an invalid token")
	}
	body, err := json.Marshal(struct {
		Endpoint string `json:"endpoint"`
		TTL      string `json:"ttl"`
	}{Endpoint: provider.resource, TTL: "3600s"})
	if err != nil {
		return "", errors.New("encode Lakebase credential request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", errors.New("create Lakebase credential request")
	}
	request.Header.Set("Authorization", "Bearer "+workspaceToken)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "metricspire/0.1")
	response, err := provider.http.Do(request)
	if err != nil {
		return "", errors.New("request Lakebase database credential")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maximumCredentialResponseBytes+1))
	if err != nil || len(data) > maximumCredentialResponseBytes {
		return "", errors.New("read Lakebase credential response")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("Lakebase credential API returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		Token      string `json:"token"`
		ExpireTime string `json:"expire_time"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", errors.New("decode Lakebase credential response")
	}
	if err := validateToken(payload.Token); err != nil {
		return "", errors.New("Lakebase credential response omitted a valid token")
	}
	expiresAt, err := time.Parse(time.RFC3339, payload.ExpireTime)
	if err != nil || !provider.now().Add(5*time.Minute).Before(expiresAt) {
		return "", errors.New("Lakebase credential response omitted a usable expiry")
	}
	return payload.Token, nil
}

func validateHost(value string) (*url.URL, error) {
	value = strings.TrimSpace(value)
	if value != "" && !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("Lakebase workspace host must be an HTTPS origin")
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed, nil
}

func validateEndpoint(value string) (string, error) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, "/")
	if len(parts) != 6 || parts[0] != "projects" || parts[2] != "branches" || parts[4] != "endpoints" {
		return "", errors.New("Lakebase endpoint must use projects/{project}/branches/{branch}/endpoints/{endpoint}")
	}
	for _, index := range []int{1, 3, 5} {
		if parts[index] == "" || len(parts[index]) > 128 {
			return "", errors.New("Lakebase endpoint contains an invalid resource identifier")
		}
		for _, character := range parts[index] {
			if character != '-' && character != '_' && (character < '0' || character > '9') && (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') {
				return "", errors.New("Lakebase endpoint contains an invalid resource identifier")
			}
		}
	}
	return value, nil
}

func validateToken(value string) error {
	if len(value) < 1 || len(value) > 16<<10 {
		return errors.New("invalid token size")
	}
	for index := range len(value) {
		if value[index] <= 0x20 || value[index] >= 0x7f {
			return errors.New("invalid token character")
		}
	}
	return nil
}
