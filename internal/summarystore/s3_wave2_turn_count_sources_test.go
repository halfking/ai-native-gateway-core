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
	// v1 回退必须覆盖 promote 排空的历史行（2026-10-13 252 审计轮演进：从
	// canonical 视图收窄到 _request_logs 分区父表——父表 hot∪各月分区都在，
	// promote 排空的行照数；且回退腿只在会话族腿为 0 时执行，此时本会话
	// 在 session_turns 无行可反连接，视图 turns 两臂 + details join 是
	// leg 1 刚证明为空的纯重复扫描，252 实测单次 1.0-5.6s × 29 次/10.2h）。
	// 钉子从「必须视图」演进为「必须父表直读」双向钉死。
	if !strings.Contains(v1FallbackCountSQL, "FROM _request_logs\n") &&
		!strings.Contains(v1FallbackCountSQL, "FROM _request_logs ") {
		t.Error("v1 回退腿应直读 _request_logs 分区父表（hot∪分区全覆盖，不落裸 hot 缺口）")
	}
	if strings.Contains(v1FallbackCountSQL, "request_logs_with_current_month") {
		t.Error("v1 回退腿不应再走 canonical 视图（turns 臂对回退场景是纯重复扫描，2026-10-13 审计轮）")
	}
	if strings.Contains(v1FallbackCountSQL, "_request_logs_hot") {
		t.Error("v1 回退腿禁止退回裸 hot 表（promote 排空的历史行会漏计）")
	}
	if !strings.Contains(v1FallbackCountSQL, "gw_session_id = $1 AND success = TRUE") {
		t.Error("v1 回退腿口径谓词丢失")
	}
}
