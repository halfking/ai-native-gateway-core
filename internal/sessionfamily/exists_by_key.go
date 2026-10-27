// Package sessionfamily holds SQL fragments that describe the **shape** of the
// session-turn family (public.session_turns_hot ∪ public.session_turns).
//
// # 为什么不在 db 包里（2026-10-27 / R169 实测坐实的硬约束）
//
// db 包是「会话族 SQL 片段」的祖居地（SessionFamilyTurnsSourceSQL 等都在那里），
// 但 db 的**测试**文件 import 了 domains/hooks/observability/telemetry，而
// telemetry → domains/dbdegradation → domains/session → domains/session/v2；
// 若 domains/session/v2 再 import db ⇒ **测试期 import 环**（go test ./db/ 直接
// 报 "import cycle not allowed in test"）。
//
// ⇒ 这类「写路径要用的族形片段」落在本**叶子包**里：它不依赖任何东西，
// 谁都能 import（包括 domains/session/v2 与 db），且没有环。
//
// 这不是「两份真相源」：片段的**实现**只有这一份；db 侧要用时 import 本包，
// 不再各自抄一份（与 internal/internaltraffic 的分层同款）。
package sessionfamily

// ExistsByKeySQL 返回「按 (tenant_id, request_id) 判存在」的**两臂** SQL
// （hot ∪ 分区父表），供**写路径**的幂等检查使用。
//
// # 为什么不用视图（2026-10-27 / R169 在 252 生产规模实测坐实）
//
// `public.session_turns_with_current_month` 的 hot 臂带一个**相关反连接**：
//
//	FROM session_turns_hot hot
//	 WHERE NOT (EXISTS (SELECT 1 FROM session_turns archived
//	                   WHERE archived.tenant_id::text = hot.tenant_id::text
//	                     AND archived.request_id = hot.request_id))
//	UNION ALL … FROM session_turns;
//
// 它用来把「已晋级到父表的重复行」从 hot 臂里剔掉。对**存在性**问题
// （写路径的幂等检查：这一行是不是已经落过？）这个反连接是**多余的**：
// 被它剔掉的 hot 行，在父表臂里仍然存在 ⇒ 两臂合起来「该键是否存在」的
// 答案**恒等**（论证见下）。
//
// 而留着它的代价是实打实的（252 生产规模，EXPLAIN 计划形态对比）：
//
//   - 现状（视图形态）：Nested Loop Anti Join + **12** 处分区引用
//     （反连接里 6 个分区 + 视图父表臂 6 个分区）
//   - 两臂形态：无反连接 + **6** 处分区引用
//   - 规划时间（同会话交替重复；机器有负载 ⇒ 方差大，读数是区间不是定值）：
//     视图形态 5.8~93 ms（中位 ~67 ms）· 两臂形态 3.8~16 ms（中位 ~4 ms）
//
// ★ 写路径每请求都重新规划（db/db.go:73 的 QueryExecModeSimpleProtocol ⇒ 无计划缓存）
//
//	⇒ 这不是「一次性」成本，而是**每请求**都付。
//
// # 等价性论证（两个方向都要走）
//
//	视图对键 K 的行集 = {hot 行 | K，且父表无 K} ∪ {父表行 | K}
//	两臂对键 K 的行集 = {hot 行 | K} ∪ {父表行 | K}
//
//	⇒ 视图行集 ⊆ 两臂行集；
//	⇒ 反过来：若两臂有 K 而视图没有 ⇒ K 只能来自某个 hot 行 ⇒ 该 hot 行
//	  被反连接剔除 ⇒ 父表**必有**同键行 ⇒ 视图的父表臂会产出它 ⇒ 矛盾；
//	⇒ 两者对「K 是否存在」的答案**恒等** ⇒ 幂等检查口径不变。
//
// ★ 视图是 security_invoker（757 迁移）⇒ 视图与直读底表都在**调用者权限/RLS**
//
//	下执行 ⇒ 可见行集一致 ⇒ 等价性不受角色影响。
//
// # 参数
//
//   - tenantExpr / requestExpr：两列的 SQL 表达式（如 "$3::varchar" / "$4"）。
//     ★ 两臂用**同一对**表达式 ⇒ 两臂的键口径逐字相同。
func ExistsByKeySQL(tenantExpr, requestExpr string) string {
	leg := func(src string) string {
		return "(SELECT 1 FROM " + src + " WHERE tenant_id = " + tenantExpr + " AND request_id = " + requestExpr + ")"
	}
	return "(" + leg("public.session_turns_hot") + " UNION ALL " + leg("public.session_turns") + ")"
}
