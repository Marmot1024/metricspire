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
	CatalogStatus        string                       `json:"catalog_status"`
	Verification         model.Verification           `json:"verification,omitempty"`
	BusinessType         governance.BusinessType      `json:"business_type,omitempty"`
	Origin               string                       `json:"origin,omitempty"`
	CalculationKind      string                       `json:"calculation_kind,omitempty"`
	FormulaSummary       string                       `json:"formula_summary,omitempty"`
	DocumentedFormula    string                       `json:"documented_formula,omitempty"`
	ServingFormula       string                       `json:"serving_formula,omitempty"`
	AuthoritativeSource  *governance.SourceReference  `json:"authoritative_source,omitempty"`
	SourceReferences     []governance.SourceReference `json:"source_references,omitempty"`
	FactGrain            string                       `json:"fact_grain,omitempty"`
	ReadinessReason      string                       `json:"readiness_reason,omitempty"`
	OwnerStatus          string                       `json:"owner_status,omitempty"`
	Issues               []string                     `json:"issues,omitempty"`
	SemanticReadiness    governance.SemanticReadiness `json:"semantic_readiness,omitempty"`
	GovernanceRevision   int64                        `json:"governance_revision,omitempty"`
	SourceImportID       string                       `json:"source_import_id,omitempty"`
	GovernanceSearchText string                       `json:"-"`
}

type CatalogIndexResponse struct {
	Items      []CatalogIndexEntry `json:"items"`
	Counts     CatalogIndexCounts  `json:"counts"`
	NextCursor string              `json:"next_cursor,omitempty"`
	Incomplete bool                `json:"incomplete,omitempty"`
}

// CatalogIndexSummary contains only fields needed to render the list and
// construct a bounded query. Full definitions and evidence are fetched when
// a row is selected.
type CatalogIndexSummary struct {
	Namespace          string                             `json:"namespace"`
	ModelName          string                             `json:"semantic_model_name"`
	Name               string                             `json:"name"`
	ExternalCode       string                             `json:"external_code,omitempty"`
	DisplayName        string                             `json:"display_name"`
	Owner              string                             `json:"owner"`
	CatalogStatus      string                             `json:"catalog_status"`
	VerificationStatus model.VerificationStatus           `json:"verification_status"`
	Deprecated         bool                               `json:"deprecated"`
	Tags               []string                           `json:"tags,omitempty"`
	ReleaseID          string                             `json:"release_id,omitempty"`
	ValueType          model.DataType                     `json:"value_type"`
	Unit               string                             `json:"unit,omitempty"`
	AllowedDimensions  []string                           `json:"allowed_dimensions"`
	DimensionDetails   []application.MetricDimensionEntry `json:"dimension_details,omitempty"`
	TimeDimension      string                             `json:"time_dimension,omitempty"`
	TimeGranularities  []model.TimeGranularity            `json:"time_granularities,omitempty"`
	Expression         model.Expression                   `json:"expression,omitempty"`
	FormulaSummary     string                             `json:"formula_summary,omitempty"`
	SourceResource     string                             `json:"source_resource,omitempty"`
	Summary            bool                               `json:"summary"`
}

func summarizeCatalogIndex(entry CatalogIndexEntry) CatalogIndexSummary {
	summary := CatalogIndexSummary{
		Namespace: entry.Namespace, ModelName: entry.ModelName, Name: entry.Name,
		ExternalCode: entry.ExternalCode, DisplayName: entry.DisplayName, Owner: entry.Owner,
		CatalogStatus: entry.CatalogStatus, VerificationStatus: entry.VerificationStatus,
		Deprecated: entry.Deprecated, Tags: entry.Tags, ReleaseID: entry.ReleaseID,
		ValueType: entry.ValueType, Unit: entry.Unit, AllowedDimensions: entry.AllowedDimensions,
		DimensionDetails: entry.DimensionDetails, TimeDimension: entry.TimeDimension,
		TimeGranularities: entry.TimeGranularities, Expression: entry.Expression,
		FormulaSummary: entry.FormulaSummary, Summary: true,
	}
	if entry.AuthoritativeSource != nil {
		summary.SourceResource = entry.AuthoritativeSource.Resource
	}
	return summary
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
	view := request.URL.Query().Get("view")
	if view != "" && view != "summary" {
		server.writeProblem(response, request, http.StatusBadRequest, "invalid_request", "Invalid request", "view must be summary", "view")
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
				}, "", application.MaxCatalogSearchResults)
				publishedChannel <- catalogPublishedResult{items: items, duration: time.Since(queryStarted), err: searchErr}
			}()
		} else {
			publishedChannel <- catalogPublishedResult{}
		}
		if principal.Has(PermissionManage) {
			go func() {
				queryStarted := time.Now()
				items, listErr := server.deps.Governance.List(ctx, namespace, "", governance.MaxImportRecords)
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

		index := searchCatalogIndex(buildCatalogIndex(published.items, governed.items), request.URL.Query().Get("q"))
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
		result := CatalogIndexResponse{
			Items: page, Counts: counts, NextCursor: nextCursor,
			Incomplete: len(published.items) == application.MaxCatalogSearchResults || len(governed.items) == governance.MaxImportRecords,
		}
		if view == "summary" {
			summaries := make([]CatalogIndexSummary, 0, len(page))
			for _, entry := range page {
				summaries = append(summaries, summarizeCatalogIndex(entry))
			}
			return writeJSON(response, http.StatusOK, struct {
				Items      []CatalogIndexSummary `json:"items"`
				Counts     CatalogIndexCounts    `json:"counts"`
				NextCursor string                `json:"next_cursor,omitempty"`
				Incomplete bool                  `json:"incomplete,omitempty"`
			}{summaries, result.Counts, result.NextCursor, result.Incomplete})
		}
		return writeJSON(response, http.StatusOK, result)
	})
}

func (server *Server) handleCatalogDetail(response http.ResponseWriter, request *http.Request) {
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
	name := strings.TrimSpace(request.URL.Query().Get("name"))
	modelName := strings.TrimSpace(request.URL.Query().Get("model"))
	if namespace == "" || name == "" {
		server.writeProblem(response, request, http.StatusBadRequest, "invalid_request", "Invalid request", "namespace and name are required", "name")
		return
	}
	server.withTimeout(response, request, server.config.ControlTimeout, func(ctx context.Context) error {
		var published []application.MetricCatalogEntry
		var records []governance.MetricRecord
		if principal.Has(PermissionQuery) {
			var err error
			published, err = server.deps.CatalogSearch.SearchActive(ctx, application.QueryScope{
				Namespace: namespace,
				Context:   model.RequestContext{Tenant: principal.Tenant, Principal: principal.Subject, Roles: principal.Roles, RequestID: requestID(request)},
			}, name, application.MaxCatalogSearchResults)
			if err != nil {
				return err
			}
		}
		if principal.Has(PermissionManage) {
			var err error
			records, err = server.deps.Governance.List(ctx, namespace, name, governance.MaxImportRecords)
			if err != nil {
				return err
			}
		}
		for _, entry := range buildCatalogIndex(published, records) {
			if entry.Name == name && (modelName == "" || entry.ModelName == modelName) {
				return writeJSON(response, http.StatusOK, entry)
			}
		}
		server.writeProblem(response, request, http.StatusNotFound, "not_found", "Not found", "metric is not visible in this namespace", "name")
		return nil
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

func searchCatalogIndex(entries []CatalogIndexEntry, search string) []CatalogIndexEntry {
	query := strings.ToLower(strings.TrimSpace(search))
	if query == "" {
		return entries
	}
	result := make([]CatalogIndexEntry, 0)
	for _, entry := range entries {
		fields := []string{entry.ExternalCode, entry.Name, entry.DisplayName, entry.Description, entry.Owner,
			strings.Join(entry.Tags, " "), entry.FormulaSummary, entry.Origin, entry.CalculationKind, entry.GovernanceSearchText}
		if entry.AuthoritativeSource != nil {
			fields = append(fields, entry.AuthoritativeSource.Resource, entry.AuthoritativeSource.Field)
		}
		for _, field := range fields {
			if strings.Contains(strings.ToLower(field), query) {
				result = append(result, entry)
				break
			}
		}
	}
	return result
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
	entry.GovernanceSearchText = strings.Join([]string{definition.DisplayName, definition.Description, definition.Owner,
		string(definition.BusinessType), string(definition.SemanticReadiness), strings.Join(definition.Issues, " ")}, " ")
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
