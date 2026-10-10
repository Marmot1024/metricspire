package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/httpapi"
)

// Only reads an already reviewed, deployed neutral fixture. Normal API reads
// generate server-side audit records; this probe does no DDL or publication.
func TestOnlineDeployedAcceptance(t *testing.T) {
	if os.Getenv("METRICSPIRE_RUN_ONLINE_DEPLOYED_ACCEPTANCE") != "approved-staging" {
		t.Skip("requires a reviewed deployed staging fixture and caller identity")
	}
	baseURL, err := deployedBaseURL(os.Getenv("METRICSPIRE_ONLINE_DEPLOYED_BASE_URL"))
	if err != nil {
		t.Fatal("online acceptance requires an HTTPS origin")
	}
	namespace := strings.TrimSpace(os.Getenv("METRICSPIRE_ONLINE_DEPLOYED_NAMESPACE"))
	token := os.Getenv("METRICSPIRE_ONLINE_DEPLOYED_USER_TOKEN")
	mode := os.Getenv("METRICSPIRE_ONLINE_DEPLOYED_EXPECTATION")
	if namespace == "" || token == "" || (mode != "allowed" && mode != "denied") {
		t.Fatal("online acceptance requires namespace, existing user token and allowed/denied expectation")
	}
	var query application.OnlineQuery
	if err := contractio.ReadFile(os.Getenv("METRICSPIRE_ONLINE_DEPLOYED_QUERY"), &query); err != nil {
		t.Fatal("cannot load the reviewed neutral online query")
	}
	var expected application.OnlineResult
	if mode == "allowed" {
		if err := contractio.ReadFile(os.Getenv("METRICSPIRE_ONLINE_DEPLOYED_EXPECTED"), &expected); err != nil || expected.ReleaseID == "" || expected.ManifestFingerprint == "" || expected.Snapshot == nil || expected.Snapshot.BatchID == "" || expected.Result == nil {
			t.Fatal("allowed acceptance requires reviewed release, batch and exact typed results")
		}
	}
	budget := time.Second
	if value := os.Getenv("METRICSPIRE_ONLINE_DEPLOYED_P95_BUDGET"); value != "" {
		budget, err = time.ParseDuration(value)
		if err != nil || budget <= 0 || budget > 10*time.Second {
			t.Fatal("online p95 budget must be positive and at most 10s")
		}
	}
	client := &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	// A 403 from managed ingress is not proof of a data-policy rejection.
	// First prove the same caller can reach an authenticated query-only route.
	identityRequest, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v1/catalog/index?namespace="+url.QueryEscape(namespace)+"&view=summary&limit=1", nil)
	identityRequest.Header.Set("Authorization", "Bearer "+token)
	identityResponse, err := client.Do(identityRequest)
	if err != nil {
		t.Fatal("cannot verify the deployed caller on an authenticated route")
	}
	_ = identityResponse.Body.Close()
	if identityResponse.StatusCode != http.StatusOK {
		t.Fatal("caller is not accepted by the deployed authenticated route")
	}
	samples := make([]time.Duration, 0, 20)
	for range 20 {
		start := time.Now()
		actual, err := onlineDeployedRead(ctx, client, baseURL, namespace, token, query, mode)
		samples = append(samples, time.Since(start))
		if err != nil {
			t.Fatal(err) // Deliberately no token, body or URL-bearing transport error.
		}
		if mode == "allowed" {
			// Coverage timestamps can be refreshed; pin release and batch and
			// compare exact results without printing customer payloads on failure.
			actualJSON, _ := json.Marshal(actual.Result)
			expectedJSON, _ := json.Marshal(expected.Result)
			if actual.ReleaseID != expected.ReleaseID || actual.ManifestFingerprint != expected.ManifestFingerprint || actual.Snapshot.BatchID != expected.Snapshot.BatchID || !bytes.Equal(actualJSON, expectedJSON) {
				t.Fatal("deployed result differs from the reviewed release, batch or expected values")
			}
		}
	}
	first := samples[0]
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	p95 := samples[18]
	t.Logf("sequential deployed sample: mode=%s requests=20 first_query_after_identity_check=%s p95=%s; not cold-start, peak-load or HA acceptance", mode, first, p95)
	if mode == "allowed" && p95 > budget {
		t.Fatal("deployed online response exceeds the reviewed p95 budget")
	}
}

func onlineDeployedRead(ctx context.Context, client *http.Client, baseURL, namespace, token string, query application.OnlineQuery, mode string) (application.OnlineResult, error) {
	encoded, err := json.Marshal(query)
	if err != nil {
		return application.OnlineResult{}, errors.New("cannot encode reviewed online query")
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/namespaces/"+url.PathEscape(namespace)+"/online-query", bytes.NewReader(encoded))
	if err != nil {
		return application.OnlineResult{}, errors.New("invalid online acceptance request")
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(r)
	if err != nil {
		return application.OnlineResult{}, errors.New("deployed online HTTP request failed")
	}
	defer response.Body.Close()
	if mode == "denied" {
		if response.StatusCode != http.StatusForbidden {
			return application.OnlineResult{}, errors.New("expected a valid identity denied data access (403), not authentication failure or success")
		}
		var problem httpapi.Problem
		if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&problem); err != nil || problem.Code != "permission_denied" {
			return application.OnlineResult{}, errors.New("deployed rejection is not the MetricSpire data permission response")
		}
		return application.OnlineResult{}, nil
	}
	if response.StatusCode != http.StatusOK {
		return application.OnlineResult{}, errors.New("deployed online query did not return 200")
	}
	var out application.OnlineResult
	if err := json.NewDecoder(io.LimitReader(response.Body, 5<<20)).Decode(&out); err != nil || out.Result == nil || out.Result.Truncated || out.Snapshot == nil || out.Snapshot.BatchID == "" || out.Snapshot.DataAsOf.IsZero() || out.ReleaseID == "" || out.ManifestFingerprint == "" {
		return application.OnlineResult{}, errors.New("deployed online response lacks a complete typed snapshot")
	}
	return out, nil
}
