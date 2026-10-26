// 钉住的行为：**会扫 session_turns** 的那条查询必须带 7 天 partition_date 下界。
//
// 这条不是性能偏好，是一条可执行的事实（2026-10-18 / 252 实测）：
//
//	无窗   → 60s statement_timeout 杀掉（另一次 126s），永远跑不完
//	有窗   → 7s 完成，返回 0
//
// 根因量级：session_turns_2026_09 分区 798,278 行 / 2.7GB，其中 330,544 行
// request_status IS NULL，且全是「823 之前的 v1 历史行、_request_logs 源行已
// 被 TTL 清除、结构性无法回填」那一族。154 上该 gauge 在 5.5 小时内失败 20 次、
// 成功 0 次。
//
// ★ 2026-10-21（R65 轮）：本文件的守卫**改瞄**了。
// 原来它守的是 remaining gauge 的两条臂（父表臂加窗 / _hot 臂不加窗）。迁移 890
// 把 remaining 查询变成常量 `SELECT 0::bigint` ⇒ 两条臂都不存在了 ⇒ 那三条
// 判据的**前提**消失（不是它们错了，是被守的对象没了）。
//
// 但这次改瞄**不是把守卫删掉**：R31 测出来的性能事实仍然成立，而且**还挂在一
// 条会扫表的语句上**——候选查询 sessionRequestStatusCandidateSQL。它同样要按
// `t.request_status IS NULL` 走 session_turns 的 pkey（该谓词没有部分索引），
// 所以「7 天下界」是它唯一能把扫描钉在有界区间里的东西。
//
// ⇒ 今天它被 `AND FALSE` 折叠掉了（源没了 ⇒ 候选集恒空 ⇒ planner 直接返回空
// Result，不扫表）⇒ 窗此刻是**空转**的。但它必须留在文本里：
//
//	· 折叠是「源没了」这一个事实；窗是「有源时怎么扫」的规格；
//	· 折叠哪天被摘掉（换新标签源、或有人「顺手」去掉 AND FALSE），窗必须已经在位；
//	· 少了窗 ⇒ 回到 R31 的形态（每次 tick 烧满预算、永远拿不到值）。
//
// ⚠ 反过来也别「好心」给 remaining 查询加窗：它现在不扫任何表（常量 0），
// 加窗既无意义，也会让人误以为那个量表还在守着什么。
package v2

import (
	"strings"
	"testing"
)

const remainingWindowPredicate = "t.partition_date >= (now() - interval '7 days')::date"

// TestSessionRequestStatusCandidate_KeepsSevenDayFloor 钉住候选查询的 7 天下界。
//
// 没有它，一次「让两条 SQL 保持一致」的顺手统一就能把空转放回来，而且不会有
// 任何测试变红——这正是 R31 那次「优化计划」以为解决了问题的同类盲区。
func TestSessionRequestStatusCandidate_KeepsSevenDayFloor(t *testing.T) {
	q := executableSQL(sessionRequestStatusCandidateSQL)
	if !strings.Contains(q, remainingWindowPredicate) {
		t.Errorf("candidate query lost its 7-day floor %q.\n"+
			"Measured on 252: window-less this shape never completes (60s and 126s\n"+
			"statement_timeout kills); windowed it returns in 7s. session_turns_2026_09\n"+
			"alone holds 330,544 structurally-unfillable NULL rows, so the un-windowed\n"+
			"form burns a full budget every tick on a production PG and has never\n"+
			"once returned a value.\n"+
			"Today the query is folded away by AND FALSE, so the window is inert —\n"+
			"but it is the SPEC for the day the source comes back, and dropping it\n"+
			"is exactly the accident this guard exists to make loud.",
			remainingWindowPredicate)
	}
}

// TestSessionRequestStatusCandidate_KeepsKeysetShape 钉住另一半：扫描必须是
// 键集分页 + LIMIT，不是「从头扫到尾」。
//
// 键集分页（t.id > $1）让每一批只走一小段，而不是每次从 0 开始重扫 1.69M 行。
func TestSessionRequestStatusCandidate_KeepsKeysetShape(t *testing.T) {
	q := executableSQL(sessionRequestStatusCandidateSQL)
	for _, want := range []string{"WHERE t.id > $1", "ORDER BY t.id", "LIMIT $2"} {
		if !strings.Contains(q, want) {
			t.Errorf("candidate query missing %q — the scan must stay keyset-paginated, "+
				"otherwise every batch re-walks the table from the head", want)
		}
	}
}

// TestSessionRequestStatusCandidate_KeepsNullLabelPredicate 前提在场：唯一能让
// 一行成为候选的谓词还在。
//
// 少一条 AND FALSE 之类的东西是「看不见的变化」；少掉这个谓词则是「每一行都成
// 候选」，两者都必须变红。
func TestSessionRequestStatusCandidate_KeepsNullLabelPredicate(t *testing.T) {
	q := executableSQL(sessionRequestStatusCandidateSQL)
	if !strings.Contains(q, "t.request_status IS NULL") {
		t.Error("candidate query lost the NULL-label predicate — every labelled row " +
			"would become a candidate again")
	}
}

// TestSessionRequestStatusRemaining_NeedsNoWindowBecauseItScansNothing 钉住
// 「remaining 查询现在不扫表」。
//
// 迁移 890 之后它是常量 0：一个表都不碰 ⇒ 既不需要 7 天下界，也不可能再烧掉
// R31 那份预算。这条判据的价值在于**双向**：如果哪天有人给它加回一个表扫描，
// 却没有同时把 7 天下界加回去，这条会红（而光靠「窗还在」的判据不会发现）。
func TestSessionRequestStatusRemaining_NeedsNoWindowBecauseItScansNothing(t *testing.T) {
	q := executableSQL(sessionRequestStatusRemainingSQL)
	if !statusRemainingConstantZeroRe.MatchString(q) {
		t.Errorf("remaining SQL is no longer the structural zero, so it may scan again — "+
			"it must then carry the 7-day floor %q, or the R31 failure mode returns "+
			"(60s+ statement_timeout kills, gauge stuck at -1).\n--- got ---\n%s",
			remainingWindowPredicate, q)
	}
	if strings.Contains(q, "session_turns") {
		t.Errorf("remaining SQL references a session_turns surface, so it scans again — "+
			"the D9 release gate must be re-derived before that happens.\n--- got ---\n%s", q)
	}
}

// ── retired premise ─────────────────────────────────────────────────────────
//
// TestSessionRequestStatusRemaining_HotArmStaysWindowless 与
// TestSessionRequestStatusRemaining_WindowAppliesToBothPredicateSites 于
// 2026-10-21（R65 轮）**移除**，不是被遗忘：
//
//   - 它们守的是 remaining gauge 的两条臂（父表臂 / _hot 臂）的谓词与窗。
//   - 迁移 890 把 remaining 查询变成常量 0 ⇒ **两条臂都不存在了** ⇒ 被守的对象
//     没了，「窗只作用于父表臂」「两臂谓词对称」这两条命题都不再有意义。
//   - 它们的**实质**没有丢：R31 测出来的「有窗才跑得完」这件事，改由本文件
//     的 TestSessionRequestStatusCandidate_KeepsSevenDayFloor 钉在**候选查询**
//     上 —— 那条查询至今仍在扫 session_turns（虽然被 AND FALSE 折叠）。
//   - _hot 臂「不许加窗」的那条担心（pre-823 进程写进 _hot 的 NULL 行是退役时会
//     丢的那一族）随整个量表一起失效：量表现在恒为 0，恒为绿灯，**根本不能
//     再当放行信号**。这一事实由
//     TestSessionRequestStatusBackfill_RemainingSQLNoLongerCoversAnySurface
//     钉住。
//
// ⚠ 若将来出现新的标签源，remaining 查询会重新变成一次真正的扫描 ⇒
//   1. 7 天下界必须先加回来（否则 R31 的空转立刻回来）；
//   2. 两个面（父表 / _hot）要重新推导一次覆盖范围；
//   3. D9 放行门要**重新推导**，不能直接复活旧断言。
