// 钉住的行为：remaining gauge 的**父表臂**必须带 7 天 partition_date 下界，
// 且该下界**只**作用于父表臂；_hot 臂保持无窗。
//
// 这条不是性能偏好，是一条可执行的事实（2026-10-18 / 252 实测）：
//
//	无窗   → 60s statement_timeout 杀掉（另一次 126s），永远跑不完
//	有窗   → 7s 完成，返回 0
//
// 根因量级：session_turns_2026_09 分区 798,278 行 / 2.7GB，其中 330,544 行
// request_status IS NULL，且全是「823 之前的 v1 历史行、_request_logs 源行已
// 被 TTL 清除、结构性无法回填」那一族（与本文件候选查询 7 天下界的论证同一批）。
// 154 上该 gauge 在 5.5 小时内失败 20 次、成功 0 次。
//
// 为什么单列一条判据，而不是并进 CoversBothSurfaces：那个测试守的是
// **两个面都要查**（退役前仍存在的行不能漏），本条守的是**父表臂要加窗**
// （否则空转回来）。两者方向相反——正因为相反，才需要各自独立钉住：
// 把它们合并成一条，会得到一个「为了满足其中一条而破坏另一条」的判据。
package v2

import (
	"strings"
	"testing"
)

const remainingWindowPredicate = "t.partition_date >= (now() - interval '7 days')::date"

// splitRemainingSQL 按 FROM public.session_turns_hot t 把两臂切开。
func splitRemainingSQL(t *testing.T) (parentArm, hotArm string) {
	t.Helper()
	q := sessionRequestStatusRemainingSQL
	i := strings.Index(q, "FROM public.session_turns_hot t")
	if i < 0 {
		t.Fatal("remaining SQL no longer has a session_turns_hot arm; " +
			"this test must be rewritten together with the SQL it pins")
	}
	return q[:i], q[i:]
}

// TestSessionRequestStatusRemaining_ParentArmIsWindowed 钉住父表臂的 7 天下界。
//
// 没有它，一次「让两条 SQL 保持一致」的顺手统一就能把空转放回来，而且不会有
// 任何测试变红——这正是 R31 那次「优化计划」以为解决了问题的同类盲区。
func TestSessionRequestStatusRemaining_ParentArmIsWindowed(t *testing.T) {
	parentArm, _ := splitRemainingSQL(t)
	if !strings.Contains(parentArm, remainingWindowPredicate) {
		t.Errorf("remaining gauge parent arm lost its 7-day floor %q.\n"+
			"Measured on 252: window-less this query never completes (60s and 126s\n"+
			"statement_timeout kills); windowed it returns in 7s. session_turns_2026_09\n"+
			"alone holds 330,544 structurally-unfillable NULL rows, so the un-windowed\n"+
			"form burns a full budget every tick on a production PG and has never\n"+
			"once returned a value.",
			remainingWindowPredicate)
	}
}

// TestSessionRequestStatusRemaining_HotArmStaysWindowless 钉住另一半：_hot 臂
// **不许**加窗。
//
// 这是最容易「好心办坏事」的地方。看到父表臂加了窗，很容易顺手把 _hot 也加上，
// 理由听起来也对（保持一致 / 减少扫描）。但那样会直接毁掉 gauge 存在的意义：
// 它是 D9 clause-4 的发布闸门，要回答「退役后仍然存在的行」——而 pre-823 进程
// 往 _hot 写的 NULL 行正是那一族（见本文件 sessionRequestStatusCandidateSQL 上方
// 的说明与 admin/session_family_two_surface_test.go 的登记）。_hot 只有约 6k 行，
// 无窗扫描本就便宜。
func TestSessionRequestStatusRemaining_HotArmStaysWindowless(t *testing.T) {
	_, hotArm := splitRemainingSQL(t)
	if strings.Contains(hotArm, "partition_date") {
		t.Errorf("remaining gauge _hot arm must NOT take the partition_date window.\n" +
			"Windowing it would hide exactly the rows the release gate exists to\n" +
			"count: NULL labels written into _hot by a still-running pre-823 binary,\n" +
			"which promote has not rotated into the parent yet. The parent arm may be\n" +
			"windowed because its out-of-window rows are provably unfillable; the hot\n" +
			"arm has no such guarantee. _hot is ~6k rows — scanning it is not a cost.")
	}
}

// TestSessionRequestStatusRemaining_WindowAppliesToBothPredicateSites 前提在场：
// 两臂各自的 NULL/NOT NULL 谓词都还在。
//
// 单独一条「下界存在」的判据会假绿：下界可以加在一臂上、而另一臂的谓词被改掉，
// 而那正是 CoversBothSurfaces 已经守的东西。这里再钉一次，是为了让
// "加窗" 这个改动**单独**就能被验，而不是依赖另一条测试恰好覆盖。
func TestSessionRequestStatusRemaining_WindowAppliesToBothPredicateSites(t *testing.T) {
	parentArm, hotArm := splitRemainingSQL(t)
	if !strings.Contains(parentArm, "t.request_status IS NULL") ||
		!strings.Contains(parentArm, "l.request_status IS NOT NULL") {
		t.Error("parent arm lost one of its predicates while being windowed")
	}
	if !strings.Contains(hotArm, "t.request_status IS NULL") ||
		!strings.Contains(hotArm, "l.request_status IS NOT NULL") {
		t.Error("hot arm lost one of its predicates")
	}
}
