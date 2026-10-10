package logging

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 2026-10-11 回归（154 生产事故）：endpoint 配置到本网关不存在的路由
// （/api/diagnostics/anomalies，实际是 /api/admin/format-anomalies），
// postBatch 对非 2xx 一律 requeue，sendWorker 每 5s 重发同一批 → 24h 内
// 9341 次 404 循环。永久性 4xx 必须：① 不再重新入队（丢弃）；
// ② 连续 permanentFailuresThreshold 次后自禁用。
func TestAnomalyReporterPermanent404DropsAndSelfDisables(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	r := NewLockFreeAnomalyReporter(srv.URL, true, 100)
	defer r.Close()

	// 入队一批报告，反复 flush。旧实现对每次 404 都 requeue，
	// 队列永远清不空；新实现丢弃 + 计数。
	for i := 0; i < 10; i++ {
		r.enqueue(AnomalyReport{RequestID: "req-1", AnomalyType: "conversion_error"})
	}
	if got := r.queue.Size(); got != 10 {
		t.Fatalf("queue size after enqueue = %d, want 10", got)
	}

	for round := 1; round <= permanentFailuresThreshold; round++ {
		ok := r.sendBatchContext(context.Background())
		if !ok {
			t.Fatalf("round %d: sendBatchContext = false, want true (permanent rejection must be treated as handled, not requeued)", round)
		}
		if got := r.queue.Size(); got != 0 {
			t.Fatalf("round %d: queue size = %d, want 0 (batch must be dropped, not requeued)", round, got)
		}
		enabled := r.enabled.Load()
		wantEnabled := round < permanentFailuresThreshold
		if enabled != wantEnabled {
			t.Fatalf("round %d: enabled = %v, want %v", round, enabled, wantEnabled)
		}
		// 重新入队一批模拟新报告继续到达（禁用前 enqueue 仍放行）。
		if wantEnabled {
			r.enqueue(AnomalyReport{RequestID: "req-next", AnomalyType: "conversion_error"})
		}
	}

	if r.enabled.Load() {
		t.Fatal("reporter must self-disable after permanentFailuresThreshold consecutive permanent rejections")
	}
	if calls != permanentFailuresThreshold {
		t.Fatalf("endpoint calls = %d, want %d", calls, permanentFailuresThreshold)
	}

	// 自禁用后 enqueue 直接拒绝，不再产生新的发送循环。
	if r.enqueue(AnomalyReport{RequestID: "req-after-disable"}) {
		t.Fatal("enqueue after self-disable must be rejected")
	}
}

// 临时性 5xx 仍走重排队语义（不能把过载的端点误杀）。
func TestAnomalyReporterTransient503StillRequeues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	r := NewLockFreeAnomalyReporter(srv.URL, true, 100)
	defer r.Close()

	r.enqueue(AnomalyReport{RequestID: "req-1", AnomalyType: "conversion_error"})
	if ok := r.sendBatchContext(context.Background()); ok {
		t.Fatal("503 must be reported as failure (requeue path)")
	}
	if got := r.queue.Size(); got != 1 {
		t.Fatalf("queue size = %d, want 1 (transient failure must requeue)", got)
	}
	if r.enabled.Load() != true {
		t.Fatal("transient failure must not disable the reporter")
	}
}

// 成功发送会清零永久失败计数，偶发 404 不会累积成自禁用。
func TestAnomalyReporterSuccessResetsPermanentCounter(t *testing.T) {
	status := http.StatusNotFound
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
	defer srv.Close()

	r := NewLockFreeAnomalyReporter(srv.URL, true, 100)
	defer r.Close()

	for i := 0; i < permanentFailuresThreshold-1; i++ {
		r.enqueue(AnomalyReport{RequestID: "req-x", AnomalyType: "conversion_error"})
		r.sendBatchContext(context.Background())
	}
	if got := r.permanentFailures.Load(); got != uint32(permanentFailuresThreshold-1) {
		t.Fatalf("permanentFailures = %d, want %d", got, permanentFailuresThreshold-1)
	}

	status = http.StatusOK
	r.enqueue(AnomalyReport{RequestID: "req-ok", AnomalyType: "conversion_error"})
	if ok := r.sendBatchContext(context.Background()); !ok {
		t.Fatal("2xx must succeed")
	}
	if got := r.permanentFailures.Load(); got != 0 {
		t.Fatalf("permanentFailures after success = %d, want 0", got)
	}
	_ = time.Second // keep time import if assertions evolve
}
