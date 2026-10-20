package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/httpapi"
)

// A complete paginated HTTP traversal at the advertised catalog scales. This
// catches dropped/duplicate rows, unstable cursors and summary/detail drift.
func TestCatalogScaleCompleteness(t *testing.T) {
	for _, size := range []int{50, 500, 1000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			handler, _, _ := newTestServer(t, httpapi.Config{ControlTimeout: 30 * time.Second})
			source := readSource(t)
			base := source.Spec.Metrics[0]
			retained := len(source.Spec.Metrics)
			for i := 0; i < size-retained; i++ {
				metric := base
				metric.Name = fmt.Sprintf("synthetic_%04d", i)
				metric.ExternalCode = fmt.Sprint(70000 + i)
				metric.DisplayName = metric.Name
				source.Spec.Metrics = append(source.Spec.Metrics, metric)
			}
			response := performJSON(handler, http.MethodPut, modelPath("draft"), "manage", httpapi.SaveDraftRequest{Source: source, ExpectedRevision: 1})
			if response.StatusCode != http.StatusOK {
				t.Fatalf("seed: %d %s", response.StatusCode, readRawBody(t, response))
			}
			response = performJSON(handler, http.MethodPost, modelPath("publish"), "manage", httpapi.PublishRequest{ExpectedRevision: 2})
			if response.StatusCode != http.StatusCreated {
				t.Fatalf("publish: %d %s", response.StatusCode, readRawBody(t, response))
			}
			checkCatalogScale(t, size, retained, func(path string) *http.Response { return performJSON(handler, http.MethodGet, path, "query", nil) }, "memory metadata / direct HTTP handler")
		})
	}
}

func checkCatalogScale(t *testing.T, size, retained int, fetch func(string) *http.Response, label string) {
	var response *http.Response
	seen := map[string]bool{}
	cursor := ""
	var pages, details []time.Duration
	summaryBytes := 0
	for page := 0; page <= size/50; page++ {
		start := time.Now()
		response = fetch("/api/v1/catalog/index?namespace=demo&status=queryable&view=summary&limit=50&cursor=" + url.QueryEscape(cursor))
		body := readRawBody(t, response)
		summaryBytes += len(body)
		var result struct {
			Items      []httpapi.CatalogIndexSummary `json:"items"`
			Counts     httpapi.CatalogIndexCounts    `json:"counts"`
			Next       string                        `json:"next_cursor"`
			Incomplete bool                          `json:"incomplete"`
		}
		if response.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &result) != nil {
			t.Fatalf("page failed: %d", response.StatusCode)
		}
		pages = append(pages, time.Since(start))
		if result.Counts.Total != size || len(result.Items) == 0 || len(result.Items) > 50 {
			t.Fatalf("wrong count or page size: %#v", result.Counts)
		}
		if size < 1000 && result.Incomplete {
			t.Fatal("small catalog marked incomplete")
		}
		for _, item := range result.Items {
			if seen[item.Name] || !item.Summary {
				t.Fatal("duplicate or incomplete summary")
			}
			seen[item.Name] = true
		}
		if result.Next == "" {
			break
		}
		if result.Next == cursor {
			t.Fatal("cursor did not advance")
		}
		cursor = result.Next
	}
	if len(seen) != size {
		t.Fatalf("traversed %d of %d", len(seen), size)
	}
	for _, i := range []int{0, (size - retained) / 2, size - retained - 1} {
		name := fmt.Sprintf("synthetic_%04d", i)
		start := time.Now()
		response = fetch("/api/v1/catalog/detail?namespace=demo&name=" + name)
		var entry httpapi.CatalogIndexEntry
		decodeResponse(t, response, &entry)
		details = append(details, time.Since(start))
		if entry.Name != name || len(entry.DimensionDetails) == 0 {
			t.Fatal("detail missing fields")
		}
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i] < pages[j] })
	sort.Slice(details, func(i, j int) bool { return details[i] < details[j] })
	t.Logf("synthetic %s: size=%d pages=%d total_bytes=%d page_p95=%s detail_max=%s; cap=1000 conservatively marks possible truncation", label, size, len(pages), summaryBytes, pages[(len(pages)*95-1)/100], details[len(details)-1])
}
