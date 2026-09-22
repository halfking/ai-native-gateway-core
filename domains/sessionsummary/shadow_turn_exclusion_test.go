package sessionsummary

import (
	"strings"
	"testing"
)

// Wave 1 A5 回归钉桩：网关影子轮（origin_actor LIKE 'goal-%'，见
// streaming.response_interceptor_helpers followUpSourceActor）不得进入
// 会话拼装链（summary message sources）。镜像/对账行保留在 session_turns，
// 只在装配型读者排除（设计 §5.12、方案 18 §3"打标不排除、过滤权在查询侧"）。
//
// 这是对 SQL 常量的契约测试：两个 MessageSource 的取材 WHERE 必须携带
// origin_actor 排除子句；若未来改写查询，请保留该语义并更新本测试。

func TestSessionAssemblyQueriesExcludeGoalShadowTurns(t *testing.T) {
	if !strings.Contains(v2SessionBodiesBaseQuery, "origin_actor") ||
		!strings.Contains(v2SessionBodiesBaseQuery, "NOT LIKE 'goal-%'") {
		t.Errorf("v2SessionBodiesBaseQuery lost the goal-%% shadow-turn exclusion:\n%s", v2SessionBodiesBaseQuery)
	}
	if !strings.Contains(sessionTurnDigestQuery, "origin_actor") ||
		!strings.Contains(sessionTurnDigestQuery, "NOT LIKE 'goal-%'") {
		t.Errorf("sessionTurnDigestQuery lost the goal-%% shadow-turn exclusion:\n%s", sessionTurnDigestQuery)
	}
}
