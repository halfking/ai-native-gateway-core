package admin

// ranking_identity.go —— 排行维度「显示名 / 分组键」与「日志过滤谓词」的**共享身份表达式**。
//
// # 为什么要有这一层
//
// `/admin/usage-trends` 的六张排行卡片与 `/admin/request-logs` 的过滤是**两个契约**：
//
//	排行侧：GROUP BY <身份表达式>，显示名取 <身份表达式>
//	过滤侧：WHERE <身份表达式> = $n
//
// ⇒ ★ 两边**必须用同一个表达式**，否则「卡片上写的条数」与「点进去看到的条数」
//   会对不上（实测过的失败形态：排行按 device_id 分组、过滤按原值匹配
//   ⇒ 卡片显示 14,674、点进去 0 条）。
//
// # 本文件的两类身份
//
//  ① **普通维度**（client_model / application_code / task_type / outbound_model）：
//     空串与 NULL 是**同一个语义**（「没有值」）⇒ 统一归到 'unknown'
//     ⇒ 排行侧显示 'unknown'（而不是一个**空白行**）、过滤侧也能按 'unknown' 命中。
//     ★ 这一条不是洁癖：实测现网 `task_type` 的最大一组（66,638 行）**原值就是空串**
//       ⇒ 排行卡片第一行渲染成**空白**、点击过去因为过滤值为空而被后端忽略
//       ⇒ 落点是**未筛选的全量**（与「过滤失效」同一种症状、另一条路径）。
//
//  ② **用户维度**（end_user_id）：客户端把 `user` 报成了 JSON 对象
//     ⇒ 身份 = JSON 里的 device_id；NULL/空串/无 device_id ⇒ 'unknown'。
//     ★ 走 `substring(... from ...)` 而不是 `::jsonb->>`：该列混着 'anonymous' /
//       NULL / 纯 id 等**非法 JSON** 值 ⇒ `::jsonb` 会对它们抛错（整条查询 500）。
//
// # 口径纪律
//
// · 表达式里**绝不能**含 '%'（会被拼进 fmt.Sprintf 的格式串的位置当作格式动词）
//   ⇒ 需要拼进格式串时**走参数位**（%s），不要直接插值。
// · 排行侧与过滤侧**都**从这里取，改一处必须想到另一处（静态门
//   `logs_ranking_dimension_filters_test.go` 钉住这件事）。

import "fmt"

// dimensionIdentityExpr 返回普通维度的身份表达式：空串/NULL ⇒ 'unknown'。
//
// ★ 返回值**不含 '%'** ⇒ 可以安全地作为 fmt.Sprintf 的参数位传进去。
func dimensionIdentityExpr(col string) string {
	return fmt.Sprintf("COALESCE(NULLIF(rl.%s, ''), 'unknown')", col)
}

// userIdentityExpr —— 请求行的「用户身份」表达式。
//
// ★★ 这是一个**跨文件契约**：排行卡片（admin/usage.go）按它**分组**、显示名取它；
//
//	点进日志页时过滤侧（admin/logs.go）也拿它做**等值匹配**
//	⇒ ⇒ 卡片上的 request_count 与点进去看到的条数**逐条对得上**。
//	★ 两边各写一份表达式是这类修复最常见的假绿来源（改了一边忘了另一边
//	  ⇒ 卡片显示 14,674、点进去 0 条）⇒ ⇒ 必须共用同一个常量。
//
// # 这个表达式同时解决三件事
//
//	① **显示**：客户端把 `user` 报成了 JSON 对象（现网 14,674 行），直接渲染
//	   会得到一坨被截断的 JSON ⇒ 提取其中的 device_id 作为可读名。
//
//	② **口径**：那段 JSON 里还含 session_id ⇒ 直接 GROUP BY 原值等于**按会话
//	   分组**（实测 14,674 行 JSON 只对应 **1 个** device_id、**117 个** session_id）
//	   ⇒「Top 10 用户」里 9 条其实是同一个设备的 9 个会话。按 device_id 分组
//	   才回到「Top 10 用户」的口径。
//
//	③ **兜底**：NULL / 空串 / JSON 里没有 device_id ⇒ 归到 'unknown'
//	   ⇒ 与过滤侧一致 ⇒ 点击 'unknown' 也能查到这些行（否则卡片有它、点进去 0 条）。
//
// # 现网形态（211 实测，request_logs_with_current_month）
//
//	anonymous                              兜底值（379,142 行）
//	NULL                                   未上报（38,657 行）
//	{"device_id":"50f0…","session_id":"…"} 客户端把 user 报成了 JSON 对象（14,674 行）
//	user-admin / auth0|user_01J… / …     真正的纯 id（仅 49 行）
//
// # 为什么用 substring(... from ...) 而不是 ::jsonb->>
//
// 本列混着 'anonymous' / NULL / 纯 id 等**非法 JSON** 值 ⇒ `::jsonb` 会对它们
// 抛错（整条查询 500）。substring 的正则形式对非 JSON 输入安全地返回 NULL。
// 再用 CASE 包住 ⇒ 只在确实以 '{' 开头时才做正则提取（PG 的 CASE 是惰性的，
// 非 JSON 行不做正则运算）。
//
// ★ 注意：它**含 '%'**（LIKE 模式 '{%' 的通配符）⇒ 必须走 fmt.Sprintf 的**参数位**，
//
//	绝不能被当成格式串。
const userIdentityExpr = `COALESCE(
	NULLIF(
		CASE WHEN rl.end_user_id LIKE '{%'
			THEN substring(rl.end_user_id from '"device_id"\s*:\s*"([^"]*)"')
		END,
		''
	),
	NULLIF(
		CASE WHEN rl.end_user_id LIKE '{%' THEN NULL ELSE rl.end_user_id END,
		''
	),
	'unknown'
)`
