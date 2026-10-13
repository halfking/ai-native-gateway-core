package streaming

// audio_usage_gate_test.go — S4 停写门在音频计量写点上的行为判据（2026-10-13）。
//
// LogAudioUsage 是宽表家族最后一个未挂门的流量写点（不走 telemetry 持久化
// 链）。本判据钉两件事：
//  1. 停写（storage.request_logs_write_enabled=false）时对 request_logs_hot
//     **零写入**——pgxmock 对未预期的 Exec 直接报错，因此「不设任何期望」
//     本身就是行为断言（与 telemetry 的 TestInsertRequestLogStopWriteGate
//     同一手法）；
//  2. 门开时 INSERT 照常发出——这是「判据在测门而不是在测空函数」的前提
//     在场断言（AGENTS.md 判据纪律第 3 条：放行侧必须配前提）。
//
// 变异验证：去掉 audio_usage_log.go 里的 RequestLogsWriteEnabled 早退后，
// 用例 1 会因 pgxmock 收到未预期 Exec 而变红（已实测）。

import (
	"context"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// withAudioGateSettings 把 settings.Global 钉在给定停写值上，恢复交由
// t.Cleanup。形状复用 session_routing_test.go 的 fakeSettingsBackend。
func withAudioGateSettings(t *testing.T, writeEnabled bool) {
	t.Helper()
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })

	store := map[string][]byte{
		"storage.request_logs_write_enabled": []byte(map[bool]string{true: "true", false: "false"}[writeEnabled]),
	}
	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, fakeSettingsBackend{store: store})
	for _, spec := range settings.StorageSpecs() {
		registry.MustRegisterSpec(spec)
	}
	settings.Global = registry
}

func TestLogAudioUsageStopWriteGate(t *testing.T) {
	t.Run("gate closed drops the request_logs_hot insert", func(t *testing.T) {
		withAudioGateSettings(t, false)
		pool, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer pool.Close()
		// 不注册任何期望：收到任何 Exec 都会让 mock.Stalled 报错。
		logger := NewPGAudioUsageLogger(pool)
		logger.LogAudioUsage(context.Background(), "default", "req-audio-gate-1", "minimax-m3", 12.5, 30)
		require.NoError(t, pool.ExpectationsWereMet())
	})

	t.Run("gate open still emits the insert", func(t *testing.T) {
		withAudioGateSettings(t, true)
		pool, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer pool.Close()
		pool.ExpectExec(`INSERT INTO request_logs_hot`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
		logger := NewPGAudioUsageLogger(pool)
		logger.LogAudioUsage(context.Background(), "default", "req-audio-gate-2", "minimax-m3", 12.5, 30)
		require.NoError(t, pool.ExpectationsWereMet())
	})
}
