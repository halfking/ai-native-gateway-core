package settings

// CategoryDispatch groups the V2 multi-tier dispatch pipeline settings.
const CategoryDispatch Category = "dispatch"

// DispatchSpecs returns the platform-scoped dispatch_v2 feature-flag. The gate
// is mirrored by an atomic cache in domains/dispatch/gate.go (see ADR-Disp-005)
// so the request hot path never touches the DB. Default ON per design; flip to
// OFF here or via KILL_DISPATCH_V2 to revert to the legacy synchronous
// candidate loop. See docs/会话优化v2/57-多层队列调度架构设计方案.md.
func DispatchSpecs() []*Spec {
	return []*Spec{
		{
			Key:         "dispatch_v2.enabled",
			Type:        TypeBool,
			Scope:       ScopePlatform,
			Category:    CategoryDispatch,
			Default:     true, // 默认启用多层队列调度（带 kill-switch 回退）
			Description: "启用 V2 多层队列调度（模型/凭据队列 + 并发模式削峰 + 分层故障转移）",
			DescriptionLong: "关闭后回退到同步候选循环（executor.Execute 的旧路径）。" +
				"凭据级并发模式（concurrency/rpm/tpm/disabled）由 credentials.concurrency_mode 控制。",
			DangerLevel: Breaking,
			HotReload:   true,
		},
		{
			Key:         "dispatch_v2.allow_model_change",
			Type:        TypeBool,
			Scope:       ScopePlatform,
			Category:    CategoryDispatch,
			Default:     false,
			Description: "允许同模型所有凭据耗尽时自动切换到其它模型",
			DangerLevel: Warning,
			HotReload:   true,
		},
	}
}
