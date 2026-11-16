package streaming

// auto_route_reselect_test.go —— auto 请求的**零候选重选**钉测
// （2026-11-16 auto 模式可用性轮）。
//
// ★ 守的是用户报障的第二个形态：
//
//	auto 决策选中的模型此刻没有可路由节点 ⇒ 修前当场 503 no_candidate，
//	客户端只看到「原来的模型不可用」，而 auto 候选链里明明还有能用的模型。
//
// 211 部署后首轮 live 验收实测（2026-11-17 00:24）：auto 选到
// `qwen2.5-7b-instruct`，该模型零候选 ⇒ HTTP 503 no_candidates。
//
// magpie 同款规则：auto 组消失时落到还有节点的成员，会话不中断
// （internal/provider/group.go:829-837 AutoStandIn）。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// reselectResolverStub 只给「有节点的模型」发候选；其余模型零候选。
type reselectResolverStub struct {
	// routable 是有可路由节点的模型集合。
	routable map[string]bool
	// asked 记录被问过的模型（按顺序）。
	asked []string
}

func (s *reselectResolverStub) Enabled() bool { return true }

func (s *reselectResolverStub) GetCandidates(ctx context.Context, model, profile, tenantID string) ([]provider.Candidate, *provider.Policy, error) {
	return s.GetCandidatesByModality(ctx, model, profile, tenantID, "")
}

func (s *reselectResolverStub) GetCandidatesByModality(_ context.Context, model, _, _, _ string) ([]provider.Candidate, *provider.Policy, error) {
	s.asked = append(s.asked, model)
	if !s.routable[model] {
		return nil, nil, nil
	}
	return []provider.Candidate{{
		CredentialID: 7,
		ProviderID:   3,
		RawModel:     model,
		BaseURL:      "http://upstream.invalid",
		Protocol:     "openai-completions",
		Routable:     true,
	}}, &provider.Policy{}, nil
}

func (s *reselectResolverStub) ModelKnown(context.Context, string) bool { return true }

func (s *reselectResolverStub) ModelModalityMismatch(context.Context, string, string, string, string) (*provider.ModalityMismatch, error) {
	return nil, nil
}

// TestAutoRequestReselect_SwitchesToRoutableModel pins the happy path: the
// first model on the chain is also dead, the second one works ⇒ switch.
func TestAutoRequestReselect_SwitchesToRoutableModel(t *testing.T) {
	stub := &reselectResolverStub{routable: map[string]bool{"live-model": true}}
	h := &ChatHandler{provider: stub}
	logCtx := &RequestLogContext{
		IsAutoRequest:      true,
		AutoFallbackModels: []string{"dead-model", "live-model"},
	}

	body := []byte(`{"model":"dead-model","messages":[]}`)
	res := h.autoRequestReselect(context.Background(), logCtx, "dead-model", "smart", "default", body)

	if !res.Switched {
		t.Fatal("expected the auto request to switch to a routable model")
	}
	if res.Model != "live-model" {
		t.Fatalf("switched model = %q, want live-model", res.Model)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(res.Candidates))
	}
	// The body must carry the NEW model, otherwise the upstream would be asked
	// for the dead one while the log says otherwise.
	var probe struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(res.Body, &probe); err != nil {
		t.Fatalf("rewritten body is not JSON: %v", err)
	}
	if probe.Model != "live-model" {
		t.Fatalf("rewritten body model = %q, want live-model", probe.Model)
	}
	// The chain must be consumed so a later round doesn't re-try the dead one.
	if len(logCtx.AutoFallbackModels) != 0 {
		t.Fatalf("fallback chain not consumed: %v", logCtx.AutoFallbackModels)
	}
}

// TestAutoRequestReselect_ChainExhausted pins the honest failure: no model
// on the chain has a node ⇒ report "not switched" so the caller keeps the
// original 503 (never fake a success).
func TestAutoRequestReselect_ChainExhausted(t *testing.T) {
	stub := &reselectResolverStub{routable: map[string]bool{}}
	h := &ChatHandler{provider: stub}
	logCtx := &RequestLogContext{
		IsAutoRequest:      true,
		AutoFallbackModels: []string{"dead-1", "dead-2"},
	}

	res := h.autoRequestReselect(context.Background(), logCtx, "chosen", "smart", "default",
		[]byte(`{"model":"chosen","messages":[]}`))
	if res.Switched {
		t.Fatalf("must not claim a switch when nothing is routable (model=%q)", res.Model)
	}
	if len(res.Tried) != 2 {
		t.Fatalf("tried = %v, want both chain entries", res.Tried)
	}
}

// TestAutoRequestReselect_NotAutoRequest pins the scope boundary: a request
// that named a model explicitly must NOT be silently re-routed — zero
// candidates for an explicit model means exactly that.
func TestAutoRequestReselect_NotAutoRequest(t *testing.T) {
	stub := &reselectResolverStub{routable: map[string]bool{"live-model": true}}
	h := &ChatHandler{provider: stub}
	logCtx := &RequestLogContext{
		IsAutoRequest:      false,
		AutoFallbackModels: []string{"live-model"},
	}

	res := h.autoRequestReselect(context.Background(), logCtx, "chosen", "smart", "default",
		[]byte(`{"model":"chosen","messages":[]}`))
	if res.Switched {
		t.Fatal("an explicit-model request must not be re-routed by the auto path")
	}
	if len(stub.asked) != 0 {
		t.Fatalf("resolver must not even be consulted, asked=%v", stub.asked)
	}
}

// TestAutoRequestReselect_RecordsLiveAction pins the observability: the
// switch shows up as a model_switch live action (the real-time stage tree
// shows the client that the model changed).
func TestAutoRequestReselect_RecordsLiveAction(t *testing.T) {
	stub := &reselectResolverStub{routable: map[string]bool{"live-model": true}}
	h := &ChatHandler{provider: stub}
	logCtx := &RequestLogContext{
		IsAutoRequest:      true,
		AutoFallbackModels: []string{"live-model"},
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	res := h.autoRequestReselect(r.Context(), logCtx, "dead-model", "smart", "default",
		[]byte(`{"model":"dead-model","messages":[]}`))
	if !res.Switched {
		t.Fatal("expected a switch")
	}
	// recordAutoReselect must be safe to call without an emitter (nil receiver).
	h.recordAutoReselect(r, "req-1", "dead-model", res, len(res.Candidates))
}

// ── 根修 5：宽退路池（AutoReselectModels）优先，窄链回退，两链互不掏空 ──

// TestAutoRequestReselect_PrefersWideReselectLadder pins the chain choice:
// the wide 48h-hot ladder wins over the narrow dispatch ladder. Without
// this, a zero-candidate request would keep walking the 3-model decision
// pool — the very pool whose members were all unusable at that moment
// (211 live evidence, 2026-11-17 00:24).
func TestAutoRequestReselect_PrefersWideReselectLadder(t *testing.T) {
	stub := &reselectResolverStub{routable: map[string]bool{"wide-live": true}}
	h := &ChatHandler{provider: stub}
	logCtx := &RequestLogContext{
		IsAutoRequest:      true,
		AutoReselectModels: []string{"wide-live"},
		AutoFallbackModels: []string{"narrow-live"},
	}

	res := h.autoRequestReselect(context.Background(), logCtx, "chosen", "smart", "default",
		[]byte(`{"model":"chosen","messages":[]}`))
	if !res.Switched {
		t.Fatal("expected a switch from the wide ladder")
	}
	if res.Model != "wide-live" {
		t.Fatalf("switched model = %q, want wide-live (the wide ladder must win)", res.Model)
	}
}

// TestAutoRequestReselect_FallsBackToNarrowLadder pins the fallback: with no
// wide ladder (no pool / query failed) the request keeps the old narrow
// behaviour instead of losing its ladder entirely.
func TestAutoRequestReselect_FallsBackToNarrowLadder(t *testing.T) {
	stub := &reselectResolverStub{routable: map[string]bool{"narrow-live": true}}
	h := &ChatHandler{provider: stub}
	logCtx := &RequestLogContext{
		IsAutoRequest:      true,
		AutoFallbackModels: []string{"narrow-live"},
	}

	res := h.autoRequestReselect(context.Background(), logCtx, "chosen", "smart", "default",
		[]byte(`{"model":"chosen","messages":[]}`))
	if !res.Switched || res.Model != "narrow-live" {
		t.Fatalf("expected the narrow ladder to be used, got switched=%v model=%q", res.Switched, res.Model)
	}
}

// TestAutoRequestReselect_WideLadderDoesNotConsumeDispatchLadder pins that
// the two ladders are consumed independently: the zero-candidate reselect
// happens BEFORE dispatch, and dispatch's model-change ladder may still be
// needed later (upstream failed after candidates existed).
func TestAutoRequestReselect_WideLadderDoesNotConsumeDispatchLadder(t *testing.T) {
	stub := &reselectResolverStub{routable: map[string]bool{"wide-live": true}}
	h := &ChatHandler{provider: stub}
	logCtx := &RequestLogContext{
		IsAutoRequest:      true,
		AutoReselectModels: []string{"wide-live"},
		AutoFallbackModels: []string{"dispatch-live"},
	}

	res := h.autoRequestReselect(context.Background(), logCtx, "chosen", "smart", "default",
		[]byte(`{"model":"chosen","messages":[]}`))
	if !res.Switched {
		t.Fatal("expected a switch")
	}
	if len(logCtx.AutoReselectModels) != 0 {
		t.Fatalf("wide ladder not consumed: %v", logCtx.AutoReselectModels)
	}
	if len(logCtx.AutoFallbackModels) != 1 || logCtx.AutoFallbackModels[0] != "dispatch-live" {
		t.Fatalf("dispatch ladder must survive the reselect, got %v", logCtx.AutoFallbackModels)
	}
}

// TestApplyAutoReselect_RewritesRequestContext pins the **wiring**: a
// successful reselect must rewrite the model, the body, the candidates, the
// policy and the modality together.
//
// Why this needs its own test: a half-rewrite (e.g. candidates replaced but
// the body still names the dead model) makes the upstream serve the model the
// log says we did NOT pick — invisible to every candidate-resolution test.
// The handler-side wiring is exactly where that defect lives.
func TestApplyAutoReselect_RewritesRequestContext(t *testing.T) {
	res := &autoReselectResult{
		Switched: true,
		Model:    "live-model",
		Body:     []byte(`{"model":"live-model","messages":[]}`),
		Candidates: []provider.Candidate{{
			CredentialID: 7,
			ProviderID:   3,
			RawModel:     "live-model",
			Routable:     true,
		}},
		Policy:   &provider.Policy{},
		Modality: "text",
	}

	clientModel := "dead-model"
	bodyBytes := []byte(`{"model":"dead-model","messages":[]}`)
	var candidates []provider.Candidate
	var policy *provider.Policy
	requestModality := "audio"

	if !applyAutoReselect(res, &clientModel, &bodyBytes, &candidates, &policy, &requestModality) {
		t.Fatal("applyAutoReselect must report the rewrite")
	}
	if clientModel != "live-model" {
		t.Errorf("clientModel = %q, want live-model", clientModel)
	}
	if !bytes.Contains(bodyBytes, []byte(`"model":"live-model"`)) {
		t.Errorf("bodyBytes still names the dead model: %s", bodyBytes)
	}
	if len(candidates) != 1 || candidates[0].RawModel != "live-model" {
		t.Errorf("candidates = %+v, want the live-model candidate", candidates)
	}
	if policy == nil {
		t.Error("policy must be carried over from the reselect")
	}
	if requestModality != "text" {
		t.Errorf("requestModality = %q, want text", requestModality)
	}
}

// TestApplyAutoReselect_NoSwitchLeavesContextUntouched pins the negative
// path: a failed reselect must not half-write anything.
func TestApplyAutoReselect_NoSwitchLeavesContextUntouched(t *testing.T) {
	res := &autoReselectResult{Switched: false}
	clientModel := "dead-model"
	bodyBytes := []byte(`{"model":"dead-model"}`)
	var candidates []provider.Candidate
	var policy *provider.Policy
	requestModality := "text"

	if applyAutoReselect(res, &clientModel, &bodyBytes, &candidates, &policy, &requestModality) {
		t.Fatal("applyAutoReselect must not report a rewrite when nothing was switched")
	}
	if clientModel != "dead-model" || requestModality != "text" || policy != nil || candidates != nil {
		t.Fatalf("context must stay untouched, got model=%q modality=%q policy=%v candidates=%v",
			clientModel, requestModality, policy, candidates)
	}
}

// TestAutoRequestReselect_WideLadderExhaustedFallsBackToNarrow pins that an
// exhausted wide ladder does not end the request: the narrow ladder is still
// tried. (211 live evidence: the three hot-top3 models were ALL unusable at
// the same moment — a wide-only ladder would have died on the same three.)
func TestAutoRequestReselect_WideLadderExhaustedFallsBackToNarrow(t *testing.T) {
	stub := &reselectResolverStub{routable: map[string]bool{"narrow-live": true}}
	h := &ChatHandler{provider: stub}
	logCtx := &RequestLogContext{
		IsAutoRequest:      true,
		AutoReselectModels: []string{"wide-dead-1", "wide-dead-2"},
		AutoFallbackModels: []string{"narrow-live"},
	}

	res := h.autoRequestReselect(context.Background(), logCtx, "chosen", "smart", "default",
		[]byte(`{"model":"chosen","messages":[]}`))
	if !res.Switched || res.Model != "narrow-live" {
		t.Fatalf("expected the narrow ladder to be tried after the wide one, got switched=%v model=%q",
			res.Switched, res.Model)
	}
	if len(res.Tried) != 3 {
		t.Fatalf("tried = %v, want the two wide entries plus the narrow one", res.Tried)
	}
	// The narrow ladder was walked ⇒ it must not stay behind for dispatch to
	// walk into the models we just proved dead.
	if len(logCtx.AutoFallbackModels) != 0 {
		t.Fatalf("narrow ladder must be consumed once walked: %v", logCtx.AutoFallbackModels)
	}
}
