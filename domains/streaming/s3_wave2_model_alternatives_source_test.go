//go:build !integration

package streaming

import (
	"strings"
	"testing"
)

// TestS3Wave2ModelAlternativesUsageSource（2026-10-13 S3 波 2）钉住
// alternativesSQL 的 usage_7d CTE 热面。停写后新流量的 canonical_model 只落
// session_turns_hot；v1 hot 只会排空。2026-09-25 的「视图 151s」裁决仍然
// 有效——本测试同时守住「不得改回视图」与「不得留在 v1 hot」两个方向。
// 判据同层：alternativesSQL 就是 Find 实际发给 PostgreSQL 的文本。
func TestS3Wave2ModelAlternativesUsageSource(t *testing.T) {
	// alternativesSQL 自 S3 波 2 起是拼接表达式（var）：渲染后的文本就是
	// 发给 PostgreSQL 的文本，判据同层依然成立。
	sql := alternativesSQL
	if !strings.Contains(sql, "FROM public.session_turns_hot") ||
		!strings.Contains(sql, "FROM public.session_turns ") {
		t.Error("usage_7d 热面未切到会话族原生源（hot ∪ parent 两腿）——停写后热门度信号将随 v1 hot 排空消失")
	}
	if strings.Contains(sql, "FROM _request_logs_hot") {
		t.Error("usage_7d 仍在直读 _request_logs_hot（停写后恒空）")
	}
	// 2026-09-25 裁决（252 实测 151s）：失败路径禁读 canonical 视图。
	// 用 FROM 形状 token——CTE 注释里提到视图名（裁决原文）不应误触发。
	if strings.Contains(sql, "FROM request_logs_with_current_month") {
		t.Error("usage_7d 不得读 canonical 视图（失败路径反连接成本，2026-09-25 实测裁决）")
	}
	// 口径谓词必须随腿迁移保留。
	if !strings.Contains(sql, "success = TRUE") || !strings.Contains(sql, "canonical_model IS NOT NULL") {
		t.Error("usage_7d 口径谓词丢失（success/canonical_model 过滤）")
	}
}
