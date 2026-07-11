package settings

// HandoffSpecs returns the configuration specs for automatic session handoff.
//
// Design rationale (2026-07-09, refactor from auto-control):
//
//   - One Spec per operator-facing setting so the admin UI can render it
//     generically (ModulesView.vue consumes ModulesView's config form, which
//     reads `Spec.Description`, `Spec.Options`, `Spec.Min/Max`).
//
//   - Scope split:
//
//   - Platform-scope ("handoff.enabled", "handoff.mode" etc.) —
//     operator-level defaults; takes effect unless overridden per
//     tenant.
//
//   - Tenant-scope (thresholds, summary engine, cooling) —
//     per-tenant fine-tuning, hot-reload.
//
//   - The hook itself (domains/hooks/handoff/trigger_hook.go) reads via
//     SettingsGetter, mirroring how goal.ModeHook does it.
//
// New settings added 2026-07-09 (Phase 2 restore):
//   - trigger.mode         {auto,manual,hybrid}   — defaults to auto (existing behavior).
//   - summary.engine       {llm,rule,hybrid}      — LLM-based summary by default.
//   - summary.model        string                 — overrides LLMGatewayAutoLLM* default.
//   - summary.keep_recent_n int                   — keep tail of last N messages verbatim.
//   - summary.max_tokens   int                    — cap summary size.
//   - summary.prompt_tpl   string                 — custom LLM summary prompt template.
//   - summary.extract_facts bool                  — promote decisions/paths to system level.
//   - cooldown_seconds     int                    — min interval between handoffs.
//   - max_per_session      int                    — per-session handoff budget.
//   - notify_level         enum{none,info,warn}   — log/notification level.
//   - notify_webhook       string                 — optional webhook URL.
//   - include_history_in_summary bool             — include full req/resp history.
func HandoffSpecs() []Spec {
	return []Spec{
		// ── 主开关 ──────────────────────────────────────────────────
		{
			Key:             "handoff.enabled",
			EnvName:         "LLM_GATEWAY_HANDOFF_ENABLED",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategorySession,
			Default:         false,
			Description:     "启用会话交接",
			DescriptionLong: "主开关：开启后，网关会在请求转发前检测上下文阈值，生成结构化摘要并透明切换到新会话。关闭后不执行自动或手动 /handoff。",
			Unit:            "",
			HotReload:       true,
			DangerLevel:     Warning,
		},

		// ── 触发模式（Phase 2 新增） ─────────────────────────────────
		{
			Key:             "handoff.trigger_mode",
			EnvName:         "LLM_GATEWAY_HANDOFF_TRIGGER_MODE",
			Type:            TypeEnum,
			Scope:           ScopePlatform,
			Category:        CategorySession,
			Default:         "auto",
			Options:         []string{"auto", "manual", "hybrid"},
			Description:     "Handoff 触发模式",
			DescriptionLong: "auto=按阈值自动触发；manual=仅响应用户 /handoff 命令；hybrid=阈值与用户命令均可触发。交接由网关在请求侧执行，skill 不需要安装在客户端。",
			Unit:            "",
			HotReload:       true,
			DangerLevel:     Warning,
		},

		{
			Key:             "handoff.client_mode",
			EnvName:         "LLM_GATEWAY_HANDOFF_CLIENT_MODE",
			Type:            TypeEnum,
			Scope:           ScopeTenant,
			Category:        CategorySession,
			Default:         "transparent",
			Options:         []string{"transparent", "explicit"},
			Description:     "客户端交接模式",
			DescriptionLong: "transparent=网关创建新会话、注入恢复包并继续转发当前请求；explicit=返回 202 和 resume_packet，由客户端创建新会话。请求可用 X-Gw-Handoff-Mode: explicit 临时选择显式模式。",
			HotReload:       true,
			DangerLevel:     Warning,
		},

		// ── Token 绝对阈值 ───────────────────────────────────────────
		{
			Key:             "handoff.absolute_threshold",
			EnvName:         "LLM_GATEWAY_HANDOFF_ABSOLUTE_THRESHOLD",
			Type:            TypeInt,
			Scope:           ScopeTenant,
			Category:        CategorySession,
			Default:         180000,
			Min:             floatPtr(10000),
			Max:             floatPtr(2000000),
			Description:     "Token 绝对阈值",
			DescriptionLong: "当会话累计 token 数达到此阈值时触发 handoff（默认 180K）。0 表示禁用绝对阈值，仅依赖百分比阈值。",
			Unit:            "tokens",
			HotReload:       true,
			DangerLevel:     Safe,
		},

		// ── Token 百分比阈值 ─────────────────────────────────────────
		{
			Key:             "handoff.percentage_threshold",
			EnvName:         "LLM_GATEWAY_HANDOFF_PERCENTAGE_THRESHOLD",
			Type:            TypeFloat,
			Scope:           ScopeTenant,
			Category:        CategorySession,
			Default:         0.8,
			Min:             floatPtr(0.5),
			Max:             floatPtr(0.95),
			Description:     "Token 百分比阈值",
			DescriptionLong: "会话 token 数达到模型上下文窗口的此百分比时触发 handoff（默认 80%）。建议 0.7-0.85。多个阈值同时触发时取最严格的。",
			Unit:            "%",
			HotReload:       true,
			DangerLevel:     Safe,
		},

		// ── 消息数阈值 ───────────────────────────────────────────────
		{
			Key:             "handoff.message_threshold",
			EnvName:         "LLM_GATEWAY_HANDOFF_MESSAGE_THRESHOLD",
			Type:            TypeInt,
			Scope:           ScopeTenant,
			Category:        CategorySession,
			Default:         0,
			Min:             floatPtr(0),
			Max:             floatPtr(1000),
			Description:     "消息数量阈值",
			DescriptionLong: "当会话消息数达到此阈值时触发 handoff。0 表示禁用（默认）。冷启动场景下建议至少 10 条以上才触发，避免过早交接。",
			Unit:            "条",
			HotReload:       true,
			DangerLevel:     Safe,
		},

		// ── 静默时长阈值（Phase 2 新增） ─────────────────────────────
		{
			Key:             "handoff.idle_minutes",
			EnvName:         "LLM_GATEWAY_HANDOFF_IDLE_MINUTES",
			Type:            TypeInt,
			Scope:           ScopeTenant,
			Category:        CategorySession,
			Default:         0,
			Min:             floatPtr(0),
			Max:             floatPtr(1440),
			Description:     "会话静默时长阈值",
			DescriptionLong: "会话空闲超过 N 分钟且未关闭时自动触发 handoff（适合异步任务/长时挂起场景）。0 表示禁用（默认）。建议 30-60 分钟。",
			Unit:            "分钟",
			HotReload:       true,
			DangerLevel:     Safe,
		},

		// ── 最小消息数门槛（Phase 2 新增） ──────────────────────────
		{
			Key:             "handoff.min_messages",
			EnvName:         "LLM_GATEWAY_HANDOFF_MIN_MESSAGES",
			Type:            TypeInt,
			Scope:           ScopeTenant,
			Category:        CategorySession,
			Default:         10,
			Min:             floatPtr(2),
			Max:             floatPtr(1000),
			Description:     "最小消息数门槛",
			DescriptionLong: "会话消息数低于此值时不触发 handoff，防止冷启动场景过度交接。默认 10。",
			Unit:            "条",
			HotReload:       true,
			DangerLevel:     Safe,
		},

		// ── Skill 名称（保留旧字段，向下兼容） ─────────────────────
		{
			Key:             "handoff.skill_name",
			EnvName:         "LLM_GATEWAY_HANDOFF_SKILL_NAME",
			Type:            TypeString,
			Scope:           ScopePlatform,
			Category:        CategorySession,
			Default:         "handoff",
			Description:     "Handoff Skill 名称",
			DescriptionLong: "触发 handoff 时调用的 skill 名称（如 /handoff）。修改后可对接自定义 skill（如 /session-resume、/compaction）。",
			Unit:            "",
			HotReload:       true,
			DangerLevel:     Safe,
		},

		// ── 摘要引擎（Phase 2 新增） ────────────────────────────────
		{
			Key:             "handoff.summary_engine",
			EnvName:         "LLM_GATEWAY_HANDOFF_SUMMARY_ENGINE",
			Type:            TypeEnum,
			Scope:           ScopeTenant,
			Category:        CategorySession,
			Default:         "llm",
			Options:         []string{"llm", "rule", "hybrid"},
			Description:     "摘要生成引擎",
			DescriptionLong: "llm=使用 LLM 调用 LLMGatewayAutoLLM 端点生成摘要（质量最佳，成本略高）；rule=取最近 N 条 + 关键事实抽取（无 LLM 成本）；hybrid=LLM + 规则混合。",
			Unit:            "",
			HotReload:       true,
			DangerLevel:     Safe,
		},

		// ── 摘要模型（Phase 2 新增） ────────────────────────────────
		{
			Key:             "handoff.summary_model",
			EnvName:         "LLM_GATEWAY_HANDOFF_SUMMARY_MODEL",
			Type:            TypeString,
			Scope:           ScopeTenant,
			Category:        CategorySession,
			Default:         "",
			Description:     "摘要生成模型",
			DescriptionLong: "摘要用大模型，留空则使用 LLMGatewayAutoLLMModel（默认）。建议选择便宜模型（如 gemini-2.5-flash、minimax-text-01）以降低成本。要求上下文窗口 ≥ 200K tokens。",
			Unit:            "",
			HotReload:       true,
			DangerLevel:     Safe,
		},

		// ── 保留最近 N 条（Phase 2 新增） ────────────────────────────
		{
			Key:             "handoff.summary_keep_recent_n",
			EnvName:         "LLM_GATEWAY_HANDOFF_SUMMARY_KEEP_RECENT_N",
			Type:            TypeInt,
			Scope:           ScopeTenant,
			Category:        CategorySession,
			Default:         4,
			Min:             floatPtr(0),
			Max:             floatPtr(50),
			Description:     "保留最近 N 条消息",
			DescriptionLong: "在摘要后保留最近 N 条完整消息不压缩，避免 LLM 丢失最新意图。0 表示全部摘要（不保留）。建议 2-8。",
			Unit:            "条",
			HotReload:       true,
			DangerLevel:     Safe,
		},

		// ── 最大摘要 token（Phase 2 新增） ───────────────────────────
		{
			Key:             "handoff.summary_max_tokens",
			EnvName:         "LLM_GATEWAY_HANDOFF_SUMMARY_MAX_TOKENS",
			Type:            TypeInt,
			Scope:           ScopeTenant,
			Category:        CategorySession,
			Default:         2000,
			Min:             floatPtr(200),
			Max:             floatPtr(8000),
			Description:     "最大摘要 token 数",
			DescriptionLong: "限制摘要本身的最大 token 数，防止摘要膨胀。建议 1000-3000。",
			Unit:            "tokens",
			HotReload:       true,
			DangerLevel:     Safe,
		},

		// ── 摘要 prompt 模板（Phase 2 新增） ─────────────────────────
		{
			Key:             "handoff.summary_prompt_tpl",
			EnvName:         "LLM_GATEWAY_HANDOFF_SUMMARY_PROMPT_TPL",
			Type:            TypeString,
			Scope:           ScopeTenant,
			Category:        CategorySession,
			Default:         "",
			Description:     "摘要 Prompt 模板",
			DescriptionLong: "自定义 LLM 摘要 prompt，支持 ${recent_n} ${keep_facts} ${max_tokens} 占位符。留空则使用内置模板。",
			Unit:            "",
			HotReload:       true,
			DangerLevel:     Safe,
		},

		// ── 关键事实抽取（Phase 2 新增） ─────────────────────────────
		{
			Key:             "handoff.summary_extract_facts",
			EnvName:         "LLM_GATEWAY_HANDOFF_SUMMARY_EXTRACT_FACTS",
			Type:            TypeBool,
			Scope:           ScopeTenant,
			Category:        CategorySession,
			Default:         true,
			Description:     "关键事实抽取",
			DescriptionLong: "从历史中抽取关键事实（决策、约定、文件路径）提升到 system 级，防止交接后丢失。",
			Unit:            "",
			HotReload:       true,
			DangerLevel:     Safe,
		},

		// ── 冷却时间（Phase 2 新增） ─────────────────────────────────
		{
			Key:             "handoff.cooldown_seconds",
			EnvName:         "LLM_GATEWAY_HANDOFF_COOLDOWN_SECONDS",
			Type:            TypeInt,
			Scope:           ScopeTenant,
			Category:        CategorySession,
			Default:         60,
			Min:             floatPtr(0),
			Max:             floatPtr(3600),
			Description:     "交接冷却时间",
			DescriptionLong: "两次交接的最小间隔秒数，防止短时间内频繁交接放大成本。0 表示无冷却。默认 60 秒。",
			Unit:            "秒",
			HotReload:       true,
			DangerLevel:     Safe,
		},

		// ── 单会话最大交接次数（Phase 2 新增） ───────────────────────
		{
			Key:             "handoff.max_per_session",
			EnvName:         "LLM_GATEWAY_HANDOFF_MAX_PER_SESSION",
			Type:            TypeInt,
			Scope:           ScopeTenant,
			Category:        CategorySession,
			Default:         5,
			Min:             floatPtr(1),
			Max:             floatPtr(50),
			Description:     "单会话最大交接次数",
			DescriptionLong: "单会话允许的 handoff 次数硬上限，防止死循环。超出后强制走新会话（不触发摘要）。",
			Unit:            "次",
			HotReload:       true,
			DangerLevel:     Safe,
		},

		// ── 失败降级（Phase 2 新增） ─────────────────────────────────
		{
			Key:             "handoff.retry_on_failure",
			EnvName:         "LLM_GATEWAY_HANDOFF_RETRY_ON_FAILURE",
			Type:            TypeInt,
			Scope:           ScopeTenant,
			Category:        CategorySession,
			Default:         1,
			Min:             floatPtr(0),
			Max:             floatPtr(3),
			Description:     "摘要失败重试次数",
			DescriptionLong: "LLM 摘要失败时是否降级到 rule 引擎重试。0=失败即放弃；1=自动降级到 rule（默认）；2/3=多次重试。",
			Unit:            "次",
			HotReload:       true,
			DangerLevel:     Safe,
		},

		// ── 通知级别（Phase 2 新增） ─────────────────────────────────
		{
			Key:             "handoff.notify_level",
			EnvName:         "LLM_GATEWAY_HANDOFF_NOTIFY_LEVEL",
			Type:            TypeEnum,
			Scope:           ScopeTenant,
			Category:        CategorySession,
			Default:         "warn",
			Options:         []string{"none", "info", "warn"},
			Description:     "通知级别",
			DescriptionLong: "触发交接时的日志/通知级别。none=静默；info=INFO 级别日志；warn=WARN 级别日志 + 审计告警。",
			Unit:            "",
			HotReload:       true,
			DangerLevel:     Safe,
		},

		// ── Webhook 通知（Phase 2 新增） ─────────────────────────────
		{
			Key:             "handoff.notify_webhook",
			EnvName:         "LLM_GATEWAY_HANDOFF_NOTIFY_WEBHOOK",
			Type:            TypeURL,
			Scope:           ScopeTenant,
			Category:        CategorySession,
			Default:         "",
			Description:     "通知 Webhook URL",
			DescriptionLong: "触发交接时调用的 Webhook URL（如飞书/Slack 机器人）。POST body 含 session_id、trigger_reason、summary_text 等。留空则不推送。",
			Unit:            "URL",
			HotReload:       true,
			DangerLevel:     Safe,
		},

		// ── 续跑提示模板（Phase 2 新增） ─────────────────────────────
		{
			Key:             "handoff.continue_hint_tpl",
			EnvName:         "LLM_GATEWAY_HANDOFF_CONTINUE_HINT_TPL",
			Type:            TypeString,
			Scope:           ScopeTenant,
			Category:        CategorySession,
			Default:         "",
			Description:     "新会话引导模板",
			DescriptionLong: "注入到新会话首条 system 的引导文案模板，支持 ${summary} ${previous_session_id} ${trigger_reason} 占位符。留空使用内置模板。",
			Unit:            "",
			HotReload:       true,
			DangerLevel:     Safe,
		},
	}
}
