package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/dbdegradation"
)

// DataAnomalyRecorder records data-level anomalies (persistence, marshal, etc.)
type DataAnomalyRecorder interface {
	RecordDataAnomaly(ctx context.Context, anomalyType, severity, requestID, message string, metadata map[string]any) error
}

const (
	StageReceived      = 0
	StageCompressed    = 1
	StageTransformed   = 2
	StageExecuting     = 3
	StageCompleted     = 4
	StageCompressFail  = 10
	StageTransformFail = 11
	StageExecuteFail   = 12
	StageResponseFail  = 13
)

const (
	StatusPending = "pending"
	StatusSuccess = "success"
	StatusFailure = "failure"
)

type RequestLogger struct {
	db              *pgxpool.Pool
	asyncQueue      chan *LogUpdate
	config          *RequestLoggerConfig
	wg              sync.WaitGroup
	done            chan struct{}
	fallback        dbdegradation.BackupWriter
	degraded        bool
	mu              sync.RWMutex
	anomalyRecorder DataAnomalyRecorder
}

type RequestLoggerConfig struct {
	QueueSize    int
	BatchSize    int
	FlushTimeout time.Duration
	Enabled      bool
}

type InitialRequest struct {
	RequestID   string
	TenantID    string
	SessionID   string
	ClientModel string
}

type LogUpdate struct {
	RequestID            string
	Stage                int
	Status               string
	Error                string
	OutboundBody         []byte
	CompressionStrategy  string
	CompressionMeta      map[string]interface{}
	CompletionTokens     int
	PromptTokens         int
	CompletedAt          time.Time
	UpstreamRequestAt    time.Time
	UpstreamResponseAt   time.Time
	UpstreamProviderID   *int64
	UpstreamCredentialID *int64
}

type UpdateBuilder struct {
	update *LogUpdate
}

func NewRequestLogger(pool *pgxpool.Pool, cfg *RequestLoggerConfig) *RequestLogger {
	if cfg == nil {
		cfg = &RequestLoggerConfig{
			QueueSize:    10000,
			BatchSize:    50,
			FlushTimeout: 100 * time.Millisecond,
			Enabled:      true,
		}
	}
	rl := &RequestLogger{
		db:         pool,
		asyncQueue: make(chan *LogUpdate, cfg.QueueSize),
		config:     cfg,
		done:       make(chan struct{}),
	}
	rl.wg.Add(1)
	go rl.worker()
	return rl
}

func (rl *RequestLogger) SetFallbackWriter(writer dbdegradation.BackupWriter) {
	rl.fallback = writer
}

func (rl *RequestLogger) SetAnomalyRecorder(recorder DataAnomalyRecorder) {
	rl.anomalyRecorder = recorder
}

func (rl *RequestLogger) ReplayFallback(ctx context.Context, record dbdegradation.BackupRecord) error {
	if strings.HasSuffix(record.RecordKey, ":initial") {
		var req InitialRequest
		if err := json.Unmarshal(record.Payload, &req); err != nil {
			return err
		}
		if rl.db == nil {
			return fmt.Errorf("request logger database not configured")
		}
		_, err := rl.db.Exec(ctx, `
			INSERT INTO request_wal_hot (request_id, tenant_id, gw_session_id, status, stage, client_model, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, NOW())
			ON CONFLICT (request_id, created_at) DO NOTHING
		`, req.RequestID, req.TenantID, req.SessionID, StatusPending, StageReceived, req.ClientModel)
		return err
	}
	var update LogUpdate
	if err := json.Unmarshal(record.Payload, &update); err != nil {
		return err
	}
	return rl.persistUpdate(ctx, &update)
}

func (rl *RequestLogger) SetDegraded(enabled bool) {
	rl.mu.Lock()
	rl.degraded = enabled
	rl.mu.Unlock()
}

func (rl *RequestLogger) isDegraded() bool {
	rl.mu.RLock()
	degraded := rl.degraded
	rl.mu.RUnlock()
	return degraded
}

func (rl *RequestLogger) Enabled() bool {
	return rl != nil && rl.config != nil && rl.config.Enabled
}

func (rl *RequestLogger) CreateInitial(ctx context.Context, req *InitialRequest) error {
	if !rl.Enabled() {
		return nil
	}
	if rl.isDegraded() && rl.fallback != nil {
		return rl.fallback.WriteRequestWAL(ctx, req.RequestID+":initial", req)
	}
	if rl.db == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// INSERT directly targets request_wal_hot (the canonical write
	// target per the 2026-07 data-lifecycle architecture). All
	// INSERT/UPDATE/DELETE on request_wal goes through *_default — the
	// parent's auto-routing is intentionally bypassed so writes never
	// land in a non-default partition (which would block subsequent
	// UPDATE/DELETE once that partition is converted to columnar storage
	// by the background migrator).
	_, err := rl.db.Exec(ctx, `
		INSERT INTO request_wal_hot (request_id, tenant_id, gw_session_id, status, stage, client_model, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())
		ON CONFLICT (request_id, created_at) DO NOTHING
	`, req.RequestID, req.TenantID, req.SessionID, StatusPending, StageReceived, req.ClientModel)

	if err != nil {
		if rl.fallback != nil {
			if fallbackErr := rl.fallback.WriteRequestWAL(ctx, req.RequestID+":initial", req); fallbackErr != nil {
				slog.Warn("request_logger: initial fallback failed", "request_id", req.RequestID, "error", fallbackErr)
			}
			return nil
		}
		slog.Warn("request_logger: CreateInitial failed",
			"request_id", req.RequestID,
			"error", err)
		return err
	}
	return nil
}

func (rl *RequestLogger) Update(update *LogUpdate) {
	if !rl.Enabled() || update == nil {
		return
	}
	if rl.isDegraded() && rl.fallback != nil {
		if err := rl.fallback.WriteRequestWAL(context.Background(), update.RequestID+":update", update); err != nil {
			slog.Warn("request_logger: degraded update fallback failed", "request_id", update.RequestID, "error", err)
		}
		return
	}
	select {
	case rl.asyncQueue <- update:
	default:
		slog.Warn("request_logger: async queue full, dropping update",
			"request_id", update.RequestID,
			"stage", update.Stage)
	}
}

func (rl *RequestLogger) UpdateSync(ctx context.Context, update *LogUpdate) error {
	if !rl.Enabled() || update == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := rl.persistUpdate(ctx, update)
	if err != nil && rl.fallback != nil {
		if fallbackErr := rl.fallback.WriteRequestWAL(ctx, update.RequestID+":update", update); fallbackErr != nil {
			return fallbackErr
		}
		return nil
	}
	return err
}

func (rl *RequestLogger) worker() {
	defer rl.wg.Done()

	batch := make([]*LogUpdate, 0, rl.config.BatchSize)
	timer := time.NewTimer(rl.config.FlushTimeout)
	defer timer.Stop()

	for {
		select {
		case <-rl.done:
			rl.flushBatch(batch)
			return
		case update := <-rl.asyncQueue:
			batch = append(batch, update)
			if len(batch) >= rl.config.BatchSize {
				rl.flushBatch(batch)
				batch = batch[:0]
				timer.Reset(rl.config.FlushTimeout)
			} else if len(batch) == 1 {
				timer.Reset(rl.config.FlushTimeout)
			}
		case <-timer.C:
			if len(batch) > 0 {
				rl.flushBatch(batch)
				batch = batch[:0]
			}
		}
	}
}

func (rl *RequestLogger) flushBatch(batch []*LogUpdate) {
	if len(batch) == 0 || rl.db == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tx, err := rl.db.Begin(ctx)
	if err != nil {
		slog.Warn("request_logger: flush batch begin failed", "error", err)
		return
	}
	//nolint:errcheck
	defer tx.Rollback(ctx)

	// 2026-07-20 P0: if any update fails inside the tx, the tx enters
	// SQLSTATE 25P02 (current transaction is aborted). Continuing to call
	// tx.Exec on subsequent updates would (a) emit one misleading
	// 25P02 warning per remaining update, drowning the real root cause, and
	// (b) make the eventual tx.Commit fail with "commit unexpectedly
	// resulted in rollback" — same end-state, but with hundreds of red
	// herrings in the gateway log during high-concurrency load (S07/S12).
	//
	// Stop the loop on first failure, fall back per-row to the on-disk
	// WAL sink (preserves the most important per-row data), and let the
	// deferred Rollback at function exit reclaim the aborted tx.
	for i, update := range batch {
		if err := rl.persistUpdateInTx(ctx, tx, update); err != nil {
			firstFailed := update
			slog.Warn("request_logger: persist update in batch failed (aborting batch)",
				"request_id", firstFailed.RequestID,
				"index", i,
				"remaining", len(batch)-i-1,
				"error", err)
			if rl.anomalyRecorder != nil {
				anomalyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
				rl.anomalyRecorder.RecordDataAnomaly(anomalyCtx,
					"persistence_failed", "high",
					firstFailed.RequestID,
					"request_wal_hot persist failed in batch: "+err.Error(),
					map[string]any{"batch_index": i, "batch_size": len(batch)},
				)
				cancel()
			}
			if rl.fallback != nil {
				for j := i + 1; j < len(batch); j++ {
					remaining := batch[j]
					if fallbackErr := rl.fallback.WriteRequestWAL(ctx, remaining.RequestID+":update", remaining); fallbackErr != nil {
						slog.Warn("request_logger: update fallback failed",
							"request_id", remaining.RequestID, "error", fallbackErr)
					}
				}
			} else {
				for j := i + 1; j < len(batch); j++ {
					remaining := batch[j]
					slog.Warn("request_logger: persist update skipped due to prior tx abort",
						"request_id", remaining.RequestID)
				}
			}
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Warn("request_logger: flush batch commit failed", "error", err)
	}
}

func (rl *RequestLogger) persistUpdate(ctx context.Context, update *LogUpdate) error {
	if rl.db == nil {
		return nil
	}
	tx, err := rl.db.Begin(ctx)
	if err != nil {
		return err
	}
	//nolint:errcheck // deferred rollback, best-effort
	defer tx.Rollback(ctx)

	if err := rl.persistUpdateInTx(ctx, tx, update); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (rl *RequestLogger) persistUpdateInTx(ctx context.Context, tx pgx.Tx, update *LogUpdate) error {
	compressionMetaJSON, err := json.Marshal(update.CompressionMeta)
	if err != nil {
		compressionMetaJSON = []byte("null")
		meta := update.CompressionMeta
		slog.Warn("request_logger: compression_meta marshal failed, using null",
			"request_id", update.RequestID, "err", err)
		if rl.anomalyRecorder != nil {
			metaCopy := make(map[string]any, len(meta))
			for k, v := range meta {
				metaCopy[k] = v
			}
			anomalyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			if recErr := rl.anomalyRecorder.RecordDataAnomaly(anomalyCtx,
				"json_marshal_failed", "medium",
				update.RequestID,
				"compression_meta json.Marshal failed: "+err.Error(),
				metaCopy,
			); recErr != nil {
				slog.Warn("request_logger: failed to record marshal anomaly",
					"request_id", update.RequestID, "error", recErr)
			}
		}
	}

	// Terminal-state guard (2026-06-22 audit P0-2):
	//
	// Once a WAL row reaches a terminal status ('success' or 'failure'),
	// a later update must NOT regress it. This was a real bug: the handler
	// writes StageCompleted/StatusSuccess via async Update(), then the
	// deferred client-disconnect safety net fires UpdateSync() with
	// StageResponseFail/StatusFailure whenever the client closes the
	// connection right after reading a successful response — clobbering
	// the success row. COALESCE($3, stage) offered no protection because
	// the incoming stage is non-zero.
	//
	// The guard works by excluding already-terminal rows from the UPDATE.
	// The safety net still does its real job — promoting rows stuck at
	// 'pending' to 'failure' — because those rows are non-terminal.
	//
	// Note: we compare against the incoming update's own status. If the
	// caller passes an empty status (incremental update that only sets
	// tokens/provider), the guard is skipped via NULLIF so legitimate
	// mid-flight field updates still apply.
	// UPDATE directly targets request_wal_hot — the canonical write
	// target per the 2026-07 data-lifecycle architecture. The inner SELECT
	// for "the latest row" also reads from request_wal_hot since rows
	// that have already been migrated to a monthly partition are no
	// longer editable (columnar storage / archived partitions do not
	// support UPDATE). Late updates after migration are silently
	// dropped, which is the intended behavior.
	_, err = tx.Exec(ctx, `
		UPDATE request_wal_hot SET
			status = COALESCE(NULLIF($2, ''), status),
			stage = COALESCE($3, stage),
			completed_at = COALESCE($4, completed_at),
			upstream_request_at = COALESCE($5, upstream_request_at),
			upstream_response_at = COALESCE($6, upstream_response_at),
			upstream_provider_id = COALESCE($7, upstream_provider_id),
			upstream_credential_id = COALESCE($8, upstream_credential_id),
			completion_tokens = COALESCE($9, completion_tokens),
			prompt_tokens = COALESCE($10, prompt_tokens),
			error = COALESCE($11, error),
		compression_strategy = COALESCE(NULLIF($12, ''), compression_strategy),
		compression_meta = COALESCE($13::text::jsonb, compression_meta)
		WHERE request_id = $1
		  AND created_at = (
		      SELECT created_at FROM request_wal_hot
		      WHERE request_id = $1
		      ORDER BY created_at DESC
		      LIMIT 1
		  )
		  AND (
		      NULLIF($2, '') IS NULL
		      OR request_wal_hot.status IS NULL
		      OR request_wal_hot.status = 'pending'
		  )
	`, update.RequestID, update.Status, update.Stage, update.CompletedAt,
		update.UpstreamRequestAt, update.UpstreamResponseAt,
		update.UpstreamProviderID, update.UpstreamCredentialID,
		update.CompletionTokens, update.PromptTokens, update.Error,
		update.CompressionStrategy, compressionMetaJSON)

	if err != nil {
		return err
	}

	if len(update.OutboundBody) > 0 {
		// 2026-07-22: Use ::text::jsonb cast to prevent 22P02 errors
		// when compression_meta contains edge-case floats (NaN/Inf).
		// Matches pattern in domains/session/v2/turn_writer.go
		compressionMetaStr := string(compressionMetaJSON)
		_, err = tx.Exec(ctx, `
			INSERT INTO request_wal_bodies (request_id, outbound_body, compression_meta)
			VALUES ($1, $2, $3::text::jsonb)
			ON CONFLICT (request_id) DO UPDATE SET
				outbound_body = EXCLUDED.outbound_body,
				compression_meta = EXCLUDED.compression_meta
		`, update.RequestID, update.OutboundBody, compressionMetaStr)
		if err != nil {
			return err
		}
	}

	return nil
}

func (rl *RequestLogger) Stop() {
	close(rl.done)
	rl.wg.Wait()
}

func (rl *RequestLogger) NewUpdateBuilder() *UpdateBuilder {
	return &UpdateBuilder{
		update: &LogUpdate{},
	}
}

func (b *UpdateBuilder) RequestID(id string) *UpdateBuilder {
	b.update.RequestID = id
	return b
}

func (b *UpdateBuilder) Stage(stage int) *UpdateBuilder {
	b.update.Stage = stage
	return b
}

func (b *UpdateBuilder) Status(status string) *UpdateBuilder {
	b.update.Status = status
	return b
}

func (b *UpdateBuilder) Error(err string) *UpdateBuilder {
	b.update.Error = err
	return b
}

func (b *UpdateBuilder) OutboundBody(body []byte) *UpdateBuilder {
	b.update.OutboundBody = body
	return b
}

func (b *UpdateBuilder) CompressionStrategy(strategy string) *UpdateBuilder {
	b.update.CompressionStrategy = strategy
	return b
}

func (b *UpdateBuilder) CompressionMeta(meta map[string]interface{}) *UpdateBuilder {
	b.update.CompressionMeta = meta
	return b
}

func (b *UpdateBuilder) CompletionTokens(tokens int) *UpdateBuilder {
	b.update.CompletionTokens = tokens
	return b
}

func (b *UpdateBuilder) PromptTokens(tokens int) *UpdateBuilder {
	b.update.PromptTokens = tokens
	return b
}

func (b *UpdateBuilder) CompletedAt(t time.Time) *UpdateBuilder {
	b.update.CompletedAt = t
	return b
}

func (b *UpdateBuilder) UpstreamRequestAt(t time.Time) *UpdateBuilder {
	b.update.UpstreamRequestAt = t
	return b
}

func (b *UpdateBuilder) UpstreamResponseAt(t time.Time) *UpdateBuilder {
	b.update.UpstreamResponseAt = t
	return b
}

func (b *UpdateBuilder) UpstreamProviderID(id *int64) *UpdateBuilder {
	b.update.UpstreamProviderID = id
	return b
}

func (b *UpdateBuilder) UpstreamCredentialID(id *int64) *UpdateBuilder {
	b.update.UpstreamCredentialID = id
	return b
}

func (b *UpdateBuilder) Build() *LogUpdate {
	return b.update
}

func (b *UpdateBuilder) LogAsync(rl *RequestLogger) {
	if rl != nil {
		rl.Update(b.update)
	}
}

func (b *UpdateBuilder) LogSync(ctx context.Context, rl *RequestLogger) error {
	if rl != nil {
		return rl.UpdateSync(ctx, b.update)
	}
	return nil
}
