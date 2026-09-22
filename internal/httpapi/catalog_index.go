package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/governance"
	"github.com/marmot1024/metricspire/internal/model"
)

const (
	catalogIndexDefaultLimit = 50
	catalogIndexMaximumLimit = 100
)

type CatalogIndexCounts struct {
	Published  int `json:"published"`
	Draft      int `json:"draft"`
	Governance int `json:"governance"`
	Total      int `json:"total"`
}

type CatalogIndexEntry struct {
	application.MetricCatalogEntry
	CatalogStatus       string                       `json:"catalog_status"`
	Verification        model.Verification           `json:"verification,omitempty"`
	BusinessType        governance.BusinessType      `json:"business_type,omitempty"`
	Origin              string                       `json:"origin,omitempty"`
	CalculationKind     string                       `json:"calculation_kind,omitempty"`
	FormulaSummary      string                       `json:"formula_summary,omitempty"`
	DocumentedFormula   string                       `json:"documented_formula,omitempty"`
	ServingFormula      string                       `json:"serving_formula,omitempty"`
	AuthoritativeSource *governance.SourceReference  `json:"authoritative_source,omitempty"`
	SourceReferences    []governance.SourceReference `json:"source_references,omitempty"`
	FactGrain           string                       `json:"fact_grain,omitempty"`
	ReadinessReason     string                       `json:"readiness_reason,omitempty"`
	OwnerStatus         string                       `json:"owner_status,omitempty"`
	Issues              []string                     `json:"issues,omitempty"`
	SemanticReadiness   governance.SemanticReadiness `json:"semantic_readiness,omitempty"`
	GovernanceRevision  int64                        `json:"governance_revision,omitempty"`
	SourceImportID      string                       `json:"source_import_id,omitempty"`
}

type CatalogIndexResponse struct {
	Items      []CatalogIndexEntry `json:"items"`
	Counts     CatalogIndexCounts  `json:"counts"`
	NextCursor string              `json:"next_cursor,omitempty"`
	Incomplete bool                `json:"incomplete,omitempty"`
}

type catalogPublishedResult struct {
	items    []application.MetricCatalogEntry
	duration time.Duration
	err      error
}

type catalogGovernanceResult struct {
	items    []governance.MetricRecord
	duration time.Duration
	err      error
}

func (server *Server) handleCatalogIndex(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		server.methodNotAllowed(response, request, http.MethodGet)
		return
	}
	principal, ok := server.authenticate(response, request)
	if !ok {
		return
	}
	if !principal.Has(PermissionQuery) && !principal.Has(PermissionManage) {
		server.writeProblem(response, request, http.StatusForbidden, "permission_denied", "Permission denied", "catalog access requires query or management permission", "")
		return
	}
	namespace := strings.TrimSpace(request.URL.Query().Get("namespace"))
	if namespace == "" {
		server.writeProblem(response, request, http.StatusBadRequest, "invalid_request", "Invalid request", "namespace query parameter is required", "namespace")
		return
	}
	status := strings.TrimSpace(request.URL.Query().Get("status"))
	if status == "" {
		status = "queryable"
	}
	if status != "queryable" && status != "governance" && status != "all" {
		server.writeProblem(response, request, http.StatusBadRequest, "invalid_request", "Invalid request", "status must be queryable, governance, or all", "status")
		return
	}
	limit := catalogIndexDefaultLimit
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > catalogIndexMaximumLimit {
			server.writeProblem(response, request, http.StatusBadRequest, "invalid_request", "Invalid request", "limit must be between 1 and 100", "limit")
			return
		}
		limit = parsed
	}
	after, err := decodeCatalogCursor(request.URL.Query().Get("cursor"))
	if err != nil {
		server.writeProblem(response, request, http.StatusBadRequest, "invalid_request", "Invalid request", "cursor is invalid", "cursor")
		return
	}

	started := time.Now()
	server.withTimeout(response, request, server.config.ControlTimeout, func(ctx context.Context) error {
		publishedChannel := make(chan catalogPublishedResult, 1)
		governanceChannel := make(chan catalogGovernanceResult, 1)
		if principal.Has(PermissionQuery) {
			go func() {
				queryStarted := time.Now()
				items, searchErr := server.deps.CatalogSearch.SearchActive(ctx, application.QueryScope{
					Namespace: namespace,
					Context:   model.RequestContext{Tenant: principal.Tenant, Principal: principal.Subject, Roles: principal.Roles, RequestID: requestID(request)},
				}, request.URL.Query().Get("q"), application.MaxCatalogSearchResults)
				publishedChannel <- catalogPublishedResult{items: items, duration: time.Since(queryStarted), err: searchErr}
			}()
		} else {
			publishedChannel <- catalogPublishedResult{}
		}
		if principal.Has(PermissionManage) {
			go func() {
				queryStarted := time.Now()
				items, listErr := server.deps.Governance.List(ctx, namespace, request.URL.Query().Get("q"), governance.MaxImportRecords)
				governanceChannel <- catalogGovernanceResult{items: items, duration: time.Since(queryStarted), err: listErr}
			}()
		} else {
			governanceChannel <- catalogGovernanceResult{}
		}
		published := <-publishedChannel
		governed := <-governanceChannel
		if published.err != nil {
			return published.err
		}
		if governed.err != nil {
			return governed.err
		}

		index := buildCatalogIndex(published.items, governed.items)
		counts := countCatalogIndex(index)
		filtered := filterCatalogIndex(index, status, after)
		page := filtered
		nextCursor := ""
		if len(page) > limit {
			page = page[:limit]
			nextCursor = encodeCatalogCursor(catalogIndexKey(page[len(page)-1]))
		}
		response.Header().Set("Server-Timing", fmt.Sprintf("published;dur=%.1f, governance;dur=%.1f, catalog_index;dur=%.1f",
			milliseconds(published.duration), milliseconds(governed.duration), milliseconds(time.Since(started))))
		return writeJSON(response, http.StatusOK, CatalogIndexResponse{
			Items: page, Counts: counts, NextCursor: nextCursor,
			Incomplete: len(published.items) == application.MaxCatalogSearchResults || len(governed.items) == governance.MaxImportRecords,
		})
	})
}

func buildCatalogIndex(published []application.MetricCatalogEntry, records []governance.MetricRecord) []CatalogIndexEntry {
	governed := make(map[string]governance.MetricRecord, len(records))
	for _, record := range records {
		governed[record.Definition.Code] = record
	}
	result := make([]CatalogIndexEntry, 0, len(published)+len(records))
	publishedCodes := make(map[string]struct{}, len(published))
	for _, metric := range published {
		entry := CatalogIndexEntry{MetricCatalogEntry: metric, CatalogStatus: "published"}
		if record, exists := governed[metric.Name]; exists {
			applyGovernanceFields(&entry, record)
		}
		result = append(result, entry)
		publishedCodes[metric.Name] = struct{}{}
	}
	for _, record := range records {
		definition := record.Definition
		if _, exists := publishedCodes[definition.Code]; exists {
			continue
		}
		status := "governance"
		if definition.SemanticReadiness == governance.ReadinessExecutableUnverified && definition.SemanticModelName != "" {
			status = "draft"
		}
		entry := CatalogIndexEntry{
			MetricCatalogEntry: application.MetricCatalogEntry{
				Namespace: record.Namespace, ModelName: definition.SemanticModelName, Name: definition.Code,
				DisplayName: definition.DisplayName, Description: definition.Description, Owner: definition.Owner,
				VerificationStatus: definition.Verification.Status, Tags: []string{string(definition.BusinessType)},
				ValueType: definition.ValueType, Unit: definition.Unit, AllowedDimensions: append([]string(nil), definition.TestDimensions...),
				TimeDimension: definition.TimeDimension,
			},
			CatalogStatus: status,
		}
		applyGovernanceFields(&entry, record)
		result = append(result, entry)
	}
	sort.SliceStable(result, func(i, j int) bool { return catalogIndexKey(result[i]) < catalogIndexKey(result[j]) })
	return result
}

func catalogIndexKey(entry CatalogIndexEntry) string {
	return entry.Name + "\x00" + entry.ModelName
}

func applyGovernanceFields(entry *CatalogIndexEntry, record governance.MetricRecord) {
	definition := record.Definition
	entry.Verification = definition.Verification
	entry.BusinessType = definition.BusinessType
	entry.Origin = definition.Origin
	entry.CalculationKind = definition.CalculationKind
	entry.DocumentedFormula = stringValue(definition.DocumentedFormula)
	entry.ServingFormula = stringValue(definition.ServingFormula)
	entry.FormulaSummary = entry.ServingFormula
	if entry.FormulaSummary == "" {
		entry.FormulaSummary = entry.DocumentedFormula
	}
	authoritative := definition.AuthoritativeSource
	entry.AuthoritativeSource = &authoritative
	entry.SourceReferences = append([]governance.SourceReference(nil), definition.SourceReferences...)
	entry.FactGrain = definition.FactGrain
	entry.ReadinessReason = definition.ReadinessReason
	entry.OwnerStatus = definition.OwnerStatus
	entry.Issues = append([]string(nil), definition.Issues...)
	entry.SemanticReadiness = definition.SemanticReadiness
	entry.GovernanceRevision = record.Revision
	entry.SourceImportID = record.SourceImportID
}

func countCatalogIndex(entries []CatalogIndexEntry) CatalogIndexCounts {
	var counts CatalogIndexCounts
	for _, entry := range entries {
		switch entry.CatalogStatus {
		case "published":
			counts.Published++
		case "draft":
			counts.Draft++
		case "governance":
			counts.Governance++
		}
	}
	counts.Total = counts.Published + counts.Draft + counts.Governance
	return counts
}

func filterCatalogIndex(entries []CatalogIndexEntry, status, after string) []CatalogIndexEntry {
	result := make([]CatalogIndexEntry, 0, len(entries))
	for _, entry := range entries {
		if after != "" && catalogIndexKey(entry) <= after {
			continue
		}
		if status == "queryable" && entry.CatalogStatus == "governance" {
			continue
		}
		if status == "governance" && entry.CatalogStatus != "governance" {
			continue
		}
		result = append(result, entry)
	}
	return result
}

func encodeCatalogCursor(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodeCatalogCursor(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) == 0 || strings.TrimSpace(string(decoded)) != string(decoded) {
		return "", errors.New("invalid catalog cursor")
	}
	return string(decoded), nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func milliseconds(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / 1000
}
