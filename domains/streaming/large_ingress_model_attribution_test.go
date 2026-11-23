package streaming

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/kaixuan/llm-gateway-go/internal/largebody"
	"github.com/kaixuan/llm-gateway-go/internal/modelattr"
	"github.com/kaixuan/llm-gateway-go/internal/sessioncontext"
)

// ★ 2026-11-24（模型归属审计）：入口拒绝此前把 client_model 硬编码成
// "<unknown>"，而 body 明明已经在手上 ⇒ 211 生产 1,290 行失败请求
// 100% 归到「未知模型」。本测试钉住「body 里有 model ⇒ 拒绝行必须记真模型」。
//
// 覆盖两条取值来源：
//   ① 未归档的早期拒绝出口（容量/编码/超限）⇒ 从 body 前缀取；
//   ② 已归档的拒绝出口（缩减失败/会话解析失败）⇒ 从归档体取。

// newIngressModelTestHandler 建一个只带遥测钩子的 handler（无 DB、无 executor）。
func newIngressModelTestHandler(t *testing.T) (*ChatHandler, *[]*telemetry.RequestLogEntry) {
	t.Helper()
	var entries []*telemetry.RequestLogEntry
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	telemetryClient := telemetry.NewClient()
	telemetryClient.SetRequestLogSink(ingressModelDiscardSink{})
	t.Cleanup(telemetryClient.Stop)
	h.SetTelemetry(telemetryClient)
	h.SetRequestLogHook(func(entry *telemetry.RequestLogEntry) {
		copied := *entry
		entries = append(entries, &copied)
	})
	return h, &entries
}

// ingressModelDiscardSink 吞掉遥测写入（本测试只看 hook 收到的条目）。
type ingressModelDiscardSink struct{}

func (ingressModelDiscardSink) PersistRequestLog(context.Context, *telemetry.RequestLogEntry) error {
	return nil
}

func ingressModelOf(entries []*telemetry.RequestLogEntry) string {
	for _, e := range entries {
		if e.ClientModel != nil {
			return *e.ClientModel
		}
	}
	return ""
}

// TestLargeIngressRejectionKeepsClientModel pins: an ingress rejection that
// happens **after the body was read** must still be attributed to the model
// the client asked for.
//
// ★ 2026-11-24（模型归属审计）：入口拒绝此前把 client_model 硬编码成
// "<unknown>"。211 生产实测 1,290 行失败请求 100% 归到「未知模型」，
// 而 body 明明已经在手上。
//
// 触发路径：客户端自称的会话不属于它 ⇒ 归档后、解析会话时 403
// session_forbidden。此时 prefix 已读入内存 ⇒ 模型可归因。
func TestLargeIngressRejectionKeepsClientModel(t *testing.T) {
	store, _ := largebody.NewStore(t.TempDir())
	defer store.Close()
	getter := &largeIngressOwnerStore{stubSessionGetter: &stubSessionGetter{got: map[string]*session.Session{
		"gw_victim": {SessionID: "gw_victim", APIKeyID: 42, TenantID: "tenant-1"},
	}}}
	h, entries := newIngressModelTestHandler(t)
	h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 99, TenantID: "tenant-1"}})
	h.sessionGetter = getter
	ingress := NewLargeIngress(h, store, LargeIngressConfig{})

	body := []byte(`{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hi"}]}`)
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer test")
	r.Header.Set("X-Gw-Session-Id", "gw_victim")
	w := httptest.NewRecorder()
	ingress.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("foreign-session request forwarded")
	})).ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if len(*entries) == 0 {
		t.Fatal("no request log entry emitted for ingress rejection")
	}
	got := ingressModelOf(*entries)
	if got != "glm-5.3-flash" {
		t.Fatalf("client_model = %q, want glm-5.3-flash（入口拒绝不得丢模型）", got)
	}
}

// TestLargeIngressCapacityRejectionKeepsPlaceholder covers the early exits
// (before any body read).
//
// ★ 这些出口**不可归因到模型**，而且这是**正确**的：入口拒绝有「拒绝时
// 不得再读网络 body」的硬契约（TestLargeIngressAuthenticatesBeforeReading
// 钉住：401 时 body 必须没被读）。在 body 未被读之前去读它，正是那条
// 契约禁止的行为（未鉴权请求会借网关把 body 读进内存，成为放大器）。
//
// ⇒ 这些行记占位符是**如实**的：网关确实不知道客户端要哪个模型。
// 断言的是「记占位符而不是空串」——空串会击穿所有 NULLIF 读链。
func TestLargeIngressCapacityRejectionKeepsPlaceholder(t *testing.T) {
	store, _ := largebody.NewStore(t.TempDir())
	defer store.Close()
	h, entries := newIngressModelTestHandler(t)
	// MaxConcurrent=1 + 已占满 ⇒ 第二个请求撞容量闸（body 尚未被读走）。
	ingress := NewLargeIngress(h, store, LargeIngressConfig{MaxConcurrent: 1})
	ingress.slots <- struct{}{} // 占满

	body := []byte(`{"model":"MiniMax-M3","messages":[]}`)
	w := httptest.NewRecorder()
	ingress.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("over-capacity request forwarded")
	})).ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(body))))

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", w.Code)
	}
	if len(*entries) == 0 {
		t.Fatal("no request log entry emitted for capacity rejection")
	}
	got := ingressModelOf(*entries)
	if got != modelattr.UnknownPlaceholder {
		t.Fatalf("client_model = %q, want %q（body 未读时不得读 body，模型确实不可知）", got, modelattr.UnknownPlaceholder)
	}
}

// TestLargeIngressAuthRejectionDoesNotReadBody pins the security contract
// that the model recovery must NOT violate: an unauthenticated rejection must
// not read the network body.
//
// ★ 这是本轮修法的**关键约束**：模型归属是「可观测性诉求」，而「拒绝时
// 不读 body」是**安全契约**。前者不得破坏后者。
func TestLargeIngressAuthRejectionDoesNotReadBody(t *testing.T) {
	store, _ := largebody.NewStore(t.TempDir())
	defer store.Close()
	h, _ := newIngressModelTestHandler(t)
	ingress := NewLargeIngress(h, store, LargeIngressConfig{})
	h.staticDataPlaneKey = "correct"

	rbody := &forbiddenRead{}
	r := httptest.NewRequest("POST", "/v1/chat/completions", rbody)
	r.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	ingress.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("unauthorized forwarded")
	})).ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if rbody.read {
		t.Fatal("ingress rejection read the network body —— 违反「拒绝时不读 body」契约")
	}
}

// TestLargeIngressRejectionWithoutModelKeepsPlaceholder pins the other half:
// when the body genuinely carries no model, the row must still carry the
// placeholder (not an empty string) so it stays distinguishable from
// "we never looked".
func TestLargeIngressRejectionWithoutModelKeepsPlaceholder(t *testing.T) {
	store, _ := largebody.NewStore(t.TempDir())
	defer store.Close()
	h, entries := newIngressModelTestHandler(t)
	ingress := NewLargeIngress(h, store, LargeIngressConfig{MaxConcurrent: 1})
	ingress.slots <- struct{}{}

	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	w := httptest.NewRecorder()
	ingress.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(w,
		httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(body))))

	if len(*entries) == 0 {
		t.Fatal("no request log entry emitted")
	}
	got := ingressModelOf(*entries)
	if got != modelattr.UnknownPlaceholder {
		t.Fatalf("client_model = %q, want %q（body 无 model 时仍写占位符）", got, modelattr.UnknownPlaceholder)
	}
}

// TestLargeIngressRejectionNormalizesAliasPrefix pins that an ingress
// rejection groups under the same model key as an accepted request: the
// alias prefix is stripped and the name is lowercased.
func TestLargeIngressRejectionNormalizesAliasPrefix(t *testing.T) {
	store, _ := largebody.NewStore(t.TempDir())
	defer store.Close()
	getter := &largeIngressOwnerStore{stubSessionGetter: &stubSessionGetter{got: map[string]*session.Session{
		"gw_victim": {SessionID: "gw_victim", APIKeyID: 42, TenantID: "tenant-1"},
	}}}
	h, entries := newIngressModelTestHandler(t)
	h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 99, TenantID: "tenant-1"}})
	h.sessionGetter = getter
	ingress := NewLargeIngress(h, store, LargeIngressConfig{})

	body := []byte(`{"model":"kx-GLM-5.3-Flash","messages":[]}`)
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer test")
	r.Header.Set("X-Gw-Session-Id", "gw_victim")
	w := httptest.NewRecorder()
	ingress.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(w, r)

	if len(*entries) == 0 {
		t.Fatal("no request log entry emitted")
	}
	got := ingressModelOf(*entries)
	if got != "glm-5.3-flash" {
		t.Fatalf("client_model = %q, want glm-5.3-flash（别名前缀须与受理路径一致地归一）", got)
	}
}

// TestRejectedRequestModelReadsArchivedBody pins the archived path: when the
// body was archived (and the prefix is gone), the model is still recoverable
// from the archive.
func TestRejectedRequestModelReadsArchivedBody(t *testing.T) {
	store, _ := largebody.NewStore(t.TempDir())
	defer store.Close()
	h, _ := newIngressModelTestHandler(t)
	ingress := NewLargeIngress(h, store, LargeIngressConfig{})

	// 归档一个 body，但**不**把 model 放进 context（模拟 prefix 不可得）。
	body := []byte(`{"model":"archived-model","messages":[]}`)
	ref, err := store.Capture(context.Background(),
		largebody.Identity{TenantID: "default", SessionID: "sess-1"},
		strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("capture: %v", err)
	}

	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(""))
	r = r.WithContext(context.WithValue(r.Context(), largeArchiveKey{}, largeArchive{ref, sessioncontext.Report{}}))

	got := ingress.rejectedRequestModel(r)
	if got != "archived-model" {
		t.Fatalf("rejectedRequestModel = %q, want archived-model", got)
	}
}

// TestRejectedRequestModelFallsBackToPlaceholder pins the no-signal case.
func TestRejectedRequestModelFallsBackToPlaceholder(t *testing.T) {
	store, _ := largebody.NewStore(t.TempDir())
	defer store.Close()
	h, _ := newIngressModelTestHandler(t)
	ingress := NewLargeIngress(h, store, LargeIngressConfig{})

	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(""))
	got := ingress.rejectedRequestModel(r)
	if got != modelattr.UnknownPlaceholder {
		t.Fatalf("rejectedRequestModel = %q, want %q", got, modelattr.UnknownPlaceholder)
	}
}

// TestNormalizeIngressModel pins the normalization contract directly.
func TestNormalizeIngressModel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"glm-5.3-flash", "glm-5.3-flash"},
		{"GLM-5.3-Flash", "glm-5.3-flash"},
		{"kx-glm-5.3-flash", "glm-5.3-flash"},
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := normalizeIngressModel(c.in); got != c.want {
			t.Errorf("normalizeIngressModel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestLargeIngressRejectionModelIsNotEmptyString pins: the rejected row must
// never carry an empty client_model (an empty string defeats every
// NULLIF-based read-side chain).
func TestLargeIngressRejectionModelIsNotEmptyString(t *testing.T) {
	store, _ := largebody.NewStore(t.TempDir())
	defer store.Close()
	h, entries := newIngressModelTestHandler(t)
	ingress := NewLargeIngress(h, store, LargeIngressConfig{MaxConcurrent: 1})
	ingress.slots <- struct{}{}

	w := httptest.NewRecorder()
	ingress.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(w,
		httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"messages":[]}`)))

	for _, e := range *entries {
		if e.ClientModel == nil {
			t.Fatal("client_model is nil; the write side must always stamp a value")
		}
		if *e.ClientModel == "" {
			t.Fatal("client_model is empty; read-side NULLIF chains cannot see it")
		}
	}
}

// jsonModelFieldExists is a tiny helper used by the archive test to assert the
// fixture really carries a model field (guards against a silently-empty body).
func jsonModelFieldExists(body []byte) bool {
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false
	}
	_, ok := parsed["model"]
	return ok
}
