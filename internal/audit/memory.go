package audit

import (
	"context"
	"sync"
)

type MemoryRecorder struct {
	mu     sync.RWMutex
	events []QueryEvent
	err    error
}

func NewMemoryRecorder() *MemoryRecorder { return &MemoryRecorder{} }

func (recorder *MemoryRecorder) Record(_ context.Context, event QueryEvent) error {
	if err := event.Validate(); err != nil {
		return err
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.err != nil {
		return recorder.err
	}
	recorder.events = append(recorder.events, event)
	return nil
}

func (recorder *MemoryRecorder) Events() []QueryEvent {
	recorder.mu.RLock()
	defer recorder.mu.RUnlock()
	return append([]QueryEvent(nil), recorder.events...)
}

func (recorder *MemoryRecorder) SetError(err error) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.err = err
}
