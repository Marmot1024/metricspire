package governance

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type memoryImportItem struct {
	code             string
	previousRevision int64
	appliedRevision  int64
}

type MemoryRepository struct {
	mu        sync.RWMutex
	records   map[string]MetricRecord
	revisions map[string]map[int64]MetricRecord
	batches   map[string]ImportBatch
	items     map[string][]memoryImportItem
	latest    map[string]string
	now       func() time.Time
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		records: make(map[string]MetricRecord), revisions: make(map[string]map[int64]MetricRecord),
		batches: make(map[string]ImportBatch), items: make(map[string][]memoryImportItem),
		latest: make(map[string]string), now: time.Now,
	}
}

func (repository *MemoryRepository) Import(_ context.Context, id string, input ImportInput) (ImportBatch, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.latest[input.Namespace] != input.ExpectedPreviousImportID {
		return ImportBatch{}, ErrConflict
	}
	now := repository.now().UTC()
	batch := ImportBatch{ID: id, Namespace: input.Namespace, SourceFingerprint: input.SourceFingerprint, PreviousImportID: input.ExpectedPreviousImportID, RecordCount: len(input.Records), CreatedBy: input.Actor, CreatedAt: now}
	for _, definition := range input.Records {
		key := recordKey(input.Namespace, definition.Code)
		previous := repository.records[key]
		if repository.revisions[key] == nil {
			repository.revisions[key] = make(map[int64]MetricRecord)
		}
		var maximum int64
		for revision := range repository.revisions[key] {
			if revision > maximum {
				maximum = revision
			}
		}
		revision := maximum + 1
		record := MetricRecord{Namespace: input.Namespace, Revision: revision, SourceImportID: id, UpdatedBy: input.Actor, UpdatedAt: now, Definition: clone(definition)}
		repository.records[key] = record
		repository.revisions[key][revision] = record
		repository.items[id] = append(repository.items[id], memoryImportItem{code: definition.Code, previousRevision: previous.Revision, appliedRevision: revision})
	}
	repository.batches[id] = batch
	repository.latest[input.Namespace] = id
	return batch, nil
}

func (repository *MemoryRepository) List(_ context.Context, namespace, search string, limit int) ([]MetricRecord, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	search = strings.ToLower(search)
	result := make([]MetricRecord, 0)
	for _, record := range repository.records {
		if record.Namespace != namespace || (search != "" && !recordMatches(record, search)) {
			continue
		}
		result = append(result, clone(record))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Definition.Code < result[j].Definition.Code })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (repository *MemoryRepository) GetImport(_ context.Context, namespace, id string) (ImportBatch, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	batch, exists := repository.batches[id]
	if !exists || batch.Namespace != namespace {
		return ImportBatch{}, ErrNotFound
	}
	return batch, nil
}

func (repository *MemoryRepository) RollbackImport(_ context.Context, namespace, id, actor string) (ImportBatch, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	batch, exists := repository.batches[id]
	if !exists || batch.Namespace != namespace {
		return ImportBatch{}, ErrNotFound
	}
	if batch.RolledBackAt != nil || repository.latest[namespace] != id {
		return ImportBatch{}, ErrConflict
	}
	for _, item := range repository.items[id] {
		if repository.records[recordKey(namespace, item.code)].Revision != item.appliedRevision {
			return ImportBatch{}, ErrConflict
		}
	}
	now := repository.now().UTC()
	for _, item := range repository.items[id] {
		key := recordKey(namespace, item.code)
		if item.previousRevision == 0 {
			delete(repository.records, key)
			continue
		}
		previous, exists := repository.revisions[key][item.previousRevision]
		if !exists {
			return ImportBatch{}, fmt.Errorf("%w: prior revision is missing", ErrConflict)
		}
		previous.UpdatedBy = actor
		previous.UpdatedAt = now
		repository.records[key] = previous
	}
	batch.RolledBackBy = actor
	batch.RolledBackAt = &now
	repository.batches[id] = batch
	repository.latest[namespace] = batch.PreviousImportID
	return batch, nil
}

func recordMatches(record MetricRecord, search string) bool {
	definition := record.Definition
	text := strings.ToLower(strings.Join([]string{definition.Code, definition.DisplayName, definition.Description, definition.Owner, string(definition.BusinessType), string(definition.SemanticReadiness), strings.Join(definition.Issues, " ")}, " "))
	return strings.Contains(text, search)
}

func recordKey(namespace, code string) string { return namespace + "\x00" + code }

func clone[T any](value T) T {
	payload, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var result T
	if err := json.Unmarshal(payload, &result); err != nil {
		panic(err)
	}
	return result
}
