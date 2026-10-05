//go:build !integration

package main

import (
	"strings"
	"testing"
)

// TestS4DriftScopeReadsBothStorageFaces 钉住「对账 SQL 必须两面都读」。
//
// # 为什么这道门存在：同一个陷阱已经踩了三次
//
// 本地真库的四个存储面**不是**同一张表的两种视图，而是**四个不同的关系**：
//
//	request_logs_hot     最后写入 2026-10-05 22:51（新鲜）
//	request_logs         最后写入 2026-10-05 13:53（**落后约 9 小时**）
//	session_turns        最后写入 2026-10-05 13:53
//	session_bodies       最后写入 2026-10-05 13:53
//
// ⇒ **父表 9 小时没写，不等于 v1 停写。** 只读父表的一笔测量会得出
// 「最近 6 小时没有 v1 流量」这个结论，而它是错的：本轮实测，父表总体在
// 1h/6h 窗口是 **0 / 0**，两面都读则是 **150 / 1000**。
//
// `dual_read_gate.go` 的文件头已经把这件事记成「§9.160.7 的 trap，
// 在本节里第三次命中 —— 包括在为抓这一类错误而写的那条规则的**证据里**」。
// 本轮第四次：先按单面取总体，得到「1h/6h 空窗」，改两面后空窗消失。
//
// ⇒ 与其再写一段注释，不如让门把它变成一条会红的断言。
//
// # 这道门**不**检查 bodies
//
// bodies 的停写前置不另立：实测它的缺口是主表腿缺口的**子集**
// （1h 2⊆2、6h 2⊆2、24h 16⊆16、72h 88⊆88、168h 458⊆463，
// bodies 独有缺口 = 0），所以主表腿的判定蕴含 bodies 腿。
// 详见 docs/audit/2026-10-06-v1-stopwrite-precondition.md。
func TestS4DriftScopeReadsBothStorageFaces(t *testing.T) {
	const sql = mirrorDriftScopeSQL

	// 两侧各要两个面。任何一面缺席，下面的计数就会在窗口里挖一个洞，
	// 而那个洞的形状恰好是「看起来流量变少了」。
	for _, face := range []string{
		"request_logs_hot", "request_logs",
		"session_turns_hot", "session_turns",
	} {
		if !strings.Contains(sql, face) {
			t.Errorf("对账 SQL 没有读 %s —— 只读一个存储面会让「最近 N 小时没有 v1」"+
				"这类结论在流量其实正常时成立（本地真库父表落后 hot 约 9 小时，"+
				"父表总体 1h/6h 是 0/0，两面都读是 150/1000）", face)
		}
	}

	// 阴性对照：判据必须能分辨「两个面」与「一个面」。
	// 没有对照的话，上面那段 if 写错了也一直绿。
	oneFace := "SELECT 1 FROM request_logs WHERE 1=1"
	if containsAll(oneFace, []string{"request_logs_hot", "request_logs"}) {
		t.Errorf("判据分不清单面与双面：一个只读 request_logs 的片段被判成双面 —— " +
			"上面四条断言此时会恒绿")
	}
	bothFaces := "FROM request_logs_hot UNION ALL FROM request_logs"
	if !containsAll(bothFaces, []string{"request_logs_hot", "request_logs"}) {
		t.Errorf("判据把一个真双面片段判成单面 —— 对照本身失效")
	}
}

func containsAll(s string, subs []string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// TestS4WindowFloorIsWeakerThanTheDocumentedExitCondition 记录一处
// **门比 spec 宽**的差异，并且**故意不让它红**。
//
// spec 自己的退出条件（settings/spec_storage.go，S4 停写 gate 那一条）：
//
//	「关闭前提：dual_read_validator 对账 7 天零漂移（plan §4 S2 退出条件）」
//
// 门实际强制的窗口下限是 `s4MinWindowHours = 24`。
// 文件头的注释说明这个常数是**照着** 7 天条件的漏检率（0.08%）推出来的，
// 但下限本身是 24 小时 ⇒ **一个 24 小时窗口就足以让门说 ready，
// 而 spec 要求的是 7 天。**
//
// 这不是缺陷判定，是**属主的决定**：spec 那句可能是文档没跟上实现，
// 也可能实现有意放宽。两种改法方向相反 ——
// 把下限提到 168h 会让门更严（可能长期不 ready），
// 把 spec 改成 24h 会放宽一个危险开关的书面条件。
// 所以本测试只**把它打印出来**，不判定对错。
func TestS4WindowFloorIsWeakerThanTheDocumentedExitCondition(t *testing.T) {
	t.Logf("S4 窗口下限 = %d 小时；spec 写的退出条件是「7 天零漂移」= %d 小时。%s",
		s4MinWindowHours, 7*24,
		"下限比书面条件宽 7 倍 ⇒ 24h 干净即可放行，而 spec 要求 7 天。属主定。")

	// 门能拒绝的四种理由，逐条列出来（供对照「现在卡在哪一条」）。
	for _, r := range []string{
		s4GateReasonV1WritesDisabled,
		s4GateReasonNoV1Traffic,
		s4GateReasonInsufficientV1Coverage,
		s4GateReasonWindowTooShort,
	} {
		if strings.TrimSpace(r) == "" {
			t.Errorf("门的一个否决理由是空串 —— 输出里会少一条 operator 提示")
		}
	}
}
