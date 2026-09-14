// Package httpapi exposes MetricSpire's authenticated product HTTP boundary.
package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/governance"
	"github.com/marmot1024/metricspire/internal/model"
)

const (
	APIPrefix = "/api/v1"

	DefaultMaxBodyBytes   int64 = 1 << 20
	DefaultControlTimeout       = 15 * time.Second
	DefaultQueryTimeout         = 2 * time.Minute
)

type Permission string

const (
	PermissionManage Permission = "model:manage"
	PermissionQuery  Permission = "query:execute"
)

// Principal is trusted output from an authentication adapter. HTTP request
// bodies have no field that can populate it.
type Principal struct {
	Tenant      string
	Subject     string
	DisplayName string
	Roles       []string
	Permissions []Permission
}

func (principal Principal) Has(permission Permission) bool {
	for _, granted := range principal.Permissions {
		if granted == permission {
			return true
		}
	}
	return false
}

type Authenticator interface {
	Authenticate(context.Context, *http.Request) (Principal, error)
}

type AuthenticatorFunc func(context.Context, *http.Request) (Principal, error)

func (function AuthenticatorFunc) Authenticate(ctx context.Context, request *http.Request) (Principal, error) {
	return function(ctx, request)
}

type ReadinessChecker interface {
	Ready(context.Context) error
}

type ReadinessFunc func(context.Context) error

func (function ReadinessFunc) Ready(ctx context.Context) error { return function(ctx) }

type ManagementService interface {
	SaveDraft(context.Context, catalog.SaveDraftInput) (catalog.Draft, error)
	Publish(context.Context, string, string, int64, string, string) (catalog.Release, error)
	Rollback(context.Context, string, string, string, string, string) (catalog.Release, error)
}

type CatalogReader interface {
	GetDraft(context.Context, string, string) (catalog.Draft, error)
	GetActiveRelease(context.Context, string, string) (catalog.Release, error)
	ListReleases(context.Context, string, string) ([]catalog.Release, error)
}

type CatalogSearcher interface {
	SearchActive(context.Context, application.QueryScope, string, int) ([]application.MetricCatalogEntry, error)
	ResolveActiveModel(context.Context, application.QueryScope, []string) (string, error)
}

type QueryService interface {
	ExplainActive(context.Context, application.QueryInput) (application.ExplainOutput, error)
	PlanActive(context.Context, application.QueryInput) (application.PlanOutput, error)
	ExecuteActive(context.Context, application.QueryInput) (application.QueryOutput, error)
	ExplainDraft(context.Context, application.QueryInput) (application.ExplainOutput, error)
	PlanDraft(context.Context, application.QueryInput) (application.PlanOutput, error)
	ExecuteDraft(context.Context, application.QueryInput) (application.QueryOutput, error)
}

type GovernanceService interface {
	Import(context.Context, governance.ImportInput) (governance.ImportBatch, error)
	List(context.Context, string, string, int) ([]governance.MetricRecord, error)
	GetImport(context.Context, string, string) (governance.ImportBatch, error)
	RollbackImport(context.Context, string, string, string) (governance.ImportBatch, error)
}

type JobService interface {
	Submit(context.Context, application.QueryInput) (application.QueryJobSnapshot, error)
	Get(string, string, string) (application.QueryJobSnapshot, error)
	Cancel(string, string, string) (application.QueryJobSnapshot, error)
}

// ExecutionCredentialProvider adds a short-lived analytical-engine credential
// to the context of an authenticated query request. Explain and plan never call
// it because they do not execute against the analytical engine.
type ExecutionCredentialProvider interface {
	AddToContext(context.Context, *http.Request, Principal) (context.Context, error)
}

type ExecutionCredentialFunc func(context.Context, *http.Request, Principal) (context.Context, error)

func (function ExecutionCredentialFunc) AddToContext(ctx context.Context, request *http.Request, principal Principal) (context.Context, error) {
	return function(ctx, request, principal)
}

type Dependencies struct {
	Authenticator       Authenticator
	AuthEndpoints       http.Handler
	Readiness           ReadinessChecker
	Management          ManagementService
	Catalog             CatalogReader
	CatalogSearch       CatalogSearcher
	Governance          GovernanceService
	Bindings            application.BindingResolver
	Queries             QueryService
	Jobs                JobService
	ExecutionCredential ExecutionCredentialProvider
}

type Config struct {
	MaxBodyBytes           int64
	ControlTimeout         time.Duration
	QueryTimeout           time.Duration
	AllowedOrigin          string
	MCPAuthorizationServer string
	AuthenticationProfile  string
	MCPVersion             string
	UIModels               []UIModelRoute
	RequestID              func() string
}

// UIModelRoute is the small, non-secret route list required by the embedded
// product UI. Physical bindings and policy contents are never exposed.
type UIModelRoute struct {
	Namespace string `json:"namespace"`
	ModelName string `json:"model_name"`
}

type UIContext struct {
	AuthenticationProfile string         `json:"authentication_profile"`
	DisplayName           string         `json:"display_name"`
	Permissions           []Permission   `json:"permissions"`
	Namespaces            []string       `json:"namespaces"`
	Models                []UIModelRoute `json:"models,omitempty"`
}

type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail"`
	Code      string `json:"code"`
	Path      string `json:"path,omitempty"`
	RequestID string `json:"request_id"`
}

type SaveDraftRequest struct {
	ExpectedRevision int64                `json:"expected_revision"`
	Source           model.SemanticSource `json:"source"`
}

type ValidateRequest struct {
	Source model.SemanticSource `json:"source"`
}

type ReviewRequest struct {
	Source model.SemanticSource `json:"source"`
}

type PublishRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Note             string `json:"note,omitempty"`
}

type RollbackRequest struct {
	ReleaseID string `json:"release_id"`
	Note      string `json:"note,omitempty"`
}

type GovernanceImportRequest struct {
	SourceFingerprint        string                        `json:"source_fingerprint"`
	ExpectedPreviousImportID string                        `json:"expected_previous_import_id,omitempty"`
	Records                  []governance.MetricDefinition `json:"records"`
}

type ReleaseSummary struct {
	ID                  string    `json:"id"`
	SourceRevision      int64     `json:"source_revision"`
	ManifestFingerprint string    `json:"manifest_fingerprint"`
	Active              bool      `json:"active"`
	CreatedBy           string    `json:"created_by"`
	Note                string    `json:"note"`
	CreatedAt           time.Time `json:"created_at"`
}
