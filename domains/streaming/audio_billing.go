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
// (keeps tests free of a full Service).
type audioMaasCharger interface {
	Enabled() bool
	ChargeRequestMultimodal(ctx context.Context, tenantID, requestID, canonicalName string, usage maas.TokenUsage) (int64, error)
}

// SetMaas wires optional tenant-credit charging for audio endpoints.
func (s *AudioService) SetMaas(svc audioMaasCharger) {
	if s == nil {
		return
	}
	s.maas = svc
}

// chargeTranscription credits the tenant for a successful ASR call and
// best-effort logs audio_seconds into request_logs_hot.
// Charge / log failures never fail the client response.
func (s *AudioService) chargeTranscription(ctx context.Context, tenantID, requestID, model string, durationSec *float64) {
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
		amount, err = s.maas.ChargeRequestMultimodal(ctx, tenantID, requestID, model, usage)
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
