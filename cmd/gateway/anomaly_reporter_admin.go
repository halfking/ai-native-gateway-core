package main

import (
	"encoding/json"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/internal/logging"
)

// handleAnomalyReporterStats 把 A2 量具字段（PermanentlyDisabled /
// PermanentRejections）经 admin API 暴露出去。9fe5320b6 落地时这两个字段
// 只有 Stats() 一个读方且只被测试调用——自禁用事件虽有 slog.Error 兜底，
// 但没有聚合视图，值班时无法从接口侧回答「报告器是否已停摆」。
//
// 报告器是 env 门控的（LLM_GATEWAY_ANOMALY_REPORTER_ENABLED）；未启用时
// 端点仍返回 200 + {"enabled":false}，让「未配置」与「已配置且停摆」在读
// 数上可区分——后者才是待处置故障。
func handleAnomalyReporterStats(r *logging.LockFreeAnomalyReporter) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		if r == nil {
			_ = enc.Encode(map[string]any{"enabled": false})
			return
		}
		st := r.Stats()
		_ = enc.Encode(struct {
			Enabled bool `json:"enabled"`
			logging.AnomalyReporterStats
		}{Enabled: true, AnomalyReporterStats: st})
	}
}
