package routing

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/limiter"
)

// ───────────────────────────────────────────────────────────────────────────
// detectSessionModelSwitch 测试
// ───────────────────────────────────────────────────────────────────────────

func TestDetectSessionModelSwitch_SameModel_NoClear(t *testing.T) {
	c := getTestRedis(t)
	defer c.Close()

	prefStore := NewSessionPreferenceStore(c, 0)
	ctx := context.Background()

	const sessionID = "test-model-switch-same"
	const credID = 91000
	const model = "minimax-m3"
	t.Cleanup(func() { _ = prefStore.Clear(ctx, sessionID) })

	// 写入偏好
	if err := prefStore.Set(ctx, sessionID, credID, model); err != nil {
		t.Fatalf("Set err=%v", err)
	}

	router := NewRouter(NewStickyCache(), limiter.New())
	router.SessionPrefStore = prefStore
	e := &Executor{Router: router}

	params := newTestExecParamsWithModel(sessionID, model)
	e.detectSessionModelSwitch(params)

	// 偏好应保留
	entry, found, _ := prefStore.Get(ctx, sessionID)
	if !found || entry.CredentialID != credID {
		t.Fatal("same model should not clear session_pref")
	}
}

func TestDetectSessionModelSwitch_DifferentModel_Clears(t *testing.T) {
	c := getTestRedis(t)
	defer c.Close()

	prefStore := NewSessionPreferenceStore(c, 0)
	ctx := context.Background()

	const sessionID = "test-model-switch-different"
	const credID = 91001
	const oldModel = "minimax-m3"
	const newModel = "glm-4-flash"
	t.Cleanup(func() { _ = prefStore.Clear(ctx, sessionID) })

	// 写入旧 model 偏好
	if err := prefStore.Set(ctx, sessionID, credID, oldModel); err != nil {
		t.Fatalf("Set err=%v", err)
	}

	router := NewRouter(NewStickyCache(), limiter.New())
	router.SessionPrefStore = prefStore
	e := &Executor{Router: router}

	// 客户端请求切到新 model
	params := newTestExecParamsWithModel(sessionID, newModel)
	e.detectSessionModelSwitch(params)

	// 偏好应被清空
	_, found, _ := prefStore.Get(ctx, sessionID)
	if found {
		t.Fatal("model switch should clear session_pref")
	}
}

func TestDetectSessionModelSwitch_NoPref_NoOp(t *testing.T) {
	c := getTestRedis(t)
	defer c.Close()

	prefStore := NewSessionPreferenceStore(c, 0)
	ctx := context.Background()

	const sessionID = "test-model-switch-no-pref"
	_ = prefStore.Clear(ctx, sessionID) // 确保清空

	router := NewRouter(NewStickyCache(), limiter.New())
	router.SessionPrefStore = prefStore
	e := &Executor{Router: router}

	// 没有偏好
	params := newTestExecParamsWithModel(sessionID, "minimax-m3")
	e.detectSessionModelSwitch(params)

	// 不应 panic，且 Get 仍 miss
	_, found, _ := prefStore.Get(ctx, sessionID)
	if found {
		t.Fatal("expected miss (no pref was set)")
	}
}

func TestDetectSessionModelSwitch_EmptySessionID_NoOp(t *testing.T) {
	c := getTestRedis(t)
	defer c.Close()

	prefStore := NewSessionPreferenceStore(c, 0)
	router := NewRouter(NewStickyCache(), limiter.New())
	router.SessionPrefStore = prefStore
	e := &Executor{Router: router}

	// sessionID 为空
	params := newTestExecParamsWithModel("", "minimax-m3")
	e.detectSessionModelSwitch(params)
	// 不 panic 即可
}

func TestDetectSessionModelSwitch_EmptyClientModel_NoOp(t *testing.T) {
	c := getTestRedis(t)
	defer c.Close()

	prefStore := NewSessionPreferenceStore(c, 0)
	ctx := context.Background()

	const sessionID = "test-empty-clientmodel"
	t.Cleanup(func() { _ = prefStore.Clear(ctx, sessionID) })

	_ = prefStore.Set(ctx, sessionID, 123, "minimax-m3")

	router := NewRouter(NewStickyCache(), limiter.New())
	router.SessionPrefStore = prefStore
	e := &Executor{Router: router}

	// clientModel 为空
	params := newTestExecParamsWithModel(sessionID, "")
	e.detectSessionModelSwitch(params)

	// 偏好应保留（因为无法判断是否切模型）
	entry, found, _ := prefStore.Get(ctx, sessionID)
	if !found || entry.CredentialID != 123 {
		t.Fatal("empty client model should not clear")
	}
}

func TestDetectSessionModelSwitch_NilStores_NoOp(t *testing.T) {
	router := NewRouter(NewStickyCache(), limiter.New())
	// SessionPrefStore = nil
	e := &Executor{Router: router}

	params := newTestExecParamsWithModel("any", "minimax-m3")
	e.detectSessionModelSwitch(params)
	// 不 panic

	// nil receiver
	var nilExec *Executor
	nilExec.detectSessionModelSwitch(params)
}

func TestDetectSessionModelSwitch_NilParams_NoOp(t *testing.T) {
	c := getTestRedis(t)
	defer c.Close()

	prefStore := NewSessionPreferenceStore(c, 0)
	router := NewRouter(NewStickyCache(), limiter.New())
	router.SessionPrefStore = prefStore
	e := &Executor{Router: router}

	e.detectSessionModelSwitch(nil)
	// 不 panic
}

// ───────────────────────────────────────────────────────────────────────────
// 端到端：写入 pref → 切模型 → 清 pref → 新偏好建立
// ───────────────────────────────────────────────────────────────────────────

func TestIntegration_SessionPrefLifecycle_ModelSwitch(t *testing.T) {
	c := getTestRedis(t)
	defer c.Close()

	routeStore := NewRouteNodeStore(c, DefaultRouteNodeConfig())
	prefStore := NewSessionPreferenceStore(c, 0)

	router := NewRouter(NewStickyCache(), limiter.New())
	router.RouteNodeStore = routeStore
	router.SessionPrefStore = prefStore
	e := &Executor{Router: router}

	ctx := context.Background()
	const sessionID = "test-integration-lifecycle"
	const cred1 = 92000
	const cred2 = 92001
	const oldModel = "minimax-m3"
	const newModel = "glm-4-flash"
	// 先清理可能残留的状态（避免测试间污染）
	_ = prefStore.Clear(ctx, sessionID)
	_ = routeStore.Delete(ctx, cred1, oldModel)
	_ = routeStore.Delete(ctx, cred2, newModel)
	t.Cleanup(func() {
		_ = prefStore.Clear(ctx, sessionID)
		_ = routeStore.Delete(ctx, cred1, oldModel)
		_ = routeStore.Delete(ctx, cred2, newModel)
	})

	// 1. 首次成功（model1, cred1）→ 写入 session_pref
	params1 := newTestExecParamsWithModel(sessionID, oldModel)
	e.recordRouteNodeSuccess(params1, cred1, oldModel)

	entry, found, _ := prefStore.Get(ctx, sessionID)
	if !found || entry.CredentialID != cred1 {
		t.Fatalf("step 1: session_pref not set: entry=%+v found=%v", entry, found)
	}

	// 2. 客户端切到 model2 → detectSessionModelSwitch 应清空 session_pref
	params2 := newTestExecParamsWithModel(sessionID, newModel)
	e.detectSessionModelSwitch(params2)

	_, found, _ = prefStore.Get(ctx, sessionID)
	if found {
		t.Fatal("step 2: session_pref should be cleared after model switch")
	}

	// 3. 新 model 下的第一次成功（cred2）→ 写入新 session_pref
	e.recordRouteNodeSuccess(params2, cred2, newModel)

	entry, found, _ = prefStore.Get(ctx, sessionID)
	if !found || entry.CredentialID != cred2 || entry.Model != newModel {
		t.Fatalf("step 3: new session_pref should be cred2/newModel, got %+v", entry)
	}

	// 4. 旧 model 的 RouteNodeState 仍然保留（独立于会话）
	oldState, found, _ := routeStore.Get(ctx, cred1, oldModel)
	if !found || oldState.SuccessCount != 1 {
		t.Fatalf("step 4: old RouteNodeState should persist, got %+v found=%v", oldState, found)
	}
}

// 辅助：构造带 ClientModel 的 ExecParams
func newTestExecParamsWithModel(sessionID, clientModel string) *ExecParams {
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	if sessionID != "" {
		r.Header.Set("X-Gw-Session-Id", sessionID)
	}
	r.Header.Set("X-Request-Id", "test-request-id-"+sessionID+":"+clientModel)
	return &ExecParams{
		R:           r,
		SessionID:   sessionID,
		ClientModel: clientModel,
	}
}