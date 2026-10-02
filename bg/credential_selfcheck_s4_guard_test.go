package bg

import (
	"strings"
	"testing"
)

// S4 停写：凭据自检的错误证据臂（2026-10-02 审计，§9.26）。
//
// pickDueCredential 的 v1 臂不是排序，是**硬过滤**（原为 INNER JOIN
// `e.last_error_at IS NOT NULL`）。停写生效 24h 后 request_logs_hot 不再产生新的
// 失败行 ⇒ 没有任何凭据能通过 ⇒ 自检**自己静默**。与 credential_recovery 的
// lookbackCandidateSQL 同形（方向 ②：证据缺失）。
//
// 真库实测（24h 窗口，按凭据去重）：
//
//	v1 侧有报错的凭据 41，session 侧 15，两侧都有 15，只有 v1 有 26。
//	再按 origin_stage 拆那 26：**全部是 node_probe**。
//
// ⇒ 业务失败两族都有；探针失败**只在 v1**，因为探针流量按设计不走 session 写路径，
//    **它无法被端口**。放弃的是「探针最近失败 ⇒ 现在复检」这条**取证捷径**，
//    不是能力——探针系统本身（node_probe / node_probe_state）不受门管。
// ⇒ 因此**不门控整个自检 worker**（那会连同仍然有效的故障发现一起停掉），
//    改为补 session 族臂把「止错」的部分保住。
//
// 本文件的三条门设计纪律（前两版各踩了一次，记在这里防止第三次）：
//
//  1. **不许用全文件子串**。第一版在 pickDueCredential 函数体内查子串，被同函数里
//     另外两条 LATERAL（self_check_runs 的 `l`）喂饱 ⇒ 「v1 臂改回 INNER JOIN」和
//     「删掉 v1 臂」两个变异都不响。**守卫被无关的同名字符串喂饱，是最危险的一种
//     假保证**：它看起来在验证，实际什么都没验证。
//  2. **断言要按臂定位**。三条 LATERAL 用别名 `e` / `se` / `l` 区分，断言必须分别
//     取到「这一条臂」再判它的 join 关键字和 ON 前置，而不是在整段 SQL 里问
//     「有没有出现过某个片段」。用节点身份（别名）定位，不用「第一个 LATERAL」。
//  3. **先剥 SQL `--` 注释再断言**。这段 SQL 里有一大段解释性中文注释，注释正文
//     本身含 `COALESCE(e.last_error_at, se.last_error_at)`；不剥注释的话，删掉真实
//     的 ORDER BY 接线也能被注释满足。这与 admin 的逐字面量门是同一条教训
//     （门被自己的注释弄红/喂饱）。
//
// 门的边界（它看不到什么）：它是**源码**门，只能证明这条 SQL 的形状没被改回退化
// 形状；它不能证明 SQL 在真库上可执行，也不能证明两族覆盖真的重叠。真库那一半由
// §9.24 的 session_turns 排除臂实测（131 条合成请求）承担，两道门不可互相替代。

func TestSelfcheckErrorArmCoversSessionFamily(t *testing.T) {
	sql := selfcheckPickSQL(t)

	// ---- 臂 e（v1）：必须仍在，且必须是 LEFT JOIN ----
	eArm, ok := lateralArmSrc(sql, "e")
	if !ok {
		t.Fatal("pickDueCredential 里找不到 v1 错误证据臂（LATERAL 别名 e）。\n" +
			"它是探针失败信号的唯一来源（真库 24h 内 41 个有报错的凭据里，\n" +
			"有 26 个只有 v1 有、且全是 node_probe），双写期也是主覆盖。")
	}
	if !strings.Contains(eArm, "LEFT JOIN LATERAL") {
		t.Fatal("v1 臂不再是 LEFT JOIN —— session 臂永远轮不上它：\n" +
			"**补了臂但没接线**是最容易自我欺骗的一种假修复（看起来做了，实际零效果）。\n" +
			"当前 join 关键字：" + armJoinKeyword(eArm))
	}
	if !strings.Contains(eArm, "FROM request_logs_hot rl") {
		t.Error("v1 臂的证据源不再读 request_logs_hot —— 请确认这是有意改动并同步本门")
	}

	// ---- 臂 se（session 族）：必须存在，且类型转换必须显式 ----
	seArm, ok := lateralArmSrc(sql, "se")
	if !ok {
		t.Fatal("自检的错误证据臂不再覆盖 session 族。\n" +
			"v1 那一臂是硬过滤，停写 24h 后没有任何凭据能通过 ⇒ 自检自己静默。\n" +
			"真库实测：24h 内 v1 有报错的凭据 41、session 侧 15、只有 v1 有的 26 条全是\n" +
			"node_probe（探针流量按设计不走 session 写路径，无法端口）。\n" +
			"补这条臂是为了保住业务失败检测。")
	}
	if !strings.Contains(seArm, "FROM session_turns st") && !strings.Contains(seArm, "FROM session_turns") {
		t.Error("session 臂的证据源不是 session_turns —— session_turns 不受 S4 停写门管，\n" +
			"换别的表会重新引入跨门边界。当前臂内容见测试输出")
	}
	// §9.28：必须同时读**两个存储面**。写方只写 session_turns_hot，冷行由
	// promote 搬到分区父表，边界随 promote 节奏移动（实测父表落后约 8.7 小时）。
	// 只读父表 ⇒ 对最新轮次盲 —— 而真库实测在父表覆盖不到的那段窗口里，
	// 旧形状看到 0 个失败轮次、两面合并看到 761 个：**这条臂存在的意义正是抓
	// 最新失败，单面读法让它对自己的目标完全失明。**
	// R32（P2-D）：SELECT 清单加了 partition_date（分区裁剪下推，见
	// credential_selfcheck.go 同址注释）——两个面的形状断言随之同步。
	for _, want := range []string{
		"SELECT ts, success, status_code, credential_id, partition_date FROM session_turns",
		"SELECT ts, success, status_code, credential_id, partition_date FROM session_turns_hot",
		"UNION ALL",
		"st.partition_date >= (now() - interval '24 hours')::date",
	} {
		if !strings.Contains(seArm, want) {
			t.Errorf("session 臂没有同时读会话族的两个存储面，缺：%s\n"+
				"只读父表会漏掉 session_turns_hot 里的最新失败轮次（实测该窗口 761 个），\n"+
				"而这条臂存在的意义恰恰是抓最新失败。710 视图同款 UNION ALL 惯例。", want)
		}
	}
	if !strings.Contains(seArm, "st.credential_id = c.id::text") {
		t.Error("session 臂没有把 credential_id 显式转成 text —— v1 侧是 bigint、\n" +
			"session_turns 侧是 text，不转换会直接报 operator does not exist: bigint = text。\n" +
			"这条是**真库执行门**才能抓的形状，源码门只能拦「转换被删掉」。")
	}

	// ---- 硬前置：必须由 COALESCE 承担，且不能退回 v1-only ----
	if !strings.Contains(seArm, "ON COALESCE(e.last_error_at, se.last_error_at) IS NOT NULL") {
		t.Error("硬前置没有由 COALESCE(e, se) 承担 —— 退回 v1-only 会让 session 臂成为装饰：\n" +
			"v1 冻结后没有任何凭据能通过硬前置，自检静默。当前 se 臂的 ON 子句见测试输出。\n" +
			"se 臂原文：" + seArm)
	}
	if !strings.Contains(eArm, ") e ON e.last_error_at IS NOT NULL") {
		t.Error("v1 臂自身的 ON 子句被改动 —— 它的语义是「无 v1 失败行时该臂产 NULL」，\n" +
			"改掉会让 LEFT JOIN 退化成别的东西。当前 v1 臂原文：" + eArm)
	}

	// ---- 排序接线：停写后唯一合格的群体 v1 值为 NULL ----
	// PostgreSQL 的 DESC 默认 NULLS FIRST，不接线会让他们全部挤到最前，
	// 丢掉「最近报错优先」的排序语义。**接了 COALESCE 但漏了 DESC 同样会红。**
	orderBy := orderByClause(sql)
	if !strings.Contains(orderBy, "COALESCE(e.last_error_at, se.last_error_at) DESC") {
		t.Error("ORDER BY 仍只按 e.last_error_at 排序 —— 停写后 session-only 群体该值为 NULL，\n" +
			"DESC 的 NULLS FIRST 会把他们全部挤到最前，丢掉「最近报错优先」的排序语义。\n" +
			"当前 ORDER BY 片段：" + orderBy)
	}
	if strings.Contains(orderBy, "e.last_error_at DESC") && !strings.Contains(orderBy, "COALESCE(e.last_error_at, se.last_error_at) DESC") {
		t.Error("ORDER BY 里存在裸的 e.last_error_at DESC")
	}
}

// TestSelfcheckWorkerIsNotGated records the triage decision: the worker must NOT
// be gated, because the probe system (node_probe / node_probe_state) is not under
// the S4 blast radius and is the authoritative unhealthiness detector. Gating the
// worker would turn a degradable evidence shortcut into a hard outage of fault
// discovery — "止错" vs "止对".
//
// 这条是**否定式**门，作用域是整个文件（不是某个函数）：worker 层任何位置
// 出现门调用都算退化。
func TestSelfcheckWorkerIsNotGated(t *testing.T) {
	if strings.Contains(mustReadSourceBg(t, "credential_selfcheck.go"), "RequestLogsWriteEnabled()") {
		t.Error("自检 worker 开始咨询 S4 门了。该 worker 的探针检测能力不依赖被冻结的证据\n" +
			"（结果写在 node_probe_state，不受门管）——门控它会连同仍然有效的故障发现一起停掉，\n" +
			"属于「止对」。要停的只是 v1 证据臂，而那已由 session 族臂接管。")
	}
}

// lateralArmSrc returns the source text of the LATERAL arm that binds `alias`:
// its JOIN line plus its ON line. Returns ok=false when no such arm exists.
//
// 按别名定位而不是按「第几个 LATERAL」定位：同一条 SQL 里有三条臂（e / se / l），
// 用序号定位在有人调整臂顺序后会静默改判。
//
// 臂的边界 = JOIN 关键字所在行 + 其 `) <alias> ON ...` 那一行。这条 SQL 里每条臂的
// ON 子句都独占一行，臂与臂之间隔着注释块，所以「取到 ON 行末」既不会吞掉下一条臂，
// 也不会把 ON 子句截断（第一版截在 `ON` 之后，结果两条关于硬前置的断言对着空串
// 判断而恒红 —— **门的边界写错时它会红成一条和被测缺陷无关的假警报**）。
func lateralArmSrc(sql, alias string) (string, bool) {
	marker := ") " + alias + " ON"
	on := strings.Index(sql, marker)
	if on < 0 {
		return "", false
	}
	open := strings.LastIndex(sql[:on], "LATERAL (")
	if open < 0 {
		return "", false
	}
	lineStart := strings.LastIndex(sql[:open], "\n") + 1
	end := strings.IndexByte(sql[on+len(marker):], '\n')
	if end < 0 {
		end = len(sql) - (on + len(marker))
	}
	return sql[lineStart : on+len(marker)+end], true
}

// armJoinKeyword returns the leading keyword(s) of an arm, for error messages.
func armJoinKeyword(arm string) string {
	line := arm
	if i := strings.IndexByte(arm, '\n'); i >= 0 {
		line = arm[:i]
	}
	return strings.TrimSpace(line)
}
