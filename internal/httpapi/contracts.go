// Package httpapi exposes MetricSpire's authenticated product HTTP boundary.
package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/catalog"
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
}

type QueryService interface {
	ExplainActive(context.Context, application.QueryInput) (application.ExplainOutput, error)
	PlanActive(context.Context, application.QueryInput) (application.PlanOutput, error)
	ExecuteActive(context.Context, application.QueryInput) (application.QueryOutput, error)
}

type JobService interface {
	Submit(application.QueryInput) (application.QueryJobSnapshot, error)
	Get(string, string, string) (application.QueryJobSnapshot, error)
	Cancel(string, string, string) (application.QueryJobSnapshot, error)
}

type Dependencies struct {
	Authenticator Authenticator
	AuthEndpoints http.Handler
	Readiness     ReadinessChecker
	Management    ManagementService
	Catalog       CatalogReader
	CatalogSearch CatalogSearcher
	Queries       QueryService
	Jobs          JobService
}

type Config struct {
	MaxBodyBytes   int64
	ControlTimeout time.Duration
	QueryTimeout   time.Duration
	AllowedOrigin  string
	RequestID      func() string
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

type PublishRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Note             string `json:"note,omitempty"`
}

type RollbackRequest struct {
	ReleaseID string `json:"release_id"`
	Note      string `json:"note,omitempty"`
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
