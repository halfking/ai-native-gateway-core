package requestflow

import "log/slog"

// Event is one request-lifecycle observation. Fields are low-cardinality
// so operators can grep a single request_id and reconstruct classify →
// action → failover → terminal without reading live-stream snapshots.
type Event struct {
	Stage        string
	RequestID    string
	Model        string
	ProviderID   int
	CredentialID int
	Kind         string
	Action       string
	Reason       string
	Retryable    bool
	Committed    bool
	Attempt      int
}

// Attrs returns the stable slog attribute list for this event.
func (e Event) Attrs() []any {
	return []any{
		"event", "request_flow",
		"stage", e.Stage,
		"request_id", e.RequestID,
		"model", e.Model,
		"provider_id", e.ProviderID,
		"credential_id", e.CredentialID,
		"kind", e.Kind,
		"action", e.Action,
		"reason", e.Reason,
		"retryable", e.Retryable,
		"committed", e.Committed,
		"attempt", e.Attempt,
	}
}

// Log writes one request_flow line. Never include bodies, keys, or prompts.
func Log(e Event) {
	slog.Info("request_flow", e.Attrs()...)
}
