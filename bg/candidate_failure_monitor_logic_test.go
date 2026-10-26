package bg

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestStalenessLastRequestSurfaceIsSessionFamilyWithProbeExclusion 是
// 2026-10-18 24h 审计轮那道门的 **890 后继**。
//
// 原门钉的是「staleness 的 lastRequest 源不得含 session_turns」——前提是
// **存在一条非 session 的请求面**（v1-only 视图
// request_logs_with_current_month_without_request_class_due_at），靠它把
// 停写期探针写的那批行挡在外面（实测假阳性 22h+）。
//
// ★ 2026-10-20（迁移 890，v1 族退役）：那个 v1-only 视图随 890 一起被 DROP
// ⇒ **只剩一个请求面** ⇒ 「换一个族」这条路不存在了 ⇒ 挡探针必须**显式**做。
// ⇒ 本门改成钉那条**等价纪律的新形态**：源是会话族热腿，**且** 查询带会话族
// 探针排除谓词（origin_stage / task_type / origin_actor 三臂）。
func TestStalenessLastRequestSurfaceIsSessionFamilyWithProbeExclusion(t *testing.T) {
	src := checkStalenessLastRequestSurface
	// 源必须是会话族**热腿**：窗口只有 5 分钟，而会话族热腿保留期 7 天
	// ⇒ 窗内的行必然还在热表里，冷腿不可能持有更新的行。
	if !strings.Contains(src, "public.session_turns_hot") {
		t.Fatalf("checkStalenessLastRequestSurface 必须读会话族热腿；got:\n%s", src)
	}
	// 只读冷父表会漏掉**最新** 5 分钟的活动（写方只写 hot，冷行由 promote
	// 异步搬走，边界随 promote 节奏移动）。
	// ⚠ 边界匹配："public.session_turns t" 与 "public.session_turns_hot t"
	// 不是同一条（hot 腿里 "session_turns" 后面跟的是 "_hot"）⇒ 不会误判。
	if strings.Contains(src, "public.session_turns t") {
		t.Fatalf("checkStalenessLastRequestSurface 只读会话族冷父表会漏掉最新 5 分钟的活动；got:\n%s", src)
	}
	// 不得引用已退役的 v1 族（890 已 DROP ⇒ 整条语句 42P01）。
	if strings.Contains(src, "_request_logs") {
		t.Fatalf("checkStalenessLastRequestSurface 不得引用退役的 v1 族；got:\n%s", src)
	}

	// 探针排除谓词是**必需**的，不是锦上添花：v1-only 腿原来顺带把探针流量
	// 挡在了外面（停写期会话族照写的那批行是探针写的 ⇒ 假阳性），890 之后
	// 只有一个请求面，必须显式排探针才能保住同一条纪律。
	monSrc := mustReadSourceBg(t, "candidate_failure_monitor.go")
	const anchor = "func (m *CandidateFailureMonitor) checkStaleness("
	i := strings.Index(monSrc, anchor)
	if i < 0 {
		t.Fatal("checkStaleness not found in candidate_failure_monitor.go")
	}
	body := monSrc[i:]
	if j := strings.Index(body, "\nfunc "); j >= 0 {
		body = body[:j]
	}
	if !strings.Contains(body, `fmt.Sprintf(probeTrafficExclusionPredicateSession, "rl", "rl", "rl")`) {
		t.Errorf("checkStaleness 必须带会话族探针排除谓词（否则探针流量会让 staleness 假阳性）：\n%s", body)
	}
	// 正向对照：这条谓词真的在排除探针（三条臂都在），而不是恒真。
	// ⚠ 没有这一条，上面那句「必须带谓词」会被「带了个空壳」骗过去。
	rendered := fmt.Sprintf(probeTrafficExclusionPredicateSession, "rl", "rl", "rl")
	for _, arm := range []string{"rl.origin_stage", "rl.task_type", "rl.origin_actor"} {
		if !strings.Contains(rendered, arm) {
			t.Errorf("会话族探针排除谓词缺 %s 臂：%s", arm, rendered)
		}
	}
}

func TestFailureLogIsStale(t *testing.T) {
	now := time.Date(2026, time.July, 14, 1, 0, 0, 0, time.UTC)
	fresh := now.Add(-5 * time.Minute)
	old := now.Add(-time.Hour)

	tests := []struct {
		name        string
		lastFailure *time.Time
		lastRequest *time.Time
		want        bool
	}{
		{name: "quiet gateway does not alert", lastFailure: &old, lastRequest: nil, want: false},
		{name: "recent failure log is healthy", lastFailure: &fresh, lastRequest: &fresh, want: false},
		{name: "old failure log is stale", lastFailure: &old, lastRequest: &fresh, want: true},
		{name: "missing failure log is stale during traffic", lastFailure: nil, lastRequest: &fresh, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := failureLogIsStale(tt.lastFailure, tt.lastRequest, now, 30*time.Minute); got != tt.want {
				t.Fatalf("failureLogIsStale() = %v, want %v", got, tt.want)
			}
		})
	}
}
