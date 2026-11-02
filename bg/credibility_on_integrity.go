package bg

// credibility_on_integrity.go — C 方案：真实会话 / 漂移 worker 检出
// model_mismatch 或 fingerprint_drift（high/critical）→ 异步触发独立可信度
// ScanNode。
//
// ★ 与 IntegrityProbePlanner 的 integrity_verify **正交**：
//   planner 入队的是探活类核实；本路径发的是标定提示词身份扫描。
// ★ 与 NotifyProbeSuccess（B）共享 inflight / featured / trusted 闸；
//   冷却独立且更短（异常要更快复测，不受 verified 长冷却压制）。
// ★ 挂钩在 NotifyRecorder（采样前）+ fingerprint_drift 落库后，
//   避免 sampleRatio 把触发面悄悄掏空。

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/modelcredibility"
	"github.com/kaixuan/llm-gateway-go/settings"
)

const defaultIntegrityAnomalyCooldownHours = 1

// NotifyIntegrityAnomaly schedules an independent credibility ScanNode after
// a high/critical model_mismatch or fingerprint_drift signal. Non-blocking.
//
// providerCode may be empty (fingerprint_drift worker currently leaves it
// NULL); trusted-skip then only matches bare-model entries.
func (w *ModelCredibilityWorker) NotifyIntegrityAnomaly(credentialID int64, rawModelName, providerCode, anomalyType, source string) {
	if w == nil || w.db == nil || w.source == nil {
		return
	}
	rawModelName = strings.TrimSpace(rawModelName)
	if credentialID <= 0 || rawModelName == "" {
		return
	}
	if !w.triggerOnIntegrityAnomalyEnabled() {
		return
	}

	key := credibilityTriggerKey(credentialID, rawModelName)
	if !w.tryBeginProbeSuccessTrigger(key) {
		return
	}

	src := source
	if src == "" {
		src = "integrity_anomaly"
	}
	anom := anomalyType
	credID := credentialID
	model := rawModelName
	prov := strings.TrimSpace(providerCode)

	Go("model_credibility_on_integrity", func() {
		defer w.endProbeSuccessTrigger(key)
		timeout := w.cfg.ProbeTimeout + 30*time.Second
		if timeout < 90*time.Second {
			timeout = 90 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		if skip, reason := w.integrityAnomalyShouldSkip(ctx, credID, model, prov); skip {
			slog.Debug("model credibility: integrity-anomaly trigger skipped",
				"credential_id", credID, "model", model, "anomaly", anom,
				"source", src, "reason", reason)
			return
		}

		res, err := w.ScanNode(ctx, credID, model)
		if err != nil {
			slog.Warn("model credibility: integrity-anomaly scan failed",
				"credential_id", credID, "model", model, "anomaly", anom,
				"source", src, "error", err)
			return
		}
		verdict := ""
		if res != nil {
			verdict = string(res.Verdict)
		}
		slog.Info("model credibility: integrity-anomaly scan done",
			"credential_id", credID, "model", model, "anomaly", anom,
			"source", src, "verdict", verdict)
	})
}

func (w *ModelCredibilityWorker) triggerOnIntegrityAnomalyEnabled() bool {
	return settings.GetPlatformBool("model_credibility.trigger_on_integrity_anomaly", true)
}

func (w *ModelCredibilityWorker) integrityAnomalyCooldown() time.Duration {
	hours := settings.GetPlatformInt("model_credibility.integrity_anomaly_cooldown_hours", defaultIntegrityAnomalyCooldownHours)
	if hours < 1 {
		hours = 1
	}
	if hours > 168 {
		hours = 168
	}
	return time.Duration(hours) * time.Hour
}

// integrityAnomalyShouldSkip applies featured + trusted + short cooldown.
// Unlike probe-success, a prior "verified" verdict does NOT stretch the
// window — an integrity hit means we recheck sooner.
func (w *ModelCredibilityWorker) integrityAnomalyShouldSkip(ctx context.Context, credentialID int64, rawModelName, providerCode string) (bool, string) {
	if w.trustedSkip(providerCode, rawModelName) {
		return true, "trusted_skip"
	}
	if w.scope() == modelcredibility.ScopeFeatured {
		ok, err := w.rawModelIsFeatured(ctx, rawModelName)
		if err != nil {
			slog.Warn("model credibility: featured check failed; skipping integrity trigger to avoid waste",
				"model", rawModelName, "error", err)
			return true, "featured_check_failed"
		}
		if !ok {
			return true, "not_featured"
		}
	}

	store := modelcredibility.NewStore(w.db)
	prev, err := store.LoadPrevious(ctx, credentialID, rawModelName)
	if err != nil {
		slog.Warn("model credibility: load previous for integrity cooldown failed; proceeding",
			"credential_id", credentialID, "model", rawModelName, "error", err)
		return false, ""
	}
	if prev == nil || prev.MeasuredAt.IsZero() {
		return false, ""
	}
	if time.Since(prev.MeasuredAt) < w.integrityAnomalyCooldown() {
		return true, "cooldown_integrity"
	}
	return false, ""
}
