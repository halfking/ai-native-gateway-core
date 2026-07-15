package settings

const CategoryRoutingState Category = "routing_state"

// RoutingStateSpecs exposes the new coordinators as shadow-only controls.
// Authoritative modes are intentionally not registered until a separate
// rollout has passed pre-production divergence checks.
func RoutingStateSpecs() []*Spec {
	return []*Spec{
		{
			Key:         "routing_state.coordinator_mode",
			Type:        TypeString,
			Scope:       ScopePlatform,
			Category:    CategoryRoutingState,
			Default:     "off",
			Description: "统一路由状态协调器模式：off 或 shadow。shadow 仅记录证据裁决差异，不写入状态。",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         "routing_state.probe_coordinator_mode",
			Type:        TypeString,
			Scope:       ScopePlatform,
			Category:    CategoryRoutingState,
			Default:     "off",
			Description: "统一探测协调器模式：off 或 shadow。shadow 仅记录准入、去重和抑制结果，不调度探测。",
			DangerLevel: Safe,
			HotReload:   true,
		},
		{
			Key:         "routing_state.capability_substitution_mode",
			Type:        TypeString,
			Scope:       ScopePlatform,
			Category:    CategoryRoutingState,
			Default:     "off",
			Description: "内部能力替代模式：off 或 shadow。shadow 仅比较扩展候选，不改变路由 winner。",
			DangerLevel: Safe,
			HotReload:   true,
		},
	}
}
