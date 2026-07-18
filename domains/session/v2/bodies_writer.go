package v2

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionBodiesWriter writes turn bodies to gateway.session_bodies
//
// It stores incremental message deltas to avoid the bloat problem
// of storing full message history in every row (as request_logs does).
type SessionBodiesWriter struct {
	db *pgxpool.Pool
}

// NewSessionBodiesWriter creates a new SessionBodiesWriter instance
func NewSessionBodiesWriter(db *pgxpool.Pool) *SessionBodiesWriter {
	return &SessionBodiesWriter{db: db}
}

// Message represents a single message in the conversation
type Message struct {
	Role       string                 `json:"role"`
	Content    string                 `json:"content,omitempty"`
	ToolCalls  []map[string]interface{} `json:"tool_calls,omitempty"`
	ToolCallID string                 `json:"tool_call_id,omitempty"`
	Name       string                 `json:"name,omitempty"`
}

// AttachmentRef represents an attachment reference (no base64 data)
type AttachmentRef struct {
	Name           string    `json:"name"`
	ObjectKey      string    `json:"object_key"`
	MIMEType       string    `json:"mime_type"`        // Renamed from ContentType for consistency
	SizeBytes      int64     `json:"size_bytes"`       // Renamed from Size for clarity
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
	SessionID  string
	TurnNo     int
	TenantID   string
	RequestID  string
	Ts         time.Time

	// Incremental deltas (core optimization)
	RequestDelta  []Message // Only new messages in this turn
	ResponseDelta []Message // This turn's response

	// Compressed outbound (for comparison/audit)
	OutboundBody []Message

	// Attachment references
	RequestAttachments  []AttachmentRef
	ResponseAttachments []AttachmentRef
}

// WriteBodies writes turn bodies to gateway.session_bodies
//
// The key optimization is RequestDelta only contains messages that
// were not present in the previous turn, avoiding exponential growth
// of storing full history in every row.
func (w *SessionBodiesWriter) WriteBodies(ctx context.Context, rec BodiesRecord) error {
	// Serialize deltas to JSONB
	requestDeltaJSON, err := json.Marshal(rec.RequestDelta)
	if err != nil {
		return fmt.Errorf("marshal request_delta: %w", err)
	}

	responseDeltaJSON, err := json.Marshal(rec.ResponseDelta)
	if err != nil {
		return fmt.Errorf("marshal response_delta: %w", err)
	}

	var outboundBodyJSON []byte
	if len(rec.OutboundBody) > 0 {
		outboundBodyJSON, err = json.Marshal(rec.OutboundBody)
		if err != nil {
			return fmt.Errorf("marshal outbound_body: %w", err)
		}
	}

	requestAttachmentsJSON, err := json.Marshal(rec.RequestAttachments)
	if err != nil {
		return fmt.Errorf("marshal request_attachments: %w", err)
	}

	responseAttachmentsJSON, err := json.Marshal(rec.ResponseAttachments)
	if err != nil {
		return fmt.Errorf("marshal response_attachments: %w", err)
	}

	partitionDate := rec.Ts.Truncate(24 * time.Hour)

	_, err = w.db.Exec(ctx, `
		INSERT INTO gateway.session_bodies (
			session_id, turn_no, tenant_id, request_id, ts,
			request_delta, response_delta, outbound_body,
			request_attachments, response_attachments,
			partition_date
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8,
			$9, $10,
			$11
		)
		ON CONFLICT (session_id, turn_no, partition_date) 
		DO UPDATE SET
			response_delta = EXCLUDED.response_delta,
			outbound_body = EXCLUDED.outbound_body,
			response_attachments = EXCLUDED.response_attachments
	`,
		rec.SessionID, rec.TurnNo, rec.TenantID, rec.RequestID, rec.Ts,
		requestDeltaJSON, responseDeltaJSON, outboundBodyJSON,
		requestAttachmentsJSON, responseAttachmentsJSON,
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

	if len(requestAttachmentsJSON) > 0 {
		if err := json.Unmarshal(requestAttachmentsJSON, &rec.RequestAttachments); err != nil {
			return nil, fmt.Errorf("unmarshal request_attachments: %w", err)
		}
	}

	if len(responseAttachmentsJSON) > 0 {
		if err := json.Unmarshal(responseAttachmentsJSON, &rec.ResponseAttachments); err != nil {
			return nil, fmt.Errorf("unmarshal response_attachments: %w", err)
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

		// Parse JSONs
		if len(requestDeltaJSON) > 0 {
			json.Unmarshal(requestDeltaJSON, &rec.RequestDelta)
		}
		if len(responseDeltaJSON) > 0 {
			json.Unmarshal(responseDeltaJSON, &rec.ResponseDelta)
		}
		if len(outboundBodyJSON) > 0 {
			json.Unmarshal(outboundBodyJSON, &rec.OutboundBody)
		}
		if len(requestAttachmentsJSON) > 0 {
			json.Unmarshal(requestAttachmentsJSON, &rec.RequestAttachments)
		}
		if len(responseAttachmentsJSON) > 0 {
			json.Unmarshal(responseAttachmentsJSON, &rec.ResponseAttachments)
		}

		bodies = append(bodies, rec)
	}

	return bodies, rows.Err()
}

// ReconstructFullHistory reconstructs full message history from incremental deltas
//
// This is useful for:
//  - Data validation (comparing with request_logs)
//  - Full context display
//  - Export/backup
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
