//go:build !integration

package quality

import (
	"strings"
	"testing"
)

// TestS3Wave2MinuteMetricsSource（2026-10-13 S3 波 2）钉住分钟聚合的源形状。
// 停写后新流量只落 session_turns；写目标 provider_metrics_minute 不是
// _request_logs 家族，不受停写门管制，聚合必须继续供数。
// 判据同层：minuteMetricsSQL 就是 collectMinuteMetrics 实际执行的文本。
func TestS3Wave2MinuteMetricsSource(t *testing.T) {
	q := minuteMetricsSQL
	if !strings.Contains(q, "FROM request_logs_with_current_month") {
		t.Error("分钟聚合源未改读 canonical 视图——停写后 provider_metrics_minute 将静默断粮")
	}
	if strings.Contains(q, "FROM _request_logs_hot") {
		t.Error("分钟聚合仍在直读 _request_logs_hot（停写后恒空）")
	}
	// provider 守卫谓词必须保留：视图会话臂 provider_id 依赖 details 层，
	// 该谓词把缺 details 的轮次显式排除（登记过的覆盖边界）。
	if !strings.Contains(q, "provider_id IS NOT NULL") || !strings.Contains(q, "provider_id > 0") {
		t.Error("provider_id 守卫谓词丢失（0/NULL provider 会污染聚合维度）")
	}
	if !strings.Contains(q, "INSERT INTO provider_metrics_minute") {
		t.Error("写目标漂移：聚合必须继续写 provider_metrics_minute")
	}
}
