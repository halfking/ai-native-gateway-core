package settings

// CategoryDispatch groups the V2 multi-tier dispatch pipeline settings.
const CategoryDispatch Category = "dispatch"

// DispatchSpecs returns the platform-scoped dispatch_v2 settings.
//
// AUDIT_24H B2b (2026-08-17): the dispatch_v2.enabled kill-switch spec was
// removed — the multi-tier pipeline is the only execute path (the legacy
// synchronous candidate loop was retired), so there is nothing for the
// switch to fall back to. allow_model_change remains a live hot-reload
// setting. See docs/会话优化v2/57-多层队列调度架构设计方案.md.
func DispatchSpecs() []*Spec {
	return []*Spec{
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
