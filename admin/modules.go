package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

	// Requires 列出依赖的其他 module.key。依赖未启用时 UI 给出软提示（不阻断 toggle）。
	// 业务侧通过 EnabledEffective() 自行决定是否启用插件逻辑。
	Requires []string `json:"requires,omitempty"`
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
	// RequirementsMet 当 Requires 全部启用时为 true。false 时 UI 应展示软提示。
	RequirementsMet bool `json:"requirements_met"`
	// MissingRequirements 列出当前未启用的依赖 key（仅 requirements_met=false 时非空）。
	MissingRequirements []string `json:"missing_requirements,omitempty"`
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
				Requires:    []string{"compression", "cache", "prompt_injection", "session_audit"},
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

// allDepsEnabled 检查 module 的所有 Requires 是否在 enabledMap 中为 true。
//
// 若 module 没有 Requires 一律返回 true。
// 用途：UI 软提示 / 插件侧 fail-secure 判断。
func allDepsEnabled(m ModuleDefinition, enabledMap map[string]bool) bool {
	for _, dep := range m.Requires {
		if !enabledMap[dep] {
			return false
		}
	}
	return true
}

// missingDeps 返回 enabledMap 中为 false 的依赖 key 列表（按 Requires 顺序）。
func missingDeps(m ModuleDefinition, enabledMap map[string]bool) []string {
	var out []string
	for _, dep := range m.Requires {
		if !enabledMap[dep] {
			out = append(out, dep)
		}
	}
	return out
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
	// 先把所有 (key → enabled) 一次性算好，避免 Requires 计算时重复查 settings。
	enabledMap := make(map[string]bool, len(defs))
	srcMap := make(map[string]string, len(defs))
	for _, m := range defs {
		en, src := resolveModuleEnabled(m)
		enabledMap[m.Key] = en
		srcMap[m.Key] = src
	}
	out := make([]ModuleWithStatus, 0, len(defs))
	for _, m := range defs {
		out = append(out, ModuleWithStatus{
			ModuleDefinition:    m,
			Enabled:             enabledMap[m.Key],
			Source:              srcMap[m.Key],
			RequirementsMet:     allDepsEnabled(m, enabledMap),
			MissingRequirements: missingDeps(m, enabledMap),
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
	// 同步构建 enabledMap，供 requirements 校验与自身 enabled 复用。
	enabledMap := make(map[string]bool, len(defs))
	for _, m := range defs {
		en, _ := resolveModuleEnabled(m)
		enabledMap[m.Key] = en
	}
	for i, m := range defs {
		if m.Key == key {
			found = &defs[i]
			break
		}
	}
	if found == nil {
		writeError(w, http.StatusNotFound, "unknown module: "+key)
		return
	}

	enabled, src := resolveModuleEnabled(*found)
	reqsMet := allDepsEnabled(*found, enabledMap)
	missing := missingDeps(*found, enabledMap)

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
			ModuleDefinition:    *found,
			Enabled:             enabled,
			Source:              src,
			RequirementsMet:     reqsMet,
			MissingRequirements: missing,
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

	// 2026-07-09: 飞书机器人运营面 API
	// （routing rules CRUD + send log list）见 admin/feishu_handlers.go
	h.registerFeishuRoutes(mux)
}

// registerFeishuRoutes 安装飞书机器人运营面路由。
//
// 所有端点位于 /api/admin/feishubot/*，admin 鉴权。
// 设计：routing rules 走 DB 表 feishu_bot_routing_rules，
// 走完整 CRUD 生命周期（list/create/update/delete）。
// send-log 是只读查询，最近 200 条。
func (h *Handler) registerFeishuRoutes(mux *http.ServeMux) {
	// /api/admin/feishubot/routing-rules
	// 用 router 子分发区分 GET/POST/PUT/DELETE
	mux.HandleFunc("/api/admin/feishubot/routing-rules", h.admin(h.feishuRoutingRulesCollection))
	mux.HandleFunc("/api/admin/feishubot/routing-rules/", h.admin(h.feishuRoutingRulesItem))
	mux.HandleFunc("/api/admin/feishubot/routing-rules:import", h.admin(h.handleFeishuRoutingRulesImport))

	// /api/admin/feishubot/send-log （只读）
	mux.HandleFunc("/api/admin/feishubot/send-log", h.admin(h.handleFeishuSendLogList))
}

// feishuRoutingRulesCollection handles GET (list) and POST (create) on the collection.
func (h *Handler) feishuRoutingRulesCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleFeishuRoutingList(w, r)
	case http.MethodPost:
		h.handleFeishuRoutingCreate(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// feishuRoutingRulesItem handles PUT (update) and DELETE on a single item.
func (h *Handler) feishuRoutingRulesItem(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPut:
		h.handleFeishuRoutingUpdate(w, r)
	case http.MethodDelete:
		h.handleFeishuRoutingDelete(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
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
//
// 注意：这是 best-effort 测试，不验证业务签名（飞书 webhook 是单向 POST）。
// 返回字段：
//   - reachable: 是否收到 HTTP 200
//   - status_code: 飞书响应码（0=非 200）
//   - error: 错误信息
//   - response_ms: 耗时（毫秒）
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

	// 构造飞书自定义机器人的 test payload（msg_type=text）
	payload := map[string]any{
		"msg_type": "text",
		"content": map[string]any{
			"text": "✅ 飞书机器人连通性测试消息（来自 llm-gateway-go 管理后台）",
		},
	}
	body, _ := json.Marshal(payload)

	// 通过 http.Client 发送（避免与现有 LarkBotChannel 耦合）
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
//
// 与 handleModulesGet 不同：本端点不返回所有 config_keys 的 value/spec，
// 只返回 feishubot.Config.AsJSON() 的聚合字段，便于前端一次性渲染。
func (h *Handler) feishuBotConfigSummary(w http.ResponseWriter, r *http.Request) {
	if settings.Global == nil {
		writeError(w, http.StatusServiceUnavailable, "settings not initialised")
		return
	}

	// 读取 feishu_bot.* 配置（不 import domains/feishubot 以避免循环）
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

	// 依赖状态
	defs := allModuleDefinitions()
	enabledMap := make(map[string]bool)
	for _, m := range defs {
		en, _ := resolveModuleEnabled(m)
		enabledMap[m.Key] = en
	}
	var fb *ModuleDefinition
	for i, m := range defs {
		if m.Key == "feishu_bot" {
			fb = &defs[i]
			break
		}
	}
	var missing []string
	if fb != nil {
		missing = missingDeps(*fb, enabledMap)
	}
	summary["requirements_met"] = len(missing) == 0
	summary["missing_requirements"] = missing

	writeJSON(w, http.StatusOK, summary)
}
