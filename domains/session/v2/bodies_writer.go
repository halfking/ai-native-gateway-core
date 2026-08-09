package v2

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// bodiesDB is the minimal DB surface that SessionBodiesWriter needs.
//
// Defined as an interface (mirroring turnDB / aggregatorDB) so unit tests can
// wire pgxmock without a live PostgreSQL instance and — more importantly —
// so WriteBodiesInTx can run inside a caller-provided pgx.Tx.
type bodiesDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// SessionBodiesWriter writes turn bodies to gateway.session_bodies
//
// It stores incremental message deltas to avoid the bloat problem
// of storing full message history in every row (as request_logs does).
type SessionBodiesWriter struct {
	db bodiesDB
}

// NewSessionBodiesWriter creates a new SessionBodiesWriter instance
func NewSessionBodiesWriter(db *pgxpool.Pool) *SessionBodiesWriter {
	return newSessionBodiesWriter(db)
}

// newSessionBodiesWriter is the test seam that accepts the bodiesDB interface
// (so tests can inject a pgxmock-backed pool or a pgx.Tx directly).
func newSessionBodiesWriter(db bodiesDB) *SessionBodiesWriter {
	return &SessionBodiesWriter{db: db}
}

// Message represents a single message in the conversation.
// Content keeps the legacy text projection used by submit-mode detection and
// token estimation; ContentRaw preserves array/null/structured provider content.
type Message struct {
	Role       string                   `json:"role"`
	Content    string                   `json:"-"`
	ContentRaw json.RawMessage          `json:"-"`
	ToolCalls  []map[string]interface{} `json:"tool_calls,omitempty"`
	ToolCallID string                   `json:"tool_call_id,omitempty"`
	Name       string                   `json:"name,omitempty"`
	// RawContent preserves a provider-native or IR-shaped content payload when
	// Content cannot represent it as a string. It is encoded by MarshalJSON.
	RawContent interface{} `json:"-"`
}

// MarshalJSON writes structured content back to the provider's original JSON
// shape while retaining the string-compatible in-memory projection.
//
// Source precedence for the `content` field:
//  1. ContentRaw — the provider-native value, kept byte-for-byte.
//  2. Content — the legacy string projection.
//  3. RawContent — the IR envelope. Only its *content block array* is written,
//     never the envelope itself: writing the whole envelope produced
//     {"role":"user","content":{"role":"user","content":[...]}}, a nested
//     message that no reader expects.
//
// The envelope's message-level payload (ir.Message.RawContent) is written to a
// sibling `raw` key rather than being folded into `content`, so a provider-native
// payload the IR parser could not model is not silently dropped on write.
func (m Message) MarshalJSON() ([]byte, error) {
	content := m.ContentRaw
	if len(content) == 0 && m.Content != "" {
		var err error
		content, err = json.Marshal(m.Content)
		if err != nil {
			return nil, err
		}
	}
	// messageRaw is extracted unconditionally: `content` may legitimately come
	// from ContentRaw (the byte-identical provider value) while the envelope
	// still carries a message-level payload that must not be dropped.
	envContent, messageRaw := irEnvelopeContent(m.RawContent)
	if len(content) == 0 {
		content = envContent
	}
	type wire struct {
		Role       string                   `json:"role"`
		Content    json.RawMessage          `json:"content,omitempty"`
		ToolCalls  []map[string]interface{} `json:"tool_calls,omitempty"`
		ToolCallID string                   `json:"tool_call_id,omitempty"`
		Name       string                   `json:"name,omitempty"`
		Raw        json.RawMessage          `json:"raw,omitempty"`
	}
	return json.Marshal(wire{
		Role:       m.Role,
		Content:    content,
		ToolCalls:  m.ToolCalls,
		ToolCallID: m.ToolCallID,
		Name:       m.Name,
		Raw:        messageRaw,
	})
}

// UnmarshalJSON accepts both legacy string content and provider content arrays.
// Sets both ContentRaw (for sessionv2mirror) and RawContent (for IR pipeline).
func (m *Message) UnmarshalJSON(data []byte) error {
	type wire struct {
		Role       string                   `json:"role"`
		Content    json.RawMessage          `json:"content"`
		ToolCalls  []map[string]interface{} `json:"tool_calls"`
		ToolCallID string                   `json:"tool_call_id"`
		Name       string                   `json:"name"`
		Raw        json.RawMessage          `json:"raw"`
	}
	var w wire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	*m = Message{
		Role:       w.Role,
		ToolCalls:  w.ToolCalls,
		ToolCallID: w.ToolCallID,
		Name:       w.Name,
	}
	if len(w.Content) == 0 || string(w.Content) == "null" {
		if len(w.Content) > 0 {
			raw := append(json.RawMessage(nil), w.Content...)
			m.ContentRaw = raw
			m.RawContent = raw
		}
		m.adoptMessageRaw(w.Raw)
		return nil
	}
	var text string
	if err := json.Unmarshal(w.Content, &text); err == nil {
		m.Content = text
		m.adoptMessageRaw(w.Raw)
		return nil
	}
	raw := append(json.RawMessage(nil), w.Content...)
	m.ContentRaw = raw
	m.RawContent = raw
	m.adoptMessageRaw(w.Raw)
	return nil
}

// adoptMessageRaw restores the sibling `raw` key written by MarshalJSON.
//
// It rebuilds a content-free IR envelope in RawContent so ToIR's envelope path
// recovers the message-level ir.Message.RawContent. ContentRaw is left
// untouched and remains the authoritative content source — ToIR merges the two,
// so a provider-native content array is not re-encoded through the envelope's
// narrower block struct.
func (m *Message) adoptMessageRaw(raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	m.RawContent = buildIREnvelope(m.Role, m.ToolCallID, m.Name, raw, m.ToolCalls)
}

// AttachmentRef represents an attachment reference (no base64 data)
type AttachmentRef struct {
	Name           string    `json:"name"`
	ObjectKey      string    `json:"object_key"`
	MIMEType       string    `json:"mime_type"`  // Renamed from ContentType for consistency
	SizeBytes      int64     `json:"size_bytes"` // Renamed from Size for clarity
	SHA256         string    `json:"sha256"`
	SourceProtocol string    `json:"source_protocol"`  // openai/anthropic/gemini/etc
	DeclaredMIME   string    `json:"declared_mime"`    // Client-declared MIME type
	SniffedMIME    string    `json:"sniffed_mime"`     // Server-detected MIME type
	ProviderFileID string    `json:"provider_file_id"` // Provider-specific file ID (e.g., Gemini file_uri)
	ExpiresAt      time.Time `json:"expires_at"`       // Expiration timestamp for provider files
	Replayable     bool      `json:"replayable"`       // Whether attachment can be replayed to upstream
}

// BodiesRecord represents turn bodies (incremental deltas)
type BodiesRecord struct {
	SessionID string
	TurnNo    int
	TenantID  string
	RequestID string
	Ts        time.Time

	// Incremental deltas (core optimization)
	RequestDelta  []Message // Only new messages in this turn
	ResponseDelta []Message // This turn's response

	// Compressed outbound (for comparison/audit)
	OutboundBody []Message

	// Attachment references
	RequestAttachments  []AttachmentRef
	ResponseAttachments []AttachmentRef
}

// safeJSONMarshal marshals v to JSON, falling back to a safe placeholder on error.
// This prevents "invalid input syntax for type json" PostgreSQL errors when
// json.Marshal produces invalid JSON (e.g., from NaN/Inf floats or malformed UTF-8).
// 2026-07-23: Added to fix sessionv2mirror shadow write failures.
func safeJSONMarshal(v interface{}) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		// Fallback to empty array/object depending on type
		switch v.(type) {
		case []interface{}, []Message, []AttachmentRef:
			return []byte("[]"), nil
		default:
			return []byte("{}"), nil
		}
	}

	// Extra safety: if marshal succeeded but produced empty string,
	// replace with null to avoid PostgreSQL parse errors
	if len(data) == 0 {
		return []byte("null"), nil
	}

	// Validate it's actually parseable JSON
	if !json.Valid(data) {
		return []byte("null"), nil
	}

	return data, nil
}

func jsonTextOrNull(data []byte) string {
	if len(data) == 0 {
		return "null"
	}
	return string(data)
}

// WriteBodies writes turn bodies to gateway.session_bodies
//
// This is the backwards-compatible wrapper that runs against the writer's own
// pool. Prefer WriteBodiesInTx when you need turn + bodies to commit atomically
// (spec §6.2).
//
// The key optimization is RequestDelta only contains messages that
// were not present in the previous turn, avoiding exponential growth
// of storing full history in every row.
func (w *SessionBodiesWriter) WriteBodies(ctx context.Context, rec BodiesRecord) error {
	return w.WriteBodiesInTx(ctx, w.db, rec)
}

// WriteBodiesInTx writes turn bodies within a caller-managed transaction
// (or directly against the pool when tx is the pool).
//
// It does NOT begin or commit; the caller controls the tx lifecycle so the
// bodies INSERT can be committed atomically with the turn INSERT (spec §6.2 —
// "turn 与 bodies 必须同事务").
//
// tx is typed as the same bodiesDB interface the writer already holds; both
// *pgxpool.Pool and pgx.Tx satisfy it, so the same code path serves the
// standalone wrapper and the atomic-coordinated caller.
func (w *SessionBodiesWriter) WriteBodiesInTx(ctx context.Context, tx bodiesDB, rec BodiesRecord) error {
	// Serialize deltas to JSONB with safe marshaling
	requestDeltaJSON, err := safeJSONMarshal(rec.RequestDelta)
	if err != nil {
		return fmt.Errorf("marshal request_delta: %w", err)
	}

	responseDeltaJSON, err := safeJSONMarshal(rec.ResponseDelta)
	if err != nil {
		return fmt.Errorf("marshal response_delta: %w", err)
	}

	var outboundBodyJSON []byte
	if len(rec.OutboundBody) > 0 {
		outboundBodyJSON, err = safeJSONMarshal(rec.OutboundBody)
		if err != nil {
			return fmt.Errorf("marshal outbound_body: %w", err)
		}
	}

	requestAttachmentsJSON, err := safeJSONMarshal(rec.RequestAttachments)
	if err != nil {
		return fmt.Errorf("marshal request_attachments: %w", err)
	}

	responseAttachmentsJSON, err := safeJSONMarshal(rec.ResponseAttachments)
	if err != nil {
		return fmt.Errorf("marshal response_attachments: %w", err)
	}

	partitionDate := calendarDate(rec.Ts)

	_, err = tx.Exec(ctx, `
		INSERT INTO gateway.session_bodies AS existing (
			session_id, turn_no, tenant_id, request_id, ts,
			request_delta, response_delta, outbound_body,
			request_attachments, response_attachments,
			partition_date
		) VALUES (
			$1, $2, $3, $4, $5,
			$6::text::jsonb, $7::text::jsonb, $8::text::jsonb,
			$9::text::jsonb, $10::text::jsonb,
			$11
		)
		ON CONFLICT (tenant_id, session_id, turn_no, partition_date)
		DO UPDATE SET
			response_delta = CASE
				WHEN EXCLUDED.response_delta IS NULL OR EXCLUDED.response_delta = 'null'::jsonb
				THEN existing.response_delta
				ELSE EXCLUDED.response_delta
			END,
			outbound_body = CASE
				WHEN EXCLUDED.outbound_body IS NULL OR EXCLUDED.outbound_body = 'null'::jsonb
				THEN existing.outbound_body
				ELSE EXCLUDED.outbound_body
			END,
			response_attachments = CASE
				WHEN EXCLUDED.response_attachments IS NULL OR EXCLUDED.response_attachments = 'null'::jsonb
				THEN existing.response_attachments
				ELSE EXCLUDED.response_attachments
			END
	`,
		rec.SessionID, rec.TurnNo, rec.TenantID, rec.RequestID, rec.Ts,
		jsonTextOrNull(requestDeltaJSON), jsonTextOrNull(responseDeltaJSON), jsonTextOrNull(outboundBodyJSON),
		jsonTextOrNull(requestAttachmentsJSON), jsonTextOrNull(responseAttachmentsJSON),
		partitionDate,
	)

	if err != nil {
		return fmt.Errorf("insert bodies: %w", err)
	}

	return nil
}

// GetBodies retrieves turn bodies by session_id and turn_no
func (w *SessionBodiesWriter) GetBodies(ctx context.Context, tenantID, sessionID string, turnNo int) (*BodiesRecord, error) {
	var rec BodiesRecord
	var requestDeltaJSON, responseDeltaJSON, outboundBodyJSON []byte
	var requestAttachmentsJSON, responseAttachmentsJSON []byte

	query := `
		SELECT 
			session_id, turn_no, tenant_id, request_id, ts,
			request_delta, response_delta, outbound_body,
			request_attachments, response_attachments
		FROM gateway.session_bodies
		WHERE tenant_id = $1 AND session_id = $2 AND turn_no = $3
		LIMIT 1
	`

	err := w.db.QueryRow(ctx, query, tenantID, sessionID, turnNo).Scan(
		&rec.SessionID, &rec.TurnNo, &rec.TenantID, &rec.RequestID, &rec.Ts,
		&requestDeltaJSON, &responseDeltaJSON, &outboundBodyJSON,
		&requestAttachmentsJSON, &responseAttachmentsJSON,
	)

	if err != nil {
		return nil, fmt.Errorf("query bodies: %w", err)
	}

	// Parse JSONs
	if len(requestDeltaJSON) > 0 {
		if err := json.Unmarshal(requestDeltaJSON, &rec.RequestDelta); err != nil {
			return nil, fmt.Errorf("unmarshal request_delta: %w", err)
		}
	}

	if len(responseDeltaJSON) > 0 {
		if err := json.Unmarshal(responseDeltaJSON, &rec.ResponseDelta); err != nil {
			return nil, fmt.Errorf("unmarshal response_delta: %w", err)
		}
	}

	if len(outboundBodyJSON) > 0 {
		if err := json.Unmarshal(outboundBodyJSON, &rec.OutboundBody); err != nil {
			return nil, fmt.Errorf("unmarshal outbound_body: %w", err)
		}
	}

	if len(requestAttachmentsJSON) > 0 && string(requestAttachmentsJSON) != "null" {
		if err := json.Unmarshal(requestAttachmentsJSON, &rec.RequestAttachments); err != nil {
			return nil, fmt.Errorf("unmarshal request_attachments: %w", err)
		}
	}

	if len(responseAttachmentsJSON) > 0 && string(responseAttachmentsJSON) != "null" {
		if err := json.Unmarshal(responseAttachmentsJSON, &rec.ResponseAttachments); err != nil {
			return nil, fmt.Errorf("unmarshal response_attachments: %w", err)
		}
	}

	return &rec, nil
}

// GetLatestBodies retrieves the most recent turn body for a session without
// scanning the full history. It is used on the per-request write path for
// previous-outbound and attachment delta detection.
//
// GetLatestBodiesInTx retrieves the latest body through a caller-managed
// transaction. It is used after LockSessionInTx so delta derivation cannot race
// another turn append for the same tenant/session.
func (w *SessionBodiesWriter) GetLatestBodiesInTx(ctx context.Context, tx bodiesDB, tenantID, sessionID string) (*BodiesRecord, error) {
	return getLatestBodies(ctx, tx, tenantID, sessionID)
}

func (w *SessionBodiesWriter) GetLatestBodies(ctx context.Context, tenantID, sessionID string) (*BodiesRecord, error) {
	return getLatestBodies(ctx, w.db, tenantID, sessionID)
}

func getLatestBodies(ctx context.Context, db bodiesDB, tenantID, sessionID string) (*BodiesRecord, error) {
	var rec BodiesRecord
	var requestDeltaJSON, responseDeltaJSON, outboundBodyJSON []byte
	var requestAttachmentsJSON, responseAttachmentsJSON []byte

	err := db.QueryRow(ctx, `
		SELECT
			session_id, turn_no, tenant_id, request_id, ts,
			request_delta, response_delta, outbound_body,
			request_attachments, response_attachments
		FROM gateway.session_bodies
		WHERE tenant_id = $1 AND session_id = $2
		ORDER BY turn_no DESC
		LIMIT 1
	`, tenantID, sessionID).Scan(
		&rec.SessionID, &rec.TurnNo, &rec.TenantID, &rec.RequestID, &rec.Ts,
		&requestDeltaJSON, &responseDeltaJSON, &outboundBodyJSON,
		&requestAttachmentsJSON, &responseAttachmentsJSON,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query latest bodies: %w", err)
	}

	if len(requestDeltaJSON) > 0 {
		if err := json.Unmarshal(requestDeltaJSON, &rec.RequestDelta); err != nil {
			return nil, fmt.Errorf("unmarshal latest request_delta: %w", err)
		}
	}
	if len(responseDeltaJSON) > 0 {
		if err := json.Unmarshal(responseDeltaJSON, &rec.ResponseDelta); err != nil {
			return nil, fmt.Errorf("unmarshal latest response_delta: %w", err)
		}
	}
	if len(outboundBodyJSON) > 0 {
		if err := json.Unmarshal(outboundBodyJSON, &rec.OutboundBody); err != nil {
			return nil, fmt.Errorf("unmarshal latest outbound_body: %w", err)
		}
	}
	if len(requestAttachmentsJSON) > 0 && string(requestAttachmentsJSON) != "null" {
		if err := json.Unmarshal(requestAttachmentsJSON, &rec.RequestAttachments); err != nil {
			return nil, fmt.Errorf("unmarshal latest request_attachments: %w", err)
		}
	}
	if len(responseAttachmentsJSON) > 0 && string(responseAttachmentsJSON) != "null" {
		if err := json.Unmarshal(responseAttachmentsJSON, &rec.ResponseAttachments); err != nil {
			return nil, fmt.Errorf("unmarshal latest response_attachments: %w", err)
		}
	}
	return &rec, nil
}

// ListAllBodies retrieves all turn bodies for a session
func (w *SessionBodiesWriter) ListAllBodies(ctx context.Context, tenantID, sessionID string) ([]BodiesRecord, error) {
	query := `
		SELECT 
			session_id, turn_no, tenant_id, request_id, ts,
			request_delta, response_delta, outbound_body,
			request_attachments, response_attachments
		FROM gateway.session_bodies
		WHERE tenant_id = $1 AND session_id = $2
		ORDER BY turn_no ASC
	`

	rows, err := w.db.Query(ctx, query, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query bodies: %w", err)
	}
	defer rows.Close()

	var bodies []BodiesRecord
	for rows.Next() {
		var rec BodiesRecord
		var requestDeltaJSON, responseDeltaJSON, outboundBodyJSON []byte
		var requestAttachmentsJSON, responseAttachmentsJSON []byte

		err := rows.Scan(
			&rec.SessionID, &rec.TurnNo, &rec.TenantID, &rec.RequestID, &rec.Ts,
			&requestDeltaJSON, &responseDeltaJSON, &outboundBodyJSON,
			&requestAttachmentsJSON, &responseAttachmentsJSON,
		)
		if err != nil {
			return nil, fmt.Errorf("scan bodies: %w", err)
		}

		// Parse JSONs - strictly report errors for data quality
		if len(requestDeltaJSON) > 0 {
			if err := json.Unmarshal(requestDeltaJSON, &rec.RequestDelta); err != nil {
				return nil, fmt.Errorf("unmarshal request_delta at turn %d: %w", rec.TurnNo, err)
			}
		}
		if len(responseDeltaJSON) > 0 {
			if err := json.Unmarshal(responseDeltaJSON, &rec.ResponseDelta); err != nil {
				return nil, fmt.Errorf("unmarshal response_delta at turn %d: %w", rec.TurnNo, err)
			}
		}
		if len(outboundBodyJSON) > 0 {
			if err := json.Unmarshal(outboundBodyJSON, &rec.OutboundBody); err != nil {
				return nil, fmt.Errorf("unmarshal outbound_body at turn %d: %w", rec.TurnNo, err)
			}
		}
		if len(requestAttachmentsJSON) > 0 {
			if err := json.Unmarshal(requestAttachmentsJSON, &rec.RequestAttachments); err != nil {
				return nil, fmt.Errorf("unmarshal request_attachments at turn %d: %w", rec.TurnNo, err)
			}
		}
		if len(responseAttachmentsJSON) > 0 {
			if err := json.Unmarshal(responseAttachmentsJSON, &rec.ResponseAttachments); err != nil {
				return nil, fmt.Errorf("unmarshal response_attachments at turn %d: %w", rec.TurnNo, err)
			}
		}

		bodies = append(bodies, rec)
	}

	return bodies, rows.Err()
}

// ReconstructFullHistory reconstructs full message history from incremental deltas
//
// This is useful for:
//   - Data validation (comparing with request_logs)
//   - Full context display
//   - Export/backup
func (w *SessionBodiesWriter) ReconstructFullHistory(ctx context.Context, tenantID, sessionID string) ([][]Message, error) {
	bodies, err := w.ListAllBodies(ctx, tenantID, sessionID)
	if err != nil {
		return nil, err
	}

	var history [][]Message
	var accumulated []Message

	for _, body := range bodies {
		// Add this turn's request delta to accumulated history
		accumulated = append(accumulated, body.RequestDelta...)

		// Add this turn's response delta
		accumulated = append(accumulated, body.ResponseDelta...)

		// Snapshot of accumulated messages after this turn
		turnSnapshot := make([]Message, len(accumulated))
		copy(turnSnapshot, accumulated)
		history = append(history, turnSnapshot)
	}

	return history, nil
}
