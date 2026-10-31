package admin

import (
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// 存储优化方案 v2 S3 波1（plan §4-S3）：admin 日志列表/详情读端从
// request_logs_with_current_month 切到 session_turns 家族原生读。
//
// 与 S1b 写侧灰度同构的读侧灰度开关：默认关闭走视图（v2 视图体本身已含
// session 家族拼装 + v1 冻结分支 × 反连接）；打开后日志查询的 FROM 直接换成
// 710 的 session 分支投影（hot ∪ parent，复用 db.SessionFamilyTurnsSourceSQL
// 的同一份 113 列契约），不再触碰 _request_logs / v1 分支 lateral / 反连接。
// 列形状与视图逐列一致（投影表达式即视图 session 分支本体），外层
// requestLogsListCols / requestLogsJoins / 行扫描零改动，API 契约保持。
//
// 原生模式的收益（wave-1 验证目标）：消除视图体里 v1 分支的 UNION ALL 扫描、
// 反连接 NOT EXISTS 探测与 lateral 形态分支——ts 窗口谓词经 UNION ALL 下推
// 直达 session_turns(_hot) 索引。
//
// 回切 = 关开关（HotReload，无 DDL）。
//
// 已知边界（登记为波1后续项，样板不覆盖）：
//   - 正文仍走 _request_logs_bodies（fetchRequestBodies/fetchRequestOutboundBody）；
//     S4 停写后须改读 session_turns.request_delta/response_delta 与
//     session_bodies final_full（新行），旧行回退 bodies 表。
//   - 原生模式只输出 session 家族已有行。
//
// ⚠️ 2026-09-30 真库核对**两次**修正了这段开关的可用前提。
//
// 第一版（已作废，勿再引用）：曾写「sessions_v2.enabled=true、
// shadow_write=true 前提下仍有 20,660 个会话 / 38,878 行只存在于
// _request_logs，且漏写持续发生（09-24 单日 8197 行）」。
// 该结论经复核**是错的**：38,229 行里绝大多数是 hook 按设计不镜像的
// 内部回环与非终态占位行（unexplained = 0），而「持续发生」是 35 天
// 滚动窗口的采数假象——按天重算后当前进程为零漏写。真正的
// genuine_loss 只有 1,459 行，已由 scripts/audit/mirror_outbox_backfill.sql
// 全量补写，复测归零。
//
// 订正后的结论（本段是当前有效的版本）：
//
//  1. 本开关保持默认 false —— 但理由**不是**「镜像不完整」，而是
//     **原生源不是全量日志视图的等价替代**。真库实测：视图里
//     2,321,464 个 request_id 有 641,452 个（27.6%）在原生源查不到
//     （无会话头流量：探针/自检按设计排除、in_progress 占位、标题/摘要
//     生成器回环）。日志列表/详情是通用流量读路径，不带会话谓词，
//     切过去会静默少 27.6% 的行。
//  2. 任何「改读 session 族原生源」的迁移，判据是**谓词形态**而非
//     「镜像是否补齐」：带 gw_session_id 的会话内读可以迁（已迁，见
//     审计 §5.5）；按 request_id / client_request_id / parent_request_id
//     反查或全量时间窗聚合的**禁止**迁。该分类已固化为守卫
//     admin/session_view_dependency_risk_test.go。
//  3. S4 停写（storage.request_logs_write_enabled=false）的活跃漏写
//     阻断已解除，但灰度期间仍须保留 734 视图的 v1 冻结分支——它是
//     当前月热数据与在线列表 last_request_id 的唯一读路径。
//
// ★★★★★ R181 轮**再订正**（★★ 本段第 1 条的前提已被推翻，★★ 别再引用它）：★★★★
//
//	★ 那条「原生源会少 27.6% 的行」的理由，**前提是「v1 还在」**。
//	  迁移 890 把 canonical 视图的第三臂（冻结 v1 体）去掉之后，视图
//	  **本身就是**会话族两臂体（session_turns_hot ∪ session_turns + 734
//	  details 层）⇒ ⇒ **视图与原生源返回同样的行** ⇒ ⇒ 「视图 vs 原生源」
//	  这个区分**已经没有对象**。
//	★★ 这是**实测**，不是推断：★ 本地真库逐行对拍 ⇒ view 2,810 行 /
//	  native 2,810 行 / native-only = 0 / view-only = 0（2026-10-29）。
//	  ★★ 252 生产库同口径对拍读数见审计档 `docs/audit/2026-10-29-R181-…`。
//	★★ 而且它是**结构性的**：两者的 FROM/JOIN 逐字相同，唯一差别是视图那版
//	  带 `::text` 冗余转型（varchar→text 是恒等）⇒ ⇒ 行集必然相同，
//	  **与数据量无关** ⇒ ⇒ 不用再担心「小库看不出来」。
//	★★ 守卫 `admin/session_view_dependency_risk_test.go` 在 2026-10-20（迁移
//	  890 那轮）**已经把不变式**从「必须留在视图上」换成「读的源必须覆盖
//	  会话族的两条腿（hot ∪ 母表）」⇒ ⇒ 本轮**不是推翻守卫**，
//	  ★ 是**把守卫早已更新的结论落到列表路径上** ★
//	★★ 于是本开关现在的**真实作用范围**收窄为：★ **详情路径**的形态选择
//	  （详情按 request_id 反查 ⇒ 视图形态即够用 ⇒ 不需要裁剪 ⇒ 保持默认视图）；
//	  ★ **列表路径**走**带分区键下界的原生形态**（见
//	  `logsSourceFromSQLWindowed`）—— ★★ 因为 canonical 视图的 118 列投影
//	  被冻结（R37 定案「118 列同名同序不许改」）⇒ ⇒ 视图**不可能**暴露
//	  partition_date ⇒ ⇒ **分区裁剪在视图形态下不可达** ⇒ ⇒ 列表路径要
//	  拿到裁剪，就只能走原生形态 ★★
const nativeTurnsReadSetting = "storage.admin_logs_native_turns_read"

// logsSourceFromSQL returns the aliased FROM-source for the logs **detail**
// queries: view-backed (default) or native session-turn family (S3 波1 灰度).
// Both shapes expose the same 113-column contract under alias `rl`.
//
// ★★ R181 轮订正（本段上方那段「27.6% 缺口」的理由**已被推翻**）：★★
// ★ 那条理由的**前提是「v1 还在」**。迁移 890 把 canonical 视图的第三臂
//
//	（冻结 v1 体）去掉之后，视图**本身就是**会话族两臂体
//	（session_turns_hot ∪ session_turns + 734 details 层）⇒ ⇒ **视图与原生源
//	返回同样的行** ⇒ ⇒ 「视图 vs 原生源」这个区分**已经没有对象**。
//
// ★★ 这不是推断，是**实测**：★ 本地真库逐行对拍 ⇒ view 2,810 行 / native 2,810 行
//
//	/ native-only = 0 / view-only = 0；★ 252 生产库同口径对拍见审计档。
//
// ★★ 而且它是**结构性的**：两者的 FROM/JOIN 逐字相同，唯一差别是视图那版带
//
//	`::text` 冗余转型（varchar→text 是恒等）⇒ ⇒ 行集必然相同，**与数据量无关**。
//
// ★★ 同一结论已写进守卫 `admin/session_view_dependency_risk_test.go`
//
//	（2026-10-20 迁移 890 那轮就已把不变式从「必须留在视图上」换成
//	「读的源必须覆盖会话族的两条腿」）⇒ ⇒ 本轮不是推翻守卫，是**把守卫早
//	已更新的结论落到列表路径上** ★
//
// ★★ 另有一条**独立**理由让列表路径**必须**走原生形态：★★
//
//	canonical 视图的 **118 列投影被冻结**（R37 定案「118 列同名同序不许改」，
//	见 admin/canonical_read_points_registry_test.go:228）⇒ ⇒ 视图**不可能**
//	暴露 partition_date ⇒ ⇒ **分区裁剪在视图形态下不可达** ⇒ ⇒ 列表路径要
//	拿到裁剪，就只能用**原生形态 + 带分区键谓词**（见 logsSourceFromSQLWindowed）。
func logsSourceFromSQL() string {
	if settings.GetPlatformBool(nativeTurnsReadSetting, false) {
		return db.SessionFamilyTurnsSourceSQL() + " rl"
	}
	return "request_logs_with_current_month rl"
}

// logsSourceFromSQLWindowed returns the **带分区键下界**的 FROM 源（R181 轮新增）。
//
// ★ 为什么列表路径必须用它（★ 实测坐实，不是推断）：★★
// `public.session_turns` 按 **partition_date** RANGE 分区，而读方只按 **ts**
// 过滤 ⇒ ⇒ PG 的分区裁剪**只认分区键谓词** ⇒ ⇒ 一个 ts 窗口会命中**全部**月分区。
// ★★ 本地真库 A/B（session_turns 有 5 个月分区 + default）：
//
//	只按 ts            ⇒ Append → 5 个分区全扫（2026_10 走 Seq Scan）
//	ts + partition_date ⇒ 只剩 session_turns_2026_10
//
// ★★ 生产侧同形态已被台账记下：252 的 **EXPLAIN 计划形态**观测台账
// （scripts/audit/asset/query-plan-observations.tsv）里，/api/logs 的列表语句
// （stmt-1d7be58136）在**1 秒窗口**上 Seq Scan 了 **4 个整月分区**。
//
// ★★★ R182 轮订正（★★ 事实性错误 ★★★）：★★ 这份台账**不是**
// ★ `pg_stat_statements` 的统计快照（★ 生成器是 cmd/tools/query_plan_form，
// ★ 走 EXPLAIN、不带 ANALYZE）⇒ ⇒ 它记的是**计划形态**，不是执行期统计。
//
// ★ 为什么不走视图形态：★ 视图的 118 列投影被冻结（R37）⇒ ⇒ 视图无法暴露
//
//	partition_date ⇒ ⇒ 视图形态**拿不到裁剪** ⇒ ⇒ 列表路径只能走原生形态。
//	★★ 走原生形态**不丢行**：★ 890 之后视图本身就是两臂体 ⇒ ⇒ 行集相同
//	（见 logsSourceFromSQL 上面的 R181 订正）★★
//
// ★ pdFrom / pdTo 是**参数占位符**（形如 "$1" / "$2"）⇒ ⇒ 由调用方决定编号
//
//	⇒ ⇒ 本函数不感知参数位置；上界/下界的**取值**由
//	`sessionPartitionDateBounds`（admin/session_partition_bounds.go）折算。
func logsSourceFromSQLWindowed(pdFrom, pdTo string) string {
	return db.SessionFamilyTurnsSourceWindowedSQL(pdFrom, pdTo) + " rl"
}
