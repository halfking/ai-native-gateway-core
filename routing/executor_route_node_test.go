package routing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/limiter"
)

// 构造一个最小的 ExecParams，仅用于 recordRouteNode* 测试
func newTestExecParams(sessionID string) *ExecParams {
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	if sessionID != "" {
		r.Header.Set("X-Gw-Session-Id", sessionID)
	}
	r.Header.Set("X-Request-Id", "test-request-id-1")
	return &ExecParams{
		R:        r,
		SessionID: sessionID,
	}
}

func newTestExecutorWithStores(t *testing.T) (*Executor, *RouteNodeStore, *SessionPreferenceStore, func()) {
	t.Helper()
	c := getTestRedis(t)

	routeNodeStore := NewRouteNodeStore(c, DefaultRouteNodeConfig())
	sessionPrefStore := NewSessionPreferenceStore(c, 0)

	router := NewRouter(NewStickyCache(), limiter.New())
	router.RouteNodeStore = routeNodeStore
	router.SessionPrefStore = sessionPrefStore

	cleanup := func() {
		// 不主动 Close redis 客户端，因为多个测试共享同一进程级 redis 连接
		// 但每个测试自己 Delete keys（已在测试内显式 t.Cleanup）
	}

	return &Executor{Router: router}, routeNodeStore, sessionPrefStore, cleanup
}

// ───────────────────────────────────────────────────────────────────────────
// recordRouteNodeSuccess 测试
// ───────────────────────────────────────────────────────────────────────────

func TestRecordRouteNodeSuccess_BothStoresUpdated(t *testing.T) {
	e, routeStore, prefStore, _ := newTestExecutorWithStores(t)
	ctx := context.Background()

	const sessionID = "test-route-node-success-1"
	const credID = 90100
	const model = "minimax-m3-success"
	t.Cleanup(func() {
		_ = routeStore.Delete(ctx, credID, model)
		_ = prefStore.Clear(ctx, sessionID)
	})

	params := newTestExecParams(sessionID)
	e.recordRouteNodeSuccess(params, credID, model)

	// 验证 RouteNodeState 已更新
	state, found, err := routeStore.Get(ctx, credID, model)
	if err != nil || !found {
		t.Fatalf("Get err=%v found=%v", err, found)
	}
	if state.SuccessCount != 1 {
		t.Fatalf("SuccessCount=%d, want 1", state.SuccessCount)
	}

	// 验证 SessionPrefStore 已设置
	entry, found, err := prefStore.Get(ctx, sessionID)
	if err != nil || !found {
		t.Fatalf("Get session_pref err=%v found=%v", err, found)
	}
	if entry.CredentialID != credID {
		t.Fatalf("session_pref=%d, want %d", entry.CredentialID, credID)
	}
}

func TestRecordRouteNodeSuccess_NoSessionID_NoPrefWrite(t *testing.T) {
	e, routeStore, _, _ := newTestExecutorWithStores(t)
	ctx := context.Background()

	const credID = 90101
	const model = "minimax-m3-success-no-session"
	t.Cleanup(func() { _ = routeStore.Delete(ctx, credID, model) })

	// SessionID 为空
	params := newTestExecParams("")
	e.recordRouteNodeSuccess(params, credID, model)

	// RouteNodeState 应有写入
	state, found, _ := routeStore.Get(ctx, credID, model)
	if !found || state.SuccessCount != 1 {
		t.Fatalf("expected state with SuccessCount=1, got found=%v state=%+v", found, state)
	}
}

func TestRecordRouteNodeSuccess_NilStores_NoOp(t *testing.T) {
	e := &Executor{Router: NewRouter(NewStickyCache(), limiter.New())}
	// RouteNodeStore 和 SessionPrefStore 都是 nil
	params := newTestExecParams("any-session")

	// 不应该 panic
	e.recordRouteNodeSuccess(params, 1, "minimax-m3")

	// nil receiver 也安全
	var nilExec *Executor
	nilExec.recordRouteNodeSuccess(params, 1, "minimax-m3")
}

// ───────────────────────────────────────────────────────────────────────────
// recordRouteNodeFailure 测试
// ───────────────────────────────────────────────────────────────────────────

func TestRecordRouteNodeFailure_CredentialLevelFailure_Counts(t *testing.T) {
	e, routeStore, _, _ := newTestExecutorWithStores(t)
	ctx := context.Background()

	const credID = 90200
	const model = "minimax-m3-fail"
	t.Cleanup(func() { _ = routeStore.Delete(ctx, credID, model) })

	params := newTestExecParams("any")

	// rate_limit 是 credential-level failure：计入
	justDisabled := e.recordRouteNodeFailure(params, credID, model, errorsx.KindRateLimit)
	if justDisabled {
		t.Fatal("1st failure should not trigger disabled")
	}

	state, _, _ := routeStore.Get(ctx, credID, model)
	if state.FailureCount != 1 {
		t.Fatalf("FailureCount=%d, want 1", state.FailureCount)
	}
}

func TestRecordRouteNodeFailure_3Streak_TriggersDisabled(t *testing.T) {
	e, routeStore, _, _ := newTestExecutorWithStores(t)
	ctx := context.Background()

	const credID = 90201
	const model = "minimax-m3-3streak"
	t.Cleanup(func() { _ = routeStore.Delete(ctx, credID, model) })

	params := newTestExecParams("any")

	// 3 次失败
	just1 := e.recordRouteNodeFailure(params, credID, model, errorsx.KindRateLimit)
	just2 := e.recordRouteNodeFailure(params, credID, model, errorsx.KindConcurrent)
	just3 := e.recordRouteNodeFailure(params, credID, model, errorsx.KindStreamTimeout)

	if just1 || just2 {
		t.Fatal("1st/2nd failure should not trigger disabled")
	}
	if !just3 {
		t.Fatal("3rd consecutive failure should trigger justDisabled")
	}

	// 验证 RouteNodeState 已 Disabled
	state, _, _ := routeStore.Get(ctx, credID, model)
	if !state.Disabled {
		t.Fatal("state should be Disabled after 3 failures")
	}
}

func TestRecordRouteNodeFailure_NonCredentialKinds_NotCounted(t *testing.T) {
	e, routeStore, _, _ := newTestExecutorWithStores(t)
	ctx := context.Background()

	const credID = 90202
	const model = "minimax-m3-transient"
	t.Cleanup(func() { _ = routeStore.Delete(ctx, credID, model) })

	params := newTestExecParams("any")

	// 这些 kind 都不计入 RouteNodeState
	nonCountingKinds := []errorsx.ErrorKind{
		errorsx.KindNetwork,
		errorsx.KindTimeout,
		errorsx.KindUpstreamDown,
		errorsx.KindContextLength,
		errorsx.KindCanceled,
		errorsx.KindModelNotFound,
	}

	for _, kind := range nonCountingKinds {
		_ = e.recordRouteNodeFailure(params, credID, model, kind)
	}

	// 验证：失败都没有被记录
	_, found, _ := routeStore.Get(ctx, credID, model)
	if found {
		t.Fatal("non-credential kinds should not create RouteNodeState")
	}
}

func TestRecordRouteNodeFailure_FatalKind_NotCounted(t *testing.T) {
	e, routeStore, _, _ := newTestExecutorWithStores(t)
	ctx := context.Background()

	const credID = 90203
	const model = "minimax-m3-fatal"
	t.Cleanup(func() { _ = routeStore.Delete(ctx, credID, model) })

	params := newTestExecParams("any")

	// fatal kind（auth/quota permanent）→ 不计入
	fatalKinds := []errorsx.ErrorKind{
		errorsx.KindAuth,
		errorsx.KindQuotaPermanent,
	}
	for _, kind := range fatalKinds {
		_ = e.recordRouteNodeFailure(params, credID, model, kind)
	}

	_, found, _ := routeStore.Get(ctx, credID, model)
	if found {
		t.Fatal("fatal kinds should not create RouteNodeState (credential is dead)")
	}
}

func TestRecordRouteNodeFailure_NilStores_NoOp(t *testing.T) {
	e := &Executor{Router: NewRouter(NewStickyCache(), limiter.New())}
	params := newTestExecParams("any")

	// nil stores 时 shouldNotPanic
	_ = e.recordRouteNodeFailure(params, 1, "minimax-m3", errorsx.KindRateLimit)
}

func TestRecordRouteNodeSuccess_AfterDisabled_Recovers(t *testing.T) {
	e, routeStore, _, _ := newTestExecutorWithStores(t)
	ctx := context.Background()

	const credID = 90204
	const model = "minimax-m3-recover"
	t.Cleanup(func() { _ = routeStore.Delete(ctx, credID, model) })

	params := newTestExecParams("any")

	// 3 次失败触发 Disabled
	for i := 0; i < 3; i++ {
		e.recordRouteNodeFailure(params, credID, model, errorsx.KindRateLimit)
	}

	state, _, _ := routeStore.Get(ctx, credID, model)
	if !state.Disabled {
		t.Fatal("should be disabled")
	}

	// 上游成功应清 Disabled
	e.recordRouteNodeSuccess(params, credID, model)

	state, _, _ = routeStore.Get(ctx, credID, model)
	if state.Disabled {
		t.Fatal("Disabled should be cleared by RecordSuccess")
	}
	if state.SuccessCount != 1 {
		t.Fatalf("SuccessCount=%d, want 1", state.SuccessCount)
	}
}

// ───────────────────────────────────────────────────────────────────────────
// isCredentialLevelFailureForRouteNode 纯函数测试
// ───────────────────────────────────────────────────────────────────────────

func TestIsCredentialLevelFailureForRouteNode(t *testing.T) {
	tests := []struct {
		kind errorsx.ErrorKind
		want bool
	}{
		{errorsx.KindRateLimit, true},
		{errorsx.KindConcurrent, true},
		{errorsx.KindStreamTimeout, true},
		{errorsx.KindTransient, true},
		// 非 credential-level
		{errorsx.KindNetwork, false},
		{errorsx.KindTimeout, false},
		{errorsx.KindUpstreamDown, false},
		{errorsx.KindContextLength, false},
		{errorsx.KindCanceled, false},
		{errorsx.KindModelNotFound, false},
		// fatal
		{errorsx.KindAuth, false},
		{errorsx.KindQuotaPermanent, false},
	}

	for _, tt := range tests {
		if got := isCredentialLevelFailureForRouteNode(tt.kind); got != tt.want {
			t.Errorf("kind=%v: got %v, want %v", tt.kind, got, tt.want)
		}
	}
}

// ───────────────────────────────────────────────────────────────────────────
// 端到端集成：SessionPref + RouteNodeState 联动
// ───────────────────────────────────────────────────────────────────────────

func TestIntegration_SessionPrefAndRouteNodeState(t *testing.T) {
	e, routeStore, prefStore, _ := newTestExecutorWithStores(t)
	ctx := context.Background()

	const sessionID = "test-integration-e2e"
	const credID = 90300
	const model = "minimax-m3-integration"
	t.Cleanup(func() {
		_ = routeStore.Delete(ctx, credID, model)
		_ = prefStore.Clear(ctx, sessionID)
	})

	params := newTestExecParams(sessionID)

	// 1. 第一次成功：建立 SessionPref
	e.recordRouteNodeSuccess(params, credID, model)

	gotEntry, found, _ := prefStore.Get(ctx, sessionID)
	if !found || gotEntry.CredentialID != credID {
		t.Fatalf("session_pref not set: got=%d found=%v", gotEntry.CredentialID, found)
	}

	// 2. 同会话后续 3 次失败：触发 RouteNodeState Disabled
	for i := 0; i < 3; i++ {
		e.recordRouteNodeFailure(params, credID, model, errorsx.KindRateLimit)
	}

	state, _, _ := routeStore.Get(ctx, credID, model)
	if !state.Disabled {
		t.Fatal("should be disabled after 3 failures")
	}

	// 3. 此时 PlanCandidates 应过滤掉该 candidate
	router := e.Router
	router.RouteNodeStore = routeStore
	router.SessionPrefStore = prefStore

	candidates := []providerCandidateForTest{
		{credentialID: credID, model: model},
		{credentialID: 90301, model: model},
	}
	_ = candidates // Placeholder

	// 直接用 IsUsable 验证
	if router.RouteNodeStore.IsUsable(ctx, credID, model) {
		t.Fatal("credID should be unusable after Disabled")
	}
	if !router.RouteNodeStore.IsUsable(ctx, 90301, model) {
		t.Fatal("credID 90301 should be usable (no failures)")
	}
}

// 类型别名（避免 import provider 包到 test 文件造成混乱）
type providerCandidateForTest = struct {
	credentialID int
	model        string
}

// 防止 http 引用警告
var _ = http.MethodPost