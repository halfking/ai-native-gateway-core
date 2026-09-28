package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	emptyResponseAttempts = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_gateway_empty_response_attempts_total",
		Help: "Empty-response attempts by normalized stream detection reason.",
	}, []string{"reason"})
)

func RecordEmptyResponseAttempt(reason string) {
	switch reason {
	case "empty_stream_no_content":
		reason = "done_no_content"
	case "early_empty_detection":
		reason = "early_empty"
	default:
		reason = "other"
	}
	emptyResponseAttempts.WithLabelValues(reason).Inc()
}

// 2026-09-29 (审计二十一轮): 移除 RecordURSMSoftEmptyResponsePenalty 与
// llm_gateway_ursm_empty_response_penalty_applied_total——R45 落地以来全仓
// 零调用点（与 RecordEmptyResponseAttempt 当年的零接线同病，二十轮只接了
// 兄弟函数），promauto 注册后恒导出 0 序列。URSM 罚分真要观测时在
// 打分路径重新接线，而不是留一个没人能读的死序列。
