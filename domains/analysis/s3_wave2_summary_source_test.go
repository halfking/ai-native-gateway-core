//go:build !integration

package analysis

import (
	"strings"
	"testing"
)

// TestS3Wave2RequestSummarySource（2026-10-13 S3 波 2）钉住请求摘要读源的
// 会话族形状。原实现只读 _request_logs 母表（hot 行今天就漏、停写后恒空集）。
// 判据同层：requestSummaryTurnsSource 就是 loadRequestLogs 实际拼进 SQL 的源。
func TestS3Wave2RequestSummarySource(t *testing.T) {
	src := requestSummaryTurnsSource
	if !strings.Contains(src, "session_id = $1") {
		t.Error("请求摘要源缺少内推 session_id = $1（外层过滤投影列丢索引）")
	}
	if got := strings.Count(src, "FROM public.session_turns"); got != 2 {
		t.Errorf("源应有 hot+parent 两腿（实存 %d 处）", got)
	}
	// gw_session_id 投影必须在：调用点 WHERE rl.gw_session_id = $1 靠它。
	if !strings.Contains(src, "gw_session_id") {
		t.Error("源投影缺 gw_session_id（调用点谓词会 42703）")
	}
	// v1 底表不得回流：摘要读者是 A 形会话内读，v1 腿是回退腿的事。
	if strings.Contains(src, "_request_logs_hot") || strings.Contains(src, "_request_logs ") {
		t.Error("会话族源里混入了 v1 底表（守卫规则的 A/B 分界被破坏）")
	}
}
