package settings

// PerTurnDigestFlagKey is the platform settings key gating the per-turn
// digest summary input. Defined here (not in domains/sessionsummary) because
// that package imports settings for the runtime read — the constant must live
// dependency-free.
const PerTurnDigestFlagKey = "sessions_summary_per_turn_digest"

// SessionsSummaryPerTurnDigestPlatformSpecs — 会话摘要 per-turn digest 输入层
// 的平台级开关（24h 审计第二轮 Track B#4）。
//
// 背景：V1/V2 MessageSource 都把每轮坍缩为最后一条 request 消息（恒为
// user prompt），assistant 回复完全不参与，摘要质量有天花板。开启本开关后，
// 摘要 LLM 的输入改为逐轮人读摘要（user 提问 + assistant 回复要点），读
// session_turns.digest 持久化 envelope（写路径 sessiondigest.Build 落库、
// session_digest_backfill 回填存量）。
//
// 安全不变量：
//   - 默认 false：改变摘要输入即改变线上摘要输出，必须运维显式开启。
//   - 开关关闭时逐字节委托 V2 session_bodies 源（v2SessionBodiesSource），
//     与既有行为完全一致；每次摘要调用都重新读开关，支持热更新。
//   - 嵌套在 sessions_v2_compression_read（V2 读主开关）之下：digest 层读
//     session_turns（V2 表），V1 回退路径不受影响。
//
// 风险提示（DescriptionLong 已告知运维）：依赖 session_turns.digest 非空；
// 存量 NULL 行由回填 job 补齐，尚未回填到的轮次会被跳过（不出现在摘要
// 输入里）。
func SessionsSummaryPerTurnDigestPlatformSpecs() []*Spec {
	return []*Spec{
		{
			Key:             PerTurnDigestFlagKey,
			EnvName:         "LLM_GATEWAY_SESSIONS_SUMMARY_PER_TURN_DIGEST",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategorySession,
			Default:         false,
			Description:     "会话摘要改用逐轮人读摘要（user 提问 + assistant 回复要点）作为输入，默认关闭",
			DescriptionLong: "开启后（需 sessions_v2_compression_read 同时为开）会话总结的消息源从「每轮最后一条 user 提问」切换为「每轮 user 提问 + assistant 回复要点」，数据来自 session_turns.digest 持久化 envelope（session_digest_backfill 负责回填存量 NULL 行，未回填到的轮次会被跳过）。关闭时恢复为 V2 session_bodies 原行为。每次摘要调用实时读取，热更新生效。",
			Unit:            "开关",
			DangerLevel:     Warning,
			HotReload:       true,
		},
	}
}
