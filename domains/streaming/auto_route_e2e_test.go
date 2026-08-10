package streaming

// auto_route_e2e_test.go — end-to-end coverage for model=auto across the
// three request protocols (chat/messages/responses), exercising the full
// maybeResolveAuto* → Decider.DecideWithFeatureFlags → decisionToWire path
// with a real (non-DB) *autoroute.Decider wired via stub Classifier/Index.
//
// Scope:
//   - success: each protocol rewrites body to the winning model and
//     produces a wire decision with matching fields.
//   - error: decider failure surfaces shouldFail=true (no silent fallback).
//   - consistency: identical signals/candidates across protocols produce
//     the same chosen model and task classification.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/autoroute"
)

// e2eStubClassifier returns a fixed classification for every call.
type e2eStubClassifier struct {
	out *autoroute.Classification
	err error
}

func (s *e2eStubClassifier) Classify(_ context.Context, _ autoroute.ClassificationSignals) (*autoroute.Classification, error) {
	return s.out, s.err
}
func (s *e2eStubClassifier) Name() string { return "heuristic" }

// e2eStubIndex returns a fixed candidate list from Recommend, regardless
// of task/profile — sufficient for wiring a real Decider in tests.
type e2eStubIndex struct {
	cands []autoroute.ScoredCandidate
}

func (s *e2eStubIndex) Recommend(_ autoroute.TaskType, _ autoroute.ClassificationSignals, _ autoroute.Profile, topN int) []autoroute.ScoredCandidate {
	if topN > 0 && len(s.cands) > topN {
		return s.cands[:topN]
	}
	return s.cands
}
func (s *e2eStubIndex) Snapshot() []autoroute.Candidate { return nil }
func (s *e2eStubIndex) LastRefresh() time.Time          { return time.Now() }

// newE2EDecider wires a real *autoroute.Decider (no DB/Redis) that always
// classifies as TaskCode with high confidence and always recommends
// "claude-sonnet-4.5" as the winner.
func newE2EDecider() *autoroute.Decider {
	cls := &e2eStubClassifier{out: &autoroute.Classification{
		Primary: autoroute.TaskCode, Confidence: 0.95, Classifier: "heuristic", Reason: "code keyword",
	}}
	idx := &e2eStubIndex{cands: []autoroute.ScoredCandidate{{
		Candidate: autoroute.Candidate{
			CanonicalName: "claude-sonnet-4.5",
			RawModel:      "claude-sonnet-4.5",
			CredentialID:  42,
		},
		Breakdown: autoroute.ScoringBreakdown{Composite: 88, MatchScore: 80, PriceScore: 70},
	}}}
	return autoroute.NewDecider(cls, nil, idx, nil)
}

// newE2EFailingDecider wires a Decider whose classifier errors AND has no
// candidates, so Decide's own default-classification fallback still hits
// the "no candidates" error path — guaranteeing DecideWithFeatureFlags
// returns a non-nil error for the shouldFail=true assertions.
func newE2EFailingDecider() *autoroute.Decider {
	cls := &e2eStubClassifier{err: context.DeadlineExceeded}
	idx := &e2eStubIndex{cands: nil}
	return autoroute.NewDecider(cls, nil, idx, nil)
}

func enableAutoOnAllProtocols(t *testing.T) {
	t.Helper()
	old := autoroute.GetFeatureFlags()
	autoroute.SetGlobalFeatureFlagsForTest(&autoroute.FeatureFlags{
		AutoOnMessages:  true,
		AutoOnResponses: true,
	})
	t.Cleanup(func() { autoroute.SetGlobalFeatureFlagsForTest(old) })
}

// --- Success path -----------------------------------------------------

// TestAutoRouteE2E_Chat_Success verifies the /v1/chat/completions path:
// model is rewritten, wire decision matches the winning candidate.
func TestAutoRouteE2E_Chat_Success(t *testing.T) {
	ch := &ChatHandler{}
	ch.SetAutoRoute(newE2EDecider())

	reqBody := &chatRequestBody{Model: autoRequestMagic, Messages: []byte(`[{"role":"user","content":"write a function"}]`)}
	rawBody := []byte(`{"model":"auto","messages":[{"role":"user","content":"write a function"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	newBody, wire, shouldFail := ch.maybeResolveAuto(reqBody, rawBody, req, 7)

	if shouldFail {
		t.Fatal("success path must not set shouldFail")
	}
	if wire == nil {
		t.Fatal("expected non-nil wire decision")
	}
	if wire.ChosenModel != "claude-sonnet-4.5" {
		t.Fatalf("chosen model: got %q", wire.ChosenModel)
	}
	if wire.TaskType != string(autoroute.TaskCode) {
		t.Fatalf("task type: got %q", wire.TaskType)
	}
	if reqBody.Model != "claude-sonnet-4.5" {
		t.Fatalf("reqBody.Model not rewritten: got %q", reqBody.Model)
	}

	var decoded map[string]any
	if err := json.Unmarshal(newBody, &decoded); err != nil {
		t.Fatalf("rewritten body not valid JSON: %v", err)
	}
	if decoded["model"] != "claude-sonnet-4.5" {
		t.Fatalf("rewritten body model: got %v", decoded["model"])
	}
}

// TestAutoRouteE2E_Messages_Success verifies the /v1/messages (Anthropic)
// path with the flag enabled.
func TestAutoRouteE2E_Messages_Success(t *testing.T) {
	enableAutoOnAllProtocols(t)

	ch := &ChatHandler{}
	ch.SetAutoRoute(newE2EDecider())
	h := &MessagesHandler{chatHandler: ch}

	rb := &messagesRequestBody{Model: autoRequestMagic, Messages: []byte(`[{"role":"user","content":"write a function"}]`)}
	rawBody := []byte(`{"model":"auto","messages":[{"role":"user","content":"write a function"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	newBody, wire, shouldFail := h.maybeResolveAutoForMessages(rb, rawBody, req, 7)

	if shouldFail {
		t.Fatal("success path must not set shouldFail")
	}
	if wire == nil {
		t.Fatal("expected non-nil wire decision")
	}
	if wire.ChosenModel != "claude-sonnet-4.5" {
		t.Fatalf("chosen model: got %q", wire.ChosenModel)
	}
	if rb.Model != "claude-sonnet-4.5" {
		t.Fatalf("reqBody.Model not rewritten: got %q", rb.Model)
	}

	var decoded map[string]any
	if err := json.Unmarshal(newBody, &decoded); err != nil {
		t.Fatalf("rewritten body not valid JSON: %v", err)
	}
	if decoded["model"] != "claude-sonnet-4.5" {
		t.Fatalf("rewritten body model: got %v", decoded["model"])
	}
}

// TestAutoRouteE2E_Responses_Success verifies the /v1/responses (OpenAI
// Responses API) path with the flag enabled.
func TestAutoRouteE2E_Responses_Success(t *testing.T) {
	enableAutoOnAllProtocols(t)

	ch := &ChatHandler{}
	ch.SetAutoRoute(newE2EDecider())
	h := &ResponsesHandler{chatHandler: ch}

	rb := &responsesRequestBody{Model: autoRequestMagic, Input: []byte(`"write a function"`)}
	rawBody := []byte(`{"model":"auto","input":"write a function"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	newBody, wire, shouldFail := h.maybeResolveAutoForResponses(rb, rawBody, req, 7)

	if shouldFail {
		t.Fatal("success path must not set shouldFail")
	}
	if wire == nil {
		t.Fatal("expected non-nil wire decision")
	}
	if wire.ChosenModel != "claude-sonnet-4.5" {
		t.Fatalf("chosen model: got %q", wire.ChosenModel)
	}
	if rb.Model != "claude-sonnet-4.5" {
		t.Fatalf("reqBody.Model not rewritten: got %q", rb.Model)
	}

	var decoded map[string]any
	if err := json.Unmarshal(newBody, &decoded); err != nil {
		t.Fatalf("rewritten body not valid JSON: %v", err)
	}
	if decoded["model"] != "claude-sonnet-4.5" {
		t.Fatalf("rewritten body model: got %v", decoded["model"])
	}
}

// --- Error path ---------------------------------------------------------

// TestAutoRouteE2E_Chat_DeciderError verifies that a decider failure on
// /v1/chat/completions surfaces shouldFail=true with no body rewrite and
// no wire decision (caller must emit 502, not mask the failure).
func TestAutoRouteE2E_Chat_DeciderError(t *testing.T) {
	ch := &ChatHandler{}
	ch.SetAutoRoute(newE2EFailingDecider())

	reqBody := &chatRequestBody{Model: autoRequestMagic, Messages: []byte(`[{"role":"user","content":"hi"}]`)}
	rawBody := []byte(`{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	newBody, wire, shouldFail := ch.maybeResolveAuto(reqBody, rawBody, req, 7)

	if !shouldFail {
		t.Fatal("decider failure must set shouldFail=true")
	}
	if newBody != nil {
		t.Fatalf("failure path must not return a rewritten body, got %d bytes", len(newBody))
	}
	if wire != nil {
		t.Fatalf("failure path must not return a wire decision, got %+v", wire)
	}
}

// TestAutoRouteE2E_Messages_DeciderError mirrors the chat-path failure
// contract for /v1/messages.
func TestAutoRouteE2E_Messages_DeciderError(t *testing.T) {
	enableAutoOnAllProtocols(t)

	ch := &ChatHandler{}
	ch.SetAutoRoute(newE2EFailingDecider())
	h := &MessagesHandler{chatHandler: ch}

	rb := &messagesRequestBody{Model: autoRequestMagic, Messages: []byte(`[{"role":"user","content":"hi"}]`)}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	newBody, wire, shouldFail := h.maybeResolveAutoForMessages(rb, []byte(`{"model":"auto"}`), req, 7)

	if !shouldFail {
		t.Fatal("decider failure must set shouldFail=true")
	}
	if newBody != nil || wire != nil {
		t.Fatalf("failure path must not return body/wire: body=%v wire=%v", newBody != nil, wire != nil)
	}
}

// TestAutoRouteE2E_Responses_DeciderError mirrors the chat-path failure
// contract for /v1/responses.
func TestAutoRouteE2E_Responses_DeciderError(t *testing.T) {
	enableAutoOnAllProtocols(t)

	ch := &ChatHandler{}
	ch.SetAutoRoute(newE2EFailingDecider())
	h := &ResponsesHandler{chatHandler: ch}

	rb := &responsesRequestBody{Model: autoRequestMagic, Input: []byte(`"hi"`)}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	newBody, wire, shouldFail := h.maybeResolveAutoForResponses(rb, []byte(`{"model":"auto"}`), req, 7)

	if !shouldFail {
		t.Fatal("decider failure must set shouldFail=true")
	}
	if newBody != nil || wire != nil {
		t.Fatalf("failure path must not return body/wire: body=%v wire=%v", newBody != nil, wire != nil)
	}
}

// --- Cross-protocol consistency -----------------------------------------

// TestAutoRouteE2E_ConsistencyAcrossProtocols verifies that chat/messages/
// responses, given equivalent "write a function" input and the same
// decider, all resolve to the same chosen model and task classification.
// This guards against per-protocol signal-extraction drift silently
// producing different routing outcomes for the same logical request.
func TestAutoRouteE2E_ConsistencyAcrossProtocols(t *testing.T) {
	enableAutoOnAllProtocols(t)
	decider := newE2EDecider()

	ch := &ChatHandler{}
	ch.SetAutoRoute(decider)

	chatReq := &chatRequestBody{Model: autoRequestMagic, Messages: []byte(`[{"role":"user","content":"write a function"}]`)}
	_, chatWire, chatFail := ch.maybeResolveAuto(chatReq,
		[]byte(`{"model":"auto","messages":[{"role":"user","content":"write a function"}]}`),
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), 7)

	msgsH := &MessagesHandler{chatHandler: ch}
	msgsReq := &messagesRequestBody{Model: autoRequestMagic, Messages: []byte(`[{"role":"user","content":"write a function"}]`)}
	_, msgsWire, msgsFail := msgsH.maybeResolveAutoForMessages(msgsReq,
		[]byte(`{"model":"auto","messages":[{"role":"user","content":"write a function"}]}`),
		httptest.NewRequest(http.MethodPost, "/v1/messages", nil), 7)

	respH := &ResponsesHandler{chatHandler: ch}
	respReq := &responsesRequestBody{Model: autoRequestMagic, Input: []byte(`"write a function"`)}
	_, respWire, respFail := respH.maybeResolveAutoForResponses(respReq,
		[]byte(`{"model":"auto","input":"write a function"}`),
		httptest.NewRequest(http.MethodPost, "/v1/responses", nil), 7)

	if chatFail || msgsFail || respFail {
		t.Fatalf("no protocol should fail: chat=%v messages=%v responses=%v", chatFail, msgsFail, respFail)
	}
	if chatWire == nil || msgsWire == nil || respWire == nil {
		t.Fatalf("all protocols must produce a wire decision: chat=%v messages=%v responses=%v",
			chatWire, msgsWire, respWire)
	}

	if chatWire.ChosenModel != msgsWire.ChosenModel || chatWire.ChosenModel != respWire.ChosenModel {
		t.Fatalf("chosen model mismatch across protocols: chat=%q messages=%q responses=%q",
			chatWire.ChosenModel, msgsWire.ChosenModel, respWire.ChosenModel)
	}
	if chatWire.TaskType != msgsWire.TaskType || chatWire.TaskType != respWire.TaskType {
		t.Fatalf("task type mismatch across protocols: chat=%q messages=%q responses=%q",
			chatWire.TaskType, msgsWire.TaskType, respWire.TaskType)
	}
}
