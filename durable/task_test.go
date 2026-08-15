package durable

import "testing"

// TestCommitStateRank：commit_state 单调序 none < metadata < content <
// tool_call < terminal（doc 18 §11.3：一旦进入 content/tool_call/terminal，
// 禁止迁移回 runnable；checkpoint 只允许前进）。
func TestCommitStateRank(t *testing.T) {
	ordered := []CommitState{
		CommitStateNone,
		CommitStateMetadata,
		CommitStateContent,
		CommitStateToolCall,
		CommitStateTerminal,
	}
	for i := 0; i+1 < len(ordered); i++ {
		if CommitStateRank(ordered[i]) >= CommitStateRank(ordered[i+1]) {
			t.Errorf("rank(%s)=%d must be < rank(%s)=%d",
				ordered[i], CommitStateRank(ordered[i]), ordered[i+1], CommitStateRank(ordered[i+1]))
		}
	}
}

// TestIsRunnableCommitState：只有 none/metadata 是 runnable claim 的全局
// 门禁（doc 18 §11.3：所有普通 execution claim 不依赖 status 分支）。
func TestIsRunnableCommitState(t *testing.T) {
	cases := map[CommitState]bool{
		CommitStateNone:     true,
		CommitStateMetadata: true,
		CommitStateContent:  false,
		CommitStateToolCall: false,
		CommitStateTerminal: false,
	}
	for cs, want := range cases {
		if got := IsRunnableCommitState(cs); got != want {
			t.Errorf("IsRunnableCommitState(%s) = %v, want %v", cs, got, want)
		}
	}
}

// TestIsTerminalStatus：终态集合与可被 claim 的 runnable 集合。
func TestIsTerminalStatus(t *testing.T) {
	terminal := []Status{
		StatusCompleted, StatusFailed, StatusExpired, StatusCanceled,
	}
	for _, s := range terminal {
		if !IsTerminalStatus(s) {
			t.Errorf("IsTerminalStatus(%s) = false, want true", s)
		}
	}
	nonTerminal := []Status{
		StatusRunning, StatusWaitingRecovery, StatusRetryScheduled, StatusResumeSafetyBlocked,
	}
	for _, s := range nonTerminal {
		if IsTerminalStatus(s) {
			t.Errorf("IsTerminalStatus(%s) = true, want false", s)
		}
	}
}

// TestCanTransition：状态机合法迁移矩阵（doc 18 §11.3）。
// 终态不可回退；resume_safety_blocked 是吸态（只允许人工/对账处理）；
// runnable 之间的迁移由 claim/reschedule/reaper 触发。
func TestCanTransition(t *testing.T) {
	legal := []struct{ from, to Status }{
		{StatusRetryScheduled, StatusRunning},      // claim
		{StatusWaitingRecovery, StatusRunning},     // claim
		{StatusRunning, StatusRetryScheduled},      // reschedule（commit_state 门禁在 SQL 层）
		{StatusRunning, StatusWaitingRecovery},     // 连接属性事件不改 status；此迁移供断连等待
		{StatusRunning, StatusCompleted},           // 终态
		{StatusRunning, StatusFailed},              // 终态
		{StatusRunning, StatusExpired},             // deadline reaper
		{StatusRetryScheduled, StatusExpired},      // deadline reaper
		{StatusWaitingRecovery, StatusExpired},     // deadline reaper
		{StatusRunning, StatusCanceled},            // 管理员/租户取消
		{StatusRunning, StatusResumeSafetyBlocked}, // safety reaper
		{StatusRetryScheduled, StatusResumeSafetyBlocked},
		{StatusWaitingRecovery, StatusResumeSafetyBlocked},
	}
	for _, tr := range legal {
		if !CanTransition(tr.from, tr.to) {
			t.Errorf("CanTransition(%s -> %s) = false, want true", tr.from, tr.to)
		}
	}
	illegal := []struct{ from, to Status }{
		{StatusCompleted, StatusRunning},
		{StatusCompleted, StatusRetryScheduled},
		{StatusFailed, StatusRunning},
		{StatusExpired, StatusRunning},
		{StatusCanceled, StatusRunning},
		{StatusResumeSafetyBlocked, StatusRunning},
		{StatusResumeSafetyBlocked, StatusRetryScheduled},
		{StatusCompleted, StatusFailed}, // 终态不可回退/互换
	}
	for _, tr := range illegal {
		if CanTransition(tr.from, tr.to) {
			t.Errorf("CanTransition(%s -> %s) = true, want false", tr.from, tr.to)
		}
	}
}
