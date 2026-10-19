package bg

import (
	"strings"
	"testing"
	"time"
)

// TestStalenessLastRequestSurfaceExcludesSessionTurns 是 2026-10-18 24h 审计
// 轮新增的形态门：把 staleness 的 lastRequest 表钉到不含 session_turns 的
// request_logs 表面。原来的 `request_logs_with_current_month` 是
// session_turns_hot ∪ session_turns ∪ request_logs 三腿 UNION，S4 停写
// 后 _request_logs_hot 冻结但 session_turns 仍写 ⇒ 该视图 max(ts) 恒新鲜
// ⇒ staleness 假阳性。本地实测 22h+。本门通过直接判 SQL 常量来防回退。
func TestStalenessLastRequestSurfaceExcludesSessionTurns(t *testing.T) {
	if strings.Contains(checkStalenessLastRequestSurface, "session_turns") {
		t.Fatalf("checkStalenessLastRequestSurface %q 含 session_turns，"+
			"会与 S4 停写后的 session_log 写腿假阳性耦合。"+
			"只允许 read 端 request_logs 形态（不含 session_turns UNION）。",
			checkStalenessLastRequestSurface)
	}
	// 钉具体形态：必须以 request_logs 开头（与 failure writer 入口同源）。
	if !strings.HasPrefix(checkStalenessLastRequestSurface, "request_logs") {
		t.Fatalf("checkStalenessLastRequestSurface %q 必须以 request_logs 开头，"+
			"否则 staleness 与 failure writer 入口会再次不同源。",
			checkStalenessLastRequestSurface)
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
