//go:build !integration

package summarystore

import (
	"strings"
	"testing"
)

// TestS3Wave2TurnCountSources（2026-10-13 S3 波 2）钉住自动摘要两个门槛
// 计数的源形状。停写后新流量轮次只落 session_turns(_hot)，原 hot-only 实现
// 让门槛恒 0、自动摘要停摆。判据同层：断言驱动包级 SQL 变量——它们就是
// CountNewTurns/CountTotalTurns 实际发给 PostgreSQL 的文本。
//
// ★ 2026-10-20（迁移 890，v1 族整体退役）：本门**反转**了 v1 那一段。
//
// 原来这里钉的是「v1 回退腿必须直读 _request_logs 分区父表」「必须带
// gw_session_id = $1 AND success = TRUE 谓词」——回退腿服务的是「镜像链
// 启用前的历史会话」（计数只数得到 v1 母表的行）。
//
// v1 族被整体 DROP 之后：① 那条 SQL 必然 42P01（整条语句失败，不是那条腿
// 失败）；② 那些历史会话**本身**也随 v1 族一起消失 ⇒ 回退腿没有任何可回退
// 的对象。⇒ countTurnsSessionSQL 成为**唯一**计数腿，会话族 0 行就是 0 行。
//
// ⇒ 反转后的判据是「唯一那条计数 SQL 里**不得**出现 v1 族引用」。
// 保留的正向断言（内推谓词、hot+parent 两腿、成功谓词）保证「会话族那条腿
// 真的在」，不是「因为什么都没写所以没引用 v1」。另配**阳性对照**证明这条
// 负向判据分得清「含 v1 引用」与「不含」。
func TestS3Wave2TurnCountSources(t *testing.T) {
	if !strings.Contains(countTurnsSessionSQL, "session_id = $1") {
		t.Error("会话族计数缺少内推的 session_id = $1 谓词（外层过滤投影列会丢索引）")
	}
	// ForSession 源是 hot ∪ parent 两腿：只查 hot 会在 promote 排空后漏行。
	if got := strings.Count(countTurnsSessionSQL, "FROM public.session_turns"); got != 2 {
		t.Errorf("会话族源应有 hot+parent 两腿（实存 %d 处 FROM public.session_turns）", got)
	}
	if !strings.Contains(countTurnsSessionSQL, "rl.success = TRUE") {
		t.Error("成功轮计数谓词丢失（门槛口径回归）")
	}

	// ---- 反转后的负向判据：唯一那条计数 SQL 不得引用 v1 族 ----
	//
	// 逐字包含（strings.Contains）而不是 LIKE：`_` 在 LIKE 里是单字符通配符，
	// `LIKE '%_request_logs%'` 既会误命中无关串、也会漏掉别的写法。
	for _, v1 := range []string{
		"_request_logs",
		"request_logs_hot",
		"request_logs_bodies",
		"request_logs_with_current_month",
	} {
		if strings.Contains(countTurnsSessionSQL, v1) {
			t.Errorf("计数 SQL 里出现了 v1 族引用 %q —— v1 族已被迁移 890 DROP，"+
				"任何提到它的语句整条 42P01 ⇒ 自动摘要的门槛计数会直接失败。", v1)
		}
	}

	// ---- 阳性对照：证明上面那条负向判据**分得清** ----
	//
	// 没有对照，上面那个 for 循环若把判据写坏（例如检查一个永远不出现的串），
	// 整段会恒绿——「判据没测到东西」与「判据测到了」必须长得不一样。
	if !strings.Contains("SELECT 1 FROM _request_logs", "_request_logs") {
		t.Error("判据分不清「含 v1 引用」与「不含」—— 上面的负向断言此时恒绿")
	}
}
