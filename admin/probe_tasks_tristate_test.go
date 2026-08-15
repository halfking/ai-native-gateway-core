package admin

// OBS-BE5 tests (2026-08-15, docs/会话优化v3/25 号 §6 / 26 号 §4):
//   - GET /api/admin/probe/tasks 三态查询核心（pending/in_flight/completed 映射、
//     origin 徽标、退避下一跳、completed 窗口 200）
//   - handler 负向（405/400/503）
//   - SSE：PublishProbeTransition 事件序列（pending→submitted、in-flight→started、
//     失败重臂→pending 带 next_retry_at），Origin 缺省推导。
// DB mock 用 pgxmock（现有 admin 测试基建）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/bg"
)

func tristateRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "dedup_key", "credential_id", "provider_id",
		"raw_model", "probe_command", "source", "status",
		"attempt", "max_attempts", "priority", "next_run_at",
		"reason_code", "result_http_status", "result_latency_ms",
		"created_at", "updated_at", "finished_at",
	})
}

func TestQueryProbeTriStateTasks_Pending(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	next := time.Now().Add(30 * time.Second)
	created, updated := time.Now().Add(-time.Minute), time.Now()
	mock.ExpectQuery("SELECT q.id").
		WithArgs(50).
		WillReturnRows(tristateRows().
			AddRow(int64(1), "node_probe:9:m", int64(9), nil,
				"glm-4", "node_probe", "request_failure", "ready",
				2, 7, int16(60), next,
				"rate_limited", nil, nil,
				created, updated, nil))

	tasks, err := queryProbeTriStateTasks(context.Background(), mock, "pending", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	task := tasks[0]
	if task.Status != "pending" || task.Outcome != "" {
		t.Fatalf("status mapping: %+v", task)
	}
	if task.Origin != "error" {
		t.Fatalf("origin: want error, got %q", task.Origin)
	}
	if task.NextRetryAtMs != next.UnixMilli() {
		t.Fatalf("next_retry_at_ms: want %d, got %d", next.UnixMilli(), task.NextRetryAtMs)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQueryProbeTriStateTasks_Completed(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	finished := time.Now().Add(-time.Second)
	created, updated := time.Now().Add(-time.Minute), finished
	httpStatus, latency := 200, 350
	mock.ExpectQuery("SELECT q.id").
		WithArgs(200).
		WillReturnRows(tristateRows().
			AddRow(int64(2), "node_probe:8:m", int64(8), int64Ptr(3),
				"glm-4", "node_probe", "admin", "success",
				1, 7, int16(60), time.Now(),
				"", int32(httpStatus), int32(latency),
				created, updated, &finished))

	tasks, err := queryProbeTriStateTasks(context.Background(), mock, "completed", 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	task := tasks[0]
	if task.Status != "completed" || task.Outcome != "success" {
		t.Fatalf("completed mapping: %+v", task)
	}
	if task.Origin != "manual" {
		t.Fatalf("origin: want manual, got %q", task.Origin)
	}
	if task.HTTPStatus == nil || *task.HTTPStatus != 200 || task.LatencyMs == nil || *task.LatencyMs != 350 {
		t.Fatalf("result fields: %+v", task)
	}
	if task.NextRetryAtMs != 0 {
		t.Fatalf("completed rows must not carry next_retry_at_ms: %+v", task)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQueryProbeTriStateTasks_InvalidStatus(t *testing.T) {
	if _, err := queryProbeTriStateTasks(context.Background(), nil, "bogus", 10); err == nil {
		t.Fatal("expected error for invalid status")
	}
}

func int64Ptr(v int64) *int64 { return &v }

// ---- handler 层 ----

func TestHandleProbeTaskList_Negative(t *testing.T) {
	h := &Handler{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/admin/probe/tasks", h.handleProbeTaskRoute)

	cases := []struct {
		name, target string
		method       string
		want         int
	}{
		{"put-405", "/api/admin/probe/tasks?status=pending", http.MethodPut, http.StatusMethodNotAllowed},
		{"bad-status-400", "/api/admin/probe/tasks?status=done", http.MethodGet, http.StatusBadRequest},
		{"bad-limit-400", "/api/admin/probe/tasks?status=pending&limit=0", http.MethodGet, http.StatusBadRequest},
		{"limit-over-200-400", "/api/admin/probe/tasks?status=completed&limit=999", http.MethodGet, http.StatusBadRequest},
		{"db-nil-503", "/api/admin/probe/tasks?status=in_flight", http.MethodGet, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(tc.method, tc.target, nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, r)
		if rr.Code != tc.want {
			t.Fatalf("%s: expected %d, got %d: %s", tc.name, tc.want, rr.Code, rr.Body.String())
		}
	}
}

func TestHandleProbeTaskList_DefaultsToPending(t *testing.T) {
	h := &Handler{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/admin/probe/tasks", h.handleProbeTaskRoute)
	// 无 status 参数 → 默认 pending → 走到 db nil 的 503（证明默认值被接受而非 400）
	r := httptest.NewRequest(http.MethodGet, "/api/admin/probe/tasks", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, r)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 (pending default accepted, db nil), got %d: %s", rr.Code, rr.Body.String())
	}
}

// ---- SSE：PublishProbeTransition 事件序列 ----

func subscribeProbeHub(hub *ProbeSSEHub) chan ProbeStreamEnvelope {
	ch := make(chan ProbeStreamEnvelope, 16)
	hub.mu.Lock()
	if hub.clients == nil {
		hub.clients = map[chan ProbeStreamEnvelope]struct{}{}
	}
	hub.clients[ch] = struct{}{}
	hub.mu.Unlock()
	return ch
}

func TestPublishProbeTransition_Sequence(t *testing.T) {
	hub := &ProbeSSEHub{}
	ch := subscribeProbeHub(hub)

	base := bgProbeTransitionForTest()
	// 1) enqueue → pending → envelope "submitted"
	pending := base
	pending.Status = "pending"
	hub.PublishProbeTransition(pending)
	env := <-ch
	if env.Type != "submitted" || env.Task == nil || env.Task.Origin != "error" {
		t.Fatalf("pending transition: %+v", env)
	}
	// 2) claim → in-flight → envelope "started"
	inflight := base
	inflight.Status = "in-flight"
	hub.PublishProbeTransition(inflight)
	env = <-ch
	if env.Type != "started" {
		t.Fatalf("in-flight transition: %+v", env)
	}
	// 3) 失败重臂（智能回退）→ pending + next_retry_at
	rearm := base
	rearm.Status = "pending"
	rearm.Attempt = 2
	rearm.NextRetryAtMs = time.Now().Add(30 * time.Second).UnixMilli()
	hub.PublishProbeTransition(rearm)
	env = <-ch
	if env.Type != "submitted" || env.Task.NextRetryAtMs != rearm.NextRetryAtMs || env.Task.Attempt != 2 {
		t.Fatalf("re-arm transition: %+v", env)
	}
	// payload 序列化：origin/next_retry_at_ms optional 字段不破坏旧形状
	raw, err := json.Marshal(env.Task)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	if generic["origin"] != "error" {
		t.Fatalf("origin not serialized: %s", raw)
	}
}

func TestPublish_OriginDerivedFromSource(t *testing.T) {
	hub := &ProbeSSEHub{}
	ch := subscribeProbeHub(hub)
	hub.Publish(ProbeStreamTask{
		ID: "t1", TaskType: "node_probe", Source: "periodic",
		Status: "pending", CredentialID: 1, Timestamp: time.Now().UnixMilli(),
	})
	env := <-ch
	if env.Task == nil || env.Task.Origin != "scheduled" {
		t.Fatalf("legacy producer origin derivation: %+v", env)
	}
}

// bgProbeTransitionForTest builds the shared transition fixture.
func bgProbeTransitionForTest() (t bg.ProbeTaskTransition) {
	t = bg.ProbeTaskTransition{
		ID:           "node_probe:9:glm-4",
		TaskType:     "node_probe",
		Source:       "request_failure",
		CredentialID: 9,
		ProviderID:   3,
		RawModel:     "glm-4",
		Attempt:      1,
		Origin:       "error",
		TimestampMs:  time.Now().UnixMilli(),
	}
	return
}
