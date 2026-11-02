package bg

// credibility_on_probe_success.go — B 方案：探活成功 → 异步触发独立可信度扫描。
//
// ★ 独立任务（不共享探活请求体）：
//   探活只证明「活着」；可信度要另发**标定提示词**（身份 / 指纹 / 能力复用
//   model_iq_runs）。混在同一次探活请求里既测不准同族降级，也会把探活
//   预算绑死在身份探测上。
//
// ★ 成本闸（探活机制本身已经很省：成功后 30 天停泊）：
//   1. settings：model_credibility.trigger_on_probe_success（默认 true）
//   2. 冷却：距上次 measured_at < cooldown 则跳过
//      · verified → 完整 cooldown（默认 = interval_hours，6h）
//      · suspected/diluted/unknown → 较短 recheck（默认 1h）—— 异常要更快复测
//   3. 进程内 inflight 去重：同一 (cred,model) 同时只跑一条
//   4. scope=featured 时，非特色模型不触发（与定时扫描同口径；手动 trigger 仍全量）
//   5. trusted_skip 命中跳过（与 integrity / 定时同口径；手动仍可查）
//
// ★ 绝不挂在 MarkNodeProbeHealthy（业务成功）上 —— 那条路径高频，
//   会把「探活预算」放大成「每次真实请求再打一次标定」。

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/modelcredibility"
	"github.com/kaixuan/llm-gateway-go/settings"
)

const (
	defaultProbeSuccessCooldownHours = 6
	defaultProbeSuccessRecheckHours  = 1
)

// NotifyProbeSuccess schedules an independent credibility ScanNode after a
// liveness/model probe reported success. Non-blocking; safe to call from
// the probe hot path.
//
// source 只进日志（如 node_probe_success），便于审计「是谁触发的」。
func (w *ModelCredibilityWorker) NotifyProbeSuccess(credentialID int64, rawModelName, source string) {
	if w == nil || w.db == nil || w.source == nil {
		return
	}
	rawModelName = strings.TrimSpace(rawModelName)
	if credentialID <= 0 || rawModelName == "" {
		return
	}
	if !w.triggerOnProbeSuccessEnabled() {
		return
	}

	key := credibilityTriggerKey(credentialID, rawModelName)
	if !w.tryBeginProbeSuccessTrigger(key) {
		return
	}

	src := source
	if src == "" {
		src = "probe_success"
	}
	credID := credentialID
	model := rawModelName
	Go("model_credibility_on_probe_success", func() {
		defer w.endProbeSuccessTrigger(key)
		timeout := w.cfg.ProbeTimeout + 30*time.Second
		if timeout < 90*time.Second {
			timeout = 90 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		if skip, reason := w.probeSuccessShouldSkip(ctx, credID, model); skip {
			slog.Debug("model credibility: probe-success trigger skipped",
				"credential_id", credID, "model", model, "source", src, "reason", reason)
			return
		}

		res, err := w.ScanNode(ctx, credID, model)
		if err != nil {
			slog.Warn("model credibility: probe-success scan failed",
				"credential_id", credID, "model", model, "source", src, "error", err)
			return
		}
		verdict := ""
		if res != nil {
			verdict = string(res.Verdict)
		}
		slog.Info("model credibility: probe-success scan done",
			"credential_id", credID, "model", model, "source", src, "verdict", verdict)
	})
}

func credibilityTriggerKey(credentialID int64, rawModelName string) string {
	return fmt.Sprintf("%d|%s", credentialID, strings.ToLower(strings.TrimSpace(rawModelName)))
}

// probeSuccessTriggerMu guards the in-flight set for probe-success triggers.
// Kept on the worker so multiple probe surfaces share one dedupe map.
func (w *ModelCredibilityWorker) ensureProbeSuccessState() {
	w.probeSuccessOnce.Do(func() {
		w.probeSuccessInflight = make(map[string]struct{})
	})
}

func (w *ModelCredibilityWorker) tryBeginProbeSuccessTrigger(key string) bool {
	w.ensureProbeSuccessState()
	w.probeSuccessMu.Lock()
	defer w.probeSuccessMu.Unlock()
	if _, ok := w.probeSuccessInflight[key]; ok {
		return false
	}
	w.probeSuccessInflight[key] = struct{}{}
	return true
}

func (w *ModelCredibilityWorker) endProbeSuccessTrigger(key string) {
	w.probeSuccessMu.Lock()
	delete(w.probeSuccessInflight, key)
	w.probeSuccessMu.Unlock()
}

func (w *ModelCredibilityWorker) triggerOnProbeSuccessEnabled() bool {
	return settings.GetPlatformBool("model_credibility.trigger_on_probe_success", true)
}

func (w *ModelCredibilityWorker) probeSuccessCooldown() time.Duration {
	hours := settings.GetPlatformInt("model_credibility.probe_success_cooldown_hours", defaultProbeSuccessCooldownHours)
	if hours < 1 {
		hours = 1
	}
	if hours > 168 {
		hours = 168
	}
	return time.Duration(hours) * time.Hour
}

func (w *ModelCredibilityWorker) probeSuccessRecheck() time.Duration {
	hours := settings.GetPlatformInt("model_credibility.probe_success_recheck_hours", defaultProbeSuccessRecheckHours)
	if hours < 1 {
		hours = 1
	}
	cooldown := w.probeSuccessCooldown()
	d := time.Duration(hours) * time.Hour
	if d > cooldown {
		return cooldown
	}
	return d
}

// probeSuccessShouldSkip applies trusted + scope + cooldown gates. Returns
// skip reason for Debug logs (empty when should run).
//
// providerCode is optional; when empty, only bare-model trusted entries match.
func (w *ModelCredibilityWorker) probeSuccessShouldSkip(ctx context.Context, credentialID int64, rawModelName string) (bool, string) {
	return w.probeSuccessShouldSkipWithProvider(ctx, credentialID, rawModelName, "")
}

func (w *ModelCredibilityWorker) probeSuccessShouldSkipWithProvider(ctx context.Context, credentialID int64, rawModelName, providerCode string) (bool, string) {
	if w.trustedSkip(providerCode, rawModelName) {
		return true, "trusted_skip"
	}
	if w.scope() == modelcredibility.ScopeFeatured {
		ok, err := w.rawModelIsFeatured(ctx, rawModelName)
		if err != nil {
			slog.Warn("model credibility: featured check failed; skipping trigger to avoid waste",
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
		slog.Warn("model credibility: load previous for cooldown failed; proceeding",
			"credential_id", credentialID, "model", rawModelName, "error", err)
		return false, ""
	}
	if prev == nil || prev.MeasuredAt.IsZero() {
		return false, ""
	}
	age := time.Since(prev.MeasuredAt)
	switch prev.Verdict {
	case modelcredibility.VerdictVerified:
		if age < w.probeSuccessCooldown() {
			return true, "cooldown_verified"
		}
	case modelcredibility.VerdictSuspected, modelcredibility.VerdictDiluted, modelcredibility.VerdictUnknown:
		if age < w.probeSuccessRecheck() {
			return true, "cooldown_recheck"
		}
	default:
		if age < w.probeSuccessCooldown() {
			return true, "cooldown_default"
		}
	}
	return false, ""
}

// rawModelIsFeatured reports whether rawModelName is on routing_policy.featured_models
// (case-insensitive). Empty featured list ⇒ nothing is featured.
func (w *ModelCredibilityWorker) rawModelIsFeatured(ctx context.Context, rawModelName string) (bool, error) {
	const q = `
		SELECT EXISTS (
		  SELECT 1
		    FROM routing_policy pol
		   WHERE pol.tenant_id = 'default'
		     AND (
		       lower($1) = ANY (SELECT lower(x) FROM unnest(COALESCE(pol.featured_models, ARRAY[]::text[])) AS x)
		     )
		)`
	var ok bool
	err := w.db.QueryRow(ctx, q, strings.TrimSpace(rawModelName)).Scan(&ok)
	return ok, err
}
