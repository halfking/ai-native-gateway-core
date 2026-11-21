// Package streaming — audio_billing.go
//
// 2026-10-08 Phase B：音频端点 MaaS 按时长扣费。
// MiniMax/Xiaomi 等 ASR 按 duration 秒计费；此前音频路径只出
// X-Gw-Audio-Seconds，不进 ChargeRequest*。本文件把成功转写接到
// maas.ChargeRequestMultimodal（TokenUsage.AudioSeconds）。
package streaming

import (
	"context"
	"log/slog"
	"math"

	"github.com/kaixuan/llm-gateway-go/maas"
)

// audioMaasCharger is the narrow surface AudioService needs from maas.Service
// (keeps tests free of a full Service). Uses the owner-carrying multiplier
// variant so the audio consume ledger rows carry owner_user.
type audioMaasCharger interface {
	Enabled() bool
	ChargeRequestMultimodalWithMultiplier(ctx context.Context, tenantID, ownerUser, requestID, canonicalName string, usage maas.TokenUsage, rateMultiplier float64) (int64, float64, error)
}

// audioTenantBudgetGate 是 AudioService 可选接入的租户月度预算闸
// （938 enforcement_mode，迁移 996）。
//
//	★ 2026-11-22 审计轮修正（P0）：音频面**同样产生 MaaS 花费**
//	（chargeTranscription → ChargeRequestMultimodalWithMultiplier →
//	maas_credit_consumption_buckets），但原先**不经过**租户级预算闸
//	⇒ hard 模式的月预算上限可被音频面绕过。
//	⇒ *maas.Service 同时满足本接口 ⇒ 既有 SetMaas 接线**自动**生效；
//	测试桩不实现本接口 ⇒ nil-safe 跳过（不改变既有测试语义）。
type audioTenantBudgetGate interface {
	CheckMonthlyBudget(ctx context.Context, tenantID string) (*maas.MonthlyBudgetVerdict, error)
}

// SetMaas wires optional tenant-credit charging for audio endpoints.
func (s *AudioService) SetMaas(svc audioMaasCharger) {
	if s == nil {
		return
	}
	s.maas = svc
	// 租户级预算闸（938 enforcement_mode）：同一 concrete Service 同时满足
	// audioTenantBudgetGate 时一并接入（审计轮 2026-11-22 P0 修正）。
	if g, ok := svc.(audioTenantBudgetGate); ok {
		s.tenantBudgetGate = g
	}
}

// chargeTranscription credits the tenant for a successful ASR call and
// best-effort logs audio_seconds into request_logs_hot.
// ownerUser is the api-key owner (same source as the telemetry
// api_key_owner_user field, extracted from the authenticate() KeyInfo by the
// caller); it rides into credit_ledger.owner_user so audio consume rows are
// visible under /api/maas/my/*. Empty = owner unknown (legacy shape).
// Charge / log failures never fail the client response.
func (s *AudioService) chargeTranscription(ctx context.Context, tenantID, ownerUser, requestID, model string, durationSec *float64) {
	if s == nil {
		return
	}
	seconds := 0.0
	if durationSec != nil && *durationSec > 0 {
		seconds = *durationSec
	}
	if seconds <= 0 {
		// Upstream omitted duration (some TTS/bridge paths). Bill a minimum
		// of 1 second so zero-duration successes cannot free-ride.
		seconds = 1
	}
	usage := maas.TokenUsage{AudioSeconds: seconds}
	var amount int64
	var err error
	if s.maas != nil && s.maas.Enabled() && tenantID != "" && tenantID != "default" {
		// F-4（2026-11-04 审计）：走带 owner 的变体，音频 consume 流水
		// 不再落 owner_user=''。multiplier 显式传 1.0——音频车道此前即按
		// 隐式 1.0 计费（chargeTokens → chargeTokensWithMultiplier(1.0)），
		// 峰谷倍数接入是独立决策，本修复不改计费金额口径。
		amount, _, err = s.maas.ChargeRequestMultimodalWithMultiplier(ctx, tenantID, ownerUser, requestID, model, usage, 1.0)
		if err != nil {
			slog.Warn("audio maas charge failed",
				"request_id", requestID,
				"tenant_id", tenantID,
				"model", model,
				"audio_seconds", seconds,
				"error", err)
		} else if amount > 0 {
			slog.Info("audio maas charged",
				"request_id", requestID,
				"tenant_id", tenantID,
				"model", model,
				"audio_seconds", seconds,
				"credits", amount)
		}
	}
	if s.usageLog != nil {
		s.usageLog.LogAudioUsage(ctx, tenantID, requestID, model, seconds, amount)
	}
}

// ceilAudioSeconds rounds duration up to whole seconds for operator display;
// charging itself uses the fractional value via math.Ceil in CalcCredits.
func ceilAudioSeconds(sec float64) int {
	if sec <= 0 {
		return 0
	}
	return int(math.Ceil(sec))
}
