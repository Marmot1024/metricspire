package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/marmot1024/metricspire/internal/compiler"
)

// MemoryRepository is a deterministic test and local-demo implementation. It
// is not a production control store and intentionally has no persistence.
type MemoryRepository struct {
	mu       sync.RWMutex
	drafts   map[string]Draft
	releases map[string]Release
	active   map[string]string
	events   map[string][]ReleaseEvent
	now      func() time.Time
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		drafts: make(map[string]Draft), releases: make(map[string]Release),
		active: make(map[string]string), events: make(map[string][]ReleaseEvent),
		now: time.Now,
	}
}

func (r *MemoryRepository) SaveDraft(_ context.Context, input SaveDraftInput) (Draft, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := catalogKey(input.Namespace, input.Source.Metadata.Name)
	current, exists := r.drafts[key]
	if (!exists && input.ExpectedRevision != 0) || (exists && current.Revision != input.ExpectedRevision) {
		return Draft{}, fmt.Errorf("%w: expected revision %d", ErrConflict, input.ExpectedRevision)
	}
	draft := Draft{
		Namespace: input.Namespace, Name: input.Source.Metadata.Name,
		Revision: input.ExpectedRevision + 1, Source: clone(input.Source),
		UpdatedBy: input.Actor, UpdatedAt: r.now().UTC(),
	}
	r.drafts[key] = draft
	return clone(draft), nil
}

func (r *MemoryRepository) GetDraft(_ context.Context, namespace, name string) (Draft, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	draft, exists := r.drafts[catalogKey(namespace, name)]
	if !exists {
		return Draft{}, ErrNotFound
	}
	return clone(draft), nil
}

func (r *MemoryRepository) Publish(_ context.Context, input PublishInput) (Release, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := catalogKey(input.Namespace, input.Name)
	draft, exists := r.drafts[key]
	if !exists {
		return Release{}, ErrNotFound
	}
	if draft.Revision != input.ExpectedRevision {
		return Release{}, fmt.Errorf("%w: draft changed before publish", ErrConflict)
	}
	storedManifest, err := compiler.Compile(draft.Source)
	if err != nil {
		return Release{}, fmt.Errorf("compile draft for publish: %w", err)
	}
	if storedManifest.Fingerprint != input.Manifest.Fingerprint {
		return Release{}, fmt.Errorf("%w: release manifest does not match draft revision", ErrConflict)
	}
	if r.active[key] != input.ExpectedActiveReleaseID {
		return Release{}, fmt.Errorf("%w: active release changed before publish", ErrConflict)
	}
	if _, exists := r.releases[releaseKey(key, input.ReleaseID)]; exists {
		return Release{}, fmt.Errorf("%w: release %s already exists", ErrConflict, input.ReleaseID)
	}
	now := r.now().UTC()
	release := Release{
		ID: input.ReleaseID, Namespace: input.Namespace, Name: input.Name,
		SourceRevision: input.ExpectedRevision, ManifestFingerprint: input.Manifest.Fingerprint,
		Manifest: clone(input.Manifest), CreatedBy: input.Actor, Note: input.Note, CreatedAt: now,
	}
	previous := r.active[key]
	r.releases[releaseKey(key, release.ID)] = release
	r.active[key] = release.ID
	r.appendEvent(key, ReleaseEvent{
		Namespace: input.Namespace, Name: input.Name, Kind: EventPublished,
		FromReleaseID: previous, ToReleaseID: release.ID, Actor: input.Actor, Note: input.Note, CreatedAt: now,
	})
	return clone(release), nil
}

func (r *MemoryRepository) Activate(_ context.Context, input ActivateInput) (Release, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := catalogKey(input.Namespace, input.Name)
	release, exists := r.releases[releaseKey(key, input.ReleaseID)]
	if !exists {
		return Release{}, ErrNotFound
	}
	previous := r.active[key]
	if previous == input.ReleaseID {
		return Release{}, fmt.Errorf("%w: release %s is already active", ErrConflict, input.ReleaseID)
	}
	r.active[key] = input.ReleaseID
	r.appendEvent(key, ReleaseEvent{
		Namespace: input.Namespace, Name: input.Name, Kind: input.Kind,
		FromReleaseID: previous, ToReleaseID: input.ReleaseID, Actor: input.Actor,
		Note: input.Note, CreatedAt: r.now().UTC(),
	})
	return clone(release), nil
}

func (r *MemoryRepository) GetRelease(_ context.Context, namespace, name, id string) (Release, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	release, exists := r.releases[releaseKey(catalogKey(namespace, name), id)]
	if !exists {
		return Release{}, ErrNotFound
	}
	return clone(release), nil
}

func (r *MemoryRepository) GetActiveRelease(ctx context.Context, namespace, name string) (Release, error) {
	r.mu.RLock()
	id := r.active[catalogKey(namespace, name)]
	r.mu.RUnlock()
	if id == "" {
		return Release{}, ErrNotFound
	}
	return r.GetRelease(ctx, namespace, name, id)
}

func (r *MemoryRepository) ListReleases(_ context.Context, namespace, name string) ([]Release, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	prefix := catalogKey(namespace, name) + "\x00"
	result := make([]Release, 0)
	for key, release := range r.releases {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			result = append(result, clone(release))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result, nil
}

func (r *MemoryRepository) ListActiveReleases(_ context.Context, namespace string) ([]Release, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	prefix := namespace + "\x00"
	result := make([]Release, 0)
	for key, id := range r.active {
		if len(key) < len(prefix) || key[:len(prefix)] != prefix {
			continue
		}
		if release, exists := r.releases[releaseKey(key, id)]; exists {
			result = append(result, clone(release))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (r *MemoryRepository) ListEvents(_ context.Context, namespace, name string) ([]ReleaseEvent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]ReleaseEvent(nil), r.events[catalogKey(namespace, name)]...), nil
}

func (r *MemoryRepository) appendEvent(key string, event ReleaseEvent) {
	event.ID = int64(len(r.events[key]) + 1)
	r.events[key] = append(r.events[key], event)
}

func catalogKey(namespace, name string) string { return namespace + "\x00" + name }
func releaseKey(catalog, id string) string     { return catalog + "\x00" + id }

func clone[T any](value T) T {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		panic(err)
	}
	return result
}
