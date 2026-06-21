package telemetry

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	StageReceived      = 0
	StageCompressed    = 1
	StageTransformed   = 2
	StageExecuting     = 3
	StageCompleted     = 4
	StageCompressFail = 10
	StageTransformFail = 11
	StageExecuteFail   = 12
	StageResponseFail  = 13
)

const (
	StatusPending  = "pending"
	StatusSuccess  = "success"
	StatusFailure  = "failure"
)

type RequestLogger struct {
	db         *pgxpool.Pool
	asyncQueue chan *LogUpdate
	config    *RequestLoggerConfig
	wg        sync.WaitGroup
	done      chan struct{}
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
	CompressionStrategy   string
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
		config:    cfg,
		done:      make(chan struct{}),
	}
	rl.wg.Add(1)
	go rl.worker()
	return rl
}

func (rl *RequestLogger) Enabled() bool {
	return rl != nil && rl.config != nil && rl.config.Enabled && rl.db != nil
}

func (rl *RequestLogger) CreateInitial(ctx context.Context, req *InitialRequest) error {
	if !rl.Enabled() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := rl.db.Exec(ctx, `
		INSERT INTO request_logs (request_id, tenant_id, gw_session_id, status, stage, client_model, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())
		ON CONFLICT (request_id, created_at) DO NOTHING
	`, req.RequestID, req.TenantID, req.SessionID, StatusPending, StageReceived, req.ClientModel)

	if err != nil {
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
	return rl.persistUpdate(ctx, update)
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
	if len(batch) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tx, err := rl.db.Begin(ctx)
	if err != nil {
		slog.Warn("request_logger: flush batch begin failed", "error", err)
		return
	}
	defer tx.Rollback(ctx)

	for _, update := range batch {
		if err := rl.persistUpdateInTx(ctx, tx, update); err != nil {
			slog.Warn("request_logger: persist update in batch failed",
				"request_id", update.RequestID,
				"error", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Warn("request_logger: flush batch commit failed", "error", err)
	}
}

func (rl *RequestLogger) persistUpdate(ctx context.Context, update *LogUpdate) error {
	tx, err := rl.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := rl.persistUpdateInTx(ctx, tx, update); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (rl *RequestLogger) persistUpdateInTx(ctx context.Context, tx pgx.Tx, update *LogUpdate) error {
	compressionMetaJSON, _ := json.Marshal(update.CompressionMeta)

	_, err := tx.Exec(ctx, `
		UPDATE request_logs SET
			status = COALESCE(NULLIF($2, ''), status),
			stage = COALESCE($3, stage),
			completed_at = COALESCE($4, completed_at),
			upstream_request_at = COALESCE($5, upstream_request_at),
			upstream_response_at = COALESCE($6, upstream_response_at),
			upstream_provider_id = COALESCE($7, upstream_provider_id),
			upstream_credential_id = COALESCE($8, upstream_credential_id),
			completion_tokens = COALESCE($9, completion_tokens),
			prompt_tokens = COALESCE($10, prompt_tokens),
			error = COALESCE($11, error)
		WHERE request_id = $1
		  AND created_at = (
		      SELECT created_at FROM request_logs
		      WHERE request_id = $1
		      ORDER BY created_at DESC
		      LIMIT 1
		  )
	`, update.RequestID, update.Status, update.Stage, update.CompletedAt,
		update.UpstreamRequestAt, update.UpstreamResponseAt,
		update.UpstreamProviderID, update.UpstreamCredentialID,
		update.CompletionTokens, update.PromptTokens, update.Error)

	if err != nil {
		return err
	}

	if len(update.OutboundBody) > 0 {
		_, err = tx.Exec(ctx, `
			INSERT INTO request_bodies (request_id, outbound_body, compression_meta)
			VALUES ($1, $2, $3)
			ON CONFLICT (request_id) DO UPDATE SET
				outbound_body = EXCLUDED.outbound_body,
				compression_meta = EXCLUDED.compression_meta
		`, update.RequestID, update.OutboundBody, compressionMetaJSON)
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
