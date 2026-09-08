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
	// From/ToCredentialID carry the candidate transition for node_switch
	// events (audit 2026-09-08 #3, dispatch V2 coverage). Zero means "not
	// applicable"; Attrs omits them so every pre-existing line keeps its
	// exact attribute list.
	FromCredentialID int
	ToCredentialID   int
}

// Attrs returns the stable slog attribute list for this event. The
// from/to_credential_id pair is appended only when set (node_switch events)
// so the base schema stays byte-identical for the other stages.
func (e Event) Attrs() []any {
	attrs := []any{
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
	if e.FromCredentialID != 0 || e.ToCredentialID != 0 {
		attrs = append(attrs,
			"from_credential_id", e.FromCredentialID,
			"to_credential_id", e.ToCredentialID,
		)
	}
	return attrs
}

// Log writes one request_flow line. Never include bodies, keys, or prompts.
func Log(e Event) {
	slog.Info("request_flow", e.Attrs()...)
}
