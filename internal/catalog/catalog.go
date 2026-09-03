// Package catalog owns the transactional lifecycle of semantic model drafts
// and immutable releases. Analytical data never enters this package.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/model"
)

var (
	ErrNotFound       = errors.New("catalog item not found")
	ErrConflict       = errors.New("catalog revision conflict")
	ErrNotPublishable = errors.New("semantic model is not publishable")
)

type Draft struct {
	Namespace string               `json:"namespace"`
	Name      string               `json:"name"`
	Revision  int64                `json:"revision"`
	Source    model.SemanticSource `json:"source"`
	UpdatedBy string               `json:"updated_by"`
	UpdatedAt time.Time            `json:"updated_at"`
}

type Release struct {
	ID                  string                 `json:"id"`
	Namespace           string                 `json:"namespace"`
	Name                string                 `json:"name"`
	SourceRevision      int64                  `json:"source_revision"`
	ManifestFingerprint string                 `json:"manifest_fingerprint"`
	Manifest            model.SemanticManifest `json:"manifest"`
	CreatedBy           string                 `json:"created_by"`
	Note                string                 `json:"note"`
	CreatedAt           time.Time              `json:"created_at"`
}

type EventKind string

const (
	EventPublished EventKind = "published"
	EventRollback  EventKind = "rollback"
)

type ReleaseEvent struct {
	ID            int64     `json:"id"`
	Namespace     string    `json:"namespace"`
	Name          string    `json:"name"`
	Kind          EventKind `json:"kind"`
	FromReleaseID string    `json:"from_release_id,omitempty"`
	ToReleaseID   string    `json:"to_release_id"`
	Actor         string    `json:"actor"`
	Note          string    `json:"note"`
	CreatedAt     time.Time `json:"created_at"`
}

type SaveDraftInput struct {
	Namespace        string
	Source           model.SemanticSource
	Actor            string
	ExpectedRevision int64
}

type PublishInput struct {
	Namespace               string
	Name                    string
	ExpectedRevision        int64
	ExpectedActiveReleaseID string
	ReleaseID               string
	Manifest                model.SemanticManifest
	Actor                   string
	Note                    string
}

type ActivateInput struct {
	Namespace string
	Name      string
	ReleaseID string
	Actor     string
	Note      string
	Kind      EventKind
}

type Repository interface {
	SaveDraft(context.Context, SaveDraftInput) (Draft, error)
	GetDraft(context.Context, string, string) (Draft, error)
	Publish(context.Context, PublishInput) (Release, error)
	Activate(context.Context, ActivateInput) (Release, error)
	GetRelease(context.Context, string, string, string) (Release, error)
	GetActiveRelease(context.Context, string, string) (Release, error)
	ListReleases(context.Context, string, string) ([]Release, error)
	ListActiveReleases(context.Context, string) ([]Release, error)
	ListEvents(context.Context, string, string) ([]ReleaseEvent, error)
}

type Service struct {
	repository Repository
}

func NewService(repository Repository) (*Service, error) {
	if repository == nil {
		return nil, errors.New("catalog repository is required")
	}
	return &Service{repository: repository}, nil
}

// SaveDraft validates the strict semantic contract before preserving a new
// revision. A revision can be unverified; publication applies the stronger gate.
func (s *Service) SaveDraft(ctx context.Context, input SaveDraftInput) (Draft, error) {
	if err := validateCommand(input.Namespace, input.Actor); err != nil {
		return Draft{}, err
	}
	if input.ExpectedRevision < 0 {
		return Draft{}, fmt.Errorf("%w: expected revision cannot be negative", ErrConflict)
	}
	if _, err := compiler.Compile(input.Source); err != nil {
		return Draft{}, fmt.Errorf("validate draft: %w", err)
	}
	return s.repository.SaveDraft(ctx, input)
}

// Publish compiles the exact expected draft revision and atomically makes the
// resulting immutable release active.
func (s *Service) Publish(ctx context.Context, namespace, name string, expectedRevision int64, actor, note string) (Release, error) {
	if err := validateCommand(namespace, actor); err != nil {
		return Release{}, err
	}
	if strings.TrimSpace(name) == "" || expectedRevision < 1 {
		return Release{}, fmt.Errorf("%w: name and positive expected revision are required", ErrConflict)
	}
	draft, err := s.repository.GetDraft(ctx, namespace, name)
	if err != nil {
		return Release{}, err
	}
	if draft.Revision != expectedRevision {
		return Release{}, fmt.Errorf("%w: draft revision is %d, expected %d", ErrConflict, draft.Revision, expectedRevision)
	}
	manifest, err := compiler.Compile(draft.Source)
	if err != nil {
		return Release{}, fmt.Errorf("compile release: %w", err)
	}
	if err := validatePublishable(manifest); err != nil {
		return Release{}, err
	}
	activeReleaseID := ""
	active, err := s.repository.GetActiveRelease(ctx, namespace, name)
	switch {
	case err == nil:
		activeReleaseID = active.ID
		if err := validateCompatibleRelease(active.Manifest, manifest); err != nil {
			return Release{}, err
		}
	case !errors.Is(err, ErrNotFound):
		return Release{}, fmt.Errorf("get active release for compatibility check: %w", err)
	}
	id := releaseID(manifest.Fingerprint, expectedRevision)
	return s.repository.Publish(ctx, PublishInput{
		Namespace: namespace, Name: name, ExpectedRevision: expectedRevision,
		ExpectedActiveReleaseID: activeReleaseID,
		ReleaseID:               id, Manifest: manifest, Actor: actor, Note: strings.TrimSpace(note),
	})
}

func (s *Service) Rollback(ctx context.Context, namespace, name, releaseID, actor, note string) (Release, error) {
	if err := validateCommand(namespace, actor); err != nil {
		return Release{}, err
	}
	if strings.TrimSpace(name) == "" || strings.TrimSpace(releaseID) == "" {
		return Release{}, fmt.Errorf("%w: name and release ID are required", ErrNotFound)
	}
	return s.repository.Activate(ctx, ActivateInput{
		Namespace: namespace, Name: name, ReleaseID: releaseID, Actor: actor,
		Note: strings.TrimSpace(note), Kind: EventRollback,
	})
}

func validatePublishable(manifest model.SemanticManifest) error {
	for _, metric := range manifest.Definitions.Metrics {
		if metric.Verification.Status != model.VerificationVerified {
			return fmt.Errorf("%w: metric %q is not verified", ErrNotPublishable, metric.Name)
		}
		if strings.TrimSpace(metric.DisplayName) == "" {
			return fmt.Errorf("%w: metric %q has no display name", ErrNotPublishable, metric.Name)
		}
		if strings.TrimSpace(metric.Description) == "" {
			return fmt.Errorf("%w: metric %q has no business definition", ErrNotPublishable, metric.Name)
		}
		if strings.TrimSpace(metric.Owner) == "" {
			return fmt.Errorf("%w: metric %q has no owner", ErrNotPublishable, metric.Name)
		}
		if len(metric.Verification.Evidence) == 0 {
			return fmt.Errorf("%w: metric %q has no verification evidence", ErrNotPublishable, metric.Name)
		}
		for _, evidence := range metric.Verification.Evidence {
			if strings.TrimSpace(evidence) == "" {
				return fmt.Errorf("%w: metric %q has empty verification evidence", ErrNotPublishable, metric.Name)
			}
		}
	}
	return nil
}

// validateCompatibleRelease keeps a published metric code stable. Descriptive
// metadata may evolve, but execution semantics cannot change under the same
// code. Deprecation is explicit and monotonic; removal requires a future
// versioned compatibility policy instead of silently breaking consumers.
func validateCompatibleRelease(active, candidate model.SemanticManifest) error {
	candidateMetrics := make(map[string]model.Metric, len(candidate.Definitions.Metrics))
	for _, metric := range candidate.Definitions.Metrics {
		candidateMetrics[metric.Name] = metric
	}
	for _, previous := range active.Definitions.Metrics {
		next, exists := candidateMetrics[previous.Name]
		if !exists {
			return fmt.Errorf("%w: published metric %q cannot be removed; deprecate it explicitly", ErrNotPublishable, previous.Name)
		}
		if previous.Deprecated && !next.Deprecated {
			return fmt.Errorf("%w: published metric %q cannot be undeprecated", ErrNotPublishable, previous.Name)
		}
		if !sameMetricExecutionContract(previous, next) {
			return fmt.Errorf("%w: published metric %q cannot change execution semantics", ErrNotPublishable, previous.Name)
		}
	}
	return nil
}

func sameMetricExecutionContract(left, right model.Metric) bool {
	type executionContract struct {
		Entity            string
		Kind              model.MetricKind
		ValueType         model.DataType
		Unit              string
		Expression        model.Expression
		AllowedDimensions []string
		TimeDimension     string
	}
	contract := func(metric model.Metric) executionContract {
		return executionContract{
			Entity: metric.Entity, Kind: metric.Kind, ValueType: metric.ValueType,
			Unit: metric.Unit, Expression: metric.Expression,
			AllowedDimensions: metric.AllowedDimensions, TimeDimension: metric.TimeDimension,
		}
	}
	return reflect.DeepEqual(contract(left), contract(right))
}

func validateCommand(namespace, actor string) error {
	if strings.TrimSpace(namespace) == "" {
		return errors.New("namespace is required")
	}
	if strings.TrimSpace(actor) == "" {
		return errors.New("actor is required")
	}
	return nil
}

func releaseID(fingerprint string, revision int64) string {
	value := strings.TrimPrefix(fingerprint, "sha256:")
	if len(value) > 20 {
		value = value[:20]
	}
	return fmt.Sprintf("rel_%s_r%d", value, revision)
}
