// Package governance stores source-backed metric records before they are safe
// enough to become executable semantic definitions.
package governance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/model"
)

var (
	ErrNotFound = errors.New("governance resource not found")
	ErrConflict = errors.New("governance import conflict")
	ErrInvalid  = errors.New("invalid governance input")
)

const MaxImportRecords = 1000

type BusinessType string

const (
	BusinessAtomic    BusinessType = "atomic"
	BusinessDerived   BusinessType = "derived"
	BusinessComposite BusinessType = "composite"
)

type SemanticReadiness string

const (
	ReadinessExecutableUnverified  SemanticReadiness = "executable_unverified"
	ReadinessNeedsDefinitionReview SemanticReadiness = "needs_definition_review"
	ReadinessNeedsRemediation      SemanticReadiness = "needs_semantic_remediation"
)

type SourceReference struct {
	Reference string `json:"reference"`
	Resource  string `json:"resource"`
	Field     string `json:"field"`
	Relation  string `json:"relation,omitempty"`
}

type Approval struct {
	ConfirmedBy string `json:"confirmed_by"`
	ConfirmedOn string `json:"confirmed_on"`
	Decision    string `json:"decision"`
	EvidenceRef string `json:"evidence_ref"`
}

// MetricDefinition is the provider-neutral governance record. It deliberately
// does not require an executable expression: unresolved metrics must remain
// visible without encouraging importers to invent SQL or formulas.
type MetricDefinition struct {
	Code                string             `json:"code"`
	DisplayName         string             `json:"display_name"`
	Description         string             `json:"description"`
	Origin              string             `json:"origin,omitempty"`
	BusinessType        BusinessType       `json:"business_type"`
	DocumentedFormula   *string            `json:"documented_formula,omitempty"`
	ServingFormula      *string            `json:"serving_formula,omitempty"`
	Unit                string             `json:"unit,omitempty"`
	AuthoritativeSource SourceReference    `json:"authoritative_source"`
	SourceReferences    []SourceReference  `json:"source_references,omitempty"`
	TimeDimension       string             `json:"time_dimension,omitempty"`
	Status              string             `json:"status"`
	ReadinessReason     string             `json:"readiness_reason,omitempty"`
	CodeReason          string             `json:"code_reason,omitempty"`
	Issues              []string           `json:"issues,omitempty"`
	Owner               string             `json:"owner"`
	OwnerStatus         string             `json:"owner_status,omitempty"`
	FactGrain           string             `json:"fact_grain,omitempty"`
	TimezonePolicy      string             `json:"timezone_policy,omitempty"`
	CalculationKind     string             `json:"calculation_kind,omitempty"`
	ValueType           model.DataType     `json:"value_type"`
	Verification        model.Verification `json:"verification"`
	SemanticReadiness   SemanticReadiness  `json:"semantic_readiness"`
	TestDimensions      []string           `json:"test_dimensions,omitempty"`
	Approval            *Approval          `json:"approval,omitempty"`
	SemanticModelName   string             `json:"semantic_model_name,omitempty"`
}

type MetricRecord struct {
	Namespace      string           `json:"namespace"`
	Revision       int64            `json:"revision"`
	SourceImportID string           `json:"source_import_id"`
	UpdatedBy      string           `json:"updated_by"`
	UpdatedAt      time.Time        `json:"updated_at"`
	Definition     MetricDefinition `json:"definition"`
}

type ImportInput struct {
	Namespace                string             `json:"namespace"`
	SourceFingerprint        string             `json:"source_fingerprint"`
	ExpectedPreviousImportID string             `json:"expected_previous_import_id,omitempty"`
	Records                  []MetricDefinition `json:"records"`
	Actor                    string             `json:"-"`
}

type ImportBatch struct {
	ID                string     `json:"id"`
	Namespace         string     `json:"namespace"`
	SourceFingerprint string     `json:"source_fingerprint"`
	PreviousImportID  string     `json:"previous_import_id,omitempty"`
	RecordCount       int        `json:"record_count"`
	CreatedBy         string     `json:"created_by"`
	CreatedAt         time.Time  `json:"created_at"`
	RolledBackBy      string     `json:"rolled_back_by,omitempty"`
	RolledBackAt      *time.Time `json:"rolled_back_at,omitempty"`
}

type Repository interface {
	Import(context.Context, string, ImportInput) (ImportBatch, error)
	List(context.Context, string, string, int) ([]MetricRecord, error)
	GetImport(context.Context, string, string) (ImportBatch, error)
	RollbackImport(context.Context, string, string, string) (ImportBatch, error)
}

type Service struct {
	repository Repository
	newID      func() string
}

func NewService(repository Repository) (*Service, error) {
	if repository == nil {
		return nil, errors.New("governance repository is required")
	}
	return &Service{repository: repository, newID: newImportID}, nil
}

func (service *Service) Import(ctx context.Context, input ImportInput) (ImportBatch, error) {
	input.Namespace = strings.TrimSpace(input.Namespace)
	input.Actor = strings.TrimSpace(input.Actor)
	input.SourceFingerprint = strings.TrimSpace(input.SourceFingerprint)
	input.ExpectedPreviousImportID = strings.TrimSpace(input.ExpectedPreviousImportID)
	if input.Namespace == "" || input.Actor == "" {
		return ImportBatch{}, fmt.Errorf("%w: namespace and actor are required", ErrInvalid)
	}
	if !fingerprintPattern.MatchString(input.SourceFingerprint) {
		return ImportBatch{}, fmt.Errorf("%w: source fingerprint must be sha256:<64 lowercase hex characters>", ErrInvalid)
	}
	if len(input.Records) == 0 || len(input.Records) > MaxImportRecords {
		return ImportBatch{}, fmt.Errorf("%w: governance import must contain between 1 and %d records", ErrInvalid, MaxImportRecords)
	}
	seen := make(map[string]struct{}, len(input.Records))
	for index := range input.Records {
		if err := validateDefinition(&input.Records[index]); err != nil {
			return ImportBatch{}, fmt.Errorf("%w: records[%d]: %v", ErrInvalid, index, err)
		}
		if _, exists := seen[input.Records[index].Code]; exists {
			return ImportBatch{}, fmt.Errorf("%w: records[%d]: duplicate metric code %q", ErrInvalid, index, input.Records[index].Code)
		}
		seen[input.Records[index].Code] = struct{}{}
	}
	sort.Slice(input.Records, func(i, j int) bool { return input.Records[i].Code < input.Records[j].Code })
	return service.repository.Import(ctx, service.newID(), input)
}

func (service *Service) List(ctx context.Context, namespace, search string, limit int) ([]MetricRecord, error) {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return nil, fmt.Errorf("%w: namespace is required", ErrInvalid)
	}
	if limit < 1 || limit > MaxImportRecords {
		return nil, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalid, MaxImportRecords)
	}
	return service.repository.List(ctx, namespace, strings.TrimSpace(search), limit)
}

func (service *Service) GetImport(ctx context.Context, namespace, importID string) (ImportBatch, error) {
	namespace, importID = strings.TrimSpace(namespace), strings.TrimSpace(importID)
	if namespace == "" || importID == "" {
		return ImportBatch{}, fmt.Errorf("%w: namespace and import ID are required", ErrInvalid)
	}
	return service.repository.GetImport(ctx, namespace, importID)
}

func (service *Service) RollbackImport(ctx context.Context, namespace, importID, actor string) (ImportBatch, error) {
	namespace, importID, actor = strings.TrimSpace(namespace), strings.TrimSpace(importID), strings.TrimSpace(actor)
	if namespace == "" || importID == "" || actor == "" {
		return ImportBatch{}, fmt.Errorf("%w: namespace, import ID, and actor are required", ErrInvalid)
	}
	return service.repository.RollbackImport(ctx, namespace, importID, actor)
}

var (
	codePattern        = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)
	fingerprintPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

func validateDefinition(definition *MetricDefinition) error {
	definition.Code = strings.TrimSpace(definition.Code)
	definition.DisplayName = strings.TrimSpace(definition.DisplayName)
	definition.Description = strings.TrimSpace(definition.Description)
	definition.Owner = strings.TrimSpace(definition.Owner)
	definition.Status = strings.TrimSpace(definition.Status)
	definition.SemanticModelName = strings.TrimSpace(definition.SemanticModelName)
	if !codePattern.MatchString(definition.Code) {
		return errors.New("code must start with a lowercase letter and contain only lowercase letters, digits, and underscores")
	}
	if definition.DisplayName == "" || definition.Description == "" || definition.Owner == "" || definition.Status == "" {
		return errors.New("display_name, description, owner, and status are required")
	}
	switch definition.BusinessType {
	case BusinessAtomic, BusinessDerived, BusinessComposite:
	default:
		return fmt.Errorf("unsupported business_type %q", definition.BusinessType)
	}
	switch definition.SemanticReadiness {
	case ReadinessExecutableUnverified, ReadinessNeedsDefinitionReview, ReadinessNeedsRemediation:
	default:
		return fmt.Errorf("unsupported semantic_readiness %q", definition.SemanticReadiness)
	}
	if definition.AuthoritativeSource.Reference == "" || definition.AuthoritativeSource.Resource == "" || definition.AuthoritativeSource.Field == "" {
		return errors.New("authoritative_source reference, resource, and field are required")
	}
	if definition.Verification.Status != model.VerificationUnverified && definition.Verification.Status != model.VerificationVerified {
		return fmt.Errorf("unsupported verification status %q", definition.Verification.Status)
	}
	if definition.SemanticReadiness == ReadinessExecutableUnverified && definition.SemanticModelName == "" {
		return errors.New("executable_unverified record requires semantic_model_name")
	}
	if definition.SemanticReadiness != ReadinessExecutableUnverified && definition.SemanticModelName != "" {
		return errors.New("non-executable record cannot name a semantic model")
	}
	return nil
}

func newImportID() string {
	data := make([]byte, 12)
	if _, err := rand.Read(data); err != nil {
		return fmt.Sprintf("gvi_%d", time.Now().UnixNano())
	}
	return "gvi_" + hex.EncodeToString(data)
}
