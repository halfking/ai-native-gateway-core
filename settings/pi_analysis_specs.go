package settings

// PIAnalysisSpecs 是 PI 错误分析的平台级配置族（2026-11-25 取证轮）。
//
//	目前只有「请求错误发现器」的三个旋钮：它把请求在上游尝试阶段的失败
//	（网关自己持久化的候选失败记录）送进 PI 分析——窗口内达到阈值的
//	(错误类型, 供应商, 模型) 分组各入队一条样本，分析前自动取证该请求的
//	账本行 / 上游尝试 / 服务日志。
//
//	★ 三个旋钮都走设置项（管理面可改、热生效），不新增环境变量——
//	  新增环境变量要过部署引擎的透传清单与登记门，而这是运维会按现场流量
//	  反复调的阈值。
//	★ 分析本身的开关（采集 / 分析 / 模型 / 超时）仍是既有的环境变量族
//	  （LLMGatewayPIError*，见 cmd/gateway/pi_agent_init.go），本族不重复。
func PIAnalysisSpecs() []*Spec {
	return []*Spec{
		{
			Key:         "pi_analysis.request_error_discovery_enabled",
			Type:        TypeBool,
			Scope:       ScopePlatform,
			Category:    CategorySecurity,
			Default:     true,
			Description: "把请求在上游尝试阶段的失败自动送进 PI 错误分析",
			DescriptionLong: "开启后 PI 分析 worker 每轮先读候选失败记录（candidate_failure_logs_hot）的最近窗口，" +
				"把达到 pi_analysis.request_error_threshold 的 (错误类型, 供应商, 模型) 分组各入队一条样本，走既有分析管线" +
				"（分析前自动取证请求账本 / 上游尝试 / 服务日志）。同一分组 24 小时内只分析一次；" +
				"客户端主动取消类失败（canceled）不入队。关闭后 PI 分析只处理 PI 检测器自身与输出守卫的入队。",
			HotReload:   true,
			DangerLevel: Safe,
		},
		{
			Key:         "pi_analysis.request_error_threshold",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategorySecurity,
			Default:     5,
			Description: "请求失败分组在窗口内出现多少次才送进 PI 分析（默认 5）",
			DescriptionLong: "防噪与省模型费用：偶发的单次失败不值得调一次模型。211 实测一小时有 1400+ 条候选失败、30 个分组，" +
				"阈值过低会把分析队列冲垮。调大 ⇒ 只关注高频失败；调小 ⇒ 更灵敏但分析更多。",
			HotReload:   true,
			DangerLevel: Safe,
		},
		{
			Key:         "pi_analysis.request_error_window_minutes",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategorySecurity,
			Default:     60,
			Description: "请求失败分组的统计窗口（分钟，5..1440；默认 60）",
			HotReload:   true,
			DangerLevel: Safe,
		},
	}
}
