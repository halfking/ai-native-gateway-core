package governance

import "testing"

func TestSessionState_IsTerminal(t *testing.T) {
	terminal := map[SessionState]bool{
		StateBlocked:    true,
		StateTerminated: true,
	}
	for s := range AllSessionStatesAsMap() {
		got := s.IsTerminal()
		want := terminal[s]
		if got != want {
			t.Errorf("IsTerminal(%s) = %v, want %v", s, got, want)
		}
	}
}

func TestSessionState_CanTransitionTo(t *testing.T) {
	cases := []struct {
		from   SessionState
		to     SessionState
		allow  bool
		reason string
	}{
		// new
		{StateNew, StateStreaming, true, "new → streaming"},
		{StateNew, StateMutated, true, "new → mutated"},
		{StateNew, StateBlocked, true, "new → blocked"},
		{StateNew, StateTerminated, true, "new → terminated"},
		{StateNew, StatePendingTool, false, "new 不能直接挂起 pending"},
		{StateNew, StateContinued, false, "new 不能直接 continued"},

		// streaming
		{StateStreaming, StatePendingTool, true, "streaming → pending_tool"},
		{StateStreaming, StatePendingApproval, true, "streaming → pending_approval"},
		{StateStreaming, StatePendingAnalysis, true, "streaming → pending_analysis"},
		{StateStreaming, StateContinued, true, "streaming → continued"},
		{StateStreaming, StateBlocked, true, "streaming → blocked"},
		{StateStreaming, StateTerminated, true, "streaming → terminated"},
		{StateStreaming, StateMutated, false, "streaming 不能直接 mutated"},

		// pending_* 可以到 continued / mutated / blocked / terminated
		{StatePendingTool, StateContinued, true, "pending_tool → continued"},
		{StatePendingTool, StateMutated, true, "pending_tool → mutated"},
		{StatePendingTool, StateBlocked, true, "pending_tool → blocked"},
		{StatePendingTool, StateTerminated, true, "pending_tool → terminated"},
		{StatePendingTool, StatePendingApproval, false, "pending_tool 不能跨挂起"},
		{StatePendingTool, StateStreaming, false, "pending_tool 不能回到 streaming"},

		// mutated → streaming
		{StateMutated, StateStreaming, true, "mutated → streaming"},
		{StateMutated, StateTerminated, false, "mutated 不能直接 terminated"},
		{StateMutated, StateContinued, false, "mutated 不能直接 continued"},

		// continued → streaming | terminated
		{StateContinued, StateStreaming, true, "continued → streaming"},
		{StateContinued, StateTerminated, true, "continued → terminated"},
		{StateContinued, StatePendingTool, false, "continued 不能直接 pending"},

		// 终态不可迁移
		{StateBlocked, StateStreaming, false, "blocked 不可迁移"},
		{StateBlocked, StateContinued, false, "blocked 不可迁移"},
		{StateTerminated, StateStreaming, false, "terminated 不可迁移"},
		{StateTerminated, StateMutated, false, "terminated 不可迁移"},
	}

	for _, c := range cases {
		got := c.from.CanTransitionTo(c.to)
		if got != c.allow {
			t.Errorf("%s: %s.CanTransitionTo(%s) = %v, want %v",
				c.reason, c.from, c.to, got, c.allow)
		}
	}
}

func TestSessionState_SelfTransition(t *testing.T) {
	// 自环全部不合法（避免状态卡死）
	all := AllSessionStates()
	for _, s := range all {
		if s.CanTransitionTo(s) {
			t.Errorf("self transition should be disallowed: %s → %s", s, s)
		}
	}
}

func TestAllSessionStates_NoDuplicates(t *testing.T) {
	seen := map[SessionState]bool{}
	for _, s := range AllSessionStates() {
		if seen[s] {
			t.Errorf("duplicate state: %s", s)
		}
		seen[s] = true
	}
	if len(seen) == 0 {
		t.Fatal("AllSessionStates returned empty")
	}
}

// AllSessionStatesAsMap 仅测试用：导出全部状态为 map 便于 IsTerminal 全覆盖断言。
func AllSessionStatesAsMap() map[SessionState]struct{} {
	out := map[SessionState]struct{}{}
	for _, s := range AllSessionStates() {
		out[s] = struct{}{}
	}
	return out
}
