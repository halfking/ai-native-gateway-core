package settings

// ProxySpecs returns hot-reloadable egress-proxy settings.
// 读取方在 proxy 包（defaultBannedRegionsOverlay，R51）。
func ProxySpecs() []*Spec {
	return []*Spec{
		{
			Key:             "proxy.default_banned_regions",
			EnvName:         "LLM_GATEWAY_PROXY_DEFAULT_BANNED_REGIONS",
			Type:            TypeString,
			Scope:           ScopePlatform,
			Category:        CategorySecurity,
			Default:         "HK",
			Description:     "代理出口平台级默认禁用地区（逗号分隔地区码）",
			DescriptionLong: "R51：海外模型厂商普遍屏蔽香港（R35 决策），平台级默认禁用 HK 出口。该列表与订阅层/节点层 banned_regions 取并集，存量订阅未回填也被覆盖；设为空字符串可整体关闭。读取走 ≤5s 缓存（R52），改后延迟生效。注意：节点选择走缓存读，admin 地区分布统计为锁外快照。",
			Unit:            "csv",
			// R52：Warning→Dangerous。这是安全开关（地理围栏总闸），Warning
			// 档允许普通管理员整体关闭平台级禁区，权限档与影响面不符。
			DangerLevel: Dangerous,
			HotReload:       true,
		},
	}
}
