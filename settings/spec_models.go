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
		{
			// 2026-10-29：/v1/models 与 /v1beta/models 默认只输出
			// 「特色模型」（routing_policy.featured_models），全量清单改为
			// 显式参数（?scope=all 或 ?all=true）才输出。
			//
			// 理由：精选清单才是「网关推荐什么」的答案——路由侧的
			// FeaturedBonus 加权（autoroute）、featured 优先选凭据
			// （bg/shared_pick.go）、候选建议的 tier-2
			// （domains/streaming）、管理侧的精选过滤（admin/routing.go）
			// 全都以它为准。此前发现端点无条件输出全量（600+ 条），
			// 客户端拿到的是一份没有优先级、没有上下文长度、名字还带
			// 供应商后缀的清单。
			//
			// 本项是**运维默认口径**，不是上限：请求参数
			// （?scope=… / ?all=…）优先于它。需要全量的客户端不必等
			// 运维改配置；把本项设为 all 则让「默认」回到全量，无需发版。
			Key:             "models.list_scope",
			Type:            TypeEnum,
			Scope:           ScopePlatform,
			Category:        CategoryModels,
			Default:         "featured",
			Options:         []string{"featured", "all"},
			Unit:            "枚举",
			Description:     "模型列表默认口径：featured（只列特色模型）| all（全量）",
			DescriptionLong: "模型发现端点（/v1/models 与 /v1beta/models）默认输出的清单口径。featured=只列 routing_policy.featured_models 里的特色模型（与路由侧的精选优先级、管理侧精选过滤同源）；all=列出当前全部可路由模型。客户端可用 ?scope=all 或 ?all=true 显式要全量（请求参数优先于本项）。注意：featured 口径筛不出任何模型时（精选清单为空、或精选模型当前都不可路由），网关会退回全量并在日志里 WARN——发现端点返回空清单会打断所有读它的客户端。",
			DangerLevel:     Warning,
			HotReload:       true,
			Observability:   "/v1/models",
		},
	}
}
