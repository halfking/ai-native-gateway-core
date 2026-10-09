// Package streaming — audio_usage_log.go
//
// 2026-10-08 Phase B：成功 ASR 后把 audio_seconds + credits_charged
// 写入 request_logs_hot（轻量 INSERT，不走完整 telemetry 列对齐）。
// 缺列时 ensure 846 会补；写入失败只打日志，不影响客户端响应。
package streaming

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AudioUsageLogger persists duration billing audit rows.
type AudioUsageLogger interface {
	LogAudioUsage(ctx context.Context, tenantID, requestID, model string, seconds float64, credits int64)
}

type pgAudioUsageLogger struct {
	pool *pgxpool.Pool
}

// NewPGAudioUsageLogger writes minimal request_logs_hot rows for audio calls.
func NewPGAudioUsageLogger(pool *pgxpool.Pool) AudioUsageLogger {
	if pool == nil {
		return nil
	}
	return &pgAudioUsageLogger{pool: pool}
}

func (l *pgAudioUsageLogger) LogAudioUsage(ctx context.Context, tenantID, requestID, model string, seconds float64, credits int64) {
	if l == nil || l.pool == nil || requestID == "" {
		return
	}
	if tenantID == "" {
		tenantID = "default"
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := l.pool.Exec(cctx, `
		INSERT INTO request_logs_hot (
			request_id, ts, tenant_id, client_model, success,
			audio_seconds, credits_charged, work_type, request_mode, request_status
		) VALUES (
			$1, NOW(), $2, $3, TRUE,
			$4, $5, 'audio_transcription', 'audio', 'completed'
		)
	`, requestID, tenantID, model, seconds, credits)
	if err != nil {
		slog.Warn("audio usage log insert failed",
			"request_id", requestID,
			"error", err)
	}
}

// SetUsageLogger wires optional request_logs_hot writer for ASR duration.
func (s *AudioService) SetUsageLogger(l AudioUsageLogger) {
	if s == nil {
		return
	}
	s.usageLog = l
}
