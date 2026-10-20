package application

import "time"

// QueryTimings are monotonic server wall times, not database CPU time or model
// reasoning. EngineMS includes pool/connection waits, network and result decode.
// ServiceMS excludes job admission/queueing, transport and client/model work.
type QueryTimings struct {
	PlanningMS float64 `json:"planning_ms"`
	EngineMS   float64 `json:"engine_ms"`
	AuditMS    float64 `json:"audit_ms"`
	ServiceMS  float64 `json:"service_ms"`
}

func elapsedMS(start time.Time) float64 { return float64(time.Since(start).Microseconds()) / 1000 }
