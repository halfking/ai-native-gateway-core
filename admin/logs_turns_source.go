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
//   - 原生模式只输出 session 家族已有行；request_logs 中尚未入 turns 的
//     历史行（镜像链启用前窗口）在该模式下不可见——本机镜像链 2026-09 起
//     全量双写，观察期（storage-observation-ledger.md）零漂移后此边界随 S4
//     停写自然消解。
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
