package ursm_test

// pg17-vacuum-bloat.sh 的「标识符引用」行为门（2026-10-19，12h 修订审计轮）
//
// # 这道门要拦的事故
//
// BLOAT_SQL 原本把 `schemaname||'.'||relname` 当作 tname 一路传下去，
// 然后直接插进两处**要发给 PG 执行的**语句：
//
//	pgstattuple_approx('$tname')
//	VACUUM FULL ${tname}
//
// 裸拼的名字只在「所有标识符都是朴素小写单段」时才恰好合法。
// 一旦 schema 名或表名含引号 / 大写 / 空格，拼出来的就是坏 SQL。
//
// # 为什么这条不升级成安全事件
//
// 后果方向是 **fail-safe**：psql 报错 ⇒ 量不出来 ⇒ 上一轮确认闸走
// 「量不出来 ⇒ SKIP」分支 ⇒ 不做 VACUUM FULL。所以现状不是「会错做」，
// 而是「会静默地永远不做」。
//
// ★ 但静默永久失效正是 §117/§118 刚刚花两轮才拆掉的那个形态：
//   列存表进候选 → pgstattuple_approx 必失败 → 被 fail-safe 跳过，
//   日志只写「量不出来」，读起来与「扩展没装」一模一样。
//   ⇒ 与其等它在某个朴素假设被打破的那天静默失效，不如现在钉住。
//
// # 为什么不用子串断言
//
// 与同文件另外两道门同理：注入假 docker，让脚本真的跑到判定分支，
// 再断言它**实际发出了哪条 VACUUM FULL**。

import (
	"os"
	"strings"
	"testing"
)

// TestVacuumBloatExecutesQuotedIdentifier 钉住「执行用名必须是引用过的形态」：
// 候选集带 qname（第 4 列）时，发出的必须是 qname 而不是裸拼的 tname。
func TestVacuumBloatExecutesQuotedIdentifier(t *testing.T) {
	skipVacuumWindows(t)

	// tname 是给人看的裸名，qname 是 format('%I.%I') 的产物。
	targets := `weird name|993697792|52.3|"public"."weird name"`
	trace, logBody := runVacuumBloat(t, targets, "55.0", "300000000")

	requireVacuumCalls(t, trace)
	if !strings.Contains(trace, `VACUUM FULL "public"."weird name"`) {
		t.Fatalf("★ 执行 VACUUM FULL 时没有用引用过的 qname —— 裸名会让 psql 报错并静默永久 SKIP\ntrace:\n%s\nlog:\n%s", trace, logBody)
	}
	if strings.Contains(trace, "VACUUM FULL weird name") {
		t.Fatalf("★ 发出了未引用的裸名 VACUUM FULL weird name —— 这正是要拦的形态\ntrace:\n%s", trace)
	}
}

// TestVacuumBloatMeasuresQuotedIdentifier 同一条路径的前半段：
// 确认闸的 pgstattuple_approx 也必须拿到 qname，否则「测量」这一步就已经坏了，
// 后面那条 VACUUM FULL 即便正确也会因为量不出来而被跳过。
func TestVacuumBloatMeasuresQuotedIdentifier(t *testing.T) {
	skipVacuumWindows(t)

	targets := `weird name|993697792|52.3|"public"."weird name"`
	trace, _ := runVacuumBloat(t, targets, "55.0", "300000000")

	requireVacuumCalls(t, trace)
	if !strings.Contains(trace, `pgstattuple_approx('"public"."weird name"')`) {
		t.Fatalf("★ 确认闸没有用 qname 调 pgstattuple_approx —— 实测这一步会直接失败\ntrace:\n%s", trace)
	}
}

// TestVacuumBloatFallsBackWhenQnameAbsent 钉住向后兼容：
// 候选集只有 3 列（tname|size|dead_pct）时必须回退到 tname 而不是执行空表名。
// 这条同时守住 §117 那几道既有门 —— 它们喂的就是 3 列候选集。
func TestVacuumBloatFallsBackWhenQnameAbsent(t *testing.T) {
	skipVacuumWindows(t)

	trace, _ := runVacuumBloat(t, "public.session_summaries|993697792|52.3", "55.0", "300000000")

	requireVacuumCalls(t, trace)
	if !strings.Contains(trace, "VACUUM FULL public.session_summaries") {
		t.Fatalf("候选集缺 qname 列时必须回退到 tname，而不是发出空/坏的表名\ntrace:\n%s", trace)
	}
}

// TestVacuumBloatCandidateSQLQuotesIdentifiers 钉住候选层本身：
// qname 必须由 format('%I.%I') 生成，而不是又一次裸拼。
// 这条是上面三条的**前提** —— 循环再守规矩，SQL 不产出 qname 也没用。
func TestVacuumBloatCandidateSQLQuotesIdentifiers(t *testing.T) {
	b, err := os.ReadFile(vacuumBloatScript)
	if err != nil {
		t.Fatalf("读脚本: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, "format('%I.%I', s.schemaname, s.relname)") {
		t.Fatalf("候选 SQL 必须用 format('%%I.%%I', …) 产出引用过的 qname")
	}
}
