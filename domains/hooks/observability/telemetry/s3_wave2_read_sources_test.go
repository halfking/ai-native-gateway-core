//go:build !integration

package telemetry

import (
	"strings"
	"testing"
)

// TestS3Wave2StopWriteReadSources（2026-10-13 S3 波 2）钉住停写切换后本包
// 两个 v1 直读面的源形状。判据同层：断言驱动包级 SQL 变量——它们就是
// FindRecentGatewaySession / lookupTurnNumber 实际发给 PostgreSQL 的文本。
func TestS3Wave2StopWriteReadSources(t *testing.T) {
	t.Run("FindRecentGatewaySession 双热腿", func(t *testing.T) {
		q := findRecentGatewaySessionSQL
		for _, leg := range []string{"FROM public.session_turns_hot", "FROM public._request_logs_hot"} {
			if !strings.Contains(q, leg) {
				t.Errorf("会话粘滞查找缺少 %q：停写后（或回滚后）的一侧将查不到会话", leg)
			}
		}
		// 会话腿 api_key_id 是 TEXT（707 S1a），必须显式 ::text 对齐 int 参数。
		if !strings.Contains(q, "t.api_key_id = $2::text") {
			t.Error("会话腿 api_key_id 缺 ::text 对齐（TEXT 列与 int 参数直接比较会 42883）")
		}
		// 粘滞语义：两腿窗口/排序/限幅必须一致，取最新一条。
		if got := strings.Count(q, "NOW() - ($4 * INTERVAL '1 second')"); got != 2 {
			t.Errorf("两腿窗口谓词应各出现一次（实存 %d 处）", got)
		}
		if !strings.HasSuffix(strings.TrimSpace(q), "LIMIT 1") {
			t.Error("查找必须 LIMIT 1（最新一条）")
		}
	})

	t.Run("lookupTurnNumber 会话族权威 + v1 回退", func(t *testing.T) {
		s := lookupTurnNoSessionSQL
		for _, leg := range []string{"FROM public.session_turns_hot WHERE session_id = $1",
			"FROM public.session_turns WHERE session_id = $1"} {
			if !strings.Contains(s, leg) {
				t.Errorf("turn_no 会话读缺少 %q——停写后新会话的序号会恒 1", leg)
			}
		}
		if !strings.Contains(s, "MAX(turn_no)") {
			t.Error("turn_no 应读 MAX(turn_no)（序号权威在会话族，COUNT 会漏补写行）")
		}
		v := lookupTurnNoV1FallbackSQL
		if !strings.Contains(v, "FROM _request_logs") || !strings.Contains(v, "gw_session_id = $1") {
			t.Error("v1 回退腿丢失（镜像前历史会话的 outbox 序号会断供）")
		}
	})
}
