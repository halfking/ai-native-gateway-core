package settings

// TieredQuotaExemptionSpecs —— 三级/四级配额闸（租户→组织→用户→application）
// 的豁免独立开关（2026-11-28 owner 决策落地）。
//
// 存量口径（迁移 996/938 时代）：default 租户与内部回环 key（IsInternal）
// 恒被豁免，豁免写死在判定体内。owner 决策：豁免改为**独立可配置**——
//   - quota.tiered_exempt_default_tenant（默认 true）：false ⇒ default 租户
//     的请求也过逐级配额闸（default 租户自身的钱包/预算行生效即拦）；
//   - quota.tiered_exempt_internal_keys（默认 true）：false ⇒ 内部回环 key
//     （IsInternal，探针旁路）也过闸（探针流量计入配额）。
//
// ★ 语义安全边界：两个开关**只收窄豁免**（true=豁免、false=不豁免），
//   不新增豁免对象；默认值 = 存量行为 ⇒ 激活即日零行为变化。
// ★ 热更（HotReload）：改完下一个请求即生效，不重启。
// ★ fail-open 纪律不受影响：设置读取失败按默认值（true=豁免）走。
func TieredQuotaExemptionSpecs() []*Spec {
	return []*Spec{
		{
			Key:         "quota.tiered_exempt_default_tenant",
			Type:        TypeBool,
			Scope:       ScopePlatform,
			Category:    CategoryRateLimit,
			Default:     true,
			Description: "default 租户豁免逐级配额闸",
			DescriptionLong: "true（默认）⇒ default 租户（管理面/平台自身所在租户）的请求**跳过**租户→组织→用户→application 逐级配额闸（存量行为）；" +
				"false ⇒ default 租户也过闸——default 租户名下的钱包/预算配置生效即拦。★ 本开关只收窄豁免、不新增豁免对象；热更，改完下个请求生效。",
			Unit:        "",
			DangerLevel: Dangerous,
			HotReload:   true,
		},
		{
			Key:         "quota.tiered_exempt_internal_keys",
			Type:        TypeBool,
			Scope:       ScopePlatform,
			Category:    CategoryRateLimit,
			Default:     true,
			Description: "内部回环 key 豁免逐级配额闸",
			DescriptionLong: "true（默认）⇒ 内部回环 key（api_keys.is_internal，探针旁路）的请求**跳过**逐级配额闸（存量行为）；" +
				"false ⇒ 内部 key 也过闸——探针/自检流量计入配额。★ 探针流量本来就被租户月度预算门（996）计入花费，关闭豁免后口径对齐；热更。",
			Unit:        "",
			DangerLevel: Dangerous,
			HotReload:   true,
		},
	}
}

// TieredExemptDefaultTenant —— 判定体运行时读取口（HotReload，读失败按默认 true）。
func TieredExemptDefaultTenant() bool {
	return GetPlatformBool("quota.tiered_exempt_default_tenant", true)
}

// TieredExemptInternalKeys —— 判定体运行时读取口（HotReload，读失败按默认 true）。
func TieredExemptInternalKeys() bool {
	return GetPlatformBool("quota.tiered_exempt_internal_keys", true)
}
