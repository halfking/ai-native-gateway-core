package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"

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

// ModuleDependency describes a dependency on another module.
type ModuleDependency struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
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
	Enabled bool   `json:"enabled"`
	Source  string `json:"source"`
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
				ConfigKeys:  []string{"compression.mode", "compression.window_fraction", "compression.llm_model"},
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
				Description: "当会话上下文达到阈值时自动执行 Handoff，生成摘要并提示新会话，防止上下文超限。",
				Capabilities: []string{
					"自动检测上下文使用率触发交接",
					"绝对 token 阈值 / 百分比阈值 / 消息数阈值",
					"自定义 Skill 名称",
					"上下文摘要生成",
				},
				Icon:        "🔄",
				Category:    "session",
				SettingKey:  "handoff.enabled",
				ConfigKeys:  []string{"handoff.threshold", "handoff.skill_name", "handoff.message_threshold"},
				DangerLevel: settings.Warning,
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
				Description: "多层防御体系：规则引擎 + LLM 智能检测 + 向量相似度 + Canary Token，支持 15 种风险类别和 11 种处理动作。",
				Capabilities: []string{
					"多层检测引擎（规则/启发式/LLM/向量/Canary）",
					"15 种风险类别（角色劫持、指令覆盖、越狱、数据窃取等）",
					"11 种处理动作（替换/脱敏/拒绝/终止/审批等）",
					"严重等级处理矩阵（可配置每级动作）",
					"LLM 智能检测（支持多引擎选择）",
					"Canary Token 泄漏检测",
					"向量相似度攻击匹配（pgvector）",
					"人工审批流程",
					"Webhook/邮件告警",
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
				},
				Icon:        "📋",
				Category:    "security",
				SettingKey:  "session_audit.enabled",
				DangerLevel: settings.Warning,
			},
			{
				Key:         "session_inspector",
				Name:        "会话健康检查",
				Description: "监控会话各项健康指标：Token 限制、不活跃超时、高频请求等，确保会话处于健康状态。",
				Capabilities: []string{
					"Token 使用量监控与告警",
					"不活跃会话检测与回收",
					"高频请求检测",
					"会话生命周期管理",
				},
				Icon:        "🔍",
				Category:    "session",
				SettingKey:  "session_inspector.enabled",
				DangerLevel: settings.Safe,
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
				Icon:       "🔐",
				Category:   "security",
				SettingKey: "security.enabled",
				ConfigKeys: []string{
					"security.mode",
					"security.llm.intent_model",
					"security.llm.threat_model",
					"security.intent.enabled",
					"security.intent.confidence_threshold",
					"security.intent.drift_threshold",
					"security.threat.enabled",
					"security.threat.severity_threshold",
					"security.threat.checks.prompt_inject",
					"security.threat.checks.jailbreak",
					"security.threat.checks.data_leak",
					"security.threat.checks.pii",
					"security.threat.checks.persona_override",
					"security.response.low_risk",
					"security.response.medium_risk",
					"security.response.high_risk",
					"security.audit.enabled",
					"security.audit.log_all",
					"security.audit.sampling_rate",
				},
				DocsURL:     "/admin/modules",
				DangerLevel: settings.Dangerous,
				Dependencies: []ModuleDependency{
					{Key: "prompt_injection", Name: "提示词注入检测", Required: true, Description: "提供提示词注入模式检测能力"},
					{Key: "output_compliance", Name: "输出合规检测", Required: true, Description: "提供PII和敏感数据检测能力"},
					{Key: "session_audit", Name: "会话审计与审批", Required: true, Description: "提供高风险操作审批流程"},
					{Key: "audit", Name: "审计日志", Required: false, Description: "记录安全检测审计日志"},
					{Key: "cache", Name: "会话缓存", Required: false, Description: "提升安全检测性能"},
					{Key: "compression", Name: "压缩管理", Required: false, Description: "降低安全检测token消耗"},
				},
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
				Description: "启用 User-Agent 和 TLS 指纹轮换，避免被上游提供商检测到非标准客户端。",
				Capabilities: []string{
					"User-Agent 轮换",
					"TLS 指纹轮换",
					"合规参考文档",
				},
				Icon:        "🎭",
				Category:    "security",
				SettingKey:  "enable_disguise",
				DangerLevel: settings.Breaking,
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
					"feishu_bot.notify_on_alert",
					"feishu_bot.notify_on_approval",
					"feishu_bot.allowed_users",
				},
				DangerLevel: settings.Safe,
				Integration: &ModuleIntegration{
					Type:        "feishu",
					Label:       "飞书",
					Description: "对接飞书自定义机器人，使用 Webhook 进行消息推送和交互",
					DocURL:      "https://open.feishu.cn/document/client-docs/bot-v3/add-custom-bot",
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

// handleModulesList returns all modules with their current enabled/disabled status.
//
// GET /api/admin/modules
func (h *Handler) handleModulesList(w http.ResponseWriter, r *http.Request) {
	if settings.Global == nil {
		writeError(w, http.StatusServiceUnavailable, "settings registry not initialised")
		return
	}
	defs := allModuleDefinitions()
	out := make([]ModuleWithStatus, 0, len(defs))
	for _, m := range defs {
		enabled, src := resolveModuleEnabled(m)
		out = append(out, ModuleWithStatus{
			ModuleDefinition: m,
			Enabled:          enabled,
			Source:           src,
		})
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

	enabled, src := resolveModuleEnabled(*found)

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
		"module": ModuleWithStatus{
			ModuleDefinition: *found,
			Enabled:          enabled,
			Source:           src,
		},
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

	// Modules without a SettingKey (e.g. memora) are runtime-driven and have
	// no on/off toggle to persist. Return 200 with the effective state so the
	// frontend can update its UI without erroring — previously this returned
	// 404, which left the optimistic UI in an inconsistent state.
	if found.SettingKey == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"enabled": true,
			"module":  found.Key,
			"message": "该模块无独立开关，状态由运行时决定: " + found.Name,
		})
		return
	}

	sp := settings.Global.Spec(found.SettingKey)
	if sp == nil {
		writeError(w, http.StatusNotFound, "module setting spec not registered: "+found.SettingKey)
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

// registerModuleRoutes installs the module management endpoints.
func (h *Handler) registerModuleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/admin/modules", h.admin(h.handleModulesList))
	mux.HandleFunc("/api/admin/modules/", h.admin(h.handleModulesRouter))
}

// modulesRouter dispatches /api/admin/modules/{key}[/toggle].
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
	writeError(w, http.StatusNotFound, "unknown modules endpoint")
}
