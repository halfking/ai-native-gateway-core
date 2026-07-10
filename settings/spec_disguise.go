package settings

// DisguiseSpecs — 8 平台级配置 — UA/TLS 伪装模块
// 对应 LLM_GATEWAY_DISGUISE_ROTATION_INTERVAL / _UA_POOL_SIZE /
// _LANG_POOL_SIZE / _PLATFORM_FILTER / _ENABLE_TLS_FINGERPRINT /
// _FP_SLOT_CONCURRENCY / _ACTIVE_GATE_SECONDS / _RECLAIM_IDLE_SECONDS
func DisguiseSpecs() []*Spec {
	min10 := 10.0
	max100 := 100.0
	min5 := 5.0
	max50 := 50.0
	min1 := 1.0
	max50_2 := 50.0
	min60 := 60.0
	max600 := 600.0
	min0 := 0.0
	max3600 := 3600.0

	return []*Spec{
		{
			Key:             "disguise.rotation_interval",
			Type:            TypeEnum,
			Scope:           ScopePlatform,
			Category:        CategorySecurity,
			Options:         []string{"5m", "15m", "30m", "1h", "6h", "24h"},
			Default:         "30m",
			Description:     "UA 轮换间隔",
			DescriptionLong: "控制 User-Agent 池的重新洗牌频率。30m=每30分钟洗牌一次，模拟浏览器重装；1h=每小时；6h=每6小时；24h=每天。较短的间隔能增加指纹多样性，但会降低单会话稳定性。",
			Unit:            "",
			DangerLevel:     Warning,
			HotReload:       true,
		},
		{
			Key:             "disguise.ua_pool_size",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategorySecurity,
			Min:             &min10,
			Max:             &max100,
			Default:         50.0,
			Description:     "UA 池大小",
			DescriptionLong: "使用的 User-Agent 字符串数量上限。较大的池能提供更高的指纹多样性，但会增加内存占用。默认 50 条。",
			Unit:            "条",
			DangerLevel:     Warning,
			HotReload:       true,
		},
		{
			Key:             "disguise.lang_pool_size",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategorySecurity,
			Min:             &min5,
			Max:             &max50,
			Default:         30.0,
			Description:     "Accept-Language 池大小",
			DescriptionLong: "使用的 Accept-Language 字符串数量上限。覆盖全球主要语言区域，默认 30 条。",
			Unit:            "条",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "disguise.platform_filter",
			Type:            TypeEnum,
			Scope:           ScopePlatform,
			Category:        CategorySecurity,
			Options:         []string{"all", "desktop", "mobile"},
			Default:         "all",
			Description:     "目标平台过滤",
			DescriptionLong: "限制使用的 UA 字符串所属平台。all=全部平台（Windows/Mac/Linux/Android/iOS）；desktop=仅桌面端（Windows/Mac/Linux）；mobile=仅移动端（Android/iOS）。",
			Unit:            "",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "disguise.enable_tls_fingerprint",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategorySecurity,
			Default:         false,
			Description:     "启用 TLS 指纹轮换",
			DescriptionLong: "使用 utls 库对 TLS ClientHello 进行指纹轮换，伪装成不同浏览器/设备的 TLS 握手特征。需要重启网关进程才能生效。参考 docs/legal/disguise-compliance.md 了解合规影响。",
			Unit:            "",
			DangerLevel:     Breaking,
			HotReload:       false,
		},
		{
			Key:             "disguise.fp_slot_concurrency",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategorySecurity,
			Min:             &min1,
			Max:             &max50_2,
			Default:         20.0,
			Description:     "每个凭据的指纹 Slot 并发数",
			DescriptionLong: "每个凭据可以同时持有的虚拟指纹 Slot 数量。每个 Slot 代表一个独立的虚拟设备身份（UA+TLS 指纹组合）。较大的值允许更多并发会话，但会增加凭据的指纹多样性和资源消耗。默认 20。",
			Unit:            "个",
			DangerLevel:     Warning,
			HotReload:       true,
		},
		{
			Key:             "disguise.active_gate_seconds",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategorySecurity,
			Min:             &min60,
			Max:             &max600,
			Default:         300.0,
			Description:     "Slot 活跃门限（秒）",
			DescriptionLong: "指纹 Slot 的活跃保护时间窗口。在此时间内活跃的 Slot 不允许被其他请求抢占，确保同一凭据的会话稳定性。默认 300 秒（5 分钟）。",
			Unit:            "秒",
			DangerLevel:     Warning,
			HotReload:       true,
		},
		{
			Key:             "disguise.reclaim_idle_seconds",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategorySecurity,
			Min:             &min0,
			Max:             &max3600,
			Default:         1800.0,
			Description:     "空闲 Slot 自动回收时间（秒）",
			DescriptionLong: "指纹 Slot 空闲超过此时间后，后台回收 goroutine 将自动清除该 Slot。默认 1800 秒（30 分钟），与 Slot TTL 一致。",
			Unit:            "秒",
			DangerLevel:     Safe,
			HotReload:       true,
		},
	}
}
