// Package streaming — audio_usage_log.go
//
// 2026-10-08 Phase B：成功 ASR 后把 audio_seconds + credits_charged
// 写入 v1 宽表（轻量 INSERT，不走完整 telemetry 列对齐）。
//
// ★ 2026-10-20（迁移 890，v1 族退役）：v1 族被整体 DROP，且会话族**没有**
// audio_seconds 列 ⇒ 本 logger 现在是**有文档的 no-op**（见 LogAudioUsage）。
// 公开接口与调用点不变。
package streaming

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgconn"
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
	// ★ 2026-10-20（迁移 890，v1 族退役）：本方法**不再写任何库**。
	//
	// 原来这里向 v1 宽表族写一行「音频计量行」（audio_seconds +
	// credits_charged）。v1 族被整体 DROP 后，这个写点**没有去处**：
	//
	//   - 会话族**没有** audio_seconds 列（真库实测：session_turns /
	//     session_turns_hot / session_turn_details* 上只有 audio_tokens，
	//     没有「秒数」这个事实）⇒ 写不进去；
	//   - usage_ledger 侧有 audio_tokens，但**没有** audio_seconds，而且
	//     计费口径与本方法不是一回事（那是账务面，不是观测面）⇒ 改写它
	//     是一次语义重设计，不在退役范围内。
	//
	// ⇒ 处置与 internal/trace.FlushToPG 的 R52 语义一致（停写即丢弃）：
	// 保留方法与调用点（公开接口不变），实现变成一个**有文档的 no-op**。
	//
	// 已登记边界（沿用 2026-10-13 的登记）：音频秒数观测（dashboard audio
	// 维度）**无会话族归宿**，与探针行同类，属「观测对象无 session 家族
	// 归宿」的登记债。计费本身走 maas/usage_ledger，**不受影响**。
	slog.Debug("audio usage log: v1 family retired, audio-seconds observation has no session-family home",
		"request_id", requestID,
		"model", model,
		"seconds", seconds)
}

// SetUsageLogger wires optional _request_logs_hot writer for ASR duration.
func (s *AudioService) SetUsageLogger(l AudioUsageLogger) {
	if s == nil {
		return
	}
	s.usageLog = l
}
