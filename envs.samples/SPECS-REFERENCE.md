<!--
本文件由 scripts/gen-env-ref 生成，勿手改。
再生成: go run ./scripts/gen-env-ref > envs.samples/SPECS-REFERENCE.md
-->

# settings 配置规格全表（机器生成）

settings 注册表（PlatformSpecs/TenantSpecs/模块级规格）中带 EnvName 的全部条目，
共 104 个环境变量。这些变量均可被 settings_kv 数据库值按 Key 覆盖（热加载），
优先级: settings_kv > 环境变量 > Default 列。作用域 platform=平台级、tenant=租户级。

| 环境变量 | 设置键 | 作用域 | 类型 | 默认值 | 选项/范围 | 说明 |
| --- | --- | --- | --- | --- | --- | --- |
| `LLM_GATEWAY_COMPRESSION_RUNNER_MODE` | `compression.runner_mode` | platform | enum | `sequential` | sequential / parallel | 压缩策略执行模式 |
| `LLM_GATEWAY_COMPRESSION_SELECTOR` | `compression.selector_mode` | platform | enum | `manual` | manual / adaptive | 压缩策略选择模式 |
| `LLM_GATEWAY_COMPRESSION_SELECTOR_SPEC` | `compression.selector_spec` | platform | string | `` |  | 手动压缩策略列表（csv） |
| `LLM_GATEWAY_COMPRESSION_STRATEGY_RUNNER_ENABLED` | `compression.strategy_runner_enabled` | platform | bool | `false` |  | 启用可选压缩策略执行器 |
| `LLM_GATEWAY_COMPRESSION_TARGET_RATIO` | `compression.adaptive_target_ratio` | platform | float | `0.8` | 0 ~ 1 | 自适应压缩目标比例 |
| `LLM_GATEWAY_CREDENTIAL_MODEL_DEPRECATED_COOLDOWN_SECONDS` | `credential.model_deprecated_cooldown_seconds` | platform | int | `2592000` | 86400 ~ 3.1536e+07 | 模型下线（deprecation）冷却（秒） |
| `LLM_GATEWAY_CREDENTIAL_MODEL_NOT_FOUND_INITIAL_COOLDOWN_SECONDS` | `credential.model_not_found_initial_cooldown_seconds` | platform | int | `1800` | 60 ~ 604800 | MNF 首次冷却（秒） |
| `LLM_GATEWAY_CREDENTIAL_MODEL_NOT_FOUND_SUSTAINED_COOLDOWN_SECONDS` | `credential.model_not_found_sustained_cooldown_seconds` | platform | int | `604800` | 1800 ~ 2.592e+06 | MNF 持续冷却（秒） |
| `LLM_GATEWAY_DISGUISE_FP_SLOT_TTL_SECONDS` | `disguise.fp_slot_ttl_seconds` | platform | int | `1800` | 300 ~ 86400 | 指纹槽自动归还 TTL（秒） |
| `LLM_GATEWAY_ERROR_PROBE_CONSECUTIVE_THRESHOLD` | `error_probe.consecutive_threshold` | platform | int | `2` | 2 ~ 10 | 触发主动探测的连续失败次数阈值。值越小越敏感（误触发风险增加），值越大越保守（响应延迟增加）。启动时读取，改动重启生效。 |
| `LLM_GATEWAY_ERROR_PROBE_ENABLED` | `error_probe.enabled` | platform | bool | `true` |  | 是否启用错误触发的主动探测（请求连续失败时立即直连上游探测，不等待周期扫描）。启动时读取，改动重启生效。 |
| `LLM_GATEWAY_ERROR_PROBE_MAX_ATTEMPTS` | `error_probe.max_attempts` | platform | int | `5` | 1 ~ 20 | 每个 (凭据, 模型) 组合最多探测轮数。超过后停止探测，依赖被动恢复机制 (passive_probe / credential_recovery)。启动时读取，改动重启生效。 |
| `LLM_GATEWAY_ERROR_PROBE_TIMEOUT_MS` | `error_probe.timeout_ms` | platform | int | `30000` | 1000 ~ 60000 | 单次直连探测的 HTTP 请求超时时间。启动时读取，改动重启生效。（毫秒） |
| `LLM_GATEWAY_ERROR_PROBE_WORKERS` | `error_probe.workers` | platform | int | `5` | 1 ~ 5 | 错误触发主动探测 worker 并发（个） |
| `LLM_GATEWAY_GOAL_AUDIT_ENABLED` | `goal.audit_enabled` | tenant | bool | `false` |  | 启用任务完成后审计 |
| `LLM_GATEWAY_GOAL_AUDIT_MAX_ROUNDS` | `goal.audit_max_rounds` | tenant | int | `3` | 1 ~ 5 | 审计最大轮数 |
| `LLM_GATEWAY_GOAL_AUDIT_MIN_CONFIDENCE` | `goal.audit_min_confidence` | tenant | float | `0.7` | 0 ~ 1 | 审计最低置信度 |
| `LLM_GATEWAY_GOAL_AUDIT_MODEL` | `goal.audit_model` | tenant | string | `auto` |  | 审计使用的模型 |
| `LLM_GATEWAY_GOAL_AUDIT_VERIFY_ENABLED` | `goal.audit_verify_enabled` | tenant | bool | `true` |  | 启用独立 VERIFY 阶段 |
| `LLM_GATEWAY_GOAL_AUDIT_VERIFY_MODEL` | `goal.audit_verify_model` | tenant | string | `auto` |  | VERIFY 使用的模型 |
| `LLM_GATEWAY_GOAL_AUTO_CONTINUE` | `goal.auto_continue_on_pause` | tenant | bool | `true` |  | 暂停时自动继续 |
| `LLM_GATEWAY_GOAL_AUTO_FIX` | `goal.auto_fix_enabled` | tenant | bool | `false` |  | 启用自动修复 |
| `LLM_GATEWAY_GOAL_AUTO_FIX_SEVERITY` | `goal.auto_fix_severity_threshold` | tenant | enum | `high` | high / medium / low | 自动修正严重程度阈值 |
| `LLM_GATEWAY_GOAL_AUTO_SELECT` | `goal.auto_select_recommended` | tenant | bool | `true` |  | 自动选择推荐选项 |
| `LLM_GATEWAY_GOAL_CLIENT_DRIVEN` | `goal.client_signal_enabled` | tenant | bool | `false` |  | 启用客户端驱动的 Goal 控制信号 |
| `LLM_GATEWAY_GOAL_CLIENT_SIGNAL_MODE` | `goal.client_signal_mode` | tenant | enum | `auto` | auto / continue / handoff / both | 客户端 Goal 控制信号模式 |
| `LLM_GATEWAY_GOAL_CLIENT_SIGNAL_ON_TOOL_CALLS` | `goal.client_signal_on_tool_calls` | tenant | bool | `false` |  | tool_calls 收尾时发 advisory gw-continue |
| `LLM_GATEWAY_GOAL_COMPLETION_CONFIDENCE` | `goal.completion_confidence` | tenant | float | `0.8` | 0 ~ 1 | 完成检测最低置信度 |
| `LLM_GATEWAY_GOAL_COST_ALERT_THRESHOLD` | `goal.cost_alert_threshold` | tenant | float | `0.85` | 0 ~ 1 | 成本告警阈值 |
| `LLM_GATEWAY_GOAL_COST_MODE` | `goal.cost_mode` | tenant | enum | `minimal` | minimal / balanced / aggressive | 成本控制模式 |
| `LLM_GATEWAY_GOAL_DETECTION_MODE` | `goal.detection_mode` | tenant | enum | `hybrid` | keyword / explicit / llm / hybrid | Goal模式检测方式 |
| `LLM_GATEWAY_GOAL_DOWNGRADE_ON_BUDGET` | `goal.downgrade_on_budget` | tenant | bool | `true` |  | 预算不足时自动降级 |
| `LLM_GATEWAY_GOAL_ENABLED` | `goal.enabled` | tenant | bool | `false` |  | 启用Goal模式 |
| `LLM_GATEWAY_GOAL_FALLBACK_AUDIT_MODEL` | `goal.fallback_audit_model` | tenant | string | `auto` |  | 审计备用模型 |
| `LLM_GATEWAY_GOAL_FALLBACK_MODELS` | `goal.fallback_models` | tenant | string | `` |  | 备选模型列表 |
| `LLM_GATEWAY_GOAL_HANDOFF_SIGNAL_THRESHOLD` | `goal.handoff_signal_threshold_tokens` | tenant | int | `200000` | 1 ~ 1e+07 | 触发 handoff 信号的上下文 token 阈值（tokens） |
| `LLM_GATEWAY_GOAL_MAX_AUTO_CONTINUE` | `goal.max_auto_continue_count` | tenant | int | `3` | 1 ~ 50 | 最大自动继续次数 |
| `LLM_GATEWAY_GOAL_MAX_FOLLOW_UPS_PER_SESSION` | `goal.max_follow_ups_per_session` | tenant | int | `50` | 1 ~ 500 | 单会话续跑总次数上限 |
| `LLM_GATEWAY_GOAL_MAX_FOLLOW_UP_DEPTH` | `goal.max_follow_up_depth` | tenant | int | `15` | 1 ~ 50 | 续跑递归最大深度（层） |
| `LLM_GATEWAY_GOAL_MAX_MODEL_SWITCH` | `goal.max_model_switch_count` | tenant | int | `3` | 0 ~ 20 | 最大模型切换次数 |
| `LLM_GATEWAY_GOAL_MAX_RETRY` | `goal.max_retry_count` | tenant | int | `3` | 1 ~ 10 | 最大重试次数 |
| `LLM_GATEWAY_GOAL_MODEL_SWITCH_ON_LOOP` | `goal.model_switch_on_loop` | tenant | bool | `true` |  | 死循环时切换模型 |
| `LLM_GATEWAY_GOAL_MONTHLY_TOKEN_LIMIT` | `goal.monthly_token_limit` | tenant | int | `500000` | 0 ~ 1e+08 | 月度token限额（tokens） |
| `LLM_GATEWAY_GOAL_REPEAT_DETECTION` | `goal.repeat_detection_enabled` | tenant | bool | `true` |  | 启用重复响应检测 |
| `LLM_GATEWAY_GOAL_REPEAT_THRESHOLD` | `goal.repeat_threshold` | tenant | int | `3` | 2 ~ 20 | 重复响应判定阈值（次） |
| `LLM_GATEWAY_GOAL_RETRIABLE_ERRORS` | `goal.retriable_errors` | tenant | string | `5xx,timeout,no_candidates,rate_limit,overloaded` |  | 可重试的错误类型 |
| `LLM_GATEWAY_GOAL_RETRY_DELAY_SECONDS` | `goal.retry_delay_seconds` | tenant | int | `20` | 5 ~ 60 | 重试延时（秒） |
| `LLM_GATEWAY_GOAL_RETRY_ON_ERROR` | `goal.retry_on_error` | tenant | bool | `false` |  | LLM错误时自动重试 |
| `LLM_GATEWAY_GOAL_RETRY_TOTAL_TIMEOUT` | `goal.retry_total_timeout_seconds` | tenant | int | `50` | 10 ~ 300 | 重试总超时（秒） |
| `LLM_GATEWAY_GOAL_SESSION_TOKEN_BUDGET` | `goal.session_token_budget` | tenant | int | `30000` | 0 ~ 1e+07 | 单次任务token预算（tokens） |
| `LLM_GATEWAY_GOAL_USE_AUTOROUTE_AUDIT` | `goal.use_autoroute_for_audit` | tenant | bool | `true` |  | 审计时使用自动路由 |
| `LLM_GATEWAY_GOAL_USE_AUTOROUTE_INTENT` | `goal.use_autoroute_for_intent` | tenant | bool | `true` |  | 意图检测使用自动路由 |
| `LLM_GATEWAY_HANDOFF_ABSOLUTE_THRESHOLD` | `handoff.absolute_threshold` | tenant | int | `300000` | 10000 ~ 2e+06 | Token 绝对阈值（tokens） |
| `LLM_GATEWAY_HANDOFF_CLIENT_MODE` | `handoff.client_mode` | tenant | enum | `transparent` | transparent / explicit | 客户端交接模式 |
| `LLM_GATEWAY_HANDOFF_CONTINUE_HINT_TPL` | `handoff.continue_hint_tpl` | tenant | string | `` |  | 新会话引导模板 |
| `LLM_GATEWAY_HANDOFF_COOLDOWN_SECONDS` | `handoff.cooldown_seconds` | tenant | int | `60` | 0 ~ 3600 | 交接冷却时间（秒） |
| `LLM_GATEWAY_HANDOFF_ENABLED` | `handoff.enabled` | tenant | bool | `false` |  | 启用会话交接 |
| `LLM_GATEWAY_HANDOFF_IDLE_MINUTES` | `handoff.idle_minutes` | tenant | int | `0` | 0 ~ 1440 | 会话静默时长阈值（分钟） |
| `LLM_GATEWAY_HANDOFF_MAX_PER_SESSION` | `handoff.max_per_session` | tenant | int | `5` | 1 ~ 50 | 单会话最大交接次数 |
| `LLM_GATEWAY_HANDOFF_MESSAGE_THRESHOLD` | `handoff.message_threshold` | tenant | int | `0` | 0 ~ 1000 | 消息数量阈值（条） |
| `LLM_GATEWAY_HANDOFF_MIN_MESSAGES` | `handoff.min_messages` | tenant | int | `10` | 2 ~ 1000 | 最小消息数门槛（条） |
| `LLM_GATEWAY_HANDOFF_NOTIFY_LEVEL` | `handoff.notify_level` | tenant | enum | `warn` | none / info / warn | 通知级别 |
| `LLM_GATEWAY_HANDOFF_NOTIFY_WEBHOOK` | `handoff.notify_webhook` | tenant | url | `` |  | 通知 Webhook URL |
| `LLM_GATEWAY_HANDOFF_PERCENTAGE_THRESHOLD` | `handoff.percentage_threshold` | tenant | float | `0.8` | 0.5 ~ 0.95 | Token 百分比阈值（%） |
| `LLM_GATEWAY_HANDOFF_RETRY_ON_FAILURE` | `handoff.retry_on_failure` | tenant | int | `1` | 0 ~ 3 | 摘要失败重试次数 |
| `LLM_GATEWAY_HANDOFF_SKILL_NAME` | `handoff.skill_name` | tenant | string | `handoff` |  | Handoff Skill 名称 |
| `LLM_GATEWAY_HANDOFF_SUMMARY_ENGINE` | `handoff.summary_engine` | tenant | enum | `llm` | llm / rule / hybrid | 摘要生成引擎 |
| `LLM_GATEWAY_HANDOFF_SUMMARY_EXTRACT_FACTS` | `handoff.summary_extract_facts` | tenant | bool | `true` |  | 关键事实抽取 |
| `LLM_GATEWAY_HANDOFF_SUMMARY_KEEP_RECENT_N` | `handoff.summary_keep_recent_n` | tenant | int | `4` | 0 ~ 50 | 保留最近 N 条消息 |
| `LLM_GATEWAY_HANDOFF_SUMMARY_MAX_TOKENS` | `handoff.summary_max_tokens` | tenant | int | `2000` | 200 ~ 8000 | 最大摘要 token 数（tokens） |
| `LLM_GATEWAY_HANDOFF_SUMMARY_MODEL` | `handoff.summary_model` | tenant | string | `` |  | 摘要生成模型 |
| `LLM_GATEWAY_HANDOFF_SUMMARY_PROMPT_TPL` | `handoff.summary_prompt_tpl` | tenant | string | `` |  | 摘要 Prompt 模板 |
| `LLM_GATEWAY_HANDOFF_TRIGGER_MODE` | `handoff.trigger_mode` | tenant | enum | `auto` | auto / manual / hybrid | Handoff 触发模式 |
| `LLM_GATEWAY_LLMGW_CONTINUE_KEYWORDS` | `llmgw_continue_keywords` | platform | string | `["继续", "continue", "go", "come on", "请继续", "接着", "keep going", "继续回答", "接着说", "go on", "続けて", "続けてください", "このまま続けて", "続きを"]` |  | 流式“继续”判定词典（个词） |
| `LLM_GATEWAY_LLMGW_RETRY_KEYWORDS` | `llmgw_retry_keywords` | platform | string | `["重试", "retry", "请重试", "再试一次", "try again", "重新回答", "再来", "再試行", "もう一度", "やり直してください", "retry please"]` |  | 流式“重试”判定词典（个词） |
| `LLM_GATEWAY_MAAS_RATE_PERIODS` | `maas.rate_periods` | platform | string | `{"enabled": false, "timezone": "Asia/Shanghai", "periods": [{"name": "peak_am", "start": "08:00", "end": "12:00", "multiplier": 3.0}, {"name": "peak_pm", "start": "17:00", "end": "21:00", "multiplier": 3.0}, {"name": "offpeak_night", "start": "23:00", "end": "07:00", "multiplier": 0.7}]}` |  | 峰谷计费时段表 |
| `LLM_GATEWAY_MAX_PROMPT_TOKENS` | `gateway.max_prompt_tokens` | platform | int | `2097152` | 0 ~ 2.097152e+06 | 入口 prompt 接收上限（估算 tokens） |
| `LLM_GATEWAY_PROBE_STATE_AGING_HOURS` | `probe.state_aging_hours` | platform | int | `2` | 1 ~ 24 | 健康证据老化窗（小时） |
| `LLM_GATEWAY_PROVIDER_PROFILE_AGGREGATION_INTERVAL` | `provider_profile.aggregation_interval` | platform | int | `86400` | 3600 ~ 259200 | 每日聚合间隔（秒），默认24小时 |
| `LLM_GATEWAY_PROVIDER_PROFILE_CLEANUP_INTERVAL` | `provider_profile.cleanup_interval` | platform | int | `604800` | 86400 ~ 2.592e+06 | 历史数据清理间隔（秒），默认7天 |
| `LLM_GATEWAY_PROVIDER_PROFILE_COLLECTION_INTERVAL` | `provider_profile.collection_interval` | platform | int | `7200` | 300 ~ 86400 | 指标采集间隔（秒），默认2小时 |
| `LLM_GATEWAY_PROVIDER_PROFILE_ENABLED` | `provider_profile.enabled` | platform | bool | `false` |  | 启用供应商画像系统（7维度供应商质量评分） |
| `LLM_GATEWAY_PROXY_DEFAULT_BANNED_REGIONS` | `proxy.default_banned_regions` | platform | string | `HK` |  | 代理出口平台级默认禁用地区（逗号分隔地区码）（csv） |
| `LLM_GATEWAY_ROUTING_NODE_DISABLED_COOLDOWN_SECONDS` | `routing.node_disabled_cooldown_seconds` | platform | int | `300` | 30 ~ 7200 | 节点 Disabled 冷却时长（秒） |
| `LLM_GATEWAY_ROUTING_NODE_FAIL_STREAK_LIMIT` | `routing.node_fail_streak_limit` | platform | int | `3` | 1 ~ 10 | 节点连续失败熔断阈值（次） |
| `LLM_GATEWAY_ROUTING_STICKY_FAILURE_THRESHOLD` | `routing.sticky_failure_threshold` | platform | int | `2` | 1 ~ 10 | Sticky 连续失败踢出阈值（次） |
| `LLM_GATEWAY_SA_CLUSTER_MODE` | `session_analytics.cluster_mode` | tenant | enum | `hybrid` | off / rule / vector / hybrid | 相似会话聚类模式 |
| `LLM_GATEWAY_SA_CLUSTER_SCHEDULE` | `session_analytics.cluster_schedule` | tenant | string | `1h` |  | 聚类运行周期 |
| `LLM_GATEWAY_SA_MODEL_CLUSTER_LABEL` | `session_analytics.model.cluster_label` | tenant | string | `auto` |  | 聚类标签模型 |
| `LLM_GATEWAY_SA_MODEL_EMBEDDING` | `session_analytics.model.embedding` | tenant | string | `auto` |  | 向量化模型 |
| `LLM_GATEWAY_SA_MODEL_REQ_SUMMARY` | `session_analytics.model.request_summary` | tenant | string | `auto` |  | 逐步摘要模型 |
| `LLM_GATEWAY_SA_MODEL_SUMMARY` | `session_analytics.model.summary` | tenant | string | `auto` |  | 会话总结模型 |
| `LLM_GATEWAY_SA_MODEL_TAGS` | `session_analytics.model.tags` | tenant | string | `auto` |  | 标签生成模型 |
| `LLM_GATEWAY_SA_MODEL_TITLE` | `session_analytics.model.title` | tenant | string | `auto` |  | 标题生成模型 |
| `LLM_GATEWAY_SA_OPT_ENABLED` | `session_analytics.optimization_enabled` | tenant | bool | `true` |  | 启用优化建议 |
| `LLM_GATEWAY_SA_REQ_SUMMARY_MODE` | `session_analytics.request_summary_mode` | tenant | enum | `rule` | rule / llm / hybrid | 逐步摘要模式 |
| `LLM_GATEWAY_SA_SUMMARY_STRATEGY` | `session_analytics.summary_strategy` | tenant | enum | `rolling` | full / rolling / map_reduce | 会话总结策略 |
| `LLM_GATEWAY_SA_TITLE_ON_FIRST` | `session_analytics.title_on_first_request` | tenant | bool | `true` |  | 首请求即生成标题 |
| `LLM_GATEWAY_SECURITY_SENSITIVE_BLOCK_SCORE` | `security.sensitive_block_score` | platform | float | `0.6` | 0.1 ~ 1 | 敏感词高危阻断阈值（分） |
| `LLM_GATEWAY_SECURITY_SENSITIVE_WARN_SCORE` | `security.sensitive_warn_score` | platform | float | `0.3` | 0.05 ~ 1 | 敏感词中危告警阈值（分） |
| `LLM_GATEWAY_SESSIONS_SUMMARY_PER_TURN_DIGEST` | `sessions_summary_per_turn_digest` | platform | bool | `false` |  | 会话摘要改用逐轮人读摘要（user 提问 + assistant 回复要点）作为输入，默认关闭（开关） |
| `LLM_GATEWAY_SESSIONS_V2_COMPRESSION_READ` | `sessions_v2_compression_read` | platform | bool | `true` |  | Read session state (compression + summaries) from V2 (session_bodies) instead of V1 (request_logs). Default on; set false to revert to V1. |
| `LLM_GATEWAY_SESSION_ANALYTICS_ENABLED` | `session_analytics.enabled` | platform | bool | `false` |  | 启用会话全景分析 |
| `LLM_GATEWAY_SESSION_SERVICE_JWT_ENABLED` | `session_service_auth.enabled` | platform | bool | `false` |  | 启用会话服务 JWT |
