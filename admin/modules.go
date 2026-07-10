package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// ModuleDefinition describes one feature module available in the system.
type ModuleDefinition struct {
	Key          string               `json:"key"`
	Name         string               `json:"name"`
	Description  string               `json:"description"`
	Capabilities []string             `json:"capabilities"`
	Icon         string               `json:"icon"`
	Category     string               `json:"category"`
	SettingKey   string               `json:"setting_key"`
	ConfigKeys   []string             `json:"config_keys"`
	DocsURL      string               `json:"docs_url"`
	DangerLevel  settings.DangerLevel `json:"danger_level"`
	Integration  *ModuleIntegration   `json:"integration,omitempty"`
	Dependencies []ModuleDependency   `json:"dependencies,omitempty"`
}

// ModuleDependency describes a dependency relationship between modules.
type ModuleDependency struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Icon        string `json:"icon"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled,omitempty"`
}

// ModuleIntegration describes external integration configuration for a module.
type ModuleIntegration struct {
	Type        string `json:"type"`
	Label       string `json:"label"`
	Description string `json:"description"`
	DocURL      string `json:"doc_url"`
}

// ModuleWithStatus extends ModuleDefinition with runtime status.
type ModuleWithStatus struct {
	ModuleDefinition
	Enabled          bool   `json:"enabled"`
	Source           string `json:"source"`
	CanToggleEnabled bool   `json:"can_toggle_enabled"`
	BlockedReason    string `json:"blocked_reason,omitempty"`
}

var (
	moduleDefs     []ModuleDefinition
	moduleDefsOnce sync.Once
)

// allModuleDefinitions returns the canonical list of feature modules.
func allModuleDefinitions() []ModuleDefinition {
	moduleDefsOnce.Do(func() {
		moduleDefs = []ModuleDefinition{
			{
				Key:         "compression",
				Name:        "会话压缩",
				Description: "智能压缩超长对话上下文，支持多模式自动压缩（阈值/4xx 响应/v4 智能/激进），显著降低 token 消耗。",
				Capabilities: []string{
					"多模式压缩（off / auto_threshold / on_4xx / smart / aggressive）",
					"v4 智能压缩含工具裁剪、滑动窗口",
					"v4 激进压缩含任务分析、主动压缩",
					"Delta-append 增量保留",
				},
				Icon:        "🗜️",
				Category:    "compression",
				SettingKey:  "compression.enabled",
				ConfigKeys:  []string{"compression.mode", "compression.window_fraction"},
				DocsURL:     "/admin/compression",
				DangerLevel: settings.Warning,
			},
			{
				Key:         "cache",
				Name:        "会话缓存",
				Description: "3 层会话缓存（L1 内存 / L2 Redis / L3 数据库），缓存会话状态以实现智能压缩和上下文快速恢复。",
				Capabilities: []string{
					"L1 内存缓存（最快）",
					"L2 Redis 缓存（中等）",
					"L3 数据库缓存（持久化）",
					"Provider 级可覆盖",
				},
				Icon:        "💾",
				Category:    "session",
				SettingKey:  "cache.enabled",
				DocsURL:     "/admin/compression",
				DangerLevel: settings.Warning,
			},
			{
				Key:         "handoff",
				Name:        "会话交接",
				Description: "当会话上下文接近窗口上限时，自动执行 Handoff：生成结构化摘要、写入交接记录、提示新会话继续，防止上下文超限和单轮成本膨胀。支持 LLM / 规则 / 混合摘要引擎，与压缩管理、任务模式、会话健康检查深度联动。",
				Capabilities: []string{
					"自动检测上下文使用率触发交接（绝对 token / 百分比 / 消息数 / 静默时长 四种阈值）",
					"自定义 Skill 名称（默认 /handoff，可对接 /session-resume 等）",
					"上下文摘要生成（LLM / 规则 / 混合三种引擎，可选 cheap model 降本）",
					"摘要保留最近 N 条 + 关键事实抽取（决策/约定/路径）",
					"单会话最大交接次数 + 冷却时间，防止死循环放大成本",
					"通知级别（none/info/warn）与 Webhook 推送（飞书/Slack/钉钉自定义机器人）",
					"与压缩管理联动：摘要引擎可复用 compression.llm_model",
					"与会话缓存联动：handoff_count + last_handoff_at 写入 session_summaries",
					"与任务模式联动：触发即抢占后续续跑注入（last-writer-wins）",
					"与会话健康检查联动：handoff_logs 可在 session-context 面板查询",
				},
				Icon:       "🔄",
				Category:   "session",
				SettingKey: "handoff.enabled",
				ConfigKeys: []string{
					"handoff.trigger_mode",
					"handoff.absolute_threshold",
					"handoff.percentage_threshold",
					"handoff.message_threshold",
					"handoff.idle_minutes",
					"handoff.min_messages",
					"handoff.skill_name",
					"handoff.summary_engine",
					"handoff.summary_model",
					"handoff.summary_keep_recent_n",
					"handoff.summary_max_tokens",
					"handoff.summary_prompt_tpl",
					"handoff.summary_extract_facts",
					"handoff.cooldown_seconds",
					"handoff.max_per_session",
					"handoff.retry_on_failure",
					"handoff.notify_level",
					"handoff.notify_webhook",
					"handoff.continue_hint_tpl",
				},
				DocsURL:     "/admin/session-context",
				DangerLevel: settings.Warning,
				Dependencies: []ModuleDependency{
					{Key: "compression", Name: "会话压缩", Icon: "🗜️", Required: true, Description: "摘要引擎可复用 compression.llm_model 便宜模型降本；压缩减少的 token 直接降低百分比阈值的触发频率"},
					{Key: "cache", Name: "会话缓存", Icon: "💾", Required: true, Description: "session_summaries 是交接记录的承载表；缓存命中降低冷启动 token，让交接判断更准确"},
					{Key: "goal", Name: "任务模式", Icon: "🎯", Required: false, Description: "当交接与任务续跑同时触发时，交接注入 last-writer-wins 抢占续跑提示，避免上下文叠加"},
					{Key: "session_inspector", Name: "会话健康检查", Icon: "🔍", Required: false, Description: "健康检查中暴露的 tokens_at_trigger / last_handoff_at 字段直接来自本模块写入的 session_summaries"},
				},
			},
			{
				Key:         "goal",
				Name:        "任务模式",
				Description: "持续执行目标的自动化管理。系统自动检测需要持续执行的任务，支持审计、意图分析、自动修复。",
				Capabilities: []string{
					"多模式检测（keyword / explicit / llm / hybrid）",
					"自动选择推荐选项",
					"暂停时自动继续",
					"自动修复（高风险，建议人工审核）",
					"自动路由审计与意图检测",
				},
				Icon:        "🎯",
				Category:    "session",
				SettingKey:  "goal.enabled",
				ConfigKeys:  []string{"goal.detection_mode", "goal.max_retry_count", "goal.auto_fix_enabled", "goal.fallback_audit_model"},
				DangerLevel: settings.Warning,
			},
			{
				Key:         "audit",
				Name:        "审计日志",
				Description: "记录所有 API 请求的审计追踪，包括谁、何时、做了什么操作，满足合规审计要求。",
				Capabilities: []string{
					"请求级审计追踪",
					"操作者身份记录",
					"长期存储与查询",
					"设置变更审计（7 天保留）",
				},
				Icon:        "📝",
				Category:    "general",
				SettingKey:  "audit.enabled",
				DangerLevel: settings.Safe,
			},
			{
				Key:         "prompt_injection",
				Name:        "提示词注入检测",
				Description: "LLM-as-judge 检测提示词注入攻击、角色劫持、指令泄漏等安全威胁，当前为检测模式（不拦截）。",
				Capabilities: []string{
					"LLM-as-judge 检测引擎",
					"10+ 常见注入模式（角色劫持、指令泄漏等）",
					"可观测模式（仅检测不拦截）",
					"支持 Webhook 告警",
				},
				Icon:        "🛡️",
				Category:    "security",
				SettingKey:  "prompt_injection.enabled",
				DangerLevel: settings.Warning,
			},
			{
				Key:         "output_compliance",
				Name:        "输出合规检测",
				Description: "检查 LLM 输出中的敏感数据（PII、密钥、内部 IP、Token 等），触发告警或自动脱敏。",
				Capabilities: []string{
					"敏感数据识别（PII / 密钥 / 内部 IP）",
					"自动脱敏处理",
					"实时告警通知",
					"自定义敏感词库",
				},
				Icon:        "🔒",
				Category:    "security",
				SettingKey:  "output_compliance.enabled",
				DangerLevel: settings.Warning,
			},
			{
				Key:         "session_audit",
				Name:        "会话审计与审批",
				Description: "高风险会话自动触发审计与审批流程。管理员可直接在系统中批准或拒绝高风险操作。",
				Capabilities: []string{
					"高风险自动识别与标记",
					"多级审批流程",
					"审批超时自动处理",
					"跨租户隔离审批",
					"多模型联合检测",
					"与飞书/钉钉/企微联动",
				},
				Icon:       "📋",
				Category:   "security",
				SettingKey: "session_audit.enabled",
				ConfigKeys: []string{
					"session_audit.enforcement_level",
					"session_audit.detector_models",
					"session_audit.approval_threshold",
					"session_audit.auto_block_threshold",
					"session_audit.approval_timeout",
					"session_audit.timeout_action",
					"session_audit.notify_channels",
					"session_audit.min_approvals",
				},
				DangerLevel: settings.Warning,
				Dependencies: []ModuleDependency{
					{Key: "prompt_injection", Name: "提示词注入检测", Icon: "🛡️", Required: true},
					{Key: "compression", Name: "会话压缩", Icon: "🗜️", Required: false},
					{Key: "cache", Name: "会话缓存", Icon: "💾", Required: false},
					{Key: "security", Name: "安全检测引擎", Icon: "🔐", Required: false},
					{Key: "feishu_bot", Name: "飞书机器人", Icon: "📱", Required: false},
				},
			},
			{
				Key:         "session_inspector",
				Name:        "会话健康检查",
				Description: "监控会话各项健康指标（Token 限制、不活跃超时、高频请求、错误率、模型切换），通过 hook 插件 + 后台 worker 协同工作，支持软关闭回收、IM/Webhook 告警、Prometheus 指标。",
				Capabilities: []string{
					"Token 使用量监控与软/硬告警（max_total / soft_warning_pct）",
					"不活跃会话自动检测与回收（idle.timeout / recycle_action）",
					"高频请求检测（RPM / burst / max_concurrent）",
					"会话生命周期管理（auto_extend / max_sessions_per_tenant / eviction）",
					"错误率与模型切换联动告警",
					"后台 worker 周期性回收 + EventBus 通知",
					"复用 feishu_bot / wechat_bot 推送告警",
					"Webhook 回调 + Prometheus 指标",
				},
				Icon:       "🔍",
				Category:   "session",
				SettingKey: "session_inspector.enabled",
				ConfigKeys: []string{
					"session_inspector.token.max_total",
					"session_inspector.token.soft_warning_pct",
					"session_inspector.token.warn_action",
					"session_inspector.token.include_output",
					"session_inspector.token.reset_cycle",
					"session_inspector.idle.timeout",
					"session_inspector.idle.absolute_max_lifetime",
					"session_inspector.idle.cleanup_interval",
					"session_inspector.idle.cleanup_batch_size",
					"session_inspector.idle.recycle_action",
					"session_inspector.rate.rpm_limit",
					"session_inspector.rate.burst_limit",
					"session_inspector.rate.burst_window_seconds",
					"session_inspector.rate.max_concurrent",
					"session_inspector.rate.strategy",
					"session_inspector.rate.observe_only",
					"session_inspector.lifecycle.auto_extend_on_activity",
					"session_inspector.lifecycle.max_sessions_per_tenant",
					"session_inspector.lifecycle.eviction_policy",
					"session_inspector.alert.enabled",
					"session_inspector.alert.notify_channels",
					"session_inspector.alert.webhook_urls",
					"session_inspector.alert.prometheus_enabled",
				},
				DocsURL:     "/docs/modules/session-inspector.md",
				DangerLevel: settings.Safe,
				Dependencies: []ModuleDependency{
					{Key: "compression", Name: "会话压缩", Icon: "🗜️", Required: true, Description: "压缩为 lifecycle / idle 检测提供 token 用量基线"},
					{Key: "cache", Name: "会话缓存", Icon: "💾", Required: true, Description: "提供 last_active_at 实时数据来源"},
					{Key: "prompt_injection", Name: "提示词注入检测", Icon: "🛡️", Required: true, Description: "威胁会话将影响健康评分"},
					{Key: "output_compliance", Name: "输出安全检测", Icon: "🔒", Required: true, Description: "PII/毒性输出影响错误率与健康分"},
					{Key: "session_audit", Name: "会话审计与审批", Icon: "📋", Required: true, Description: "高风险会话审批通过后健康检查方可放行"},
				},
			},
			{
				Key:         "security",
				Name:        "安全检测引擎",
				Description: "综合意图分析与威胁检测引擎，识别越狱尝试、数据泄露、恶意指令等安全威胁。",
				Capabilities: []string{
					"意图分析（intent classification）",
					"威胁检测（threat detection）",
					"预设响应策略",
					"与审计模块联动",
				},
				Icon:        "🔐",
				Category:    "security",
				SettingKey:  "security.enabled",
				DangerLevel: settings.Dangerous,
			},
			{
				Key:         "rate_limit",
				Name:        "限流控制",
				Description: "多维度流量控制：RPM（每分钟请求数）、并发数、TPM（每分钟 Token 数），保护后端服务稳定性。",
				Capabilities: []string{
					"RPM 限流（每分钟请求数）",
					"并发限流（同时处理数）",
					"TPM 限流（每分钟 Token 数）",
					"平台级默认值 + 租户级覆盖",
					"滑动窗口算法（秒级精度）",
				},
				Icon:        "🚦",
				Category:    "rate_limit",
				SettingKey:  "rate_limit.enabled",
				ConfigKeys:  []string{"default.rate_limit_rpm", "default.rate_limit_concurrent", "default.rate_limit_tpm"},
				DangerLevel: settings.Breaking,
			},
			{
				Key:         "format_conversion",
				Name:        "格式转换",
				Description: "自动转换不同协议之间的消息格式：OpenAI ↔ Anthropic，使客户端可以无感知切换模型提供商。",
				Capabilities: []string{
					"Anthropic 格式 → OpenAI 模型（Q2）",
					"OpenAI 格式 → Anthropic 模型（Q3）",
					"Provider 级可覆盖",
				},
				Icon:        "🔀",
				Category:    "general",
				SettingKey:  "format_conversion.enabled",
				DangerLevel: settings.Safe,
			},
			{
				Key:         "disguise",
				Name:        "UA/TLS 伪装",
				Description: "启用 User-Agent 和 TLS 指纹轮换，避免被上游提供商检测到非标准客户端。支持客户端指纹建档、凭据级 Slot 绑定和并发控制。",
				Capabilities: []string{
					"User-Agent 轮换",
					"TLS 指纹轮换",
					"客户端指纹建档",
					"凭据级 Slot 绑定",
					"合规参考文档",
					"Slot 并发控制",
				},
				Icon:       "🎭",
				Category:   "security",
				SettingKey: "enable_disguise",
				ConfigKeys: []string{
					"disguise.rotation_interval",
					"disguise.ua_pool_size",
					"disguise.lang_pool_size",
					"disguise.platform_filter",
					"disguise.enable_tls_fingerprint",
					"disguise.fp_slot_concurrency",
					"disguise.active_gate_seconds",
				},
				DocsURL:     "/docs/legal/disguise-compliance.md",
				DangerLevel: settings.Breaking,
				Dependencies: []ModuleDependency{
					{Key: "compression", Name: "会话压缩", Icon: "📦", Required: true, Description: "压缩会话携带一致的指纹元数据"},
					{Key: "cache", Name: "会话缓存", Icon: "💾", Required: true, Description: "缓存保留跨请求的 Slot 绑定"},
					{Key: "prompt_injection", Name: "提示词注入检测", Icon: "🛡️", Required: true, Description: "安全管线受益于稳定客户端身份"},
				},
			},
			{
				Key:         "feishu_bot",
				Name:        "飞书机器人",
				Description: "对接飞书自定义机器人，实现远程运维通知、风险告警推送、审批操作执行等功能。",
				Capabilities: []string{
					"实时告警推送（注入攻击、高延迟、错误率飙升）",
					"高风险操作审批通知与飞书内操作",
					"系统状态查询",
					"飞书签名验证",
					"用户白名单控制",
				},
				Icon:       "📱",
				Category:   "integration",
				SettingKey: "feishu_bot.enabled",
				ConfigKeys: []string{
					"feishu_bot.webhook_url",
					"feishu_bot.verify_token",
					"feishu_bot.encrypt_key",
					"feishu_bot.connection_mode",
					"feishu_bot.notify_on_alert",
					"feishu_bot.notify_on_approval",
					"feishu_bot.allowed_users",
					"feishu_bot.alert.severity_min",
					"feishu_bot.alert.rate_limit_per_minute",
					"feishu_bot.alert.dedup_window_seconds",
					"feishu_bot.alert.quiet_hours_enabled",
					"feishu_bot.alert.quiet_hours_start",
					"feishu_bot.alert.quiet_hours_end",
					"feishu_bot.alert.card_template",
					"feishu_bot.approval.expiry_reminder_minutes",
					"feishu_bot.approval.auto_mention_on_critical",
					"feishu_bot.commands.enabled",
					"feishu_bot.commands.admin_only",
					"feishu_bot.signature_required",
					"feishu_bot.timestamp_window_seconds",
				},
				DangerLevel: settings.Safe,
				Integration: &ModuleIntegration{
					Type:        "feishu",
					Label:       "飞书",
					Description: "对接飞书自定义机器人，使用 Webhook 进行消息推送和交互",
					DocURL:      "https://open.feishu.cn/document/client-docs/bot-v3/add-custom-bot",
				},
				Dependencies: []ModuleDependency{
					{Key: "compression", Name: "会话压缩", Icon: "🗜️", Required: true, Description: "压缩管理提供上下文审计元数据"},
					{Key: "cache", Name: "会话缓存", Icon: "💾", Required: true, Description: "缓存提供审批决策复用"},
					{Key: "prompt_injection", Name: "提示词注入检测", Icon: "🛡️", Required: true, Description: "注入检测触发风险告警"},
					{Key: "session_audit", Name: "会话审计与审批", Icon: "📋", Required: true, Description: "审批流程提供回调目标"},
				},
			},
			{
				Key:         "wechat_bot",
				Name:        "微信机器人",
				Description: "对接企业微信自定义机器人，实现远程运维通知、风险告警推送、审批操作执行等功能。依赖压缩管理、提示词注入检测、会话缓存、会话审计与审批等模块。",
				Capabilities: []string{
					"实时告警推送（注入攻击、高延迟、错误率飙升）",
					"高风险操作审批通知与微信内操作",
					"系统状态查询",
					"企业微信签名验证（SHA1 + AES-CBC 解密）",
					"用户白名单控制",
				},
				Icon:       "💬",
				Category:   "integration",
				SettingKey: "wechat_bot.enabled",
				ConfigKeys: []string{
					"wechat_bot.webhook_url",
					"wechat_bot.corp_id",
					"wechat_bot.agent_id",
					"wechat_bot.corp_secret",
					"wechat_bot.encoding_aes_key",
					"wechat_bot.verify_token",
					"wechat_bot.notify_on_alert",
					"wechat_bot.notify_on_approval",
					"wechat_bot.notify_on_latency",
					"wechat_bot.notify_on_error_rate",
					"wechat_bot.latency_threshold_ms",
					"wechat_bot.error_rate_threshold",
					"wechat_bot.allowed_users",
				},
				DangerLevel: settings.Safe,
				Integration: &ModuleIntegration{
					Type:        "wechat",
					Label:       "企业微信",
					Description: "对接企业微信自定义机器人，支持群机器人 Webhook 和应用消息推送，实现告警通知与审批交互",
					DocURL:      "https://developer.work.weixin.qq.com/document/path/91770",
				},
				Dependencies: []ModuleDependency{
					{Key: "compression", Name: "会话压缩", Icon: "🗜️", Required: true, Description: "上下文压缩，支持摘要推送"},
					{Key: "prompt_injection", Name: "提示词注入检测", Icon: "🛡️", Required: true, Description: "注入攻击告警来源"},
					{Key: "cache", Name: "会话缓存", Icon: "💾", Required: true, Description: "审批流程查询上下文"},
					{Key: "session_audit", Name: "会话审计", Icon: "📋", Required: true, Description: "高风险会话审批通知来源"},
				},
			},
			{
				Key:         "dingtalk_bot",
				Name:        "钉钉机器人",
				Description: "对接钉钉自定义机器人，实现远程运维通知、风险告警推送、审批操作执行等功能。依赖压缩管理、提示词注入检测、会话缓存、会话审计与审批等模块。",
				Capabilities: []string{
					"实时告警推送（注入攻击、高延迟、错误率飙升）",
					"高风险操作审批通知与钉钉内操作（加签回调验签）",
					"系统状态查询（/status、/health 指令）",
					"钉钉加签验证（HMAC-SHA256 + Base64）",
					"用户白名单控制（手机号/UserID）",
					"群机器人 Webhook 与工作通知（应用消息）双模式",
				},
				Icon:       "🤖",
				Category:   "integration",
				SettingKey: "dingtalk_bot.enabled",
				ConfigKeys: []string{
					"dingtalk_bot.webhook_url",
					"dingtalk_bot.sign_secret",
					"dingtalk_bot.app_key",
					"dingtalk_bot.app_secret",
					"dingtalk_bot.agent_id",
					"dingtalk_bot.base_url",
					"dingtalk_bot.notify_on_alert",
					"dingtalk_bot.notify_on_latency",
					"dingtalk_bot.notify_on_error_rate",
					"dingtalk_bot.latency_threshold_ms",
					"dingtalk_bot.error_rate_threshold",
					"dingtalk_bot.notify_on_approval",
					"dingtalk_bot.callback_url",
					"dingtalk_bot.verify_signature",
					"dingtalk_bot.enable_status_query",
					"dingtalk_bot.allowed_users",
					"dingtalk_bot.card_type",
					"dingtalk_bot.at_all",
					"dingtalk_bot.rate_limit_per_min",
				},
				DangerLevel: settings.Safe,
				Dependencies: []ModuleDependency{
					{Key: "compression", Name: "会话压缩", Icon: "🗜️", Required: true, Description: "上下文压缩，支持摘要推送"},
					{Key: "prompt_injection", Name: "提示词注入检测", Icon: "🛡️", Required: true, Description: "注入攻击告警来源"},
					{Key: "cache", Name: "会话缓存", Icon: "💾", Required: true, Description: "审批流程查询上下文"},
					{Key: "session_audit", Name: "会话审计", Icon: "📋", Required: true, Description: "高风险会话审批通知来源"},
				},
				Integration: &ModuleIntegration{
					Type:        "dingtalk",
					Label:       "钉钉",
					Description: "对接钉钉自定义机器人，支持群机器人 Webhook（加签）与工作通知（应用消息），实现告警通知与审批交互",
					DocURL:      "https://developers.dingtalk.com/document/orgapp/custom-bot-to-send-group-chat-messages",
				},
			},
			{
				Key:         "session_analytics",
				Name:        "会话全景分析",
				Description: "通过 hook 插件准实时分析会话：自动总结/标题生成、多维标签、逐步摘要、相似会话聚类、优化建议，输出会话全景图（做了什么、做得如何、省了多少、花了多少）。",
				Capabilities: []string{
					"AI 自动总结与标题生成（增量滚动）",
					"多维标签（任务/客户端/LLM/主题/意图/质量）",
					"逐步请求/回复摘要（规则默认+LLM可选）",
					"相似会话聚类（标签粗分+向量细分）",
					"优化建议与潜在成本节省量化",
					"分阶段 LLM 模型配置（每阶段可指定或 auto）",
					"会话全景图（成本/token/节省/合规/流向）",
				},
				Icon:       "📊",
				Category:   "session",
				SettingKey: "session_analytics.enabled",
				ConfigKeys: []string{
					"session_analytics.title_on_first_request",
					"session_analytics.request_summary_mode",
					"session_analytics.summary_strategy",
					"session_analytics.cluster_mode",
					"session_analytics.cluster_schedule",
					"session_analytics.optimization_enabled",
					"session_analytics.model.title",
					"session_analytics.model.summary",
					"session_analytics.model.tags",
					"session_analytics.model.request_summary",
					"session_analytics.model.embedding",
					"session_analytics.model.cluster_label",
				},
				DocsURL:     "/admin/session-analytics",
				DangerLevel: settings.Safe,
				Dependencies: []ModuleDependency{
					{Key: "compression", Name: "会话压缩", Icon: "🗜️", Required: true, Description: "提供增量摘要、上下文裁剪和压缩节省量分析"},
					{Key: "cache", Name: "会话缓存", Icon: "💾", Required: true, Description: "提供会话复用、缓存命中和节省量分析"},
					{Key: "prompt_injection", Name: "提示词注入检测", Icon: "🛡️", Required: true, Description: "提供风险识别、意图辅助和安全标签"},
					{Key: "output_compliance", Name: "输出合规检测", Icon: "🔒", Required: true, Description: "提供合规状态、脱敏结果和风险流向"},
				},
			},
			{
				Key:         "memora",
				Name:        "Memora 记忆服务",
				Description: "长期记忆服务，跨越会话边界保留用户事实和偏好，实现上下文持久化与智能恢复。",
				Capabilities: []string{
					"跨会话记忆持久化",
					"智能事实检索（L1 注入）",
					"记忆写入/暂停/恢复控制",
					"健康度监控与 Ping 检测",
				},
				Icon:        "🧠",
				Category:    "general",
				SettingKey:  "", // no single toggle; status is runtime
				DangerLevel: settings.Safe,
			},
		}

		// Detect circular dependencies at initialization time
		if err := detectCircularDependencies(moduleDefs); err != nil {
			panic("Module dependency graph contains cycles: " + err.Error())
		}
	})
	return moduleDefs
}

// resolveModuleEnabled reads the setting associated with a module and returns
// the effective enabled value.  Modules without a setting key always return true.
func resolveModuleEnabled(m ModuleDefinition) (enabled bool, source string) {
	if m.SettingKey == "" {
		return true, "default"
	}
	sp := settings.Global.Spec(m.SettingKey)
	if sp == nil {
		return true, "default"
	}
	raw, src, err := settings.Global.EffectiveValue(sp.Scope, m.SettingKey, "")
	if err != nil {
		return true, "default"
	}
	if raw == nil {
		return true, "default"
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return true, "default"
	}
	return v, src
}

// detectCircularDependencies checks if the module dependency graph contains cycles.
// Returns an error describing the cycle if found, nil otherwise.
func detectCircularDependencies(modules []ModuleDefinition) error {
	// Build adjacency list
	graph := make(map[string][]string)
	moduleSet := make(map[string]bool)

	for _, m := range modules {
		moduleSet[m.Key] = true
		graph[m.Key] = make([]string, 0, len(m.Dependencies))
		for _, dep := range m.Dependencies {
			graph[m.Key] = append(graph[m.Key], dep.Key)
		}
	}

	// DFS-based cycle detection with path tracking
	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)

	state := make(map[string]int)
	path := make([]string, 0)

	var dfs func(string) error
	dfs = func(node string) error {
		if state[node] == visiting {
			// Found cycle: construct cycle path
			cycleStart := -1
			for i, p := range path {
				if p == node {
					cycleStart = i
					break
				}
			}
			if cycleStart >= 0 {
				cyclePath := append(path[cycleStart:], node)
				return fmt.Errorf("circular dependency detected: %s", strings.Join(cyclePath, " -> "))
			}
			return fmt.Errorf("circular dependency detected involving: %s", node)
		}

		if state[node] == visited {
			return nil
		}

		state[node] = visiting
		path = append(path, node)

		for _, neighbor := range graph[node] {
			// Skip dependencies that don't exist in the module set
			// (they might be external or optional)
			if !moduleSet[neighbor] {
				continue
			}

			if err := dfs(neighbor); err != nil {
				return err
			}
		}

		path = path[:len(path)-1]
		state[node] = visited
		return nil
	}

	// Check all nodes (handles disconnected components)
	for _, m := range modules {
		if state[m.Key] == unvisited {
			if err := dfs(m.Key); err != nil {
				return err
			}
		}
	}

	return nil
}

func moduleStatusMap(defs []ModuleDefinition) map[string]ModuleWithStatus {
	statuses := make(map[string]ModuleWithStatus, len(defs))
	for _, m := range defs {
		enabled, src := resolveModuleEnabled(m)
		statuses[m.Key] = ModuleWithStatus{
			ModuleDefinition: m,
			Enabled:          enabled,
			Source:           src,
			CanToggleEnabled: true,
		}
	}
	for key, status := range statuses {
		blocked := requiredDependencyBlockReason(statuses, status.ModuleDefinition)
		status.BlockedReason = blocked
		status.CanToggleEnabled = blocked == ""
		if len(status.Dependencies) > 0 {
			deps := make([]ModuleDependency, 0, len(status.Dependencies))
			for _, dep := range status.Dependencies {
				dep.Enabled = statuses[dep.Key].Enabled
				deps = append(deps, dep)
			}
			status.Dependencies = deps
		}
		statuses[key] = status
	}
	return statuses
}

func requiredDependencyBlockReason(statuses map[string]ModuleWithStatus, mod ModuleDefinition) string {
	missing := make([]string, 0, len(mod.Dependencies))
	for _, dep := range mod.Dependencies {
		if !dep.Required {
			continue
		}
		if depStatus, ok := statuses[dep.Key]; !ok || !depStatus.Enabled {
			missing = append(missing, dep.Name)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return "需先启用依赖模块: " + strings.Join(missing, "、")
}

// handleModulesList returns all modules with their current enabled/disabled status.
//
// GET /api/admin/modules
func (h *Handler) handleModulesList(w http.ResponseWriter, r *http.Request) {
	if settings.Global == nil {
		writeError(w, http.StatusServiceUnavailable, "settings registry not initialised")
		return
	}
	defs := allModuleDefinitions()
	statusMap := moduleStatusMap(defs)
	out := make([]ModuleWithStatus, 0, len(defs))
	for _, m := range defs {
		out = append(out, statusMap[m.Key])
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// handleModulesGet returns a single module with full detail and config values.
//
// GET /api/admin/modules/{key}
func (h *Handler) handleModulesGet(w http.ResponseWriter, r *http.Request) {
	if settings.Global == nil {
		writeError(w, http.StatusServiceUnavailable, "settings registry not initialised")
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/api/admin/modules/")
	key = strings.Split(key, "/")[0]

	defs := allModuleDefinitions()
	var found *ModuleDefinition
	for _, m := range defs {
		if m.Key == key {
			found = &m
			break
		}
	}
	if found == nil {
		writeError(w, http.StatusNotFound, "unknown module: "+key)
		return
	}

	statusMap := moduleStatusMap(defs)

	// Collect config values for each config key
	config := make(map[string]any)
	for _, ck := range found.ConfigKeys {
		sp := settings.Global.Spec(ck)
		if sp == nil {
			continue
		}
		raw, src2, err := settings.Global.EffectiveValue(sp.Scope, ck, "")
		if err != nil {
			continue
		}
		var v any
		if raw != nil {
			_ = json.Unmarshal(raw, &v)
		}
		config[ck] = map[string]any{
			"value":  v,
			"source": src2,
			"spec":   sp,
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"module": statusMap[found.Key],
		"config": config,
	})
}

// handleModulesToggle toggles a module's enabled/disabled state.
//
// PUT /api/admin/modules/{key}/toggle
// Body: { "enabled": true }
func (h *Handler) handleModulesToggle(w http.ResponseWriter, r *http.Request) {
	if settings.Global == nil || h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "settings not available")
		return
	}

	// Parse key from path: /api/admin/modules/{key}/toggle
	rest := strings.TrimPrefix(r.URL.Path, "/api/admin/modules/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] != "toggle" {
		writeError(w, http.StatusNotFound, "unknown modules endpoint")
		return
	}
	key := parts[0]

	// Find the module
	defs := allModuleDefinitions()
	var found *ModuleDefinition
	for _, m := range defs {
		if m.Key == key {
			found = &m
			break
		}
	}
	if found == nil {
		writeError(w, http.StatusNotFound, "unknown module: "+key)
		return
	}

	sp := settings.Global.Spec(found.SettingKey)
	if sp == nil {
		writeError(w, http.StatusNotFound, "module has no setting key")
		return
	}

	// Permission gate
	if sp.DangerLevel >= settings.Dangerous && !IsSuperAdminOrLegacy(r) {
		writeError(w, http.StatusForbidden, "super_admin required for this module")
		return
	}

	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	store, ok := h.dbSettingsStore()
	if !ok {
		writeError(w, http.StatusInternalServerError, "settings store not wired")
		return
	}

	if body.Enabled {
		statusMap := moduleStatusMap(defs)
		if blockedReason := statusMap[found.Key].BlockedReason; blockedReason != "" {
			if !r.URL.Query().Has("cascade") {
				writeError(w, http.StatusConflict, blockedReason)
				return
			}
			cascaded, err := applyCascadeEnable(
				defs, statusMap, found,
				func(scope settings.Scope, key string, value bool) error {
					_, e := store.Set(scope, key, value)
					return e
				},
				func(keys []string) {
					for _, k := range keys {
						for _, m := range defs {
							if m.Key == k {
								if m.SettingKey == "" {
									break
								}
								sp := settings.Global.Spec(m.SettingKey)
								if sp == nil {
									break
								}
								_, _ = store.Set(sp.Scope, m.SettingKey, false)
								break
							}
						}
					}
				},
			)
			if err != nil {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"status":   "ok",
				"enabled":  true,
				"module":   found.Key,
				"message":  "模块已启用（已自动开启 " + strconv.Itoa(len(cascaded)) + " 个依赖）: " + found.Name,
				"cascaded": cascaded,
			})
			return
		}
	}

	if _, err := store.Set(sp.Scope, found.SettingKey, body.Enabled); err != nil {
		writeError(w, http.StatusInternalServerError, "save failed: "+err.Error())
		return
	}

	statusStr := "禁用"
	if body.Enabled {
		statusStr = "启用"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"enabled": body.Enabled,
		"module":  found.Key,
		"message": "模块已" + statusStr + ": " + found.Name,
	})
}

// applyCascadeEnable 是"级联依赖 + 主模块"一次性启用的纯流程：
//
//  1. 复用 cascadeEnableDepsWithWriter 开启必需依赖，依赖失败 → 整体回滚
//  2. 依赖全部成功后写主模块；主模块写盘失败 → 关闭已经开启的依赖
//
// 关键不变量：调用返回时，对外表现要么"全部启用"要么"全部回到原状"。
// 写盘 + 回滚均通过 writer / rollback 注入，便于纯函数测试。
func applyCascadeEnable(
	defs []ModuleDefinition,
	statuses map[string]ModuleWithStatus,
	root *ModuleDefinition,
	writer func(scope settings.Scope, key string, value bool) error,
	rollback func(keys []string),
) ([]string, error) {
	if root == nil {
		return nil, fmt.Errorf("nil root module")
	}
	cascaded, err := cascadeEnableDepsWithWriter(defs, statuses, root.Key, writer, rollback)
	if err != nil {
		return nil, err
	}
	sp := settings.Global.Spec(root.SettingKey)
	if sp == nil {
		rollback(cascaded)
		return nil, fmt.Errorf("root module setting spec not registered: %s", root.SettingKey)
	}
	if err := writer(sp.Scope, root.SettingKey, true); err != nil {
		rollback(cascaded)
		return nil, fmt.Errorf("主模块启用失败: %v", err)
	}
	return cascaded, nil
}

// cascadeEnableDependencies 按模块列表稳定顺序开启所有当前未启用的必需依赖。
// 返回成功开启的依赖 key（module key）列表。任一失败时已开启项回滚，返回 error。
//
// 注意：依赖级联只在一次调用内收敛一层（直接必需依赖），不递归。
// 这样行为可预测、失败面可控；二级依赖未启用时，主模块 toggle 会再次被 blockedReason 拦截。
func (h *Handler) cascadeEnableDependencies(defs []ModuleDefinition, statuses map[string]ModuleWithStatus, rootKey string) ([]string, error) {
	store, ok := h.dbSettingsStore()
	if !ok {
		return nil, fmt.Errorf("settings store not wired")
	}
	return cascadeEnableDepsWithWriter(defs, statuses, rootKey, func(scope settings.Scope, key string, value bool) error {
		_, err := store.Set(scope, key, value)
		return err
	}, func(keys []string) {
		// 通过 module key 查 defs，转 setting key 再写 false
		for _, k := range keys {
			for _, m := range defs {
				if m.Key == k {
					if m.SettingKey == "" {
						continue
					}
					sp := settings.Global.Spec(m.SettingKey)
					if sp == nil {
						continue
					}
					_, _ = store.Set(sp.Scope, m.SettingKey, false)
					break
				}
			}
		}
	})
}

// cascadeEnableDepsWithWriter 是级联启用的核心纯逻辑：仅依赖可注入的 writer，便于单测。
//
// cascaded 的元素是 module key（不是 setting key），rollback 接收 module key 列表并写回 false。
func cascadeEnableDepsWithWriter(
	defs []ModuleDefinition,
	statuses map[string]ModuleWithStatus,
	rootKey string,
	writer func(scope settings.Scope, key string, value bool) error,
	rollback func(keys []string),
) ([]string, error) {
	statuses = cloneModuleStatusMap(statuses)
	cascaded := make([]string, 0, 4)

	requiredDeps := make(map[string]struct{})
	for _, m := range defs {
		if m.Key != rootKey {
			continue
		}
		for _, d := range m.Dependencies {
			if d.Required {
				requiredDeps[d.Key] = struct{}{}
			}
		}
	}

	for _, m := range defs {
		if m.Key == rootKey {
			continue
		}
		if _, needed := requiredDeps[m.Key]; !needed {
			continue
		}
		status := statuses[m.Key]
		if status.Enabled || status.SettingKey == "" {
			continue
		}
		sp := settings.Global.Spec(m.SettingKey)
		if sp == nil {
			continue
		}
		// 危险级及以上不允许后端自动开启：必须由 super_admin 手动操作，避免越权。
		// 权限隔离比功能完整性更重要，宁可回滚也不自动启用。
		if sp.DangerLevel >= settings.Dangerous {
			rollback(cascaded)
			return nil, fmt.Errorf("依赖模块 %s 危险级别过高，无法自动启用，请手动开启", m.Name)
		}
		if err := writer(sp.Scope, m.SettingKey, true); err != nil {
			rollback(cascaded)
			return nil, fmt.Errorf("自动启用 %s 失败: %v", m.Name, err)
		}
		cascaded = append(cascaded, m.Key)
	}

	return cascaded, nil
}

// rollbackCascadedDependencies 把级联开启的依赖逐个关闭（恢复原状）。
func (h *Handler) rollbackCascadedDependencies(keys []string) {
	if len(keys) == 0 {
		return
	}
	store, ok := h.dbSettingsStore()
	if !ok {
		return
	}
	defs := allModuleDefinitions()
	for _, k := range keys {
		var def *ModuleDefinition
		for i := range defs {
			if defs[i].Key == k {
				def = &defs[i]
				break
			}
		}
		if def == nil || def.SettingKey == "" {
			continue
		}
		sp := settings.Global.Spec(def.SettingKey)
		if sp == nil {
			continue
		}
		_, _ = store.Set(sp.Scope, def.SettingKey, false)
	}
}

func cloneModuleStatusMap(in map[string]ModuleWithStatus) map[string]ModuleWithStatus {
	out := make(map[string]ModuleWithStatus, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// registerModuleRoutes installs the module management endpoints.
func (h *Handler) registerModuleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/admin/modules", h.admin(h.handleModulesList))
	mux.HandleFunc("/api/admin/modules/", h.admin(h.handleModulesRouter))
}

// modulesRouter dispatches /api/admin/modules/{key}[/toggle|/test|/config].
func (h *Handler) handleModulesRouter(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/admin/modules/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "unknown modules endpoint")
		return
	}
	if r.Method == http.MethodGet && len(parts) == 1 {
		h.handleModulesGet(w, r)
		return
	}
	if r.Method == http.MethodPut && len(parts) == 2 && parts[1] == "toggle" {
		h.handleModulesToggle(w, r)
		return
	}
	if r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "test" {
		h.handleModulesTest(w, r)
		return
	}
	if r.Method == http.MethodGet && len(parts) == 2 && parts[1] == "config" {
		h.handleModulesConfig(w, r)
		return
	}
	writeError(w, http.StatusNotFound, "unknown modules endpoint")
}

// handleModulesTest 模块连通性测试。
//
// 当前实现：
//   - 对 feishu_bot：发送一条测试消息到 webhook_url，返回成功/失败/错误信息
//   - 其他模块：返回 501 Not Implemented（保留扩展点）
//
// POST /api/admin/modules/{key}/test
func (h *Handler) handleModulesTest(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/admin/modules/"), "/")
	if len(parts) < 2 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "missing module key")
		return
	}
	key := parts[0]

	switch key {
	case "feishu_bot":
		h.testFeishuBotWebhook(w, r)
	default:
		writeError(w, http.StatusNotImplemented, "test endpoint not implemented for module: "+key)
	}
}

// testFeishuBotWebhook 向 feishu_bot.webhook_url 发送测试消息。
func (h *Handler) testFeishuBotWebhook(w http.ResponseWriter, r *http.Request) {
	if settings.Global == nil {
		writeError(w, http.StatusServiceUnavailable, "settings not initialised")
		return
	}
	sp := settings.Global.Spec("feishu_bot.webhook_url")
	if sp == nil {
		writeError(w, http.StatusNotFound, "feishu_bot.webhook_url spec not found")
		return
	}
	raw, _, err := settings.Global.EffectiveValue(sp.Scope, sp.Key, "")
	if err != nil || raw == nil {
		writeError(w, http.StatusBadRequest, "feishu_bot.webhook_url not configured")
		return
	}
	var url string
	if err := json.Unmarshal(raw, &url); err != nil || url == "" {
		writeError(w, http.StatusBadRequest, "feishu_bot.webhook_url is empty")
		return
	}

	payload := map[string]any{
		"msg_type": "text",
		"content": map[string]any{
			"text": "✅ 飞书机器人连通性测试消息（来自 llm-gateway-go 管理后台）",
		},
	}
	body, _ := json.Marshal(payload)

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"reachable": false,
			"error":     "build request failed: " + err.Error(),
		})
		return
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"reachable":   false,
			"error":       err.Error(),
			"response_ms": elapsed,
		})
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	var larkResp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	_ = json.Unmarshal(respBody, &larkResp)

	if resp.StatusCode == http.StatusOK && larkResp.Code == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"reachable":   true,
			"status_code": resp.StatusCode,
			"response_ms": elapsed,
			"message":     "ok",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"reachable":   resp.StatusCode == http.StatusOK,
		"status_code": resp.StatusCode,
		"lark_code":   larkResp.Code,
		"lark_msg":    larkResp.Msg,
		"response_ms": elapsed,
		"error":       fmt.Sprintf("feishu returned %d/%d: %s", resp.StatusCode, larkResp.Code, larkResp.Msg),
	})
}

// handleModulesConfig 返回模块的运行配置 + 状态聚合。
//
// 与 handleModulesGet 的区别：仅返回运营需要的聚合字段（精简版）；
// 适用于面板下方的「运行状态」卡片，减少前端解析成本。
//
// GET /api/admin/modules/{key}/config
func (h *Handler) handleModulesConfig(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/admin/modules/"), "/")
	if len(parts) < 2 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "missing module key")
		return
	}
	key := parts[0]

	switch key {
	case "feishu_bot":
		h.feishuBotConfigSummary(w, r)
	default:
		writeError(w, http.StatusNotImplemented, "config endpoint not implemented for module: "+key)
	}
}

// feishuBotConfigSummary 返回 feishu_bot 模块的配置摘要 + 依赖状态。
func (h *Handler) feishuBotConfigSummary(w http.ResponseWriter, r *http.Request) {
	if settings.Global == nil {
		writeError(w, http.StatusServiceUnavailable, "settings not initialised")
		return
	}

	summary := map[string]any{}

	readString := func(key string) string {
		sp := settings.Global.Spec(key)
		if sp == nil {
			return ""
		}
		raw, _, err := settings.Global.EffectiveValue(sp.Scope, key, "")
		if err != nil || raw == nil {
			return ""
		}
		var v string
		_ = json.Unmarshal(raw, &v)
		return v
	}
	readBool := func(key string) bool {
		sp := settings.Global.Spec(key)
		if sp == nil {
			return false
		}
		raw, _, err := settings.Global.EffectiveValue(sp.Scope, key, "")
		if err != nil || raw == nil {
			return false
		}
		var v bool
		_ = json.Unmarshal(raw, &v)
		return v
	}
	readInt := func(key string) int {
		sp := settings.Global.Spec(key)
		if sp == nil {
			return 0
		}
		raw, _, err := settings.Global.EffectiveValue(sp.Scope, key, "")
		if err != nil || raw == nil {
			return 0
		}
		var v int
		_ = json.Unmarshal(raw, &v)
		return v
	}

	summary["enabled"] = readBool("feishu_bot.enabled")
	summary["webhook_url_set"] = readString("feishu_bot.webhook_url") != ""
	summary["verify_token_set"] = readString("feishu_bot.verify_token") != ""
	summary["encrypt_key_set"] = readString("feishu_bot.encrypt_key") != ""
	summary["connection_mode"] = readString("feishu_bot.connection_mode")
	summary["notify_on_alert"] = readBool("feishu_bot.notify_on_alert")
	summary["notify_on_approval"] = readBool("feishu_bot.notify_on_approval")
	summary["allowed_user_count"] = len(strings.Split(readString("feishu_bot.allowed_users"), ","))
	summary["alert_severity_min"] = readString("feishu_bot.alert.severity_min")
	summary["alert_rate_limit_min"] = readInt("feishu_bot.alert.rate_limit_per_minute")
	summary["alert_dedup_window_sec"] = readInt("feishu_bot.alert.dedup_window_seconds")
	summary["quiet_hours_enabled"] = readBool("feishu_bot.alert.quiet_hours_enabled")
	summary["quiet_hours_window"] = readString("feishu_bot.alert.quiet_hours_start") + "–" + readString("feishu_bot.alert.quiet_hours_end")
	summary["card_template"] = readString("feishu_bot.alert.card_template")
	summary["approval_expiry_min"] = readInt("feishu_bot.approval.expiry_reminder_minutes")
	summary["approval_mention_crit"] = readBool("feishu_bot.approval.auto_mention_on_critical")
	summary["commands_enabled"] = readBool("feishu_bot.commands.enabled")
	summary["commands_admin_only"] = readBool("feishu_bot.commands.admin_only")
	summary["signature_required"] = readBool("feishu_bot.signature_required")
	summary["timestamp_window_sec"] = readInt("feishu_bot.timestamp_window_seconds")

	writeJSON(w, http.StatusOK, summary)
}

// registerFeishuRoutes installs feishu_bot module admin endpoints.
func (h *Handler) registerFeishuRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/admin/feishubot/routing-rules", h.admin(h.handleFeishuRoutingList))
	mux.HandleFunc("/api/admin/feishubot/routing-rules/", h.admin(h.feishuRoutingRulesItem))
	mux.HandleFunc("/api/admin/feishubot/send-log", h.admin(h.handleFeishuSendLogList))
}

func (h *Handler) feishuRoutingRulesCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.handleFeishuRoutingList(w, r)
	} else if r.Method == http.MethodPost {
		h.handleFeishuRoutingCreate(w, r)
	} else {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) feishuRoutingRulesItem(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		h.handleFeishuRoutingUpdate(w, r)
	} else if r.Method == http.MethodDelete {
		h.handleFeishuRoutingDelete(w, r)
	} else {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// allDepsEnabled checks if all required dependencies are enabled.
func allDepsEnabled(m ModuleDefinition, enabledMap map[string]bool) bool {
	for _, dep := range m.Dependencies {
		if dep.Required && !enabledMap[dep.Key] {
			return false
		}
	}
	return true
}

// missingDeps returns the list of required dependencies that are not enabled.
func missingDeps(m ModuleDefinition, enabledMap map[string]bool) []string {
	var missing []string
	for _, dep := range m.Dependencies {
		if dep.Required && !enabledMap[dep.Key] {
			missing = append(missing, dep.Key)
		}
	}
	return missing
}
