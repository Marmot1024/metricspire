package audit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/marmot1024/metricspire/internal/audit"
)

func TestQueryEventRequiresTraceabilityWithoutSensitivePayloadFields(t *testing.T) {
	event := validEvent()
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	event.Principal = ""
	if err := event.Validate(); err == nil {
		t.Fatal("audit event without principal was accepted")
	}
}

func TestMemoryRecorderCopiesEventsAndFailsClosed(t *testing.T) {
	recorder := audit.NewMemoryRecorder()
	event := validEvent()
	if err := recorder.Record(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	events := recorder.Events()
	events[0].Principal = "mutated"
	if recorder.Events()[0].Principal != "principal" {
		t.Fatal("memory recorder leaked mutable event storage")
	}
	want := errors.New("unavailable")
	recorder.SetError(want)
	if err := recorder.Record(context.Background(), event); !errors.Is(err, want) {
		t.Fatalf("Record() error = %v, want unavailable", err)
	}
}

func validEvent() audit.QueryEvent {
	return audit.QueryEvent{
		RequestID: "request", Tenant: "tenant", Principal: "principal",
		Namespace: "namespace", ModelName: "model", ReleaseID: "release",
		ManifestFingerprint: "sha256:manifest", LogicalFingerprint: "sha256:logical",
		PhysicalFingerprint: "sha256:physical", JobID: "job",
		Kind: audit.EventQueryStarted, OccurredAt: time.Now().UTC(),
	}
}
