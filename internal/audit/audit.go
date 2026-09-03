// Package audit defines structured, non-sensitive query audit events.
package audit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrUnavailable = errors.New("query audit is unavailable")

type EventKind string

const (
	EventQueryStarted   EventKind = "query_started"
	EventQuerySucceeded EventKind = "query_succeeded"
	EventQueryFailed    EventKind = "query_failed"
	EventQueryCancelled EventKind = "query_cancelled"
)

// QueryEvent deliberately excludes SQL, filter values, credentials, and result
// rows. Fingerprints preserve traceability without copying sensitive payloads.
type QueryEvent struct {
	RequestID           string    `json:"request_id"`
	Tenant              string    `json:"tenant"`
	Principal           string    `json:"principal"`
	Namespace           string    `json:"namespace"`
	ModelName           string    `json:"model_name"`
	ReleaseID           string    `json:"release_id"`
	ManifestFingerprint string    `json:"manifest_fingerprint"`
	LogicalFingerprint  string    `json:"logical_fingerprint"`
	PhysicalFingerprint string    `json:"physical_fingerprint"`
	JobID               string    `json:"job_id,omitempty"`
	Kind                EventKind `json:"kind"`
	ErrorCode           string    `json:"error_code,omitempty"`
	RowCount            int       `json:"row_count,omitempty"`
	Truncated           bool      `json:"truncated,omitempty"`
	OccurredAt          time.Time `json:"occurred_at"`
}

func (event QueryEvent) Validate() error {
	values := []struct {
		name  string
		value string
	}{
		{"request ID", event.RequestID}, {"tenant", event.Tenant}, {"principal", event.Principal},
		{"namespace", event.Namespace}, {"model name", event.ModelName}, {"release ID", event.ReleaseID},
		{"manifest fingerprint", event.ManifestFingerprint}, {"logical fingerprint", event.LogicalFingerprint},
		{"physical fingerprint", event.PhysicalFingerprint},
	}
	for _, value := range values {
		if strings.TrimSpace(value.value) == "" {
			return fmt.Errorf("audit %s is required", value.name)
		}
	}
	switch event.Kind {
	case EventQueryStarted, EventQuerySucceeded, EventQueryFailed, EventQueryCancelled:
	default:
		return fmt.Errorf("invalid audit event kind %q", event.Kind)
	}
	if event.OccurredAt.IsZero() {
		return errors.New("audit occurred time is required")
	}
	return nil
}

type Recorder interface {
	Record(context.Context, QueryEvent) error
}

type RecorderFunc func(context.Context, QueryEvent) error

func (function RecorderFunc) Record(ctx context.Context, event QueryEvent) error {
	return function(ctx, event)
}

// DiscardRecorder is limited to local CLI compatibility. Product transports
// must inject a durable recorder and fail closed when it is unavailable.
var DiscardRecorder Recorder = RecorderFunc(func(context.Context, QueryEvent) error { return nil })
