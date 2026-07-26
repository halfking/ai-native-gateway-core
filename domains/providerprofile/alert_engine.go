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
}

// NewAlertEngine 创建告警引擎
func NewAlertEngine(profiles ProfileSource, alerts AlertStore, actor CredentialActor, cfg AlertConfig) *AlertEngine {
	return &AlertEngine{profiles: profiles, alerts: alerts, actor: actor, cfg: cfg, now: time.Now}
}

// SetClock 注入时钟（测试用）
func (e *AlertEngine) SetClock(f func() time.Time) { e.now = f }

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

	// 1. 先处理 auto_disabled
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
			_ = e.alerts.SaveIfNew(ctx, &a)
			_ = e.actor.RecordEvent(ctx, credentialID, "profile_alert_suppressed", map[string]interface{}{"type": string(a.Type), "reason": "whitelist"})
			res.Action = "none"
			return res, nil
		}
		if err := e.actor.Disable(ctx, credentialID, a.Message); err != nil {
			return res, fmt.Errorf("disable credential: %w", err)
		}
		a.ActionTaken = "disabled"
		_ = e.alerts.SaveIfNew(ctx, &a)
		_ = e.actor.RecordEvent(ctx, credentialID, "profile_auto_disabled", map[string]interface{}{"reason": a.Message, "dimension": a.Dimension, "score": a.CurrentScore})
		res.Action = "disabled"
		return res, nil
	}

	// 2. 处理 auto_enabled：仅当当前是 disabled 才执行
	for _, a := range alerts {
		if a.Type != AlertTypeAutoEnabled {
			continue
		}
		lc, lerr := e.actor.CurrentLifecycle(ctx, credentialID)
		if lerr != nil {
			return res, lerr
		}
		if lc.Lifecycle != "disabled" {
			// 当前未禁用，无需恢复；丢弃该告警
			continue
		}
		if lc.ManualDisabled {
			// 管理员手动禁用，不自动恢复；记录但不动作
			a.ActionTaken = "none"
			a.Details = map[string]interface{}{"suppressed": "manual_disabled"}
			_ = e.alerts.SaveIfNew(ctx, &a)
			continue
		}
		if err := e.actor.Enable(ctx, credentialID, a.Message); err != nil {
			// ErrManualDisabled 等视为"不动作"
			a.ActionTaken = "none"
			_ = e.alerts.SaveIfNew(ctx, &a)
			continue
		}
		a.ActionTaken = "enabled"
		_ = e.alerts.SaveIfNew(ctx, &a)
		_ = e.actor.RecordEvent(ctx, credentialID, "profile_auto_enabled", map[string]interface{}{"reason": a.Message, "score": a.CurrentScore})
		res.Action = "enabled"
		return res, nil
	}

	// 3. 记录其余告警（score_drop / trend_drop / dimension_low）—— 仅告警，不动作
	for _, a := range alerts {
		if a.Type == AlertTypeAutoDisabled || a.Type == AlertTypeAutoEnabled {
			continue
		}
		a.ActionTaken = "none"
		_ = e.alerts.SaveIfNew(ctx, &a)
	}

	return res, nil
}
