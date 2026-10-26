package settings

// SessionsV2Specs contains Sessions V2 feature flag configuration.
//
// 会话优化V2灰度发布的Feature Flag控制配置。
// 用于控制新表体系(sessions/session_turns/session_bodies)的流量路由。
//
// Created: 2026-07-17, Migration 430
func SessionsV2Specs() []*Spec {
	return []*Spec{
		{
			Key:             "sessions_v2.enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategorySession,
			Default:         false,
			Description:     "Sessions V2 总开关",
			DescriptionLong: "启用会话存储V2架构。★ 2026-10-24：v1 `_request_logs` 族已随迁移 890 物理删除 ⇒ 「关闭时完全使用 V1」这条路已不存在；会话族是唯一事实源，本键与其余 sessions_v2.* 开关只影响会话族内部的读路由。",
			Unit:            "",
			DangerLevel:     Dangerous,
			HotReload:       true,
			Observability:   "/api/admin/sessions/v2/status",
		},
		{
			Key:             "sessions_v2.shadow_write",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategorySession,
			Default:         false,
			Description:     "启用副写模式（已废弃，仅剩会话族写腿）",
			DescriptionLong: "★ 已废弃（2026-10-25 12h 审计轮订正）：原文写「本键当前只余『读路由』这一半的语义」⇒ ★★ **该句不成立**：本键是**写**开关（cmd/gateway/main_v2_pipeline.go、domains/session/v2/pipeline_hook.go、internal/sessionv2mirror/{hook,backlog}.go 都读它决定要不要写会话族），**不是读路由开关**。★ 真实语义：v1 `_request_logs` 族已随迁移 890 物理删除 ⇒ 「同时写 _request_logs」那一半**没有落库对象** ⇒ 本键现在只开关**会话族的写腿**（V2=会话族），改本键仍会改变写入行为（不是无效键）。★ 生效前提：sessions_v2.enabled=true。",
			Unit:            "",
			DangerLevel:     Warning,
			HotReload:       true,
			Observability:   "/api/admin/sessions/v2/shadow_metrics",
		},
		{
			Key:             "sessions_v2.rollout_percent",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategorySession,
			Default:         0,
			Description:     "灰度百分比",
			DescriptionLong: "V2表的流量灰度百分比(0-100)。0表示不路由到V2，100表示全部路由到V2。基于session_id哈希决定路由。",
			Unit:            "%",
			DangerLevel:     Dangerous,
			HotReload:       true,
			Observability:   "/api/admin/sessions/v2/rollout_stats",
			Min:             floatPtr(0),
			Max:             floatPtr(100),
		},
		{
			Key:             "sessions_v2.l3_read",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategorySession,
			Default:         false,
			Description:     "已废弃: 启用从V2表读取(L3缓存)",
			DescriptionLong: "★ 已废弃。2026-10-25 12h 审计轮实测：本键**没有任何生产读者**（全仓字面量口径 + const 口径都为零；连宽松的 token 扫描也只命中本 spec 自身）⇒ ★★ 运营在设置页改它**不产生任何效果**。★ 真正的 L3 读路由由会话族内部实现决定，不由本键控制。",
			Unit:            "",
			DangerLevel:     Dangerous,
			HotReload:       true,
			Observability:   "/api/admin/sessions/v2/l3_hit_rate",
		},
		{
			Key:             "sessions_v2.dual_read",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategorySession,
			Default:         false,
			Description:     "已废弃: 双读对账模式",
			DescriptionLong: "★ 已废弃。2026-10-25 12h 审计轮订正：原文写「差异对比现在只反映会话族内部的两条读路径」⇒ ★★ **该句与仓内门直接矛盾**：门 `cmd/gateway/dual_read_validator_test.go` 的 `TestDualReadValidator_RetiredFamilyIssuesNoQuery` 断言**对账路径一条查询都不发、且如实报「未测量」** ⇒ ★★ 本键**没有任何生产读者**（全仓字面量扫描：字面量口径 + const 口径都为零），运营改它**不产生任何效果**。★ 退役原因：v1 `_request_logs` 族已随迁移 890 物理删除 ⇒ v1↔会话族 的对账在语义上没有对象了。★ 活的对账能力已不存在，请勿依赖 /api/admin/sessions/v2/dual_read_diff。",
			Unit:            "",
			DangerLevel:     Warning,
			HotReload:       true,
			Observability:   "/api/admin/sessions/v2/dual_read_diff",
		},
		{
			Key:             "sessions_v2.primary_read",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategorySession,
			Default:         false,
			Description:     "已废弃: V2作为主读取源",
			DescriptionLong: "★ 已废弃。2026-10-25 12h 审计轮订正：原文写「降级路径若存在，只能是会话族内部的备用读源」⇒ ★★ **该句掩盖了真相**：本键**没有任何生产读者**（字面量口径 + const 口径都为零）⇒ ★★ 运营改它**不产生任何效果**（不是「决定降级路径」，而是「什么都不决定」）。★ 退役原因：v1 `_request_logs` 族已随迁移 890 物理删除 ⇒ 没有可降级的 V1 备份，且**没有替代实现接管本键**。",
			Unit:            "",
			DangerLevel:     Breaking,
			HotReload:       true,
			Observability:   "/api/admin/sessions/v2/primary_read_stats",
		},
		{
			Key:      "sessions_v2.write_timeout_ms",
			Type:     TypeInt,
			Scope:    ScopePlatform,
			Category: CategorySession,
			// ★ 2026-10-20：500 → 2000。**这不是调参偏好，是修一个被静默撤销的修复。**
			//
			// internal/sessionv2mirror/hook.go 的注释写着「raise the default budget
			// to 2000ms」——它把常量 defaultShadowWriteTimeoutMs 提到了 2000。
			// 但 `settings.GetPlatformInt(key, fallback)` 的解析顺序是
			// **DB → env → spec.Default → 代码里的 fallback**：
			// spec.Default 命中时，代码里那个 2000 永远用不上。
			// 而本条的 spec.Default 一直是 500 ⇒ **那次「提到 2000」的修复
			// 从来没有生效过**，实际预算一直是 500ms。
			//
			// 代价（本地实测，PG 修好 shared_buffers 之后仍然复现）：
			// 一次 shadow write 要串行做 session_dim 维度写 + session_turns
			// + session_turn_details + session_bodies + session_summaries，
			// 500ms 撑不住 ⇒ 细节层写超时 ⇒
			//   ① 正文（session_bodies）**一行都不落**（jsonb_typeof 读数是 null）；
			//   ② 整笔写失败 ⇒ 走 outbox 异步重放 ⇒ **重放顺序 ≠ 请求顺序**
			//      ⇒ turn_no 与时间序错位（实测：第 1 轮拿到 turn_no=3）；
			//   ③ trace 找不到父行 ⇒ trace_events 丢失。
			// ⇒ 「请求返回 200 但数据不完整」这一类**静默**失效，源头就在这个 500。
			//
			// 判据：settings/session_write_timeout_default_test.go 钉住
			// 「spec.Default == internal/sessionv2mirror 的常量」，两边不许再各写各的。
			Default:     2000,
			Description: "V2写入超时时间",
			DescriptionLong: "写入V2表的超时时间(毫秒)。超时后不阻塞主流程，异步重试。" +
				"⚠️ 该默认值必须与 internal/sessionv2mirror 的 defaultShadowWriteTimeoutMs 一致：" +
				"settings 的解析顺序是 DB → env → spec.Default → 代码 fallback，" +
				"spec.Default 命中时代码里的常量不生效，两边不一致就等于悄悄改预算。",
			Unit:        "毫秒",
			DangerLevel: Safe,
			HotReload:   true,
			Min:         floatPtr(100),
			Max:         floatPtr(5000),
		},
		{
			Key:             "sessions_v2.read_timeout_ms",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategorySession,
			Default:         300,
			Description:     "已废弃: V2读取超时时间",
			DescriptionLong: "★ 已废弃。2026-10-25 12h 审计轮订正：原文写「超时后的行为由会话族内部的读路由决定」⇒ ★★ **该句掩盖了真相**：本键**没有任何生产读者**（全仓字面量口径 + const 口径都为零）⇒ ★★ 运营改它**不产生任何效果**（不是「决定读路由」，而是「什么都不决定」）。★ 退役原因：原语义里的「超时后降级到 _request_logs」那一腿已随迁移 890 物理删除，而**降级目标与超时值都没有替代实现**。",
			Unit:            "毫秒",
			DangerLevel:     Safe,
			HotReload:       true,
			Min:             floatPtr(50),
			Max:             floatPtr(3000),
		},
		{
			// R29 审计（2026-09-15）：mirror shadow-write 失败登记（712
			// session_mirror_outbox）与重放器的 kill switch。未注册时
			// getPlatformBool 对 Spec==nil 直接返回 fallback 不读
			// settings_kv——运维 PUT 无效，应急热关能力落空。
			Key:             "sessions_v2.mirror_outbox",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategorySession,
			Default:         true,
			Description:     "Mirror 失败登记开关",
			DescriptionLong: "shadow-write 失败的会话轮次持久登记到 session_mirror_outbox（迁移 712）。关闭后失败行只进进程内 backlog（重启即丢，GAP-2 修复前行为）。",
			Unit:            "",
			DangerLevel:     Warning,
			HotReload:       true,
		},
		{
			Key:             "sessions_v2.mirror_outbox_replay",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategorySession,
			Default:         true,
			Description:     "Mirror 失败重放开关",
			DescriptionLong: "MirrorOutboxReaper 周期重放 session_mirror_outbox 欠账行到 V2 表。关闭后登记行滞留（pending gauge 持续增长），用于应急止血。",
			Unit:            "",
			DangerLevel:     Warning,
			HotReload:       true,
		},
		{
			Key:             "sessions_v2.mirror_outbox_max_attempts",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategorySession,
			Min:             floatPtr(5),
			Max:             floatPtr(30),
			Default:         10,
			Description:     "Mirror 失败重放次数上限",
			DescriptionLong: "session_mirror_outbox 欠账行重放多少次后转入 dead-letter（重放次数 >= 本值即 markDead）。MirrorOutboxReaper.requeue 每次调用都经 currentMaxAtts 重新读取本键，因此改动**即时生效、无需重启**。范围 5-30：下限防止一次配置失误把每一行都在首次失败时就判死（欠账被静默吞掉），上限防止把重试路径饿死。越界值由 Go 侧 currentMaxAtts 钳制到 [5,30]，故登记 Min/Max 与代码钳制必须同时存在——只写消费点不登记本键会让 GetPlatformInt 恒返回 fallback，热重载形同虚设（审计 §23 F-15）。",
			Unit:            "次",
			DangerLevel:     Warning,
			HotReload:       true,
		},
		{
			Key:             "sessions_v2.compression_enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategorySession,
			Default:         true,
			Description:     "已废弃: 启用V2压缩检测",
			DescriptionLong: "★ 已废弃。2026-10-25 12h 审计轮实测：本键**没有任何生产读者**（字面量口径 + const 口径都为零）⇒ ★★ 运营改它**不产生任何效果**。★ submit_mode 的实际写入口径由会话族写入器决定，不由本键控制。",
			Unit:            "",
			DangerLevel:     Safe,
			HotReload:       true,
		},
		{
			Key:         "sessions_v2.turn_logs_retention_hours",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategorySession,
			Default:     24,
			Description: "环节日志保留时间（已废弃，无效果）",
			// R73 审计 B-F6：该键自登记起无任何 Get 消费者——turn_logs 的
			// 活 TTL 键是 lifecycle.session_turn_logs_ttl_hours（写入端
			// turn_logs_writer 与清扫端 partition_manager 双端消费）。
			// 原 DescriptionLong「超时后自动清理」为假（F-15 同类：登记
			// 未消费第 5 例）。改为此废弃声明防误导运维；键本身保留以
			// 兼容已存过该键的 settings 存储，下一清理轮连同
			// spec_sessions_v2_test.go 的键清单一起删除。
			DescriptionLong: "已废弃别名：真实生效键为 lifecycle.session_turn_logs_ttl_hours（Min1/Max168/Default24）。本键修改无任何效果。",
			Unit:            "小时",
			DangerLevel:     Safe,
			HotReload:       true,
			Min:             floatPtr(1),
			Max:             floatPtr(168), // 7天
		},
		{
			Key:             "sessions_v2.turns_list_routing",
			Type:            TypeEnum,
			Scope:           ScopePlatform,
			Category:        CategorySession,
			Default:         "tree",
			Options:         []string{"tree", "dual", "v2"},
			Description:     "会话轮次列表路由模式",
			DescriptionLong: "控制 /api/admin/sessions/{id}/turns 使用 tree、dual 对账或统一 V2 metadata 契约。",
			DangerLevel:     Warning,
			HotReload:       true,
		},
		{
			// CO-5 (docs/修订0811/19 §3): sessions_v2.enabled 时
			// 兼容保留的旧开关。新请求始终保存完整请求/响应体；历史 false
			// 值不再启用摘要信封，避免大请求的唯一审计副本被截断。
			Key:             "sessions_v2.request_bodies_full",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategorySession,
			Default:         true,
			Description:     "已废弃别名: sessions_v2 启用时仍全量落库请求/响应体",
			DescriptionLong: "★ 已废弃别名。2026-10-25 12h 审计轮实测：本键**没有任何生产读者**（字面量口径 + const 口径都为零）⇒ ★★ 本键修改无任何效果。★ 真实生效键为 `storage.session_turns_bodies_enabled` 与 `storage.session_final_full_enabled`（见 domains/session/v2/session_writer_v2.go）。★ 2026-10-24 已补记：原文写的 _request_logs_bodies 已随迁移 890 退役。",
			Unit:            "",
			DangerLevel:     Warning,
			HotReload:       true,
		},
	}
}
