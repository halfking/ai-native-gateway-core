// Package routingstate - 路由状态观察者（LEGACY）
//
// ⚠️ LEGACY: 此包用于影子模式观察，已被 domains/ursm/v2 替代。
//
// Phase 3 (2026-07-25) 状态:
//   - ✅ ShadowObserver 仅用于影子模式（迁移期间观察数据对比）
//   - ✅ 在 URSM v2 authoritative 模式下跳过（由 StateBackend.IsAuthoritative 守卫）
//   - ⏸️ 等待 v3.0 移除（URSM v2 充分验证后）
//
// 维护原则:
//   - 不接受新功能
//   - 仅在影子模式下激活（settings 控制）
//   - URSM v2 验证完成后即可移除
//
// 替代关系:
//   - 生产路径：domains/ursm/v2.Manager.FilterAndScore()
//   - 影子模式：此包仅用于数据对比，不影响生产决策

package routingstate

import (
	"strings"

	"github.com/kaixuan/llm-gateway-go/settings"
)

const (
	stateCoordinatorModeKey = "routing_state.coordinator_mode"
	probeCoordinatorModeKey = "routing_state.probe_coordinator_mode"
)

// ShadowObserver is the optional executor seam for passive state/probe
// observation. It never writes routing state, invalidates caches, or dispatches
// probes. Platform settings are read for every event so shadow mode is hot
// reloadable without rebuilding the gateway.
//
// ⚠️ LEGACY: 仅用于影子模式对比。新决策请使用 domains/ursm/v2.Manager。
type ShadowObserver struct {
	state *Coordinator
	probe *ProbeCoordinator
}

func NewShadowObserver() *ShadowObserver {
	return &ShadowObserver{
		state: NewCoordinator(ModeShadow),
		probe: NewProbeCoordinator(ModeShadow),
	}
}

func (o *ShadowObserver) ObserveState(evidence Evidence) {
	if o == nil || !shadowEnabled(stateCoordinatorModeKey) {
		return
	}
	transition := o.state.Observe(evidence)
	recordEvidence(evidence.Source, evidence.Scope, transition.Accepted, transition.Reason)
}

func (o *ShadowObserver) ObserveProbe(task ProbeTask) {
	if o == nil || !shadowEnabled(probeCoordinatorModeKey) {
		return
	}
	decision := o.probe.Observe(task)
	recordProbeDecision(task.Trigger, task.Scope, decision.Accepted, decision.Reason)
	if decision.Accepted {
		// Shadow mode owns no execution lifecycle. Release immediately so a
		// previous observation never suppresses a later real probe signal.
		o.probe.Complete(task)
	}
}

func shadowEnabled(key string) bool {
	return strings.EqualFold(strings.TrimSpace(settings.GetPlatformString(key, "off")), string(ModeShadow))
}
