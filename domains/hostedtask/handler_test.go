package hostedtask

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
)

// fakeStore 实现了 TaskStore，模拟幂等/租户/终态语义（矩阵 B，无 DB）。
type fakeStore struct {
	mu          sync.Mutex
	tasks       map[string]*Task // id → task
	byIdem      map[string]*Task // tenant|idem → task
	cancelCalls int              // recall 不得对终态任务触发 CAS 取消（R63）
}

func newFakeStore() *fakeStore {
	return &fakeStore{tasks: map[string]*Task{}, byIdem: map[string]*Task{}}
}

func (f *fakeStore) CreateTask(_ context.Context, in CreateInput) (*Task, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.byIdem[in.TenantID+"|"+in.IdempotencyKey]; ok {
		if existing.RequestHash != in.RequestHash {
			return nil, false, ErrIdempotencyConflict
		}
		return existing, false, nil
	}
	task := &Task{
		ID: "ht_" + in.IdempotencyKey, TenantID: in.TenantID, Goal: in.Goal,
		DoneWhen: in.DoneWhen, // 与真库 store.go CreateTask 同款持久化
		Status:   StatusDelegated, IdempotencyKey: in.IdempotencyKey,
		RequestHash: in.RequestHash, GwSessionID: "gw_ht_x",
		DeadlineAt: time.Now().Add(time.Hour), Revision: 1,
	}
	f.tasks[task.ID] = task
	f.byIdem[in.TenantID+"|"+in.IdempotencyKey] = task
	return task, true, nil
}

func (f *fakeStore) GetTask(_ context.Context, tenantID, id string) (*Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[id]
	if !ok || t.TenantID != tenantID {
		return nil, ErrNotFound
	}
	return t, nil
}

func (f *fakeStore) ListEvents(_ context.Context, tenantID, taskID string, _ int) ([]Event, error) {
	if _, err := f.GetTask(context.Background(), tenantID, taskID); err != nil {
		return nil, err
	}
	return []Event{{TaskID: taskID, Seq: 1, Type: EventAccepted}}, nil
}

func (f *fakeStore) CancelTask(_ context.Context, tenantID, id string) (*Task, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCalls++
	t, ok := f.tasks[id]
	if !ok || t.TenantID != tenantID {
		return nil, false, ErrNotFound
	}
	if t.Status.Terminal() {
		return t, false, nil
	}
	now := time.Now()
	t.Status = StatusCancelled
	t.CompletedAt = &now
	return t, true, nil
}

func newTestHandler(t *testing.T, store TaskStore) http.Handler {
	t.Helper()
	h := NewHandler(store, nil, Config{
		Workspaces:        map[string]string{"ws1": "/srv/ws1"},
		DefaultDeadline:   time.Hour,
		MaxDeadline:       24 * time.Hour,
		CallbackAllowlist: []string{"127.0.0.1"},
	}, nil, nil)
	return h
}

func doReq(handler http.Handler, method, path, key string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

const validBody = `{"goal":"修复构建","done_when":"三门全绿","environment":{"workspace_id":"ws1"}}`

// 矩阵 B：幂等/校验/方法/取消语义。
func TestHandlerIdempotentReplay(t *testing.T) {
	h := newTestHandler(t, newFakeStore())

	r1 := doReq(h, http.MethodPost, "/v1/hosted-tasks", "0123456789abcdef", validBody)
	if r1.Code != http.StatusAccepted {
		t.Fatalf("first create = %d, want 202: %s", r1.Code, r1.Body.String())
	}
	// 同键同体 → 200 原任务。
	r2 := doReq(h, http.MethodPost, "/v1/hosted-tasks", "0123456789abcdef", validBody)
	if r2.Code != http.StatusOK {
		t.Fatalf("replay = %d, want 200", r2.Code)
	}
	if !strings.Contains(r2.Body.String(), `"replayed":true`) {
		t.Errorf("replay must flag replayed=true: %s", r2.Body.String())
	}
	// 同键异体 → 409。
	r3 := doReq(h, http.MethodPost, "/v1/hosted-tasks", "0123456789abcdef", `{"goal":"另一个目标","environment":{"workspace_id":"ws1"}}`)
	if r3.Code != http.StatusConflict {
		t.Fatalf("conflicting body = %d, want 409", r3.Code)
	}
}

func TestHandlerValidation(t *testing.T) {
	h := newTestHandler(t, newFakeStore())

	// 缺 Idempotency-Key / 长度不足。
	if c := doReq(h, http.MethodPost, "/v1/hosted-tasks", "", validBody).Code; c != http.StatusBadRequest {
		t.Errorf("missing idem key = %d, want 400", c)
	}
	if c := doReq(h, http.MethodPost, "/v1/hosted-tasks", "short", validBody).Code; c != http.StatusBadRequest {
		t.Errorf("short idem key = %d, want 400", c)
	}
	// 缺 goal。
	if c := doReq(h, http.MethodPost, "/v1/hosted-tasks", "0123456789abcdef", `{}`).Code; c != http.StatusBadRequest {
		t.Errorf("missing goal = %d, want 400", c)
	}
	// 未知 workspace / 裸路径拒绝。
	if c := doReq(h, http.MethodPost, "/v1/hosted-tasks", "0123456789abcdef", `{"goal":"g","environment":{"workspace_id":"nope"}}`).Code; c != http.StatusBadRequest {
		t.Errorf("unknown workspace = %d, want 400", c)
	}
	// callback SSRF：metadata/私网拒绝；allowlist 内回环放行。
	for _, url := range []string{"http://169.254.169.254/latest", "http://10.1.2.3/x", "http://192.168.0.1/x", "file:///etc/passwd"} {
		body := `{"goal":"g","callback":{"url":"` + url + `"}}`
		if c := doReq(h, http.MethodPost, "/v1/hosted-tasks", "0123456789abcdef", body).Code; c != http.StatusBadRequest {
			t.Errorf("callback %s = %d, want 400", url, c)
		}
	}
}

func TestHandlerNotFoundUnifiesCrossTenant(t *testing.T) {
	store := newFakeStore()
	h := newTestHandler(t, store)
	doReq(h, http.MethodPost, "/v1/hosted-tasks", "0123456789abcdef", validBody)

	// 另一租户查询 → 统一 404（§4.1）。
	req := httptest.NewRequest(http.MethodGet, "/v1/hosted-tasks/ht_0123456789abcdef", nil)
	req = req.WithContext(withTenant(req.Context(), "tenant-B"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant get = %d, want 404", rec.Code)
	}
}

func TestHandlerMethodNotAllowedGuards(t *testing.T) {
	h := newTestHandler(t, newFakeStore())
	if c := doReq(h, http.MethodGet, "/v1/hosted-tasks", "", "").Code; c != http.StatusMethodNotAllowed {
		t.Errorf("GET collection = %d, want 405", c)
	}
	if c := doReq(h, http.MethodDelete, "/v1/hosted-tasks/ht_x", "", "").Code; c != http.StatusMethodNotAllowed {
		t.Errorf("DELETE task = %d, want 405", c)
	}
	// R63：recall 只接受 POST（P0 501 已升级为 §3.3 轻量快照路径）。
	if c := doReq(h, http.MethodGet, "/v1/hosted-tasks/ht_x/recall", "", "").Code; c != http.StatusMethodNotAllowed {
		t.Errorf("GET recall = %d, want 405", c)
	}
}

func TestHandlerResultRunning202AndMethodGuards(t *testing.T) {
	store := newFakeStore()
	h := newTestHandler(t, store)
	doReq(h, http.MethodPost, "/v1/hosted-tasks", "0123456789abcdef", validBody)

	// 非终态 result → 202 + Retry-After。
	req := httptest.NewRequest(http.MethodGet, "/v1/hosted-tasks/ht_0123456789abcdef/result", nil)
	req = req.WithContext(withTenant(req.Context(), ""))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("running result = %d, want 202", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("202 result must carry Retry-After")
	}
	// result 只接受 GET。
	if c := doReq(h, http.MethodPost, "/v1/hosted-tasks/ht_0123456789abcdef/result", "", "").Code; c != http.StatusMethodNotAllowed {
		t.Errorf("POST result = %d, want 405", c)
	}
}

func TestHandlerCancelTerminalConflict(t *testing.T) {
	store := newFakeStore()
	h := newTestHandler(t, store)
	doReq(h, http.MethodPost, "/v1/hosted-tasks", "0123456789abcdef", validBody)

	// 第一次取消 → 202 requested。
	r1 := doReq(h, http.MethodPost, "/v1/hosted-tasks/ht_0123456789abcdef/cancel", "", "")
	if r1.Code != http.StatusAccepted || !strings.Contains(r1.Body.String(), `"cancel_status":"requested"`) {
		t.Fatalf("cancel = %d %s, want 202 requested", r1.Code, r1.Body.String())
	}
	// 终态后再取消 → 409（矩阵 H/§4.1）。
	r2 := doReq(h, http.MethodPost, "/v1/hosted-tasks/ht_0123456789abcdef/cancel", "", "")
	if r2.Code != http.StatusConflict {
		t.Errorf("second cancel = %d, want 409", r2.Code)
	}
}

// ─── recall（§3.3 ④ 轻量快照路径，R63 P1 子集：cancel + 包，不动 ACC）──────

func TestHandlerRecallRunningCancelsAndReturnsPacket(t *testing.T) {
	store := newFakeStore()
	h := NewHandler(store, nil, Config{Workspaces: map[string]string{"ws1": "/srv/ws1"}}, nil, nil)
	fired := make(chan string, 1)
	h.SetACCCancel(func(_ context.Context, commandID string) { fired <- commandID })

	task, _, err := store.CreateTask(context.Background(), CreateInput{
		TenantID: "", Goal: "修复构建", DoneWhen: "三门全绿", IdempotencyKey: "0123456789abcdef",
		RequestHash: "x", Deadline: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	task.AccCommandID = "cmd-1"
	store.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/hosted-tasks/"+task.ID+"/recall", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("recall running task = %d, want 200 (P0 was 501): %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`"recall_status":"cancelled"`,
		`"goal":"修复构建"`,
		`"done_when":"三门全绿"`,
		`"current_result":{}`,
		`"next_owner":"recall_caller"`,
		// recall 自身取消的任务须如实携带"未跑完"blocker（调用方续跑的依据）。
		`"blockers":["cancelled: task did not run to completion"]`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("recall body missing %s: %s", want, body)
		}
	}
	// 轻量路径的 cancel：任务本地终态化（cancelled）。
	store.mu.Lock()
	st := task.Status
	store.mu.Unlock()
	if st != StatusCancelled {
		t.Errorf("after recall status = %s, want cancelled", st)
	}
	// 收尾钩子与 cancel 端点同款：尽力补发 ACC cancel(requested)。
	select {
	case got := <-fired:
		if got != "cmd-1" {
			t.Errorf("accCancel command = %s, want cmd-1", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("accCancel hook not fired after recall of running task")
	}
}

func TestHandlerRecallTerminalTaskSkipsCancel(t *testing.T) {
	store := newFakeStore()
	h := NewHandler(store, nil, Config{Workspaces: map[string]string{"ws1": "/srv/ws1"}}, nil, nil)

	task, _, err := store.CreateTask(context.Background(), CreateInput{
		TenantID: "", Goal: "已完成的任务", IdempotencyKey: "0123456789abcdef",
		RequestHash: "x", Deadline: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	task.Status = StatusCompleted
	task.Result = map[string]any{
		"outcome":       "success",
		"summary":       "三门全绿",
		"artifact_refs": []any{"file:///srv/ws1/out.md"},
	}
	store.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/hosted-tasks/"+task.ID+"/recall", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("recall terminal task = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`"recall_status":"already_terminal"`,
		`"outcome":"success"`,
		`"artifact_refs":["file:///srv/ws1/out.md"]`,
		`"blockers":[]`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("recall body missing %s: %s", want, body)
		}
	}
	store.mu.Lock()
	calls := store.cancelCalls
	store.mu.Unlock()
	if calls != 0 {
		t.Errorf("recall of terminal task must not attempt CancelTask, got %d calls", calls)
	}
}

func TestHandlerRecallNeedsReviewPacketDerivation(t *testing.T) {
	store := newFakeStore()
	h := NewHandler(store, nil, Config{Workspaces: map[string]string{"ws1": "/srv/ws1"}}, nil, nil)

	task, _, err := store.CreateTask(context.Background(), CreateInput{
		TenantID: "", Goal: "g", IdempotencyKey: "0123456789abcdef",
		RequestHash: "x", Deadline: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	task.Status = StatusNeedsReview
	task.Result = map[string]any{"outcome": "unknown_outcome"}
	store.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/hosted-tasks/"+task.ID+"/recall", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("recall needs_review = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// needs_review → blockers 非空 + requires_input_from=[human]（人工对账）。
	if !strings.Contains(body, `"blockers":["needs_review`) {
		t.Errorf("needs_review must surface a blocker: %s", body)
	}
	if !strings.Contains(body, `"requires_input_from":["human"]`) {
		t.Errorf("needs_review must require human input: %s", body)
	}
}

func TestHandlerRecallNotFoundAndMethodGuard(t *testing.T) {
	h := newTestHandler(t, newFakeStore())
	// 不存在/跨租户 → 统一 404（§4.1）。
	if c := doReq(h, http.MethodPost, "/v1/hosted-tasks/ht_missing/recall", "", "").Code; c != http.StatusNotFound {
		t.Errorf("recall missing = %d, want 404", c)
	}
}

func TestHandlerNeedsReviewExposedAsFailed(t *testing.T) {
	store := newFakeStore()
	h := newTestHandler(t, store)
	task, _, err := store.CreateTask(context.Background(), CreateInput{
		TenantID: "", Goal: "g", IdempotencyKey: "0123456789abcdef",
		RequestHash: "x", Deadline: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	task.Status = StatusNeedsReview
	task.Result = map[string]any{"outcome": "unknown_outcome"}
	store.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/hosted-tasks/"+task.ID+"/result", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("needs_review result = %d, want 200", rec.Code)
	}
	// §3.1：P0 暴露 failed(unknown)。
	if !strings.Contains(rec.Body.String(), `"status":"failed"`) || !strings.Contains(rec.Body.String(), `"outcome":"unknown_outcome"`) {
		t.Errorf("needs_review exposure wrong: %s", rec.Body.String())
	}
}

func TestAuthenticateUsesCrossPackageInvalidKeyError(t *testing.T) {
	// §2.4：无效 key 必须回 401（跨包 authentication.InvalidKeyError 断言），
	// 基础设施错误回 503。
	kv := &stubVerifier{mode: "invalid"}
	h := NewHandler(newFakeStore(), kv, Config{Workspaces: map[string]string{}}, nil, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/hosted-tasks", strings.NewReader(validBody))
	req.Header.Set("Authorization", "Bearer sk-bad")
	req.Header.Set("Idempotency-Key", "0123456789abcdef")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("invalid key = %d, want 401 (local-copy type trap would give 503)", rec.Code)
	}

	kv.mode = "infra"
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/hosted-tasks", strings.NewReader(validBody))
	req.Header.Set("Authorization", "Bearer sk-bad")
	req.Header.Set("Idempotency-Key", "0123456789abcdef")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("infra error = %d, want 503", rec.Code)
	}
}

type stubVerifier struct{ mode string }

func (s *stubVerifier) Enabled() bool { return true }
func (s *stubVerifier) Verify(_ context.Context, _ string) (KeyInfo, error) {
	switch s.mode {
	case "invalid":
		return KeyInfo{}, &authentication.InvalidKeyError{Message: "invalid"}
	case "infra":
		return KeyInfo{}, errors.New("db down")
	}
	return KeyInfo{ID: 1, TenantID: "tenant-A"}, nil
}

func withTenant(ctx context.Context, tenant string) context.Context {
	return context.WithValue(ctx, ctxKeyTenantID{}, tenant)
}
