//go:build !integration

package main

import (
	"strings"
	"testing"
)

// TestS3Wave2WaterfallSources（2026-10-13 S3 波 2）钉住调度瀑布两个 DB 读端
// 的源形状。停写后新流量的 t0..t9 只落 session_turns(_hot)；v1 hot 只会
// 排空。判据同层：两个包级常量就是发给 PostgreSQL 的文本。
func TestS3Wave2WaterfallSources(t *testing.T) {
	t.Run("点查走 canonical 视图", func(t *testing.T) {
		q := waterfallByRequestSQL
		if !strings.Contains(q, "FROM request_logs_with_current_month") {
			t.Error("request_id 点查未走 canonical 视图（B 形态守卫规则的唯一完整源）")
		}
		if strings.Contains(q, "FROM _request_logs_hot") {
			t.Error("点查仍在直读 _request_logs_hot（停写后排空）")
		}
		// 调度计时 t0..t9 必须齐全——瀑布图的主体就是它们。
		for _, col := range []string{
			"t0_arrived_at", "t1_total_enqueued_at", "t2_total_dequeued_at",
			"t3_model_enqueued_at", "t4_model_dequeued_at", "t5_cred_enqueued_at",
			"t6_cred_dequeued_at", "t7_forward_start_at", "t8_response_start_at",
			"t9_response_end_at",
		} {
			if !strings.Contains(q, col) {
				t.Errorf("点查投影缺调度计时列 %s", col)
			}
		}
	})
	t.Run("列表走 canonical 视图", func(t *testing.T) {
		q := waterfallListSQL
		if !strings.Contains(q, "FROM request_logs_with_current_month") {
			t.Error("瀑布列表未走 canonical 视图——停写后热窗排空面板恒空")
		}
		if strings.Contains(q, "FROM _request_logs_hot") {
			t.Error("瀑布列表仍在直读 _request_logs_hot")
		}
		if !strings.Contains(q, "ORDER BY t0_arrived_at DESC") {
			t.Error("列表按 t0 倒序的近期语义丢失")
		}
	})
	t.Run("LastOutboundForSession 会话族腿", func(t *testing.T) {
		q := lastOutboundSessionSQL
		if got := strings.Count(q, "session_id = $1"); got != 2 {
			t.Errorf("会话族腿应有内推 session_id = $1 两处（实存 %d）", got)
		}
		if !strings.Contains(q, "ON sb.request_id = rl.request_id") {
			t.Error("bodies JOIN 必须 request_id 单键（helper 合同）")
		}
		if !strings.Contains(q, "sb.outbound_body IS NOT NULL") {
			t.Error("outbound_body 过滤丢失（L3 冷启动的语义前提）")
		}
	})
}
