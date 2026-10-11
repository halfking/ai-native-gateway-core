package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/logging"
)

// A2 量具出库回归门（2026-10-11）：PermanentlyDisabled / PermanentRejections
// 此前只有 Stats() 一个读方且仅测试调用（9fe5320b6 审计遗留 P3）。本门钉住
// admin 端点的三种读数形态：未启用（enabled=false）、健康（enabled=true +
// 零值量具）、已自禁用（permanently_disabled=true 可从读数上区分）。

func TestAnomalyReporterStatsEndpointNotConfiguredIsDistinguishable(t *testing.T) {
	rec := httptest.NewRecorder()
	handleAnomalyReporterStats(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/anomaly-reporter/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (nil reporter is 'not configured', not an error)", rec.Code)
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, rec.Body.String())
	}
	if body.Enabled {
		t.Fatalf("nil reporter must answer enabled=false, got: %s", rec.Body.String())
	}
}

func TestAnomalyReporterStatsEndpointExposesA2Gauges(t *testing.T) {
	r := logging.NewLockFreeAnomalyReporter("http://127.0.0.1:1/none", false, 8)
	rec := httptest.NewRecorder()
	handleAnomalyReporterStats(r).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/anomaly-reporter/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Enabled             bool   `json:"enabled"`
		QueueCapacity       int64  `json:"queue_capacity"`
		PermanentlyDisabled bool   `json:"permanently_disabled"`
		PermanentRejections uint64 `json:"permanent_rejections"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, rec.Body.String())
	}
	if !body.Enabled {
		t.Fatalf("enabled must be true when a reporter is wired, got: %s", rec.Body.String())
	}
	if body.QueueCapacity != 8 {
		t.Fatalf("queue_capacity = %d, want 8 (untagged fields must stay readable via snake_case tags)", body.QueueCapacity)
	}
	if body.PermanentlyDisabled || body.PermanentRejections != 0 {
		t.Fatalf("fresh reporter must read healthy: permanently_disabled=%v permanent_rejections=%d",
			body.PermanentlyDisabled, body.PermanentRejections)
	}
}

// 自禁用本身（永久 4xx → permanently_disabled=true）的机制门在
// internal/logging/lockfree_anomaly_reporter_permanent_test.go —— 驱动它需要
// 包内私有的 enqueue/sendBatchContext。本文件只钉「端点如实透传 Stats()」
// 这一层契约：A2 字段一旦从读数上消失，本测试与 lockfree 门会一前一后红。
