// Package bg — probe_model_scope.go
//
// 自动探测的**模型作用域门**：这道门回答「这个模型允许被自动探测吗」。
//
// #作用域语义（2026-11-26 探测工作模式修正轮）
//
// 四道判据，**按顺序**短路：
//
//	① 多模态门：图形类 / 视频类模型 —— 「多模态模型自动探测」关闭时（默认）
//	   一律不在自动作用域内。识别器见 probe_multimodal_model.go。
//	② 全量探测开关：「模型全量探测」打开时（默认关闭）跳过下面的收窄，
//	   「不知状态」的模型也自动探测。
//	③ 静态精选清单（运维策展的白名单）：featured 模型始终在作用域内。
//	④ 7 天业务用量集合：近 7 天被**客户端正常调用**过的模型在作用域内。
//
// ★ 「被正常调用」的两条口径：
//
//	· **探测流量不算使用** —— 统计源是会话族（session_turns ∪
//	  session_turns_hot），并用 probeTrafficExclusionPredicateSession 排除
//	  探针自身流量（origin_stage='business' AND task_type<>'probe_triggered'
//	  AND origin_actor 不在探针 actor 词表）。
//	· **7 天窗口** —— 名义 7 天；实现用分区键下界裁剪
//	  （db.SessionFamilyTurnsSourceLowerBoundSQL），把月分区表压到最近一周。
//
// # ★ 为什么是「缓存集合」而不是逐模型查询（2026-11-26 真库实测）
//
// 这道门每轮探测都会被调（每个候选对一次）。逐模型 EXISTS 的实测代价：
//
//	· 会话族逐模型 EXISTS（7 天窗口，过滤在计算列上）= **8.79 秒**/次
//	  （扫 297,685 行）—— ⇒ 不可接受。
//	· 改走基表 model 列走索引的单模型 EXISTS = **1043 ms**/次 ——仍太贵。
//	· ★ 缓存填充（一次 DISTINCT 取回全量模型清单）——★ **判据源决定成败**：
//	    · 走**全投影源**（118 列 + LEFT JOIN session_turn_details）=
//	      **34.33 秒**/次 ⇒ ⇒ 直接击穿 ModelTier 的 **10 秒**刷新超时
//	      ⇒ ⇒ 缓存**永远装不上** ⇒ ⇒ 门恒 fail-open ⇒ ⇒ 修正等于没生效。
//	    · 走**瘦源**（7 列、不连 details）= **0.75–0.91 秒**/次 —— 可接受，
//	      且摊薄到 10 分钟一次的刷新里 ⇒ ⇒ 门判定退化成**内存查表**（O(1)）。
//
// ⇒ 缓存挂在 ModelTier 的同一条刷新循环上（bg/model_tier.go 的 refresh）：
//   不新增 goroutine、不新增连接、同源同口径，且沿用 ModelTier 既有的
//   「任一段失败 ⇒ 保旧集，不做全量替换」纪律（R67-B）。
//
// ★ 为什么用会话族、**不用** request_metrics：生产实测 request_metrics 的
//  业务行在 11-21～11-25 **整段缺失**（逐日 11-14=1419 … 11-20=675、
//   11-21..11-25=0、11-26=614），而 session_turns 逐日连续 ⇒ ⇒ 用量权威
//   只能是会话族（request_metrics 版本虽只要 20ms，但那个数是假的）。
//
// # 失败开放（fail-open）
//
// 这道门的**目的是**收敛常态探测量（INV-3），不是阻断恢复路径 ⇒ DB 读不出
// 结论时按「在作用域内」放行 + 告警，而不是拒绝。原实现是 fail-**closed**
// （返回 err ⇒ 调用点当拒绝 ⇒ 一次读抖动就把恢复探测挡死），与门的意图
// 相反，2026-11-05 审计已改回 fail-open，本轮保持。
//
// # 恢复复检豁免（automaticProbeSourceBypassesModelScope）
//
// 上面两处 fail-open 注释已把「恢复探测不能被这道门废掉」写成设计意图，但
// 「模型冷了」这个**正常判定结果**同样会永久卡死恢复复检：失败对被摘出
// 路由 → 不再产生流量 → 滚出用量窗口 → automatic 探测持续 skip → 上游
// 恢复后状态永不更正（Catch-22）。因此两个**天然只承载恢复复检**的 source
// 绕过本门：
//
// - "periodic"：仅由队列泵提交，pumpDueStatesSQL 只挑带
//     nodeProbeErrorEvidenceSQL（错误证据）的到期行 —— 健康停放行（INV-1）
//     永远进不了泵，豁免不重开常态巡检；
//   - "credential_recovery"：凭据/绑定级恢复事件（availability/quota 恢复、
//     到期绑定复验）的验证探针 —— 其目标对象本就处于不可用状态。
//
// request_failure / selfcheck 依旧过门：它们要么由真实流量触发（模型必在
// 用量窗内），要么选点自带近窗流量条件。
//
// ★★ 2026-11-26 变更：**多模态门不在豁免范围内** —— 恢复复检同样是真实
// 的出图/出片请求（成本摆在那里），而 2026-11-26 那场 gpt-image 风暴的
// 触发源正是 periodic / credential_recovery 这两条恢复路径 ⇒ ⇒豁免若
// 覆盖多模态门，等于这次修正对风暴源头完全无效。代价（诚实登记）：图形/
// 视频类模型的凭据恢复状态不再被自动更正，需要时置
// probe.multimodal_auto_probe=true 或手工探测。
package bg

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// ErrProbeModelOutOfAutomaticScope marks automatic probes rejected because the
// model is neither featured nor recently used by real traffic.
var ErrProbeModelOutOfAutomaticScope = fmt.Errorf("automatic probe model out of scope")

// probeScopeUsageWindowHours is the nominal window of the "recently used by
// business traffic" set that gates automatic probe scope: 7 days.
//
// ★ 口径与实现必须一起读：窗口是**名义值**，实际可见范围受会话族的保留期
// 与分区裁剪约束（父表按月分区 ⇒7 天窗口只裁掉更早的月分区，热腿是
// 最近若干小时）。这比「名义 24h、实际只有 8h」的老口径**更宽**，且不再
// 依赖热腿保留期 ⇒ ⇒ 一个「7 天内有人用过但热腿已滚出」的模型仍能被
// 探到。
const probeScopeUsageWindowHours = 168

// probeScopeUsageWindowInterval is the SQL spelling of probeScopeUsageWindowHours.
const probeScopeUsageWindowInterval = "interval '168 hours'"

// automaticProbeSourceBypassesModelScope reports whether an automatic probe
// from the given source is a recovery re-verification that must not be
// blocked by the featured ∪ recent-usage scope gate. See the file header for
// the full rationale — the gate tightens steady-state probing (INV-3), not
// recovery; blocking a recovery re-check of an out-of-window failed pair
// would leave a recovered credential marked unavailable forever (the pair
// gets no traffic while unavailable, so it can never re-enter the usage
// window on its own).
//
// Keep this set closed: every entry must be a source whose rows provably
// carry failure evidence (or target already-unavailable objects), so the
// exemption can never re-open normal-state continuous probing (INV-1).
func automaticProbeSourceBypassesModelScope(source string) bool {
	switch source {
	case "periodic", "credential_recovery":
		return true
	}
	return false
}

// automaticProbeModelInScope reports whether an automatic probe may target
// rawModel. Manual tasks never call this.
//
// ★ 2026-11-05 审计修：失败**开放**（fail-open）—— DB 读错时按「在范围内」
// 放行，而不是拒绝。理由：这道门的**目的是**「别让恢复探测被卡住」
// （INV-3 收紧的是**常态**探测量，不是恢复路径）⇒ 若一次瞬时读抖动
// 就把恢复探测挡在门外，恢复探测反而被这道门废掉 ⇒ 与门的意图相反。
// ⇒ 因此读错时**放行 + 告警**，让调用方知道「本次判定不可信」；
// 不可信不等于「拒绝」。
//
// ★ 2026-11-26（探测工作模式修正轮）：判据源从「逐模型查库」换成
// 「缓存集合 + 内存查表」⇒ 门判定不再打库（逐模型 7 天窗口实测 8.79s/次）
// ⇒ 缓存不可用（尚未装载 / 刷新失败）时同样按 fail-open 放行 + 返回错误，
// 让调用点把「本次判定不可信」记进审计。
func (q *ProbeQueue) automaticProbeModelInScope(ctx context.Context, rawModel string) (bool, error) {
	if q == nil || q.db == nil {
		// 无 DB ⇒ 无法判定 ⇒ 按 fail-open 放行（仅记告警）。
		slog.Warn("probe model scope: database unavailable, treating model as in scope",
			"model", rawModel)
		return true, nil
	}
	model := strings.TrimSpace(rawModel)
	if model == "" {
		return false, nil
	}

	// ① 多模态门：图形类 / 视频类默认不参与自动探测（成本门）。
	//    ★ 这道门**不在**恢复豁免范围内（见文件头）。
	if !settings.ProbeMultimodalAutoProbe() && probeGraphicVideoModel(model) {
		return false, nil
	}

	// ② 全量探测开关：打开时「不知状态的模型也探测」。
	if settings.ProbeFullModelProbe() {
		return true, nil
	}

	// ③ 静态精选清单（运维策展的白名单）。
	if globalIsFeaturedModel(model, "") {
		return true, nil
	}

	// ④ 7 天业务用量集合（缓存）。
	used, available := globalIsRecentlyUsedModel(model)
	if available {
		return used, nil
	}

	// ★ fail-open：缓存不可用（ModelTier 未装载 / 刷新尚未成功过）⇒ 放行，
	//   并把错误原样返回给调用方，让它能落审计/告警。
	err := fmt.Errorf("recent-usage model set unavailable (model tier not loaded)")
	slog.Warn("probe model scope: recent-usage set unavailable, treating model as in scope",
		"model", model, "error", err)
	return true, err
}

// automaticProbeRecentUsageModelsSQL lists every model that real (non-probe)
// business traffic called inside the scope window.
//
// ★ 这条 SQL **只用于填充缓存**（ModelTier 的 refresh，每 10 分钟一次），
//   不是门判定路径 —— 门判定读的是内存集合。
//   逐模型 EXISTS 在同一窗口下实测 8.79s/次（会话族）/ 1043ms/次（基表索引），
//   而这条 DISTINCT 全量清单只要 0.57s ⇒ ⇒ 这就是「缓存集合」路线的依据。
//
// ★★★ 为什么必须走【瘦源】而不是全投影源（2026-11-26 真库实测，154 生产库）：
//
//	同一 7 天窗口、同一排除谓词：
//
//	  全投影源（118 列 + LEFT JOIN session_turn_details）= **34.33 秒**
//	  瘦源（db.SessionFamilyTurnsModelNamesSourceSQL，7 列、不连 details）
//	                                                = **0.57 秒**
//
//	⇒ 60 倍差距的成因**只有** details 左连接 ⇒ ⇒ 这条查询必须走瘦源，
//   否则它会直接击穿 ModelTier 的 10 秒刷新超时 ⇒ ⇒ 作用域门的缓存**永远
//   装不上** ⇒ ⇒ 门会一直 fail-open（等于这次修正没生效）。
//
// ★ 分区裁剪：pdFrom 用 (NOW() - interval '168 hours') AT TIME ZONE 'UTC'
//   —— 与 ts 谓词**逐字同源** ⇒ 由不变式
//   「partition_date == (ts AT TIME ZONE 'UTC')::date」可**证明**
//   ⇒ 下界是 ts 谓词的蕴含条件 ⇒ ⇒ **不丢任何一行**。
//
// ★ 探测流量不计：excl 是会话族那条探针排除谓词（origin_stage +
//   task_type + origin_actor 三臂），三列都在 turns 表上 ⇒ ⇒ 谓词在瘦源
//   上与在全投影源上**逐字等价**；actor 词表出自 probekind.SQLActorList()
//   （唯一出处，不手抄）。
//
// ★ 列口径：瘦源给三列模型名（outbound_model / canonical_model /
//   raw_model_name）—— 生产库抽样 5,000 行实测，t.raw_model_name 与
//   d.client_model 仅 1.9% 不同 ⇒ ⇒ raw_model_name 是 client_model 的
//   等价替身。★ 诚实登记：比全投影源**少一个**模型名列（client_model）。
func automaticProbeRecentUsageModelsSQL() string {
	excl := fmt.Sprintf(probeTrafficExclusionPredicateSession, "rl", "rl", "rl")
	pdFrom := "(NOW() - " + probeScopeUsageWindowInterval + ") AT TIME ZONE 'UTC'"
	return `
		SELECT DISTINCT rl.raw_model_name, rl.outbound_model, rl.canonical_model
		FROM ` + db.SessionFamilyTurnsModelNamesSourceSQL(pdFrom) + ` rl
		WHERE rl.ts >= now() - ` + probeScopeUsageWindowInterval + `
		  AND ` + excl
}
