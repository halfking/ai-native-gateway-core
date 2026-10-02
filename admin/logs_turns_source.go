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
// 的同一份 113 列契约），不再触碰 request_logs / v1 分支 lateral / 反连接。
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
//   - 正文仍走 request_logs_bodies（fetchRequestBodies/fetchRequestOutboundBody）；
//     S4 停写后须改读 session_turns.request_delta/response_delta 与
//     session_bodies final_full（新行），旧行回退 bodies 表。
//   - 原生模式只输出 session 家族已有行。
//
// ⚠️ 2026-09-30 真库核对**两次**修正了这段开关的可用前提。
//
// 第一版（已作废，勿再引用）：曾写「sessions_v2.enabled=true、
// shadow_write=true 前提下仍有 20,660 个会话 / 38,878 行只存在于
// request_logs，且漏写持续发生（09-24 单日 8197 行）」。
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
const nativeTurnsReadSetting = "storage.admin_logs_native_turns_read"

// logsSourceFromSQL returns the aliased FROM-source for the logs list/detail
// queries: view-backed (default) or native session-turn family (S3 波1 灰度).
// Both shapes expose the same 113-column contract under alias `rl`.
func logsSourceFromSQL() string {
	if settings.GetPlatformBool(nativeTurnsReadSetting, false) {
		return db.SessionFamilyTurnsSourceSQL() + " rl"
	}
	return "request_logs_with_current_month rl"
}
