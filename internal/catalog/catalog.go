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

type ReleaseChannel string

const (
	ReleaseChannelCertified ReleaseChannel = "certified"
	ReleaseChannelTrial     ReleaseChannel = "trial"
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
	Channel             ReleaseChannel         `json:"channel"`
	CreatedBy           string                 `json:"created_by"`
	Note                string                 `json:"note"`
	CreatedAt           time.Time              `json:"created_at"`
}

type EventKind string

const (
	EventPublished   EventKind = "published"
	EventRollback    EventKind = "rollback"
	EventDeactivated EventKind = "deactivated"
)

type ReleaseEvent struct {
	ID            int64     `json:"id"`
	Namespace     string    `json:"namespace"`
	Name          string    `json:"name"`
	Kind          EventKind `json:"kind"`
	FromReleaseID string    `json:"from_release_id,omitempty"`
	ToReleaseID   string    `json:"to_release_id,omitempty"`
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
	Channel                 ReleaseChannel
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

type DeactivateInput struct {
	Namespace string
	Name      string
	Actor     string
	Note      string
}

type Repository interface {
	SaveDraft(context.Context, SaveDraftInput) (Draft, error)
	GetDraft(context.Context, string, string) (Draft, error)
	Publish(context.Context, PublishInput) (Release, error)
	Activate(context.Context, ActivateInput) (Release, error)
	Deactivate(context.Context, DeactivateInput) (Release, error)
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
	return s.publish(ctx, namespace, name, expectedRevision, actor, note, ReleaseChannelCertified)
}

// PublishTrial permits an explicitly configured trial deployment to expose
// executable, unverified definitions without changing their verification status.
// The transport boundary decides which namespaces may use this channel.
func (s *Service) PublishTrial(ctx context.Context, namespace, name string, expectedRevision int64, actor, note string) (Release, error) {
	return s.publish(ctx, namespace, name, expectedRevision, actor, note, ReleaseChannelTrial)
}

func (s *Service) publish(ctx context.Context, namespace, name string, expectedRevision int64, actor, note string, channel ReleaseChannel) (Release, error) {
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
	switch channel {
	case ReleaseChannelTrial:
		if issues := publicationIssues(manifest, false); len(issues) > 0 {
			return Release{}, fmt.Errorf("%w: %s", ErrNotPublishable, issues[0])
		}
	case ReleaseChannelCertified:
		if err := validatePublishable(manifest); err != nil {
			return Release{}, err
		}
	default:
		return Release{}, fmt.Errorf("%w: unsupported release channel %q", ErrNotPublishable, channel)
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
		ReleaseID:               id, Manifest: manifest, Channel: channel,
		Actor: actor, Note: strings.TrimSpace(note),
	})
}

func IsTrialRelease(release Release) bool {
	return release.Channel == ReleaseChannelTrial
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

// Deactivate removes the active pointer without deleting immutable releases.
// A later rollback may deliberately reactivate a historical release.
func (s *Service) Deactivate(ctx context.Context, namespace, name, actor, note string) (Release, error) {
	if err := validateCommand(namespace, actor); err != nil {
		return Release{}, err
	}
	if strings.TrimSpace(name) == "" || strings.TrimSpace(note) == "" {
		return Release{}, fmt.Errorf("%w: name and deactivation note are required", ErrConflict)
	}
	return s.repository.Deactivate(ctx, DeactivateInput{
		Namespace: namespace, Name: name, Actor: actor, Note: strings.TrimSpace(note),
	})
}

func validatePublishable(manifest model.SemanticManifest) error {
	issues := PublicationIssues(manifest)
	if len(issues) > 0 {
		return fmt.Errorf("%w: %s", ErrNotPublishable, issues[0])
	}
	return nil
}

// PublicationIssues returns every missing governance field so a review UI can
// explain the full correction set before the authoritative publish attempt.
func PublicationIssues(manifest model.SemanticManifest) []string {
	return publicationIssues(manifest, true)
}

func publicationIssues(manifest model.SemanticManifest, requireVerified bool) []string {
	issues := make([]string, 0)
	for _, metric := range manifest.Definitions.Metrics {
		if requireVerified && metric.Verification.Status != model.VerificationVerified {
			issues = append(issues, fmt.Sprintf("metric %q is not verified", metric.Name))
		}
		if strings.TrimSpace(metric.DisplayName) == "" {
			issues = append(issues, fmt.Sprintf("metric %q has no display name", metric.Name))
		}
		if strings.TrimSpace(metric.Description) == "" {
			issues = append(issues, fmt.Sprintf("metric %q has no business definition", metric.Name))
		}
		if strings.TrimSpace(metric.Owner) == "" {
			issues = append(issues, fmt.Sprintf("metric %q has no owner", metric.Name))
		}
		if len(metric.Verification.Evidence) == 0 {
			issues = append(issues, fmt.Sprintf("metric %q has no verification evidence", metric.Name))
		}
		for _, evidence := range metric.Verification.Evidence {
			if strings.TrimSpace(evidence) == "" {
				issues = append(issues, fmt.Sprintf("metric %q has empty verification evidence", metric.Name))
				break
			}
		}
	}
	return issues
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
