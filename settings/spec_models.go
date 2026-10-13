package settings

const CategoryModels Category = "models"

// ModelsSpecs registers the /v1/models discovery-surface controls.
func ModelsSpecs() []*Spec {
	return []*Spec{
		{
			// r1027（2026-10-14）：/v1/models 只列真正可路由的模型。
			// 旧查询以 model_offers.available 为活度源，而路由实际走
			// credential_model_bindings + 探测状态——余额耗尽/协议失配的
			// 死凭据只翻后者，客户端于是从发现端点选中永远 no_candidate
			// 的死模型（154 实测：opencode 对 minimax-m2.7-quickspeed 的
			// 固定辅助请求 400，grok-4.7 间歇 no_candidate）。
			// 关闭本开关即回到 model_offers 旧语义（逃生舱，热加载）。
			Key:             "models.list_only_routable",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryModels,
			Default:         true,
			Description:     "/v1/models 只列当前有可路由候选的模型（r1027）",
			DescriptionLong: "开启后 /v1/models 的活度源从 model_offers.available 换成与路由一致的 credential_model_bindings.available + 凭据/供应商存活门 + node_probe_state.consecutive_failures=0（与 UrsmlHealthyKeyReconciler 的 healthyCandidateSQL 同款谓词），零绑定与死凭据模型不再出现在发现列表。关闭即回到 model_offers 旧语义。注意：绑定探测翻转会使个别模型在列表中短暂进出，这是如实反映当前可路由性。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
	}
}
