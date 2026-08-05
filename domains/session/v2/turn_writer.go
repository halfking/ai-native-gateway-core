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
type turnDB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type TurnWriter struct {
	db turnDB
}

// NewTurnWriter creates a new TurnWriter instance
func NewTurnWriter(db *pgxpool.Pool) *TurnWriter {
	return newTurnWriter(db)
}

func newTurnWriter(db turnDB) *TurnWriter {
	return &TurnWriter{db: db}
}

// TurnRecord represents a single turn's metadata
type TurnRecord struct {
	SessionID string
	TurnNo    int // Turn number within session (populated on read)
	TenantID  string
	RequestID string
	Ts        time.Time

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
	PromptTokens     int
	CompletionTokens int
	CacheReadTokens  int
	CacheWriteTokens int
	CostUSD          float64

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
// This is the backwards-compatible wrapper that owns its own transaction. It
// begins a tx, delegates to AppendTurnInTx, and commits on success.
//
// Prefer AppendTurnInTx when you need turn + bodies to commit atomically
// (spec §6.2). This method is kept so existing callers (and their tests) are
// unaffected.
func (w *TurnWriter) AppendTurn(ctx context.Context, rec TurnRecord) (turnNo int, err error) {
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	turnNo, err = w.AppendTurnInTx(ctx, tx, rec)
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit tx: %w", err)
	}
	return turnNo, nil
}

// AppendTurnInTx appends a new turn within a caller-managed transaction.
//
// It does NOT begin or commit; the caller controls the tx lifecycle so the
// turn INSERT can be committed atomically with the bodies INSERT (spec §6.2 —
// "turn 与 bodies 必须同事务，失败时整体可重试，无孤儿 turn"). The advisory
// lock is still acquired inside this tx (pg_advisory_xact_lock releases on
// commit/rollback), so concurrency semantics are identical to AppendTurn.
//
// Process:
//  1. Acquire advisory lock based on (tenant_id, session_id) hash
//  2. Query MAX(turn_no) for this session
//  3. Insert new turn with turn_no = MAX + 1 (ON CONFLICT DO NOTHING)
//  4. On conflict (RowsAffected == 0), re-read the real turn_no for the
//     idempotency key (request_id, partition_date) so retries / concurrent
//     inserts see a stable number.
//
// 2026-07-28 Step 3 Round 3: the previous pre-INSERT probe (SELECT turn_no
// WHERE request_id=… before the INSERT) was removed. ON CONFLICT DO NOTHING
// + RowsAffected==0 post-read is sufficient and avoids a redundant round
// trip on the hot path.
func (w *TurnWriter) AppendTurnInTx(ctx context.Context, tx pgx.Tx, rec TurnRecord) (turnNo int, err error) {
	if rec.SubmitMode == "" {
		rec.SubmitMode = "full"
	}
	if rec.InjectionVerdict == "" {
		rec.InjectionVerdict = "skip"
	}
	if rec.OutputVerdict == "" {
		rec.OutputVerdict = "skip"
	}
	if rec.SourceKind == "" {
		rec.SourceKind = "live"
	}
	if rec.Quality == "" {
		rec.Quality = "verified"
	}

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

	partitionDate := rec.Ts.Truncate(24 * time.Hour)

	// 3. Serialize compression_meta to JSONB
	compressionMetaJSON, err := json.Marshal(rec.CompressionMeta)
	if err != nil {
		return 0, fmt.Errorf("marshal compression_meta: %w", err)
	}
	// 2026-07-22: 转为 string 以使用 ::text::jsonb cast，避免 22P02 错误
	// 与 analysis/bus/publisher.go 和 telemetry/client.go 保持一致
	compressionMetaStr := string(compressionMetaJSON)

	// 4. Insert turn record
	result, err := tx.Exec(ctx, `
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
			$7, $8, $9::text::jsonb, $10,
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
		rec.CompressionApplied, rec.CompressionStrategy, compressionMetaStr, rec.TokensSaved,
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

	// Concurrent insert may have raced past us; resolve to the real turn_no
	// for the idempotency key so retries see the existing row's number.
	if result.RowsAffected() == 0 {
		err = tx.QueryRow(ctx, `
			SELECT turn_no
			FROM gateway.session_turns
			WHERE session_id = $1 AND tenant_id = $2
			  AND request_id = $3 AND partition_date = $4
		`, rec.SessionID, rec.TenantID, rec.RequestID, partitionDate).Scan(&turnNo)
		if err != nil {
			return 0, fmt.Errorf("read existing turn_no: %w", err)
		}

		// 2026-08-05 (v2 mirror bug): the mirror fires this write TWICE per
		// request via telemetry onPersisted — once for the INSERT-persist
		// (before the session compressor has run, so compression_strategy is
		// empty and submit_mode is only LCS-inferred) and once for the
		// UPDATE-persist (after compression, carrying the real
		// compression_strategy and the authoritative X-Gw-Submit-Mode verdict).
		// The initial insert wins the ON CONFLICT DO NOTHING above, so the
		// second fire's fields were silently dropped: session_turns.
		// compression_strategy stayed empty for every row and submit_mode was
		// pinned to the first-fire value. Backfill those late-arriving fields
		// on the conflict path. COALESCE(NULLIF(...)) guarantees a later empty
		// fire can never blank a value an earlier fire populated (monotonic
		// enrichment), so this stays idempotent under retries.
			_, err = tx.Exec(ctx, `
				UPDATE gateway.session_turns
				   SET compression_applied      = $5 OR compression_applied,
				       compression_strategy     = COALESCE(NULLIF($6, ''), compression_strategy),
				       compression_meta         = CASE
				                                      WHEN $7 <> '' AND $7 <> 'null'
				                                      THEN $7::text::jsonb
				                                      ELSE compression_meta
				                                  END,
				       compression_tokens_saved = CASE
				                                      WHEN $8 <> 0 THEN $8
				                                      ELSE compression_tokens_saved
				                                  END,
				       -- submit_mode: only an informative (non-default) verdict may
				       -- overwrite. rec.SubmitMode defaults to 'full', so a later
				       -- fire that lacks the header/previous-body context (e.g. a
				       -- failure-path UPDATE) can never regress a 'delta' /
				       -- 'inferred_compressed' verdict an earlier fire established.
				       submit_mode              = CASE
				                                      WHEN $9 <> '' AND $9 <> 'full'
				                                      THEN $9
				                                      ELSE submit_mode
				                                  END
				 WHERE session_id = $1 AND tenant_id = $2
				   AND request_id = $3 AND partition_date = $4
			`,
			rec.SessionID, rec.TenantID, rec.RequestID, partitionDate,
			rec.CompressionApplied, rec.CompressionStrategy, compressionMetaStr,
			rec.TokensSaved, rec.SubmitMode,
		)
		if err != nil {
			return 0, fmt.Errorf("backfill turn compression/submit_mode: %w", err)
		}
	}

	return turnNo, nil
}

// BeginTx begins a new transaction on the underlying pool.
//
// Exposed so SessionWriterV2 can begin one tx and feed it to both
// AppendTurnInTx and WriteBodiesInTx (spec §6.2 atomicity).
func (w *TurnWriter) BeginTx(ctx context.Context) (pgx.Tx, error) {
	return w.db.Begin(ctx)
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
			source_kind, quality,
			COALESCE(attachment_count, 0),
			COALESCE(attachment_total_bytes, 0),
			COALESCE(multimodal_types, '{}')
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
