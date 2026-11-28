package executors

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/redis/go-redis/v9"
)

// 2026-11-28（glm-5.3-flash 单候选 429 事故）：指纹槽预过滤必须是排序而不是
// 过滤。211 实测 4/4 硬失败同形——稳定供应商 33370/587 的凭据因槽被他人占满被
// 预过滤摘除，请求只剩智谱一家，一次 429 就终态「All providers unavailable」。
// 本文件从三个层钉住修法：分区（纯函数）→ 梯子（dispatchRoute）→ 端到端。

const fpFailoverSlotKey = "llmgw:tenant:default:cred_fp_slot:%d:0"

func newFpFailoverExecutor(t *testing.T, activeGateSeconds int) (*Executor, *credentialfpslot.Manager, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	fpMgr := credentialfpslot.New(credentialfpslot.Config{DefaultLimit: 1, Enabled: true, ActiveGateSeconds: activeGateSeconds}, client)

	lim := newLimiterForTest()
	t.Cleanup(lim.Stop)
	e := &Executor{
		Circuit:         newCircuitManagerForTest(),
		Limiter:         lim,
		Router:          NewRouter(NewStickyCache(), lim),
		FpSlots:         fpMgr,
		UpstreamTimeout: 5 * time.Second,
		StreamTimeout:   10 * time.Second,
	}
	wireDispatchPipelineForTest(t, e)
	return e, fpMgr, mr
}

func fpFailoverCandidate(credentialID int, apiKey string) provider.Candidate {
	fpLimit := 1
	cand := provider.Candidate{
		ProviderID:   credentialID,
		CredentialID: credentialID,
		BaseURL:      "http://unused.invalid",
		Protocol:     "openai-completions",
		APIKey:       apiKey,
		RawModel:     "glm-5.3-flash",
		FpSlotLimit:  &fpLimit,
		Tier:         1,
		BillingMode:  "token",
	}
	cand.Routable = true
	return cand
}

func fpFailoverParams(candidates []provider.Candidate) *ExecParams {
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(""))
	r.Header.Set("X-Request-Id", "fp-failover")
	return &ExecParams{
		W:                           httptest.NewRecorder(),
		R:                           r,
		BodyBytes:                   []byte(`{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hi"}]}`),
		ClientModel:                 "glm-5.3-flash",
		OutboundModel:               "glm-5.3-flash",
		Candidates:                  candidates,
		StickyKey:                   "default:9:89:default",
		Policy:                      &provider.Policy{TierFallbackMax: 4, RetryPerCredential: 0},
		DispatchAllowProviderChange: true,
	}
}

func refCredentialIDs(refs []dispatch.CredentialRef) []int {
	out := make([]int, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.CredentialID)
	}
	return out
}

func equalIDs(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// 1. 分区（纯函数层）
// ---------------------------------------------------------------------------

func TestFpPartitionOrdersThreeTiersAndDropsNothing(t *testing.T) {
	e, fpMgr, mr := newFpFailoverExecutor(t, 5)

	// cred 11: the caller already holds a pin. cred 12: held by a holder idle
	// past the gate. cred 13: held by an ACTIVE holder. cred 10: free.
	acquireFpSlot(t, fpMgr, 11, "caller")
	acquireFpSlot(t, fpMgr, 12, "idle-user")
	acquireFpSlot(t, fpMgr, 13, "busy-user")
	mr.FastForward(6 * time.Second)
	// Refresh only the busy holder so it stays inside the gate.
	acquireFpSlot(t, fpMgr, 13, "busy-user")

	free := fpFailoverCandidate(10, "sk-free")
	pinned := fpFailoverCandidate(11, "sk-pinned")
	preemptable := fpFailoverCandidate(12, "sk-preemptable")
	slotless := fpFailoverCandidate(13, "sk-slotless")
	params := fpFailoverParams(nil)

	primary, pre, slot := e.fpPartitionCandidates(params, []provider.Candidate{slotless, free, preemptable, pinned}, "caller")
	if !equalIDs(candidateIDs(primary), []int{10, 11}) {
		t.Fatalf("primary = %v, want [10 11] (free + pinned, router order kept)", candidateIDs(primary))
	}
	if !equalIDs(candidateIDs(pre), []int{12}) {
		t.Fatalf("preemptable = %v, want [12]", candidateIDs(pre))
	}
	if !equalIDs(candidateIDs(slot), []int{13}) {
		t.Fatalf("slotless = %v, want [13]", candidateIDs(slot))
	}
	if len(primary)+len(pre)+len(slot) != 4 {
		t.Fatalf("partition dropped a candidate: %d+%d+%d", len(primary), len(pre), len(slot))
	}
}

func TestFpPartitionInternalRequestIgnoresSlotState(t *testing.T) {
	e, fpMgr, _ := newFpFailoverExecutor(t, 5)
	acquireFpSlot(t, fpMgr, 13, "busy-user")

	params := fpFailoverParams(nil)
	params.InternalRequest = true
	saturated := fpFailoverCandidate(13, "sk-saturated")

	primary, pre, slot := e.fpPartitionCandidates(params, []provider.Candidate{saturated}, "self-check")
	if len(primary) != 1 || primary[0].CredentialID != 13 || len(pre) != 0 || len(slot) != 0 {
		t.Fatalf("internal request must not be ordered by slot state: primary=%v preemptable=%v slotless=%v", candidateIDs(primary), candidateIDs(pre), candidateIDs(slot))
	}
}

func candidateIDs(cands []provider.Candidate) []int {
	out := make([]int, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.CredentialID)
	}
	return out
}

func acquireFpSlot(t *testing.T, m *credentialfpslot.Manager, credentialID int, holder string) {
	t.Helper()
	one := 1
	lease, ok := m.Acquire(t.Context(), credentialID, &one, holder, "default")
	if !ok || lease == nil {
		t.Fatalf("Acquire(%d, %q) failed", credentialID, holder)
	}
}

// ---------------------------------------------------------------------------
// 2. 梯子（dispatchRoute）
// ---------------------------------------------------------------------------

// TestDispatchRouteExposesTailOnlyAfterFirstAttempt pins the zero-regression
// property: the first route is exactly the pre-fix primary set; the demoted
// candidates appear only once a candidate has been tried, preemptable first.
func TestDispatchRouteExposesTailOnlyAfterFirstAttempt(t *testing.T) {
	e, fpMgr, mr := newFpFailoverExecutor(t, 5)
	ctx := t.Context()
	acquireFpSlot(t, fpMgr, 12, "idle-user")
	acquireFpSlot(t, fpMgr, 13, "busy-user")
	mr.FastForward(6 * time.Second)
	acquireFpSlot(t, fpMgr, 13, "busy-user")

	primaryCand := fpFailoverCandidate(10, "sk-primary")
	preemptable := fpFailoverCandidate(12, "sk-preemptable")
	slotless := fpFailoverCandidate(13, "sk-slotless")
	params := fpFailoverParams([]provider.Candidate{primaryCand, preemptable, slotless})

	primary, pre, slot := e.fpPartitionCandidates(params, params.Candidates, "caller")
	dctx := &dispatchCtx{
		params: params, candidates: primary, byModel: mapCandidatesByModel(primary),
		initialModel: "glm-5.3-flash", holder: "caller", tTotal: time.Now(),
		fpTail:        append(append([]provider.Candidate{}, pre...), slot...),
		fpTailByModel: mapCandidatesByModel(append(append([]provider.Candidate{}, pre...), slot...)),
		fpTailTier:    map[int]string{12: "preemptable", 13: "slotless"},
	}
	qr := dispatch.NewQueuedRequest("fp-route", "default", "glm-5.3-flash", ctx, dctx)

	refs, err := e.dispatchRoute(ctx, qr)
	if err != nil {
		t.Fatalf("first route: %v", err)
	}
	if !equalIDs(refCredentialIDs(refs), []int{10}) {
		t.Fatalf("first route = %v, want exactly the primary set [10] (byte-identical to the pre-fix behaviour)", refCredentialIDs(refs))
	}

	if qr.TriedCredentials == nil {
		qr.TriedCredentials = map[int]struct{}{}
	}
	qr.TriedCredentials[10] = struct{}{}
	refs, err = e.dispatchRoute(ctx, qr)
	if err != nil {
		t.Fatalf("re-route: %v", err)
	}
	if !equalIDs(refCredentialIDs(refs), []int{12, 13}) {
		t.Fatalf("re-route = %v, want the tail [12 13] with the preemptable candidate first", refCredentialIDs(refs))
	}
}

// TestDispatchRouteExposesTailWhenPrimarySetIsGone covers the one first-route
// case where the tail must show: the primary candidates did not survive the
// Router availability gate, so without the tail the request would end with
// zero attempts ("no route") while a healthy demoted candidate was available.
func TestDispatchRouteExposesTailWhenPrimarySetIsGone(t *testing.T) {
	e, fpMgr, _ := newFpFailoverExecutor(t, 5)
	acquireFpSlot(t, fpMgr, 13, "busy-user")
	ctx := t.Context()
	params := fpFailoverParams([]provider.Candidate{fpFailoverCandidate(13, "sk-slotless")})
	_, _, slotless := e.fpPartitionCandidates(params, params.Candidates, "caller")
	dctx := &dispatchCtx{
		params: params, byModel: map[string][]provider.Candidate{},
		initialModel: "glm-5.3-flash", holder: "caller", tTotal: time.Now(),
		fpTail:        slotless,
		fpTailByModel: mapCandidatesByModel(slotless),
		fpTailTier:    map[int]string{13: "slotless"},
	}
	qr := dispatch.NewQueuedRequest("fp-route-empty-primary", "default", "glm-5.3-flash", ctx, dctx)

	refs, err := e.dispatchRoute(ctx, qr)
	if err != nil {
		t.Fatalf("route with empty primary set: %v", err)
	}
	if !equalIDs(refCredentialIDs(refs), []int{13}) {
		t.Fatalf("route = %v, want the tail [13] instead of a zero-attempt no-route", refCredentialIDs(refs))
	}
}

// ---------------------------------------------------------------------------
// 3. 端到端：事故复现
// ---------------------------------------------------------------------------

// TestExecuteDemotedCandidateTakesOverAfterPrimary429 replays the 211 incident:
// the caller has a slot on the primary credential only, the healthy provider's
// slot pool is full, and the primary answers 429. Before the fix the healthy
// provider had been dropped at the prefilter and the request ended with
// "All providers unavailable" after a single upstream attempt.
func TestExecuteDemotedCandidateTakesOverAfterPrimary429(t *testing.T) {
	e, fpMgr, mr := newFpFailoverExecutor(t, 5)
	const tailCredential = 57
	acquireFpSlot(t, fpMgr, tailCredential, "busy-user")

	var primaryCalls, tailCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer sk-primary":
			primaryCalls.Add(1)
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"1302","message":"您的账户已达到速率限制，请您控制请求频率"}}`))
		default:
			tailCalls.Add(1)
			_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"glm-5.3-flash","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
		}
	}))
	defer upstream.Close()

	primaryCand := fpFailoverCandidate(62, "sk-primary")
	tailCand := fpFailoverCandidate(tailCredential, "sk-tail")
	primaryCand.BaseURL, tailCand.BaseURL = upstream.URL, upstream.URL
	params := fpFailoverParams([]provider.Candidate{primaryCand, tailCand})

	result, err := e.Execute(params)
	if err != nil {
		t.Fatalf("Execute must fail over to the demoted candidate, got: %v", err)
	}
	if result == nil || result.Candidate.CredentialID != tailCredential {
		t.Fatalf("request must be served by the demoted candidate %d, got %+v", tailCredential, result)
	}
	if primaryCalls.Load() != 1 || tailCalls.Load() != 1 {
		t.Fatalf("upstream calls = primary:%d tail:%d, want 1 each", primaryCalls.Load(), tailCalls.Load())
	}
	// Degraded-continue, not preemption: the pool is held by an ACTIVE holder,
	// so the request ran without a slot and that holder keeps it.
	holder, err := mr.Get(strings.ReplaceAll(fpFailoverSlotKey, "%d", "57"))
	if err != nil || holder != "busy-user" {
		t.Fatalf("slot holder = %q err=%v, want busy-user (an ACTIVE holder is never preempted)", holder, err)
	}
}

// TestExecutePreemptableTailRotatesCallerOntoTheCredential pins the balance
// half of the fix: a preemptable tail candidate takes over an idle holder's
// slot, so the caller ends up with a pin there and is first-choice on it from
// then on — traffic spreads instead of piling onto one provider.
func TestExecutePreemptableTailRotatesCallerOntoTheCredential(t *testing.T) {
	e, fpMgr, mr := newFpFailoverExecutor(t, 5)
	acquireFpSlot(t, fpMgr, 57, "idle-user")
	mr.FastForward(6 * time.Second)

	var primaryCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer sk-primary":
			primaryCalls.Add(1)
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"1302","message":"rate limited"}}`))
		default:
			_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"glm-5.3-flash","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
		}
	}))
	defer upstream.Close()

	primaryCand := fpFailoverCandidate(62, "sk-primary")
	tailCand := fpFailoverCandidate(57, "sk-tail")
	primaryCand.BaseURL, tailCand.BaseURL = upstream.URL, upstream.URL
	candidates := []provider.Candidate{primaryCand, tailCand}

	result, err := e.Execute(fpFailoverParams(candidates))
	if err != nil {
		t.Fatalf("Execute must fail over to the preemptable candidate, got: %v", err)
	}
	if result.Candidate.CredentialID != 57 || primaryCalls.Load() != 1 {
		t.Fatalf("served by %d after %d primary calls, want credential 57 after exactly 1 primary attempt", result.Candidate.CredentialID, primaryCalls.Load())
	}

	// The caller now holds the slot on 57 ⇒ primary on the next request.
	next := fpFailoverParams(candidates)
	primary, _, _ := e.fpPartitionCandidates(next, candidates, clientTokenOf(next.StickyKey, extractClientType(next.R)))
	found := false
	for _, c := range primary {
		if c.CredentialID == 57 {
			found = true
		}
	}
	if !found {
		t.Fatalf("credential 57 must be first-choice for the caller after the takeover, primary=%v", candidateIDs(primary))
	}
	holder, err := mr.Get(strings.ReplaceAll(fpFailoverSlotKey, "%d", "57"))
	if want := clientTokenOf(next.StickyKey, extractClientType(next.R)); err != nil || holder != want {
		t.Fatalf("slot holder = %q err=%v, want the caller %q to own the slot it took over", holder, err, want)
	}
}

// TestExecuteInternalRequestNeverHoldsFpSlot pins the capacity half of the
// fix: gateway-internal traffic (self-check key 398 on 211 held one of only
// two slots on the stable credential) must not take an identity slot.
func TestExecuteInternalRequestNeverHoldsFpSlot(t *testing.T) {
	e, _, mr := newFpFailoverExecutor(t, 5)
	served := atomic.Int64{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"glm-5.3-flash","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()

	cand := fpFailoverCandidate(57, "sk-internal")
	cand.BaseURL = upstream.URL
	params := fpFailoverParams([]provider.Candidate{cand})
	params.InternalRequest = true

	if _, err := e.Execute(params); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if keys := mr.Keys(); len(keys) != 0 {
		t.Fatalf("internal request must not create fingerprint-slot keys, got %v", keys)
	}
	if served.Load() == 0 {
		t.Fatal("upstream was never reached")
	}
}
