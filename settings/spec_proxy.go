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
			DescriptionLong: "R51：海外模型厂商普遍屏蔽香港（R35 决策），平台级默认禁用 HK 出口。该列表与订阅层/节点层 banned_regions 取并集，存量订阅未回填也被覆盖；设为空字符串可整体关闭。每次节点选择实时读取，改后即热生效。",
			Unit:            "csv",
			DangerLevel:     Warning,
			HotReload:       true,
		},
	}
}
