package client

// session_analysis_test.go —— memora v2 会话分析推送器的判据（审计 G-2）。
//
// # 这道门钉什么
//
// 1. **契约面**（memora 契约 v1.1）：路径、两个必需头（Idempotency-Key /
//    X-Correlation-ID）、Authorization 的 service JWT（必需 claim 齐全、
//    scope 含 memory.write、**actor_id** 稳定）、体里的必填字段。
//    ★ **不**发 X-Project-ID：契约的必填头清单里没有它，而 memora 的
//      ProjectScope 一旦收到它就会让 ProjectAuthz 生效（无 authorizer 时
//      503）⇒ 发它会把一条本来能通的路由变成 503。
//    ★ 幂等键必须**逐字**等于 `<dispatch_id>-<analysis_kind>`：memora 的
//    handler 拿头与 payload.IdempotencyKey() 比对，不一致 ⇒ 422。
// 2. **零调用**：禁用态（缺 base URL / 缺 JWT 密钥 / 缺项目 id）⇒
//    **一次 HTTP 都不发**。这是「功能没开就不敲门」的硬要求。
// 3. **同一键只推一次**：重复触发 ⇒ 不产生第二次调用。
// 4. **memora 宕机不冒泡**：5xx / 连接失败 ⇒ 返回错误（由调用方吞掉），
//    409（idempotency_key_reuse）⇒ **不算错误**（memora 侧已落过、没有
//    新行，这是幂等重放的成功形态）。
//
// 判据纪律：第 2 条必须用「**计数**」而不是「没报错」来断言 ——
// 一个恒不调的桩也满足「没报错」。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func testPusher(t *testing.T, srvURL string, project string) *SessionAnalysisPusher {
	t.Helper()
	return NewSessionAnalysisPusher(SessionAnalysisConfig{
		BaseURL:   srvURL,
		JWTSecret: "test-secret",
		Issuer:    "llm-gateway-test",
		Audience:  "memora-test",
		TenantID:  "default",
		ProjectID: project,
		Timeout:   2 * time.Second,
	})
}

// TestSessionAnalysisPushSendsTheContractedRequest 是第 1 条。
func TestSessionAnalysisPushSendsTheContractedRequest(t *testing.T) {
	var (
		gotPath    string
		gotHeaders http.Header
		gotBody    map[string]any
		calls      atomic.Int32
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotPath = r.URL.Path
		gotHeaders = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"idempotency_key":"k"}}`))
	}))
	defer srv.Close()

	p := testPusher(t, srv.URL, "proj-1")
	payload := SessionAnalysisPayload{
		DispatchID:      "proj-1:default:sess-1:provisional",
		AnalysisKind:    "session_metadata",
		SourceSessionID: "sess-1",
		SchemaVersion:   "session-analysis/v1",
		Status:          "provisional",
		Title:           "会话标题",
		// 契约 v1.1 的三个新形态都在这里带上：结构化 work_types、
		// 一等的 features、与 features 分开的 tags。
		WorkTypes: []SessionAnalysisWorkType{
			{Value: "code", Source: "rule", Confidence: 0.8},
		},
		Features: []SessionAnalysisTag{
			{Key: "feature-k", Value: "feature-v", Source: "rule", Confidence: 0.5},
		},
		Tags: []SessionAnalysisTag{
			{Key: "topic", Value: "tag-v", Source: "auto", Confidence: 1},
		},
	}
	if err := p.Push(context.Background(), payload); err != nil {
		t.Fatalf("push failed: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d（应为 1）", calls.Load())
	}
	if gotPath != SessionAnalysisPath {
		t.Errorf("path = %q（应为 %q）", gotPath, SessionAnalysisPath)
	}
	// 幂等键必须逐字等于 <dispatch_id>-<analysis_kind>。
	wantKey := payload.IdempotencyKey()
	if got := gotHeaders.Get("Idempotency-Key"); got != wantKey {
		t.Errorf("Idempotency-Key = %q（应为 %q）—— memora 不一致就 422", got, wantKey)
	}
	if got := gotHeaders.Get("X-Correlation-ID"); strings.TrimSpace(got) == "" {
		t.Errorf("X-Correlation-ID 为空 —— memora 缺它就 422 missing_header")
	}
	// ★ **不发** X-Project-ID：project_id 走 body 顶层字段（memora 的
	//   session-analysis 路由就是这么设计的）；发这个头会让 memora 的
	//   ProjectScope 挂上作用域、进而让 ProjectAuthz 生效（无 authorizer
	//   时 503 acl_authorizer_unavailable）。
	if got := gotHeaders.Get("X-Project-ID"); got != "" {
		t.Errorf("X-Project-ID = %q（必须**不发** —— 契约 v1.1 的必填头清单里没有它，"+
			"且发它会让 memora 的 ProjectAuthz 生效 ⇒ 无 authorizer 时 503）", got)
	}
	// service JWT：必需 claim 齐全 + scope 含 memory.write。
	authz := gotHeaders.Get("Authorization")
	if !strings.HasPrefix(authz, "Bearer ") {
		t.Fatalf("Authorization = %q（应为 Bearer <jwt>）", authz)
	}
	claims := jwt.MapClaims{}
	_, _, err := jwt.NewParser().ParseUnverified(strings.TrimPrefix(authz, "Bearer "), claims)
	if err != nil {
		t.Fatalf("parse jwt: %v", err)
	}
	// ★ actor_id：memora 用它做 resolve-or-create 建出来的项目的 owner
	//   （EnsureProjectScope(ctx, tenantID, projectID, identity.ActorID)）
	//   ⇒ 必须是**稳定**的值，否则同一项目可能被反复判成「别人建的」。
	for _, claim := range []string{"iss", "aud", "sub", "tenant_id", "scope", "exp", "actor_id"} {
		if v, ok := claims[claim]; !ok || v == nil || v == "" {
			t.Errorf("service JWT 缺必需 claim %q（memora 的 V2JWTAuth 缺任一即 401）：claims=%v", claim, claims)
		}
	}
	scope, _ := claims["scope"].(string)
	if !strings.Contains(scope, "memory.write") {
		t.Errorf("scope = %q（必须含 memory.write，否则 memora 403 insufficient_scope）", scope)
	}
	// 体里的三个必填字段。
	for _, field := range []string{"project_id", "dispatch_id", "analysis_kind"} {
		if v, ok := gotBody[field]; !ok || v == "" {
			t.Errorf("请求体缺必填字段 %q：body=%v", field, gotBody)
		}
	}
	if gotBody["project_id"] != "proj-1" {
		t.Errorf("body.project_id = %v（应为 proj-1）", gotBody["project_id"])
	}
	// ★ features 与 tags 是**两个字段**（契约 v1.1 第 ② 条）：
	//   features = 网关 sessionmeta.Result.Features；tags = session_tags 的行。
	//   两者同构但来源不同，memora 分别落到两个元数据键 ⇒ **不能互串**。
	if _, ok := gotBody["features"]; !ok {
		t.Errorf("请求体没有 features 字段（契约 v1.1：features 是一等字段）")
	}
	if _, ok := gotBody["tags"]; !ok {
		t.Errorf("请求体没有 tags 字段（契约 v1.1：tags = 网关 session_tags 的行）")
	}
	// work_types 必须是**结构化对象**（契约 v1.1 第 ① 条）。
	wt, _ := gotBody["work_types"].([]any)
	if len(wt) != 1 {
		t.Fatalf("work_types = %v（应为 1 个结构化对象）", gotBody["work_types"])
	}
	obj, _ := wt[0].(map[string]any)
	if obj["value"] != "code" {
		t.Errorf("work_types[0].value = %v（应为 code）", obj["value"])
	}
	if _, ok := obj["confidence"]; !ok {
		t.Errorf("work_types[0] 缺 confidence（必须是结构化对象，不是裸字符串）：%v", obj)
	}
}

// TestSessionAnalysisFeaturesAndTagsAreNotInterchanged 钉住「不互串」。
//
// 这是 memora 二轮审计点名的缺陷形态：网关形态的 features 数组被**静默**
// 落进 tags ⇒ 分析被打上错误的标签，而且**不报任何错**。
func TestSessionAnalysisFeaturesAndTagsAreNotInterchanged(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := testPusher(t, srv.URL, "proj-1")
	err := p.Push(context.Background(), SessionAnalysisPayload{
		DispatchID:   "d1",
		AnalysisKind: "session_metadata",
		Title:        "t",
		Features: []SessionAnalysisTag{
			{Key: "feature-k", Value: "feature-v", Source: "rule", Confidence: 0.5},
		},
		Tags: []SessionAnalysisTag{
			{Key: "topic", Value: "tag-v", Source: "auto", Confidence: 1},
		},
	})
	if err != nil {
		t.Fatalf("push failed: %v", err)
	}
	feats, _ := gotBody["features"].([]any)
	tags, _ := gotBody["tags"].([]any)
	if len(feats) != 1 {
		t.Fatalf("features = %v（应为 1 条）", gotBody["features"])
	}
	if len(tags) != 1 {
		t.Fatalf("tags = %v（应为 1 条）", gotBody["tags"])
	}
	f, _ := feats[0].(map[string]any)
	g, _ := tags[0].(map[string]any)
	if f["key"] != "feature-k" {
		t.Errorf("features[0].key = %v（应为 feature-k）", f["key"])
	}
	if g["key"] != "topic" {
		t.Errorf("tags[0].key = %v（应为 topic）", g["key"])
	}
	// ★ 反向断言：features 里**不许**出现 tags 的内容。
	for _, raw := range feats {
		obj, _ := raw.(map[string]any)
		if obj["key"] == "topic" {
			t.Errorf("features 里混进了 tags 的内容（key=topic）—— 这就是「静默贴错标签」缺陷")
		}
	}
}

// TestSessionAnalysisProjectScopeConflictIsAnError 钉住 409 的两种形态。
//
// 契约 v1.1 §6：409 有两个 code，语义**相反**：
//
//	· idempotency_key_reuse —— 已落过域、无新行 ⇒ **成功**；
//	· project_scope_conflict —— 该 project id 被别的租户占用 ⇒ **错误**
//	  （静默当成功会让分析永远丢在门外且没人知道）。
func TestSessionAnalysisProjectScopeConflictIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"project_scope_conflict","message":"owned by another tenant"}}`))
	}))
	defer srv.Close()

	p := testPusher(t, srv.URL, "proj-1")
	err := p.Push(context.Background(), SessionAnalysisPayload{
		DispatchID: "d1", AnalysisKind: "session_metadata", Title: "t",
	})
	if err == nil {
		t.Fatalf("409 project_scope_conflict 必须算错误（静默当成功 ⇒ 分析永远丢在门外）")
	}
	if code := SessionAnalysisHTTPStatusOf(err); code != http.StatusConflict {
		t.Errorf("错误里没带状态码：%v", err)
	}
	if !strings.Contains(err.Error(), "project_scope_conflict") {
		t.Errorf("错误里没有错误码（排障要靠它）：%v", err)
	}
}

// TestSessionAnalysisFailedPushReleasesTheReservation 钉住「失败可重试」。
//
// ★ 为什么：去重表在**发请求前**占位（并发去重要求它），但失败时若不
//
//	释放，这次分析就**永远推不出去**了（除非进程重启）—— 那是丢数据。
//	memora 侧的 409 是跨进程兜底，本地失败必须自己可重试。
func TestSessionAnalysisFailedPushReleasesTheReservation(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusInternalServerError)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()

	p := testPusher(t, srv.URL, "proj-1")
	payload := SessionAnalysisPayload{DispatchID: "d1", AnalysisKind: "session_metadata", Title: "t"}
	for i := 0; i < 3; i++ {
		if err := p.Push(context.Background(), payload); err == nil {
			t.Fatalf("第 %d 次推送应当失败（5xx）", i)
		}
	}
	if calls.Load() != 3 {
		t.Errorf("失败后重试只发生了 %d 次（应为 3）—— 失败时没释放去重占位 ⇒ "+
			"这次分析永远推不出去", calls.Load())
	}
}

// TestSessionAnalysisDisabledMakesNoHTTPRequests 是第 2 条（零调用）。
func TestSessionAnalysisDisabledMakesNoHTTPRequests(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cases := []struct {
		name string
		cfg  SessionAnalysisConfig
	}{
		{"no base url", SessionAnalysisConfig{JWTSecret: "s", ProjectID: "p"}},
		{"no jwt secret", SessionAnalysisConfig{BaseURL: srv.URL, ProjectID: "p"}},
		{"no project id", SessionAnalysisConfig{BaseURL: srv.URL, JWTSecret: "s"}},
		{"zero value", SessionAnalysisConfig{}},
	}
	for _, c := range cases {
		p := NewSessionAnalysisPusher(c.cfg)
		if !p.Disabled() {
			t.Errorf("%s: 推送器应当是禁用态", c.name)
		}
		err := p.Push(context.Background(), SessionAnalysisPayload{
			DispatchID: "d", AnalysisKind: "session_metadata", Title: "t",
		})
		if err != nil {
			t.Errorf("%s: 禁用态不应返回错误：%v", c.name, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("禁用态发起了 %d 次 HTTP 调用（应为 0）", calls.Load())
	}
}

// TestSessionAnalysisSameKeyIsPushedOnce 是第 3 条。
func TestSessionAnalysisSameKeyIsPushedOnce(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := testPusher(t, srv.URL, "proj-1")
	payload := SessionAnalysisPayload{
		DispatchID:   "proj-1:default:sess-1:provisional",
		AnalysisKind: "session_metadata",
		Title:        "t",
	}
	for i := 0; i < 5; i++ {
		if err := p.Push(context.Background(), payload); err != nil {
			t.Fatalf("push #%d failed: %v", i, err)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("同一 (dispatch_id, analysis_kind) 推了 %d 次（应为 1）", calls.Load())
	}
	// 另一个 kind ⇒ 另一次调用（键不同）。
	other := payload
	other.AnalysisKind = "other_kind"
	if err := p.Push(context.Background(), other); err != nil {
		t.Fatalf("push other kind failed: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("不同 analysis_kind 推了 %d 次（应为 2）", calls.Load())
	}
}

// TestSessionAnalysisFailureIsReportedButReuseIsNot 是第 4 条。
func TestSessionAnalysisFailureIsReportedButReuseIsNot(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusInternalServerError)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()

	p := testPusher(t, srv.URL, "proj-1")
	payload := SessionAnalysisPayload{DispatchID: "d1", AnalysisKind: "session_metadata", Title: "t"}

	err := p.Push(context.Background(), payload)
	if err == nil {
		t.Fatalf("memora 5xx 应当返回错误（由调用方吞掉）")
	}
	if code := SessionAnalysisHTTPStatusOf(err); code != http.StatusInternalServerError {
		t.Errorf("错误里没带状态码：%v", err)
	}

	// 409 = 幂等重放（memora 侧已落过、没有新行）⇒ **不是错误**。
	status.Store(http.StatusConflict)
	p2 := testPusher(t, srv.URL, "proj-1")
	if err := p2.Push(context.Background(), payload); err != nil {
		t.Errorf("409 idempotency_key_reuse 不该算错误：%v", err)
	}
}

// TestSessionAnalysisEmptyPayloadIsNotSent 镜像 memora 的 Validate 规则：
// 没有可沉淀内容的载荷**不发**（不去换一个注定 422 的请求）。
func TestSessionAnalysisEmptyPayloadIsNotSent(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := testPusher(t, srv.URL, "proj-1")
	if err := p.Push(context.Background(), SessionAnalysisPayload{
		DispatchID: "d", AnalysisKind: "session_metadata",
	}); err != nil {
		t.Fatalf("push failed: %v", err)
	}
	if calls.Load() != 0 {
		t.Errorf("空载荷发了 %d 次请求（应为 0）—— 那注定被 memora 422 拒掉", calls.Load())
	}
}
