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
	// v1 回退必须走 canonical 视图而不是裸 hot 表：回退服务的正是
	// 镜像前历史会话，那些行早已被 promote 出 hot。
	if !strings.Contains(v1FallbackCountSQL, "FROM request_logs_with_current_month") {
		t.Error("v1 回退腿应读 canonical 视图（裸 hot 表数不到 promote 排空的历史行）")
	}
	if !strings.Contains(v1FallbackCountSQL, "gw_session_id = $1 AND success = TRUE") {
		t.Error("v1 回退腿口径谓词丢失")
	}
}
