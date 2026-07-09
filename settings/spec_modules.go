package settings

import "encoding/json"

// CategoryModules groups all module-level settings together
const CategoryModules Category = "modules"

// ModuleSpecs returns all platform-scoped module management specs.
// Each feature module gets an enable/disable toggle; new enterprise
// integrations (Feishu bot, webhook, etc.) are defined here as well.
func ModuleSpecs() []*Spec {
	return []*Spec{
		// ── Audit logging ─────────────────────────────────────────────
		{
			Key:             "audit.enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         true,
			Description:     "审计日志",
			DescriptionLong: "记录所有 API 请求的审计追踪（谁、何时、做了什么）。关闭后仍保留当前请求的部分关键日志，但审计管理页面将不可用。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "prompt_injection.enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         true,
			Description:     "提示词注入检测",
			DescriptionLong: "LLM-as-judge 检测提示词注入攻击和角色劫持。仅检测模式，不拦截请求。关闭后完全跳过注入分析。",
			DangerLevel:     Warning,
			HotReload:       true,
		},
		{
			Key:             "output_compliance.enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         true,
			Description:     "输出合规检测",
			DescriptionLong: "检查 LLM 输出是否包含敏感数据（PII、密钥、内部 IP 等）。发现时触发告警或脱敏。关闭后跳过所有输出检查。",
			DangerLevel:     Warning,
			HotReload:       true,
		},
		{
			Key:             "session_audit.enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         true,
			Description:     "会话审计与审批",
			DescriptionLong: "高风险会话自动触发审计审批流程。管理员可以批准/拒绝高风险操作。关闭后所有会话直接放行。",
			DangerLevel:     Warning,
			HotReload:       true,
		},
		{
			Key:             "session_inspector.enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         true,
			Description:     "会话健康检查",
			DescriptionLong: "监控会话 token 限制、不活跃超时、高频请求等健康指标。关闭后跳过所有会话健康检查。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "security.enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         true,
			Description:     "安全检测（意图+威胁）",
			DescriptionLong: "意图分析与威胁检测引擎。识别恶意意图（越狱、数据泄露）并触发预设响应策略。关闭后跳过所有安全检测。",
			DangerLevel:     Dangerous,
			HotReload:       true,
		},
		{
			Key:             "rate_limit.enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         true,
			Description:     "限流控制",
			DescriptionLong: "RPM / 并发 / TPM 多维度流量控制。关闭后将不限制任何请求速率（危险操作，仅限调试场景）。",
			DangerLevel:     Breaking,
			HotReload:       true,
		},
		{
			Key:             "feishu_bot.enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         false,
			Description:     "飞书机器人集成",
			DescriptionLong: "对接飞书机器人实现远程运维：告警通知、审批执行、状态查询。需要先配置 Webhook URL 和验证令牌。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "feishu_bot.webhook_url",
			Type:            TypeURL,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         "",
			Description:     "飞书机器人 Webhook URL",
			DescriptionLong: "飞书自定义机器人的 Webhook 地址（格式：https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxxxx）。必须先启用飞书机器人模块。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "feishu_bot.verify_token",
			Type:            TypeString,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         "",
			Description:     "飞书机器人验证令牌",
			DescriptionLong: "用于验证飞书回调请求来源的签名令牌。可在飞书机器人安全设置中获取。留空则不验证签名。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "feishu_bot.notify_on_alert",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         true,
			Description:     "告警推送",
			DescriptionLong: "当系统检测到异常（注入攻击、高延迟、错误率飙升）时自动推送飞书消息。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "feishu_bot.notify_on_approval",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         true,
			Description:     "审批通知",
			DescriptionLong: "高风险会话需要审批时通过飞书通知管理员。管理员可直接在飞书消息中操作审批。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "feishu_bot.allowed_users",
			Type:            TypeString,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         "",
			Description:     "飞书白名单用户",
			DescriptionLong: "允许通过飞书执行操作的用户 OpenID 列表（逗号分隔）。留空表示允许所有已绑定用户。",
			DangerLevel:     Safe,
			HotReload:       true,
		},

		// ── 飞书机器人 — 连接 ───────────────────────────────────────────
		{
			Key:             "feishu_bot.encrypt_key",
			Type:            TypeString,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         "",
			Description:     "飞书机器人加密密钥",
			DescriptionLong: "飞书加密回调的 EncryptKey。开启签名验证后用于解密/校验加密回调。留空则跳过加密层（仍校验 token 字段）。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "feishu_bot.connection_mode",
			Type:            TypeEnum,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         "webhook",
			Options:         []string{"webhook", "app"},
			Description:     "连接模式",
			DescriptionLong: "webhook：飞书自定义机器人（推荐，零成本）。app：飞书自建企业应用（需 AppID/AppSecret，支持私聊/群 @ 单人）。",
			DangerLevel:     Safe,
			HotReload:       true,
		},

		// ── 飞书机器人 — 告警转发 ────────────────────────────────────────
		{
			Key:             "feishu_bot.alert.severity_min",
			Type:            TypeEnum,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         "high",
			Options:         []string{"low", "medium", "high", "critical"},
			Description:     "告警最低严重度",
			DescriptionLong: "仅推送达到此严重度的告警。critical=致命（强阻断）；high=高（推荐）；medium=中（需审计）；low=低（信息性）。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "feishu_bot.alert.rate_limit_per_minute",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         20,
			Min:             float64Ptr(1),
			Max:             float64Ptr(200),
			Unit:            "条/分钟",
			Description:     "告警速率限制",
			DescriptionLong: "单位时间窗口（60s）内最多推送的告警条数。超出后告警将聚合为「X 条告警被节流」一条摘要，避免告警风暴。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "feishu_bot.alert.dedup_window_seconds",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         60,
			Min:             float64Ptr(0),
			Max:             float64Ptr(600),
			Unit:            "秒",
			Description:     "告警去重窗口",
			DescriptionLong: "同一类型告警在该时间窗口内的重复触发将被合并为一条带计数器的卡片。0 表示禁用去重。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "feishu_bot.alert.quiet_hours_enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         false,
			Description:     "启用免打扰时段",
			DescriptionLong: "开启后，在免打扰时段内仅推送 critical 级别告警，避免夜间打扰。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "feishu_bot.alert.quiet_hours_start",
			Type:            TypeString,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         "22:00",
			Description:     "免打扰开始时间",
			DescriptionLong: "免打扰时段开始（24h HH:MM，本地时区）。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "feishu_bot.alert.quiet_hours_end",
			Type:            TypeString,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         "08:00",
			Description:     "免打扰结束时间",
			DescriptionLong: "免打扰时段结束（24h HH:MM，本地时区）。如开始 > 结束视为跨夜时段。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "feishu_bot.alert.card_template",
			Type:            TypeEnum,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         "standard",
			Options:         []string{"compact", "standard", "verbose"},
			Description:     "告警卡片模板",
			DescriptionLong: "compact：单行紧凑卡片；standard：标准字段+摘要；verbose：含完整证据/调用栈。",
			DangerLevel:     Safe,
			HotReload:       true,
		},

		// ── 飞书机器人 — 审批 ────────────────────────────────────────────
		{
			Key:             "feishu_bot.approval.expiry_reminder_minutes",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         5,
			Min:             float64Ptr(1),
			Max:             float64Ptr(60),
			Unit:            "分钟",
			Description:     "审批到期前提醒",
			DescriptionLong: "在审批超时前 N 分钟再次推送飞书提醒，避免管理员错过审批。0 表示不重复提醒。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "feishu_bot.approval.auto_mention_on_critical",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         true,
			Description:     "关键风险自动 @ 提及",
			DescriptionLong: "风险级别为 critical 时在飞书卡片中 @ 提及所有白名单用户，确保第一时间响应。",
			DangerLevel:     Safe,
			HotReload:       true,
		},

		// ── 飞书机器人 — 命令面板 ────────────────────────────────────────
		{
			Key:             "feishu_bot.commands.enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         true,
			Description:     "启用飞书命令",
			DescriptionLong: "开启后管理员可在飞书对话中通过 /status /help /stats /audit 等命令与系统交互。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "feishu_bot.commands.admin_only",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         true,
			Description:     "仅白名单用户可执行命令",
			DescriptionLong: "开启后只有白名单内用户可执行命令；关闭则任何触发回调的飞书用户都可执行（强烈建议保持开启）。",
			DangerLevel:     Safe,
			HotReload:       true,
		},

		// ── 飞书机器人 — 安全 ────────────────────────────────────────────
		{
			Key:             "feishu_bot.signature_required",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         true,
			Description:     "强制签名校验",
			DescriptionLong: "开启后所有飞书回调必须携带有效签名，否则拒绝。生产环境务必开启（调试时可临时关闭）。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "feishu_bot.timestamp_window_seconds",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         300,
			Min:             float64Ptr(10),
			Max:             float64Ptr(3600),
			Unit:            "秒",
			Description:     "时间戳防重放窗口",
			DescriptionLong: "飞书回调 timestamp 与当前时间相差超过此秒数则拒绝。防止重放攻击，默认 5 分钟。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
	}
}

// float64Ptr 简化 Spec.Min/Max 字段。
func float64Ptr(v float64) *float64 { return &v }

// IsSessionAnalyticsEnabled reads the session_analytics.enabled setting
// (platform fallback) to decide whether the analysis hook plugin is active.
// Mirrors resolveModuleEnabled in admin/modules.go but usable from the
// pipeline/hook layer without an HTTP request.
func IsSessionAnalyticsEnabled() bool {
	if Global == nil {
		return false
	}
	sp := Global.Spec("session_analytics.enabled")
	if sp == nil {
		return false
	}
	raw, _, err := Global.EffectiveValue(sp.Scope, sp.Key, "")
	if err != nil || raw == nil {
		return false
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return false
	}
	return v
}
