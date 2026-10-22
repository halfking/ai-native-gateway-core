package main

import (
	"context"
	"strings"
	"testing"
)

// TestPrefixAnalyzeDetectsPrefixRelation 钉住「前缀性质」判定器。
//
// ★ 为什么这个判定器是 §143.71 的关键：仓内 `DeriveTurnDeltas` 是【前缀差分】
//
//	（delta_i = full_i − full_{i−1}）⇒ 「能拆出多少轮」完全取决于
//	「前一轮的累积体是不是下一轮累积体的前缀」。
//	⇒ 判定器若判错 ⇒ 恢复面的量级就会算错 ⇒ ⇒ 必须钉死。
//
// ★ 合成数据刻意覆盖三种形态：
//
//	① 真前缀（role 与 content 都成立）⇒ 差分可拆
//	② role 前缀成立但 content 不成立 ⇒ ★ 能切轮，但差分会多出内容（污染）
//	③ role 前缀也不成立 ⇒ 差分不可用
//
// ★★ 注意 ② 这一条：cum2 与 cum3 的 role 序列都是 [u,a,u] ⇒ role 前缀【成立】，
//
//	但 cum3 把 cum2 的 assistant 内容从 a1 改成了 aX ⇒ content 前缀【不成立】。
//	⇒ ⇒ ★ 这正是「切轮可行」与「差分正确」必须分成两个判据的原因。
func TestPrefixAnalyzeDetectsPrefixRelation(t *testing.T) {
	base := t.TempDir()
	// 同一会话的四轮累积体（★ 顺序：第 1 轮 → 第 2 轮 → 第 3 轮（content 被改写）→ 第 4 轮（role 变了））。
	//   第 1 轮：[u1]
	//   第 2 轮：[u1, a1, u2]     ⇒ 第 1 轮是第 2 轮的【真前缀】
	//   第 3 轮：[u1, aX, u2, a2] ⇒ role 前缀成立，但 content 不成立（a1 → aX）
	//   第 4 轮：[t0, u9]         ⇒ role 前缀不成立（t vs u）
	c1 := recLine(t, "req-1", "gw_sess_p", `{"messages":[{"role":"user","content":"q1"}]}`)
	c2 := recLine(t, "req-2", "gw_sess_p", `{"messages":[{"role":"user","content":"q1"},{"role":"assistant","content":"a1"},{"role":"user","content":"q2"}]}`)
	c3 := recLine(t, "req-3", "gw_sess_p", `{"messages":[{"role":"user","content":"q1"},{"role":"assistant","content":"aX"},{"role":"user","content":"q2"},{"role":"assistant","content":"a2"}]}`)
	c4 := recLine(t, "req-4", "gw_sess_p", `{"messages":[{"role":"tool","content":"t0"},{"role":"user","content":"q9"}]}`)
	writeBackup(t, base, "sessions-2026-09-01.jsonl.gz", buildGzip(t, []string{c1, c2, c3, c4}))

	res, err := analyzePrefix(context.Background(), base, "2026-09", "")
	if err != nil {
		t.Fatalf("分析失败：%v", err)
	}
	if res.Records != 4 {
		t.Errorf("记录数应为 4，实得 %d", res.Records)
	}
	if res.WithBody != 4 {
		t.Errorf("带 body 的记录应为 4，实得 %d", res.WithBody)
	}
	if res.Unparsed != 0 {
		t.Errorf("不该有解析失败的记录，实得 %d", res.Unparsed)
	}
	if res.Sessions != 1 {
		t.Errorf("会话数应为 1，实得 %d", res.Sessions)
	}
	if res.MultiTurn != 1 {
		t.Errorf("多轮会话数应为 1，实得 %d", res.MultiTurn)
	}
	// 相邻对 = 3 对（第 1→2、第 2→3、第 3→4）。
	// ★ role 级：第 1→2、第 2→3 成立；第 3→4 不成立（t vs u）。
	if res.PairRoleOK != 2 || res.PairRoleBroken != 1 {
		t.Errorf("role 前缀判据应是 2 成立 / 1 不成立，实得 ok=%d broken=%d",
			res.PairRoleOK, res.PairRoleBroken)
	}
	// ★ content 级：只有第 1→2 成立；第 2→3（a1→aX）与第 3→4 都应不成立。
	if res.PairContentOK != 1 {
		t.Errorf("content 前缀成立的相邻对应为 1，实得 %d", res.PairContentOK)
	}
	if res.PairContentBad != 2 {
		t.Errorf("content 前缀不成立的相邻对应为 2，实得 %d", res.PairContentBad)
	}
	// ★ 累积深度：第 3 轮累积体有 2 条 user 消息 ⇒ 最大深度 2，多轮记录 2 条。
	if res.MaxUserMsgs != 2 {
		t.Errorf("最大累积深度应为 2，实得 %d", res.MaxUserMsgs)
	}
	if res.AccumRecords != 2 {
		t.Errorf("多轮累积体记录数应为 2（第 2、3 轮），实得 %d", res.AccumRecords)
	}
	// ★ 轮数上界 = 该会话所有记录里 user 消息数的最大值 = 2。
	if res.SplittableTurns != 2 {
		t.Errorf("可拆轮数上界应为 2，实得 %d", res.SplittableTurns)
	}
	// ★ 反例样本不得带正文（只给会话 id 前 8 位与长度）。
	for _, s := range res.Samples {
		if strings.Contains(s.Session, "q1") || strings.Contains(s.Session, "a1") {
			t.Errorf("样本里出现了内容片段：%+v", s)
		}
	}
}

// TestPrefixAnalyzeChainSemantics 钉住【链算法】的语义。
//
// ★★ 为什么链是本轮的核心：
//
//	「捞回第 N 轮 ⇒ 前 N 轮都能重建」**不成立** —— 真实累积体会被改写、被裁剪
//	（245 实测：content 级前缀成立率仅 51.6%）⇒ ⇒ 能还原多少轮，取决于
//	【有多少条累积体互为真前缀】⇒ ⇒ 必须按「链」算，不能按「最深一条」算。
//
// ★ 合成 4 条累积体（同一会话）：
//
//	C1 = [u1]
//	C2 = [u1, a1, u2]         ⇒ C1 是 C2 的真前缀（content OK）
//	C3 = [u1, aX, u2, a2]     ⇒ role 前缀成立但 content 被改写 ⇒ content 链断
//	C4 = [u1, aX, u2, a2, u3]  ⇒ C3 是 C4 的真前缀（content OK）
//
// ⇒ content 链：C1→C2（长 2）→ 断 → C3→C4（长 2）⇒ 最长 = 2
// ⇒ role 链：4 条全连 ⇒ 最长 = 4
// ⇒ 两条链的【覆盖轮数】都取链末端的 user 消息数 = 2
func TestPrefixAnalyzeChainSemantics(t *testing.T) {
	base := t.TempDir()
	c1 := recLine(t, "req-1", "gw_sess_c", `{"messages":[{"role":"user","content":"q1"}]}`)
	c2 := recLine(t, "req-2", "gw_sess_c", `{"messages":[{"role":"user","content":"q1"},{"role":"assistant","content":"a1"},{"role":"user","content":"q2"}]}`)
	c3 := recLine(t, "req-3", "gw_sess_c", `{"messages":[{"role":"user","content":"q1"},{"role":"assistant","content":"aX"},{"role":"user","content":"q2"},{"role":"assistant","content":"a2"}]}`)
	c4 := recLine(t, "req-4", "gw_sess_c", `{"messages":[{"role":"user","content":"q1"},{"role":"assistant","content":"aX"},{"role":"user","content":"q2"},{"role":"assistant","content":"a2"},{"role":"user","content":"q3"}]}`)
	writeBackup(t, base, "sessions-2026-09-03.jsonl.gz", buildGzip(t, []string{c1, c2, c3, c4}))

	res, err := analyzePrefix(context.Background(), base, "2026-09", "")
	if err != nil {
		t.Fatalf("分析失败：%v", err)
	}
	if res.Sessions != 1 || res.MultiTurn != 1 {
		t.Fatalf("会话数/多轮会话数应各为 1，实得 %d / %d", res.Sessions, res.MultiTurn)
	}
	// ★ 上界：最深一条累积体的 user 消息数 = 3（C4 有 3 条 user）。
	if res.SplittableTurns != 3 {
		t.Errorf("轮数上界应为 3，实得 %d", res.SplittableTurns)
	}
	// ★ content 链最长 2 条 ⇒ 覆盖轮数 = 链末端（C4）的 user 数 = 3？
	//   ★ 不对：content 链有两段（C1→C2 与 C3→C4），取【最深链末端】的 user 数。
	//   C2 的 user 数 = 2；C4 的 user 数 = 3 ⇒ 最深的那段末端是 C4 ⇒ 覆盖 3 轮。
	if res.ContentChainTurns != 3 {
		t.Errorf("content 链覆盖轮数应为 3，实得 %d", res.ContentChainTurns)
	}
	if res.MaxContentChainLen != 2 {
		t.Errorf("最长 content 链应为 2 条，实得 %d", res.MaxContentChainLen)
	}
	// ★ role 链 4 条全连 ⇒ 最长 4 条；覆盖轮数 = 链末端 C4 的 user 数 = 3。
	if res.RoleChainTurns != 3 {
		t.Errorf("role 链覆盖轮数应为 3，实得 %d", res.RoleChainTurns)
	}
	if res.MaxRoleChainLen != 4 {
		t.Errorf("最长 role 链应为 4 条，实得 %d", res.MaxRoleChainLen)
	}
	if res.ContentChainSess != 1 || res.RoleChainSess != 1 {
		t.Errorf("有链的会话数应各为 1，实得 content=%d role=%d",
			res.ContentChainSess, res.RoleChainSess)
	}
	// ★ 相邻对共 3 对：content 成立 2（C1→C2、C3→C4）、不成立 1（C2→C3）。
	if res.PairContentOK != 2 || res.PairContentBad != 1 {
		t.Errorf("content 前缀相邻对应为 ok=2 bad=1，实得 ok=%d bad=%d",
			res.PairContentOK, res.PairContentBad)
	}
	if res.PairRoleOK != 3 || res.PairRoleBroken != 0 {
		t.Errorf("role 前缀相邻对应为 ok=3 bad=0，实得 ok=%d bad=%d",
			res.PairRoleOK, res.PairRoleBroken)
	}
}

// TestPrefixAnalyzeSeparatesUnparsedFromSingleTurn 钉住一条区分：
//
//	「没解析出消息数组」≠「解析出来了但只有一条 user 消息」。
//
// ★ 若把两者混同 ⇒ 「没解析出来」会被读成「不是累积体」⇒
//
//	⇒ 多轮占比被系统性低估 ⇒ 恢复面被低估（这正是本轮反复出现的量具族坑）。
func TestPrefixAnalyzeSeparatesUnparsedFromSingleTurn(t *testing.T) {
	base := t.TempDir()
	// 一条正常单轮体 + 一条「正文不是可解析 JSON」的记录。
	ok := recLine(t, "req-ok", "gw_sess_u", `{"messages":[{"role":"user","content":"q"}]}`)
	bad := recLine(t, "req-bad", "gw_sess_v", `this-is-not-json`)
	writeBackup(t, base, "sessions-2026-09-02.jsonl.gz", buildGzip(t, []string{ok, bad}))

	res, err := analyzePrefix(context.Background(), base, "2026-09", "")
	if err != nil {
		t.Fatalf("分析失败：%v", err)
	}
	if res.Unparsed != 1 {
		t.Errorf("应恰有 1 条解析失败，实得 %d", res.Unparsed)
	}
	// ★ 正常形态的对照：确实解析出 1 条 user 消息 ⇒ 多轮计数不得把它算进去。
	if res.AccumRecords != 0 {
		t.Errorf("单轮体不应计入多轮，实得 %d", res.AccumRecords)
	}
	if res.MaxUserMsgs != 1 {
		t.Errorf("最大累积深度应为 1，实得 %d", res.MaxUserMsgs)
	}
}
