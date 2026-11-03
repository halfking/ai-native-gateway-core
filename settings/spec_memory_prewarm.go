package settings

// MemoryPrewarmSpecs 是 memora 上下文预热注入（P1-2③，ADR
// memory-prewarm-injection §2）的灰度开关配置。
//
// 读者：internal/memoraprewarm/wiring.go 的 SyncGateFromSettings（15s 周期
// 灌 Gate）；数据面 Gate.Allowed 纯原子读，本组键的热更生效上界 =
// GateSyncInterval。
func MemoryPrewarmSpecs() []*Spec {
	return []*Spec{
		{
			Key:             "memory.prewarm.enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategorySession,
			Default:         false,
			Description:     "memora 上下文预热注入总开关",
			DescriptionLong: "开启后，名单内租户的会话请求将触发 memora 记忆预热（异步旁路，G3 不阻塞数据面）并把命中的长期记忆注入出站请求最后一条 user 消息尾部（有界 2KB）。**默认关闭**：注入改变出站 prompt 字节，属数据面行为变化，须与 P1-4 评测门禁（Recall@5 ≥ 0.25 且 nDCG@10 ≥ 90%）联用放量。放行名单见 memory.prewarm.tenant_allowlist；名单外租户天然构成对照组。",
			Unit:            "",
			DangerLevel:     Dangerous,
			HotReload:       true,
			Observability:   "gateway_memory_prewarm_total{outcome}",
		},
		{
			Key:             "memory.prewarm.tenant_allowlist",
			Type:            TypeString,
			Scope:           ScopePlatform,
			Category:        CategorySession,
			Default:         "",
			Description:     "memora 预热注入租户灰度名单（逗号分隔）",
			DescriptionLong: "逗号分隔的租户 ID 名单。**空 = 全平台放量**（须 memory.prewarm.enabled 同时开启）；非空 = 仅名单内租户放行，名单外租户既不预热也不注入（对照组）。生效上界 15s（settings→Gate 同步周期）。",
			Unit:            "",
			DangerLevel:     Warning,
			HotReload:       true,
			Observability:   "gateway_memory_prewarm_total{outcome}",
		},
	}
}
