package hostedtask

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/internal/hostedcallback"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// 矩阵 E：回调退避 30s ×2 封顶 1h。
func TestCallbackBackoffCapped(t *testing.T) {
	if got := CallbackBackoff(0); got != 30*time.Second {
		t.Errorf("first backoff = %v", got)
	}
	if got := CallbackBackoff(1); got != time.Minute {
		t.Errorf("second backoff = %v", got)
	}
	if got := CallbackBackoff(20); got != time.Hour {
		t.Errorf("backoff must cap at 1h, got %v", got)
	}
}

// S2-F2（R60）回归：recalled 事件的回调 payload 必须携带 recall_status +
// handoff_packet（§3.3：每次召回交付一次最新包），事件类型可区分召回投递。
func TestBuildCallbackDelivery_RecalledCarriesHandoffPacket(t *testing.T) {
	task := &Task{
		ID:     "ht_x",
		Status: StatusCancelled,
		Result: map[string]any{"summary": "partial"},
		Goal:   "collect",
	}
	ev := &Event{
		Type: EventRecalled,
		Seq:  7,
		Payload: map[string]any{
			"recall_status": string(RecallCancelled),
			"next_owner":    "recall_caller",
			"handoff_packet": StructuredHandoffPacket{
				Goal:          "collect",
				CurrentResult: task.Result,
				NextOwner:     "recall_caller",
			},
		},
	}
	eventType, payload := buildCallbackDelivery(task, ev, 1)
	if eventType != "hosted_task.recalled" {
		t.Errorf("eventType = %q, want hosted_task.recalled (consumer must distinguish recall)", eventType)
	}
	if payload["recall_status"] != string(RecallCancelled) {
		t.Errorf("payload.recall_status = %v, want %q", payload["recall_status"], RecallCancelled)
	}
	packet, ok := payload["handoff_packet"].(StructuredHandoffPacket)
	if !ok {
		t.Fatalf("payload.handoff_packet missing or wrong type: %T", payload["handoff_packet"])
	}
	if packet.Goal != "collect" || packet.NextOwner != "recall_caller" {
		t.Errorf("packet content wrong: %+v", packet)
	}
	// 既有形状字段保持。
	if payload["status"] != string(StatusCancelled) || payload["attempt"] != 1 {
		t.Errorf("base payload fields lost: %+v", payload)
	}
	if _, ok := payload["result"].(map[string]any); !ok {
		t.Errorf("payload.result missing: %+v", payload)
	}
	// 投递体可正常序列化（BuildEnvelope 契约）。
	if _, err := hostedcallback.BuildEnvelope("hosted_ht_x_ev7", eventType, task.ID, "t1", payload); err != nil {
		t.Errorf("build envelope: %v", err)
	}
}

// S2-F2（R60）回归：旧行（事件不可读/事件里无包）保持既有投递形状——
// payload 不含 handoff_packet/recall_status 字段，但投递体构建成功。
func TestBuildCallbackDelivery_LegacyRowKeepsShapeAndDelivers(t *testing.T) {
	task := &Task{ID: "ht_old", Status: StatusCancelled, Result: map[string]any{"summary": "s"}}

	for name, ev := range map[string]*Event{
		"nil event (load failed)": nil,
		"recalled without packet": {Type: EventRecalled, Seq: 3, Payload: map[string]any{
			"recall_status": "cancelled",
		}},
		"non-recalled event": {Type: EventCancelled, Seq: 3, Payload: map[string]any{
			"actor": "tenant",
		}},
	} {
		t.Run(name, func(t *testing.T) {
			eventType, payload := buildCallbackDelivery(task, ev, 2)
			switch {
			case ev != nil && ev.Type == EventRecalled:
				// 事件行是召回（即便包已被剥掉/缺失）：类型仍如实标 recalled，
				// recall_status 有则带，包字段缺省。
				if eventType != "hosted_task.recalled" {
					t.Errorf("eventType = %q, want hosted_task.recalled", eventType)
				}
				if payload["recall_status"] != "cancelled" {
					t.Errorf("payload.recall_status = %v, want cancelled", payload["recall_status"])
				}
			default:
				if eventType != "hosted_task.cancelled" {
					t.Errorf("eventType = %q, want hosted_task.cancelled (existing shape)", eventType)
				}
				if _, present := payload["recall_status"]; present {
					t.Errorf("payload.recall_status must be absent on legacy shape")
				}
			}
			if _, present := payload["handoff_packet"]; present {
				t.Errorf("payload.handoff_packet must be absent when the event carries no packet")
			}
			body, err := hostedcallback.BuildEnvelope("hosted_ht_old_ev3", eventType, task.ID, "t1", payload)
			if err != nil {
				t.Fatalf("build envelope (delivery must still succeed): %v", err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatalf("envelope not valid JSON: %v", err)
			}
		})
	}
}

// S2-F2（R60）端到端回归（库级，TEST_PG_URL 约定同 TestStoreAgainstPostgres）：
// 召回任务 → DeliverDueCallbacks 实投 → 接收方收到的 envelope 含
// handoff_packet/recall_status；旧行（事件 payload 被剥掉包）投递仍成功且
// payload 不含包字段。
func TestCallbackDeliveryAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL not set; delivery integration requires a live PostgreSQL (convention: tests/integration gating)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TEST_PG_URL: %v", err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	for _, f := range []string{
		"../../sql/migrations/startup/711_hosted_tasks.sql",
		"../../sql/migrations/startup/742_hosted_task_recalled_event.sql",
	} {
		up, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(ctx, string(up)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	t.Cleanup(func() {
		for _, f := range []string{
			"../../sql/migrations/startup/742_hosted_task_recalled_event.down.sql",
			"../../sql/migrations/startup/711_hosted_tasks.down.sql",
		} {
			down, err := os.ReadFile(f)
			if err == nil {
				_, _ = admin.Exec(context.Background(), string(down))
			}
		}
		_ = admin.Close(context.Background())
	})
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	store := NewStore(pool)

	// 回调接收方（safehttpclient 对回环默认阻断，allowlist 放行 127.0.0.1）。
	var mu sync.Mutex
	var received []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		received = append(received, body)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	keyring, err := secret.NewKeyring(map[string][32]byte{"test": [32]byte{}}, "test")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	urlEnc, err := secret.EncryptAESGCM([]byte(srv.URL), keyring)
	if err != nil {
		t.Fatalf("encrypt url: %v", err)
	}
	secEnc, err := secret.EncryptAESGCM([]byte("callback-secret"), keyring)
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}

	task, _, err := store.CreateTask(ctx, CreateInput{
		TenantID: "tenant-cb", APIKeyID: 1, Goal: "goal-cb", DoneWhen: "done-cb",
		CallbackURLEnc: urlEnc, CallbackSecEnc: secEnc,
		CallbackHash:   "hash-cb",
		IdempotencyKey: "idem-cb-1", RequestHash: "hash-cb-1",
		Deadline: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// 终态落定写入 PG 权威 result（状态机 delegated→dispatching→running→
	// completed），再召回（recalled 事件 + 回调重置）。
	if ok, err := store.BeginDispatch(ctx, task.ID); err != nil || !ok {
		t.Fatalf("begin dispatch: ok=%v err=%v", ok, err)
	}
	if err := store.RecordDispatchSuccess(ctx, task.ID, "cmd-cb", "run-cb", "key-cb"); err != nil {
		t.Fatalf("record dispatch: %v", err)
	}
	if settled, _, err := store.SettleTask(ctx, task.ID, SettleInput{
		To: StatusCompleted, EventType: EventCompleted,
		Result: map[string]any{"summary": "done-cb"},
	}); err != nil || !settled {
		t.Fatalf("settle: settled=%v err=%v", settled, err)
	}
	if _, err := store.RecallTask(ctx, "tenant-cb", task.ID); err != nil {
		t.Fatalf("recall: %v", err)
	}

	deps := CallbackDeps{
		Store:     store,
		Deliverer: hostedcallback.NewDeliverer(5*time.Second, []string{"127.0.0.1"}),
		Keyring:   keyring,
	}
	delivered, err := DeliverDueCallbacks(ctx, deps, time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if delivered != 1 {
		t.Fatalf("delivered = %d, want 1", delivered)
	}
	mu.Lock()
	if len(received) != 1 {
		mu.Unlock()
		t.Fatalf("receiver got %d envelopes, want 1", len(received))
	}
	env := received[0]
	mu.Unlock()
	if env["type"] != "hosted_task.recalled" {
		t.Errorf("envelope.type = %v, want hosted_task.recalled", env["type"])
	}
	payload, _ := env["payload"].(map[string]any)
	if payload == nil {
		t.Fatalf("envelope.payload missing: %+v", env)
	}
	if payload["recall_status"] == nil {
		t.Errorf("callback payload missing recall_status: %+v", payload)
	}
	packet, ok := payload["handoff_packet"].(map[string]any)
	if !ok {
		t.Fatalf("callback payload missing handoff_packet: %+v", payload)
	}
	if packet["goal"] != "goal-cb" {
		t.Errorf("handoff_packet.goal = %v, want goal-cb", packet["goal"])
	}
	if cr, ok := packet["current_result"].(map[string]any); !ok || cr["summary"] != "done-cb" {
		t.Errorf("handoff_packet.current_result = %v, want the PG result snapshot", packet["current_result"])
	}

	// 旧行路径：剥掉事件 payload 里的包 → 重置回调 → 仍投递成功且无包字段。
	if _, err := pool.Exec(ctx, `
		UPDATE hosted_task_events
		SET payload = payload - 'handoff_packet' - 'recall_status'
		WHERE task_id = $1 AND event_type = 'recalled'
	`, task.ID); err != nil {
		t.Fatalf("strip packet: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE hosted_task_callbacks
		SET status = 'pending', next_attempt_at = NOW(), attempts = 0
		WHERE task_id = $1
	`, task.ID); err != nil {
		t.Fatalf("reset callback: %v", err)
	}
	mu.Lock()
	received = nil
	mu.Unlock()
	delivered, err = DeliverDueCallbacks(ctx, deps, time.Now().UTC(), 10)
	if err != nil || delivered != 1 {
		t.Fatalf("legacy deliver: delivered=%d err=%v", delivered, err)
	}
	mu.Lock()
	if len(received) != 1 {
		mu.Unlock()
		t.Fatalf("legacy receiver got %d envelopes, want 1", len(received))
	}
	legacyPayload, _ := received[0]["payload"].(map[string]any)
	mu.Unlock()
	if _, present := legacyPayload["handoff_packet"]; present {
		t.Errorf("legacy payload must not carry handoff_packet: %+v", legacyPayload)
	}
	if _, present := legacyPayload["recall_status"]; present {
		t.Errorf("legacy payload must not carry recall_status: %+v", legacyPayload)
	}
	// 本例召回的是 completed 终态任务（already_terminal 路径，行状态保持
	// completed）：既有形状字段 = PG 权威状态，不被召回改写。
	if legacyPayload["status"] != "completed" {
		t.Errorf("legacy payload.status = %v, want completed (投递形状保持)", legacyPayload["status"])
	}
}

// ── R61（2026-09-24）：GetEvent 瞬时错误必须可重试，ErrNotFound 降级 ──────

type stubCallbackStore struct {
	task    *Task
	taskErr error
	event   *Event
	evErr   error
}

func (s *stubCallbackStore) GetTask(context.Context, string, string) (*Task, error) {
	return s.task, s.taskErr
}

func (s *stubCallbackStore) GetEvent(context.Context, string, string, int64) (*Event, error) {
	return s.event, s.evErr
}

type stubCallbackDeliverer struct {
	calls int
}

func (d *stubCallbackDeliverer) Deliver(context.Context, string, string, []byte, string) (hostedcallback.Result, error) {
	d.calls++
	return hostedcallback.Result{StatusCode: 200, Delivered: true}, nil
}

func TestDeliverOneTransientEventErrorIsRetryable(t *testing.T) {
	kr, err := secret.NewKeyring(map[string][32]byte{"test": [32]byte{}}, "test")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	urlEnc, err := secret.EncryptAESGCM([]byte("http://cb.example/hook"), kr)
	if err != nil {
		t.Fatalf("encrypt url: %v", err)
	}
	secEnc, err := secret.EncryptAESGCM([]byte("cb-secret"), kr)
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}
	job := CallbackJob{
		TaskID: "ht_test", TenantID: "default", EventID: "hosted_ht_test_ev3",
		EventSeq: 3, URLEnc: urlEnc, SecretEnc: secEnc,
	}
	task := &Task{ID: "ht_test", TenantID: "default", Status: StatusCompleted}
	ctx := context.Background()

	// ErrNotFound（旧行/事件已不可读）→ ev=nil 降级 legacy 形状，照常投递。
	delivererOK := &stubCallbackDeliverer{}
	out := deliverOne(ctx, &stubCallbackStore{task: task, evErr: ErrNotFound}, delivererOK, kr, job)
	if !out.delivered || out.retryable {
		t.Errorf("ErrNotFound must degrade to legacy delivery, got %+v", out)
	}
	if delivererOK.calls != 1 {
		t.Errorf("ErrNotFound path must still deliver, calls=%d", delivererOK.calls)
	}

	// 瞬时 DB 错误 → 可重试失败，绝不投递（吞掉会把 recalled 回调静默
	// 降级成无包形状并标记已投递，§3.3 包投递契约破坏且永不重试）。
	delivererSkip := &stubCallbackDeliverer{}
	out = deliverOne(ctx, &stubCallbackStore{task: task, evErr: errors.New("conn closed")}, delivererSkip, kr, job)
	if out.delivered {
		t.Errorf("transient GetEvent error must not deliver, got %+v", out)
	}
	if !out.retryable {
		t.Errorf("transient GetEvent error must be retryable, got %+v", out)
	}
	if delivererSkip.calls != 0 {
		t.Errorf("transient GetEvent error must not reach deliverer, calls=%d", delivererSkip.calls)
	}
}

// r0924b（2026-09-24）回归钉桩：GetTask 的 ErrNotFound（任务行已被硬删，
// 如 742.down 场景）重试永不恢复——必须按不可重试处理直接进 DLQ 终态
// （RecordCallbackOutcome 的 !Retryable 分支），而不是此前的一律
// retryable=true 让任务在 8 次指数退避里空转；瞬时 DB 错误维持可重试。
func TestDeliverOneTaskNotFoundIsNotRetryable(t *testing.T) {
	kr, err := secret.NewKeyring(map[string][32]byte{"test": [32]byte{}}, "test")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	urlEnc, err := secret.EncryptAESGCM([]byte("http://cb.example/hook"), kr)
	if err != nil {
		t.Fatalf("encrypt url: %v", err)
	}
	secEnc, err := secret.EncryptAESGCM([]byte("cb-secret"), kr)
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}
	job := CallbackJob{
		TaskID: "ht_gone", TenantID: "default", EventID: "hosted_ht_gone_ev3",
		EventSeq: 3, URLEnc: urlEnc, SecretEnc: secEnc,
	}
	task := &Task{ID: "ht_gone", TenantID: "default", Status: StatusCompleted}
	ctx := context.Background()

	// GetTask ErrNotFound → 不可重试（DLQ/终态），绝不投递（任务行都不在，
	// 无从构建投递体）。
	delivererNotFound := &stubCallbackDeliverer{}
	out := deliverOne(ctx, &stubCallbackStore{taskErr: ErrNotFound}, delivererNotFound, kr, job)
	if out.delivered {
		t.Errorf("GetTask ErrNotFound must not deliver, got %+v", out)
	}
	if out.retryable {
		t.Errorf("GetTask ErrNotFound must be non-retryable (straight to DLQ terminal state), got %+v", out)
	}
	if out.errText == "" {
		t.Errorf("GetTask ErrNotFound must carry the load-task error text for the DLQ row")
	}
	if delivererNotFound.calls != 0 {
		t.Errorf("GetTask ErrNotFound must not reach deliverer, calls=%d", delivererNotFound.calls)
	}

	// GetTask 瞬时 DB 错误 → 仍可重试（退避后重投），绝不投递。
	delivererTransient := &stubCallbackDeliverer{}
	out = deliverOne(ctx, &stubCallbackStore{taskErr: errors.New("conn closed")}, delivererTransient, kr, job)
	if out.delivered {
		t.Errorf("transient GetTask error must not deliver, got %+v", out)
	}
	if !out.retryable {
		t.Errorf("transient GetTask error must remain retryable, got %+v", out)
	}
	if delivererTransient.calls != 0 {
		t.Errorf("transient GetTask error must not reach deliverer, calls=%d", delivererTransient.calls)
	}

	// 对照：任务行正常（ErrNotFound 仅在 event 上）→ 照常投递，确认修复
	// 没有把 GetTask 判定误伤到健康任务。
	delivererHealthy := &stubCallbackDeliverer{}
	out = deliverOne(ctx, &stubCallbackStore{task: task, evErr: ErrNotFound}, delivererHealthy, kr, job)
	if !out.delivered || out.retryable {
		t.Errorf("healthy task with legacy (nil) event must still deliver, got %+v", out)
	}
	if delivererHealthy.calls != 1 {
		t.Errorf("healthy task must reach deliverer once, calls=%d", delivererHealthy.calls)
	}
}
