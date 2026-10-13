// Package streaming — audio_usage_log.go
//
// 2026-10-08 Phase B：成功 ASR 后把 audio_seconds + credits_charged
// 写入 _request_logs_hot（轻量 INSERT，不走完整 telemetry 列对齐）。
// 缺列时 ensure 846 会补；写入失败只打日志，不影响客户端响应。
package streaming

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// AudioUsageLogger persists duration billing audit rows.
type AudioUsageLogger interface {
	LogAudioUsage(ctx context.Context, tenantID, requestID, model string, seconds float64, credits int64)
}

// audioUsageExecDB 是 logger 实际依赖的最小面（只有 Exec）。*pgxpool.Pool
// 与测试侧 pgxmock.PgxPoolIface 都满足——比钉死 *pgxpool.Pool 更可测。
type audioUsageExecDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type pgAudioUsageLogger struct {
	pool audioUsageExecDB
}

// NewPGAudioUsageLogger writes minimal _request_logs_hot rows for audio calls.
func NewPGAudioUsageLogger(pool audioUsageExecDB) AudioUsageLogger {
	if pool == nil {
		return nil
	}
	return &pgAudioUsageLogger{pool: pool}
}

func (l *pgAudioUsageLogger) LogAudioUsage(ctx context.Context, tenantID, requestID, model string, seconds float64, credits int64) {
	if l == nil || l.pool == nil || requestID == "" {
		return
	}
	// S4 停写门控（storage.request_logs_write_enabled，2026-10-13 补挂）：
	// 这是宽表家族最后一个未挂门的流量写点——音频计量行不经过 telemetry
	// 持久化链（主 INSERT/bodies/trace 全在各自门内），此前停写后仍会向
	// _request_logs_hot 落新行，破坏「v1 冻结」不变式（并让以 max(ts) 为
	// 冻结点的读侧会话补丁腿失真）。处置与 internal/trace.FlushToPG 的 R52
	// 语义一致：停写即丢弃（计费本身走 maas/usage_ledger，不受影响）。
	// 已登记边界：音频秒数观测（dashboard audio 维度）停写后无会话族
	// 落点，与探针行同类，属「观测对象无 session 家族归宿」的登记债。
	if !settings.RequestLogsWriteEnabled() {
		return
	}
	if tenantID == "" {
		tenantID = "default"
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := l.pool.Exec(cctx, `
		INSERT INTO _request_logs_hot (
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

// SetUsageLogger wires optional _request_logs_hot writer for ASR duration.
func (s *AudioService) SetUsageLogger(l AudioUsageLogger) {
	if s == nil {
		return
	}
	s.usageLog = l
}
