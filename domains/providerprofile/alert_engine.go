package providerprofile

import (
	"context"
	"fmt"
	"time"
)

// ProfileSource 向告警引擎提供"某 credential 最近 N 天的 daily profiles"。
type ProfileSource interface {
	Recent(ctx context.Context, credentialID int64, days int) ([]DailyProfile, error)
}

// EvaluateResult 单次评估的结果（供日志/测试断言）
type EvaluateResult struct {
	CredentialID int64
	ProviderID   int64
	Action       string // disabled / enabled / none
	Alerts       []Alert
}

// AlertEngine 评估并执行自动处理。
type AlertEngine struct {
	profiles ProfileSource
	alerts   AlertStore
	actor    CredentialActor
	cfg      AlertConfig
	now      func() time.Time
	// onAlert (2026-08-11) is an optional callback fired when a quality
	// degradation alert triggers (score_drop / trend_drop / dimension_low).
	// The gateway uses it to request model-IQ re-tests for the credential's
	// nodes (a "suspicious action" trigger, see docs/model-iq/01-design.md §3.4).
	// nil = disabled. Best-effort: errors from the handler are logged, not
	// propagated, so alert evaluation stays robust.
	onAlert func(ctx context.Context, credentialID, providerID int64, alertType AlertType)
}

// NewAlertEngine 创建告警引擎
func NewAlertEngine(profiles ProfileSource, alerts AlertStore, actor CredentialActor, cfg AlertConfig) *AlertEngine {
	return &AlertEngine{profiles: profiles, alerts: alerts, actor: actor, cfg: cfg, now: time.Now}
}

// SetClock 注入时钟（测试用）
func (e *AlertEngine) SetClock(f func() time.Time) { e.now = f }

// SetAlertHandler wires an optional handler invoked when a quality-degradation
// alert (score_drop / trend_drop / dimension_low) is recorded. It is the hook
// the gateway uses to trigger model-IQ re-tests. nil disables the hook.
func (e *AlertEngine) SetAlertHandler(fn func(ctx context.Context, credentialID, providerID int64, alertType AlertType)) {
	e.onAlert = fn
}

// EvaluateAll 对所有活跃凭证评估。credentialIDs 由调用方传入（来自 GatewayCredentialLister）。
func (e *AlertEngine) EvaluateAll(ctx context.Context, credentialIDs []int64) []EvaluateResult {
	var results []EvaluateResult
	for _, credID := range credentialIDs {
		res, err := e.EvaluateCredential(ctx, credID)
		if err != nil {
			res = EvaluateResult{CredentialID: credID, Action: "none"}
		}
		results = append(results, res)
	}
	return results
}

// EvaluateCredential 评估并执行单个凭证。
func (e *AlertEngine) EvaluateCredential(ctx context.Context, credentialID int64) (EvaluateResult, error) {
	// 取最近 8 天（7d 对比 + 今天）
	profiles, err := e.profiles.Recent(ctx, credentialID, 8)
	if err != nil {
		return EvaluateResult{CredentialID: credentialID, Action: "none"}, fmt.Errorf("load profiles: %w", err)
	}
	if len(profiles) == 0 {
		return EvaluateResult{CredentialID: credentialID, Action: "none"}, nil
	}
	providerID := profiles[0].ProviderID

	alerts := EvaluateAlerts(profiles, credentialID, providerID, e.cfg)
	res := EvaluateResult{CredentialID: credentialID, ProviderID: providerID, Alerts: alerts, Action: "none"}

	// 1. 记录 auto_disabled 告警，但不改变 credential 生命周期。
	// 模型级停用必须由 model_probe_state -> credential_model_bindings 完成；
	// 画像按 credential 聚合，不能让单个模型的异常下线整个凭据。
	for _, a := range alerts {
		if a.Type != AlertTypeAutoDisabled {
			continue
		}
		wl, werr := e.actor.IsWhitelisted(ctx, providerID)
		if werr != nil {
			return res, werr
		}
		if wl {
			a.ActionTaken = "none"
			a.Details = map[string]interface{}{"suppressed": "whitelist"}
			if err := e.alerts.SaveIfNew(ctx, &a); err != nil {
				return res, fmt.Errorf("save suppressed alert: %w", err)
			}
			if err := e.actor.RecordEvent(ctx, credentialID, "profile_alert_suppressed", map[string]interface{}{"type": string(a.Type), "reason": "whitelist"}); err != nil {
				return res, fmt.Errorf("record suppressed alert event: %w", err)
			}
			res.Action = "none"
			return res, nil
		}

		a.ActionTaken = "none"
		a.Details = map[string]interface{}{"action": "advisory_only"}
		if err := e.alerts.SaveIfNew(ctx, &a); err != nil {
			return res, fmt.Errorf("save auto-disable advisory: %w", err)
		}
		if err := e.actor.RecordEvent(ctx, credentialID, "profile_auto_disable_advisory", map[string]interface{}{"reason": a.Message, "dimension": a.Dimension, "score": a.CurrentScore}); err != nil {
			return res, fmt.Errorf("record auto-disable advisory: %w", err)
		}
		return res, nil
	}

	// 2. 记录 auto_enabled 告警，但不自动恢复 credential 生命周期。
	for _, a := range alerts {
		if a.Type != AlertTypeAutoEnabled {
			continue
		}
		a.ActionTaken = "none"
		a.Details = map[string]interface{}{"action": "advisory_only"}
		if err := e.alerts.SaveIfNew(ctx, &a); err != nil {
			return res, fmt.Errorf("save auto-enable advisory: %w", err)
		}
		if err := e.actor.RecordEvent(ctx, credentialID, "profile_auto_enable_advisory", map[string]interface{}{"reason": a.Message, "score": a.CurrentScore}); err != nil {
			return res, fmt.Errorf("record auto-enable advisory: %w", err)
		}
		return res, nil
	}

	// 3. 记录其余告警（score_drop / trend_drop / dimension_low）—— 仅告警，不动作
	for _, a := range alerts {
		if a.Type == AlertTypeAutoDisabled || a.Type == AlertTypeAutoEnabled {
			continue
		}
		a.ActionTaken = "none"
		if err := e.alerts.SaveIfNew(ctx, &a); err != nil {
			return res, fmt.Errorf("save alert: %w", err)
		}
		// 2026-08-11: notify the model-IQ "suspicious action" handler for
		// quality-degradation alerts so it can re-test the credential's nodes.
		// The handler is best-effort and must not panic (it logs its own errors).
		if e.onAlert != nil {
			e.onAlert(ctx, credentialID, providerID, a.Type)
		}
	}

	return res, nil
}
