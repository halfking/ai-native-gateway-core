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

		// ── WeChat bot (企业微信机器人) ────────────────────────────────
		{
			Key:             "wechat_bot.enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         false,
			Description:     "微信机器人集成",
			DescriptionLong: "对接企业微信自定义机器人，实现远程运维通知、风险告警推送、审批操作执行等功能。依赖压缩管理、提示词注入检测、会话缓存、会话审计与审批等模块。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "wechat_bot.webhook_url",
			Type:            TypeURL,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         "",
			Description:     "企业微信群机器人 Webhook URL",
			DescriptionLong: "企业微信群机器人的 Webhook 地址（格式：https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxx）。配置后通过群机器人推送消息，无需 access_token。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "wechat_bot.corp_id",
			Type:            TypeString,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         "",
			Description:     "企业 CorpID",
			DescriptionLong: "企业微信的企业 ID（CorpID）。在企业管理后台「我的企业」页面获取。配置后可使用应用消息推送（比群机器人更灵活）。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "wechat_bot.agent_id",
			Type:            TypeString,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         "",
			Description:     "应用 AgentID",
			DescriptionLong: "企业微信自建应用的 AgentID。在应用管理页面创建应用后获取。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "wechat_bot.corp_secret",
			Type:            TypeString,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         "",
			Description:     "应用 Secret",
			DescriptionLong: "企业微信自建应用的 Secret（敏感信息，不会出现在日志中）。在应用管理页面获取，用于获取 access_token。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "wechat_bot.encoding_aes_key",
			Type:            TypeString,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         "",
			Description:     "回调加密 EncodingAESKey",
			DescriptionLong: "企业微信回调加密的 AES Key（43 字符）。在应用的「接收消息」设置中生成。配置后回调消息将使用 AES-CBC 解密。留空则按明文处理。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "wechat_bot.verify_token",
			Type:            TypeString,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         "",
			Description:     "回调验证 Token",
			DescriptionLong: "企业微信回调验证令牌。在应用的「接收消息」设置中自定义。用于验证回调请求来源的 SHA1 签名。留空则不验证签名。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "wechat_bot.notify_on_alert",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         true,
			Description:     "安全告警推送",
			DescriptionLong: "当系统检测到安全异常（提示词注入攻击、角色劫持等）时自动推送微信消息。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "wechat_bot.notify_on_approval",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         true,
			Description:     "审批通知",
			DescriptionLong: "高风险会话需要审批时通过微信通知管理员。管理员可直接在微信消息中操作审批。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "wechat_bot.notify_on_latency",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         true,
			Description:     "高延迟告警",
			DescriptionLong: "当请求延迟超过阈值时自动推送告警。延迟阈值通过 wechat_bot.latency_threshold_ms 配置。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "wechat_bot.notify_on_error_rate",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         true,
			Description:     "错误率飙升告警",
			DescriptionLong: "当错误率超过阈值时自动推送告警。错误率阈值通过 wechat_bot.error_rate_threshold 配置。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "wechat_bot.latency_threshold_ms",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         5000,
			Description:     "延迟告警阈值（毫秒）",
			DescriptionLong: "触发高延迟告警的阈值，单位毫秒。默认 5000ms（5秒）。仅当 notify_on_latency 开启时生效。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "wechat_bot.error_rate_threshold",
			Type:            TypeFloat,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         0.1,
			Description:     "错误率告警阈值",
			DescriptionLong: "触发错误率告警的阈值，取值 0.0~1.0。默认 0.1（10%）。仅当 notify_on_error_rate 开启时生效。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:             "wechat_bot.allowed_users",
			Type:            TypeString,
			Scope:           ScopePlatform,
			Category:        CategoryModules,
			Default:         "",
			Description:     "微信白名单用户",
			DescriptionLong: "允许通过微信执行操作的用户 UserID 列表（逗号分隔）。留空表示允许所有已绑定用户。用户ID为企业微信中的用户标识。",
			DangerLevel:     Safe,
			HotReload:       true,
		},
	}
}

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
