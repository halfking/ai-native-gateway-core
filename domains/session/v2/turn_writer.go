// Package v2 implements Sessions V2 storage architecture
//
// This package provides parallel session storage alongside the existing
// request_logs system. The V2 architecture splits data into:
//   - sessions: session snapshots (one per session)
//   - session_turns: turn metadata (no bodies)
//   - session_bodies: incremental message deltas (columnar storage)
//   - session_turn_logs: processing stage logs (24h TTL)
//
// V2 can be enabled via Feature Flags without affecting V1 (request_logs).
package v2

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TurnWriter writes turn metadata to gateway.session_turns
//
// It ensures turn_no is monotonically increasing within each session
// using PostgreSQL advisory locks to prevent concurrent conflicts.
type TurnWriter struct {
	db *pgxpool.Pool
}

// NewTurnWriter creates a new TurnWriter instance
func NewTurnWriter(db *pgxpool.Pool) *TurnWriter {
	return &TurnWriter{db: db}
}

// TurnRecord represents a single turn's metadata
type TurnRecord struct {
	SessionID  string
	TurnNo     int    // Turn number within session (populated on read)
	TenantID   string
	RequestID  string
	Ts         time.Time

	// Submit mode detection
	SubmitMode string // full | delta | snapshot | inferred_compressed | attachment_only

	// Compression metadata
	CompressionApplied  bool
	CompressionStrategy string
	CompressionMeta     map[string]interface{}
	TokensSaved         int

	// Governance verdicts (L2 cache mirror)
	InjectionVerdict string // pass | warn | block | skip
	OutputVerdict    string // pass | warn | block | skip

	// Routing & model
	Model        string
	Provider     string
	CredentialID string

	// Usage & cost
	PromptTokens      int
	CompletionTokens  int
	CacheReadTokens   int
	CacheWriteTokens  int
	CostUSD           float64

	// Performance
	LatencyMs  int
	StatusCode int
	Success    bool
	ErrorKind  string

	// Data quality
	SourceKind string // live | backfill
	Quality    string // verified | inferred | partial | rejected

	// Attachment metadata (added in migration 431)
	AttachmentCount      int      // Number of attachments in this turn
	AttachmentTotalBytes int64    // Total bytes of all attachments
	MultimodalTypes      []string // Types present: ["image", "audio", "video", "document"]
}

// AppendTurn appends a new turn to the session, returning the assigned turn_no
//
// This method uses PostgreSQL advisory locks to ensure turn_no is
// monotonically increasing within the same session, even under concurrent
// requests.
//
// Process:
//  1. Acquire advisory lock based on (tenant_id, session_id) hash
//  2. Query MAX(turn_no) for this session
//  3. Insert new turn with turn_no = MAX + 1
//  4. Release lock on commit
//
// If the same request_id already exists (idempotency), the insert is skipped
// via ON CONFLICT DO NOTHING.
func (w *TurnWriter) AppendTurn(ctx context.Context, rec TurnRecord) (turnNo int, err error) {
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// 1. Acquire advisory lock
	lockKey := hashSessionKey(rec.TenantID, rec.SessionID)
	_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lockKey)
	if err != nil {
		return 0, fmt.Errorf("acquire advisory lock: %w", err)
	}

	// 2. Get next turn_no
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(turn_no), 0) + 1
		FROM gateway.session_turns
		WHERE tenant_id = $1 AND session_id = $2
	`, rec.TenantID, rec.SessionID).Scan(&turnNo)
	if err != nil {
		return 0, fmt.Errorf("get next turn_no: %w", err)
	}

	// 3. Serialize compression_meta to JSONB
	compressionMetaJSON, err := json.Marshal(rec.CompressionMeta)
	if err != nil {
		return 0, fmt.Errorf("marshal compression_meta: %w", err)
	}

	// 4. Insert turn record
	partitionDate := rec.Ts.Truncate(24 * time.Hour)
	
	_, err = tx.Exec(ctx, `
		INSERT INTO gateway.session_turns (
			session_id, turn_no, tenant_id, request_id, ts,
			submit_mode, 
			compression_applied, compression_strategy, compression_meta, compression_tokens_saved,
			injection_verdict, output_verdict,
			model, provider, credential_id,
			prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens, cost_usd,
			latency_ms, status_code, success, error_kind,
			source_kind, quality,
			attachment_count, attachment_total_bytes, multimodal_types,
			partition_date
		) VALUES (
			$1, $2, $3, $4, $5,
			$6,
			$7, $8, $9, $10,
			$11, $12,
			$13, $14, $15,
			$16, $17, $18, $19, $20,
			$21, $22, $23, $24,
			$25, $26,
			$27, $28, $29,
			$30
		)
		ON CONFLICT (request_id, partition_date) DO NOTHING
	`,
		rec.SessionID, turnNo, rec.TenantID, rec.RequestID, rec.Ts,
		rec.SubmitMode,
		rec.CompressionApplied, rec.CompressionStrategy, compressionMetaJSON, rec.TokensSaved,
		rec.InjectionVerdict, rec.OutputVerdict,
		rec.Model, rec.Provider, rec.CredentialID,
		rec.PromptTokens, rec.CompletionTokens, rec.CacheReadTokens, rec.CacheWriteTokens, rec.CostUSD,
		rec.LatencyMs, rec.StatusCode, rec.Success, rec.ErrorKind,
		rec.SourceKind, rec.Quality,
		rec.AttachmentCount, rec.AttachmentTotalBytes, rec.MultimodalTypes,
		partitionDate,
	)

	if err != nil {
		return 0, fmt.Errorf("insert turn: %w", err)
	}

	// 5. Commit transaction (releases advisory lock)
	err = tx.Commit(ctx)
	if err != nil {
		return 0, fmt.Errorf("commit tx: %w", err)
	}

	return turnNo, nil
}

// GetTurn retrieves a single turn by request_id
func (w *TurnWriter) GetTurn(ctx context.Context, requestID string) (*TurnRecord, error) {
	var rec TurnRecord
	var compressionMetaJSON []byte

	query := `
		SELECT 
			session_id, turn_no, tenant_id, request_id, ts,
			submit_mode,
			compression_applied, compression_strategy, compression_meta, 
			COALESCE(compression_tokens_saved, 0),
			COALESCE(injection_verdict, 'skip'),
			COALESCE(output_verdict, 'skip'),
			model, provider, credential_id,
			COALESCE(prompt_tokens, 0), COALESCE(completion_tokens, 0),
			COALESCE(cache_read_tokens, 0), COALESCE(cache_write_tokens, 0),
			COALESCE(cost_usd, 0),
			COALESCE(latency_ms, 0), COALESCE(status_code, 0),
			COALESCE(success, false), error_kind,
			source_kind, quality,
			COALESCE(attachment_count, 0),
			COALESCE(attachment_total_bytes, 0),
			COALESCE(multimodal_types, '{}')
		FROM gateway.session_turns
		WHERE request_id = $1
		LIMIT 1
	`

	err := w.db.QueryRow(ctx, query, requestID).Scan(
		&rec.SessionID, &rec.TurnNo, &rec.TenantID, &rec.RequestID, &rec.Ts,
		&rec.SubmitMode,
		&rec.CompressionApplied, &rec.CompressionStrategy, &compressionMetaJSON, &rec.TokensSaved,
		&rec.InjectionVerdict, &rec.OutputVerdict,
		&rec.Model, &rec.Provider, &rec.CredentialID,
		&rec.PromptTokens, &rec.CompletionTokens,
		&rec.CacheReadTokens, &rec.CacheWriteTokens,
		&rec.CostUSD,
		&rec.LatencyMs, &rec.StatusCode,
		&rec.Success, &rec.ErrorKind,
		&rec.SourceKind, &rec.Quality,
		&rec.AttachmentCount, &rec.AttachmentTotalBytes, &rec.MultimodalTypes,
	)

	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("turn not found: %s", requestID)
	}
	if err != nil {
		return nil, fmt.Errorf("query turn: %w", err)
	}

	// Parse compression_meta
	if len(compressionMetaJSON) > 0 {
		err = json.Unmarshal(compressionMetaJSON, &rec.CompressionMeta)
		if err != nil {
			return nil, fmt.Errorf("unmarshal compression_meta: %w", err)
		}
	}

	return &rec, nil
}

// ListTurns retrieves all turns for a session, ordered by turn_no
func (w *TurnWriter) ListTurns(ctx context.Context, tenantID, sessionID string, limit int) ([]TurnRecord, error) {
	if limit <= 0 {
		limit = 100
	}

	query := `
		SELECT 
			session_id, turn_no, tenant_id, request_id, ts,
			submit_mode,
			compression_applied, compression_strategy, compression_meta,
			COALESCE(compression_tokens_saved, 0),
			COALESCE(injection_verdict, 'skip'),
			COALESCE(output_verdict, 'skip'),
			model, provider, credential_id,
			COALESCE(prompt_tokens, 0), COALESCE(completion_tokens, 0),
			COALESCE(cache_read_tokens, 0), COALESCE(cache_write_tokens, 0),
			COALESCE(cost_usd, 0),
			COALESCE(latency_ms, 0), COALESCE(status_code, 0),
			COALESCE(success, false), error_kind,
			source_kind, quality
		FROM gateway.session_turns
		WHERE tenant_id = $1 AND session_id = $2
		ORDER BY turn_no ASC
		LIMIT $3
	`

	rows, err := w.db.Query(ctx, query, tenantID, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("query turns: %w", err)
	}
	defer rows.Close()

	var turns []TurnRecord
	for rows.Next() {
		var rec TurnRecord
		var compressionMetaJSON []byte

		err := rows.Scan(
			&rec.SessionID, &rec.TenantID, &rec.RequestID, &rec.Ts,
			&rec.SubmitMode,
			&rec.CompressionApplied, &rec.CompressionStrategy, &compressionMetaJSON, &rec.TokensSaved,
			&rec.InjectionVerdict, &rec.OutputVerdict,
			&rec.Model, &rec.Provider, &rec.CredentialID,
			&rec.PromptTokens, &rec.CompletionTokens,
			&rec.CacheReadTokens, &rec.CacheWriteTokens,
			&rec.CostUSD,
			&rec.LatencyMs, &rec.StatusCode,
			&rec.Success, &rec.ErrorKind,
			&rec.SourceKind, &rec.Quality,
		)
		if err != nil {
			return nil, fmt.Errorf("scan turn: %w", err)
		}

		// Parse compression_meta
		if len(compressionMetaJSON) > 0 {
			err = json.Unmarshal(compressionMetaJSON, &rec.CompressionMeta)
			if err != nil {
				return nil, fmt.Errorf("unmarshal compression_meta: %w", err)
			}
		}

		turns = append(turns, rec)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate turns: %w", err)
	}

	return turns, nil
}

// hashSessionKey generates a deterministic int64 hash for advisory lock
//
// Uses SHA256 to hash "tenant_id:session_id", then takes first 8 bytes
// as int64. This ensures the same session always gets the same lock key.
func hashSessionKey(tenantID, sessionID string) int64 {
	h := sha256.New()
	h.Write([]byte(tenantID + ":" + sessionID))
	sum := h.Sum(nil)
	
	// Take first 8 bytes as int64
	return int64(binary.BigEndian.Uint64(sum[:8]))
}
