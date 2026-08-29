// Package requestdetail provides in-flight request content storage
// (process memory meta + per-request_id local files) and a read locator
// that prefers local hot content before DB dual-write sources.
package requestdetail

import "encoding/json"

// Source names the layer that supplied the detail payload.
type Source string

const (
	SourceMemory       Source = "memory"
	SourceFile         Source = "file"
	SourceLive         Source = "live_stream"
	SourceRequestLogs  Source = "request_logs"
	SourceSessionTurns Source = "session_turns"
)

// Persistence classifies whether the request has been written to DB yet.
type Persistence string

const (
	PersistenceInFlight  Persistence = "in_flight"
	PersistencePersisted Persistence = "persisted"
)

// Bodies holds decoded request/response/outbound payloads.
type Bodies struct {
	RequestBody  json.RawMessage `json:"request_body,omitempty"`
	ResponseBody json.RawMessage `json:"response_body,omitempty"`
	OutboundBody json.RawMessage `json:"outbound_body,omitempty"`
}

// Meta is the lightweight in-memory request identity (no full bodies).
type Meta struct {
	RequestID   string  `json:"request_id"`
	TenantID    string  `json:"tenant_id,omitempty"`
	GwSessionID *string `json:"gw_session_id,omitempty"`
	GwTaskID    *string `json:"gw_task_id,omitempty"`
	ClientModel *string `json:"client_model,omitempty"`
	Status      *string `json:"request_status,omitempty"`
	Success     *bool   `json:"success,omitempty"`
	LatencyMs   *int    `json:"latency_ms,omitempty"`
	TurnNumber  *int    `json:"turn_number,omitempty"`
}

// Detail is the unified admin detail payload.
type Detail struct {
	Source      Source      `json:"source"`
	Persistence Persistence `json:"persistence"`
	Meta        Meta        `json:"meta"`
	Bodies      *Bodies     `json:"bodies,omitempty"`
	Warning     string      `json:"warning,omitempty"`
}

// filePayload is the on-disk JSON shape (filename = request_id.json).
type filePayload struct {
	Meta   Meta   `json:"meta"`
	Bodies Bodies `json:"bodies"`
}
