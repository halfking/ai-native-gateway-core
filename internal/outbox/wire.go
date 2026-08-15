// wire.go renders the gateway-authoritative event envelope. Gateway and SM
// share the handoff-required `type` discriminator for schema version 1.0.
//
// GW-1.2 (docs/全面优化v1/README.md): the durable outbox must emit events whose
// envelope carries event_id (idempotency key), correlation_id,
// schema_version:"1.0", occurred_at, type and payload at the top level.
//
// Storage compatibility: the outbox_events table keeps schema_version as INT
// (1). WireSchemaVersion is the string the v1 schema requires on the wire; the
// int ↔ "1.0" mapping is the single place where the two representations meet.
//
// Backward compatibility for existing consumers (see docs/omni-ref2/02-CROSS-REPO-EVENT-CONTRACT.md):
//   - aggregate_id / aggregate_version are still rendered on the wire
//     (additive; the v1 schema allows extra properties).
//   - schema_version changes from the JSON number 1 to the string "1.0" — the
//     one intentional wire-format change required by the v1 schema freeze.
package outbox

import (
	"encoding/json"
	"fmt"
	"time"
)

// WireSchemaVersion is the schema_version value required on the wire by
// gateway-event-schema-v1.json.
const WireSchemaVersion = "1.0"

// wireEnvelope is the Gateway → SM wire format for one outbox event.
//
// Field set per gateway-event-schema-v1.json EventEnvelope, plus the legacy
// aggregate_id/aggregate_version pair retained for existing consumers.
type wireEnvelope struct {
	EventID       string `json:"event_id"`
	SchemaVersion string `json:"schema_version"`
	EventType     string `json:"type"`
	TenantID      string `json:"tenant_id"`

	// SessionID is required by the v1 schema description for any event other
	// than session.opened.v1; for request events it equals AggregateID.
	SessionID     string `json:"session_id,omitempty"`
	RequestID     string `json:"request_id,omitempty"`
	CorrelationID string `json:"correlation_id,omitempty"`
	SourceSystem  string `json:"source_system,omitempty"`

	OccurredAt time.Time      `json:"occurred_at"`
	Payload    map[string]any `json:"payload"`

	// Legacy aggregate fields (omni-ref2 contract). Omitted when zero so
	// hand-built envelopes without aggregates stay minimal.
	AggregateID      string `json:"aggregate_id,omitempty"`
	AggregateVersion int    `json:"aggregate_version,omitempty"`
}

// RenderWireEnvelope marshals env into the gateway-event-schema-v1.json wire
// format: schema_version is rendered as "1.0" and session_id / request_id /
// correlation_id are lifted to the envelope top level. For envelopes written
// before the v1 alignment (IDs only inside payload), the payload values are
// used as fallbacks so old outbox rows still dispatch with correlation info.
func RenderWireEnvelope(env EventEnvelope) ([]byte, error) {
	w := wireEnvelope{
		EventID:          env.EventID,
		SchemaVersion:    WireSchemaVersion,
		EventType:        env.EventType,
		TenantID:         env.TenantID,
		SessionID:        firstNonEmpty(env.SessionID, env.AggregateID, payloadString(env.Payload, "session_id")),
		RequestID:        firstNonEmpty(env.RequestID, payloadString(env.Payload, "request_id")),
		CorrelationID:    firstNonEmpty(env.CorrelationID, payloadString(env.Payload, "correlation_id")),
		SourceSystem:     env.SourceSystem,
		OccurredAt:       env.OccurredAt,
		Payload:          env.Payload,
		AggregateID:      env.AggregateID,
		AggregateVersion: env.AggregateVersion,
	}
	if w.OccurredAt.IsZero() {
		w.OccurredAt = time.Now().UTC()
	}
	b, err := json.Marshal(w)
	if err != nil {
		return nil, fmt.Errorf("outbox.RenderWireEnvelope: marshal: %w", err)
	}
	return b, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func payloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	if s, ok := payload[key].(string); ok {
		return s
	}
	return ""
}
