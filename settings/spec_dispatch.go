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
			Key:      "dispatch_v2.allow_model_change",
			Type:     TypeBool,
			Scope:    ScopePlatform,
			Category: CategoryDispatch,
			// 2026-11-16（auto 模式可用性轮）：默认 false → true。
			//
			// auto 请求（model="auto"）此前没有模型级回退：同模型凭据耗尽后
			// 直接把错误抛给客户端 ⇒ 会话中途断掉，表现为「原来的模型不可用」
			//（211 实测：30 天 87 笔 auto 请求 13 笔失败，fallback_used 全 false，
			// 其中 7 笔集中在同一条坏凭据上）。
			//
			// 作用域**仅 auto 请求**：消费点除本闸门外还要求
			// `DispatchAllowModelChange`/`qr.AllowModelChange`，二者唯一来源是
			// `logCtx.IsAutoRequest`。非 auto 请求行为不变。
			Default:     true,
			Description: "允许 auto 请求在同模型所有凭据耗尽时自动切换到其它模型（仅影响 model=auto 的请求）",
			DangerLevel: Warning,
			HotReload:   true,
		},
	}
}
