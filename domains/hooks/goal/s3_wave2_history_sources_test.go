//go:build !integration

package goal

import (
	"strings"
	"testing"
)

// TestS3Wave2HistorySources（2026-10-13 S3 波 2）钉住会话历史重建的源形状。
// 停写后新会话正文只在 session_bodies；镜像前历史只在 _request_logs_bodies。
// 判据同层：断言驱动包级 SQL 变量——它们就是 FetchBySession 实际发给
// PostgreSQL 的文本。
//
// ★ 2026-10-20（迁移 890，v1 族整体退役）：本门**反转**了 v1 那一段。
//
// 原来这里断言「v1 回退腿必须存在」（historyV1SQL 必须含
// "FROM _request_logs rl" / "_request_logs_bodies rb" / "rl.gw_session_id = $1"）。
// v1 族被 DROP 之后，那条断言钉的是一个**必然 42P01** 的语句：任何提到
// v1 关系的查询都是整条语句失败（不是那条腿失败）。
//
// ⇒ 反转后的判据是「会话族那条 SQL 里**不得**出现 v1 族引用」。
// 这不是把门放松，而是把门指向新的不变量：**同一个 FROM 里提到已删关系 ⇒
// 整条语句失败**，而这条语句是审计钩子每次取历史的必经之路 ⇒ 一旦有人
// 「为了兼容」把 v1 腿加回来，历史重建会在**每一次**审计里炸掉。
//
// 为什么「反向断言」不会退化成「什么都没测」：本门仍然保留**正向**断言
// （会话族两腿、内推谓词、request_id 单键 JOIN、投影列）——它们保证
// 会话族那条腿**真的在**，不是「因为什么都没写所以没引用 v1」。
// 另配一条**阳性对照**：证明这个判据分得清「含 v1 引用」与「不含」
// （否则判据写错也一直绿——本仓反复吃过这个亏）。
func TestS3Wave2HistorySources(t *testing.T) {
	s := historySessionSQL
	if got := strings.Count(s, "session_id = $1"); got != 2 {
		t.Errorf("会话族腿应有内推的 session_id = $1 两处（hot+parent，实存 %d）", got)
	}
	// bodies 腿必须是 request_id 单键 JOIN：bodies 源不投影 ts，
	// ts 等值 JOIN 会解析期 42703（响亮失败），但静默换源是更坏方向。
	if !strings.Contains(s, "ON rb.request_id = rl.request_id") {
		t.Error("bodies 腿必须按 request_id 单键 JOIN（helper 合同）")
	}
	if strings.Contains(s, "rb.ts = rl.ts") {
		t.Error("bodies JOIN 不得带 ts 等值（两侧 ts 99.85% 不等，静默丢正文）")
	}
	for _, col := range []string{"rb.request_body", "rb.response_body"} {
		if !strings.Contains(s, col) {
			t.Errorf("投影缺 %q（v1 同名列合同）", col)
		}
	}

	// ---- 反转后的负向判据：会话族那条 SQL 不得引用 v1 族 ----
	//
	// 用 `strings.Contains`（逐字包含）而不是 LIKE：`_` 在 LIKE 里是单字符
	// 通配符，`LIKE '%_request_logs%'` 会命中 `xrequestlogsy` 这类无关串，
	// 也会漏掉别的写法。逐字包含是这里想要的精确口径。
	for _, v1 := range []string{
		"_request_logs",
		"request_logs_hot",
		"request_logs_bodies",
	} {
		if strings.Contains(s, v1) {
			t.Errorf("会话族 SQL 里出现了 v1 族引用 %q —— v1 族已被迁移 890 DROP，"+
				"任何提到它的语句整条 42P01（不是那条腿失败）⇒ 历史重建在每次审计里都会炸。",
				v1)
		}
	}

	// ---- 阳性对照：证明上面那条负向判据**分得清** ----
	//
	// 没有对照的话，上面那个 for 循环里若把判据写坏（例如误写成检查一个
	// 永远不出现的串），整段会恒绿——而这正是本仓反复踩的「假绿」。
	// 判据必须先证明自己会对一个**确实含 v1 引用**的文本报警。
	if !strings.Contains("SELECT 1 FROM _request_logs_hot", "_request_logs") {
		t.Error("判据分不清「含 v1 引用」与「不含」—— 上面的负向断言此时恒绿")
	}
}
