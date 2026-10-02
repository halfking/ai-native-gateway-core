package goal

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

// Wave 3 B12 multi-round audit pipeline tests: audit → auto-fix → audit …
// up to 3 rounds, then an INDEPENDENT VERIFY stage on the final round.

const auditFailJSON = `{"passed":false,"confidence":0.9,"issues":[{"severity":"high","category":"security","description":"x","location":"a.go:1"}],"suggestions":[{"title":"fix","description":"d","auto_fixable":true}],"summary":"bad"}`
const auditPassJSON = `{"passed":true,"confidence":0.9,"summary":"ok"}`

// roundStore composes the legacy fake with the B12 round-aware write.
type roundStore struct {
	*fakeStore
}

func (s *roundStore) UpdateSessionAuditRound(_ context.Context, tenantID, sessionID string, auditResult []byte, round int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if !ok || sess.TenantID != tenantID {
		return false, nil
	}
	cur := 0
	if len(sess.AuditResult) > 0 {
		var r AuditResult
		_ = json.Unmarshal(sess.AuditResult, &r)
		cur = r.Round
		if cur <= 0 {
			cur = 1
		}
	}
	if cur >= round {
		return false, nil
	}
	sess.AuditResult = auditResult
	return true, nil
}

func seedCompletedAuditSession(t *testing.T, store *fakeStore) {
	t.Helper()
	store.seed(&Session{
		SessionID:    "s1",
		TenantID:     "t1",
		State:        StateCompleted,
		OriginalGoal: "build it",
	})
}

func auditReq(action string) *response.InterceptRequest {
	return &response.InterceptRequest{
		TenantID:      "t1",
		SessionID:     "s1",
		ResponseBody:  []byte(`{}`),
		FollowUpAction: action,
	}
}

func newMultiRoundHook(store GoalStore, responses ...string) *AuditHook {
	return NewAuditHook(store, &stubLLMCaller{responses: responses}, AuditConfig{
		Enabled:        true,
		AutoFixEnabled: true,
		FallbackModel:  "audit-model",
	})
}

func TestAuditMultiRound_ThreeRoundsThenVerify(t *testing.T) {
	store := &roundStore{fakeStore: newFakeStore()}
	seedCompletedAuditSession(t, store.fakeStore)
	// audit r1 fail, audit r2 fail, audit r3 fail, verify verdict.
	hook := newMultiRoundHook(store, auditFailJSON, auditFailJSON, auditFailJSON, `{"verified":true,"confidence":0.8,"summary":"resolved"}`)

	res1, err := hook.InterceptNonStream(context.Background(), auditReq(""))
	if err != nil {
		t.Fatalf("round1: %v", err)
	}
	if res1 == nil || res1.Action != auditAutoFixAction || len(res1.InjectFollowUp) == 0 {
		t.Fatalf("round1 must inject a fix, got %+v", res1)
	}
	round, _ := auditRoundOf(store.sessions["s1"].AuditResult)
	if round != 1 {
		t.Fatalf("persisted round = %d, want 1", round)
	}

	res2, err := hook.InterceptNonStream(context.Background(), auditReq(auditAutoFixAction))
	if err != nil {
		t.Fatalf("round2: %v", err)
	}
	if res2 == nil || len(res2.InjectFollowUp) == 0 {
		t.Fatalf("round2 must inject another fix, got %+v", res2)
	}
	round, _ = auditRoundOf(store.sessions["s1"].AuditResult)
	if round != 2 {
		t.Fatalf("persisted round = %d, want 2", round)
	}

	res3, err := hook.InterceptNonStream(context.Background(), auditReq(auditAutoFixAction))
	if err != nil {
		t.Fatalf("round3: %v", err)
	}
	if res3 != nil && len(res3.InjectFollowUp) > 0 {
		t.Fatal("final round must not inject another fix")
	}
	var final AuditResult
	if err := json.Unmarshal(store.sessions["s1"].AuditResult, &final); err != nil {
		t.Fatalf("unmarshal final: %v", err)
	}
	if final.Round != 3 {
		t.Fatalf("final round = %d, want 3", final.Round)
	}
	if final.Verify == nil || !final.Verify.Verified {
		t.Fatalf("final result must carry an independent verify verdict, got %+v", final.Verify)
	}

	// Pipeline is final: another fix response must not re-audit.
	if _, err := hook.InterceptNonStream(context.Background(), auditReq(auditAutoFixAction)); err != nil {
		t.Fatalf("post-final call: %v", err)
	}
	if calls := len(hook.llmCaller.(*stubLLMCaller).calls); calls != 4 {
		t.Fatalf("llm calls = %d, want 4 (3 audits + 1 verify)", calls)
	}
}

func TestAuditMultiRound_PassOnRound2Ends(t *testing.T) {
	store := &roundStore{fakeStore: newFakeStore()}
	seedCompletedAuditSession(t, store.fakeStore)
	hook := newMultiRoundHook(store, auditFailJSON, auditPassJSON)

	if _, err := hook.InterceptNonStream(context.Background(), auditReq("")); err != nil {
		t.Fatalf("round1: %v", err)
	}
	res2, err := hook.InterceptNonStream(context.Background(), auditReq(auditAutoFixAction))
	if err != nil {
		t.Fatalf("round2: %v", err)
	}
	if res2 != nil && len(res2.InjectFollowUp) > 0 {
		t.Fatal("a passed round must not inject a fix")
	}
	round, passed := auditRoundOf(store.sessions["s1"].AuditResult)
	if round != 2 || !passed {
		t.Fatalf("final round/passed = %d/%v, want 2/true", round, passed)
	}
	if calls := len(hook.llmCaller.(*stubLLMCaller).calls); calls != 2 {
		t.Fatalf("llm calls = %d, want 2 (no verify after a pass)", calls)
	}
}

func TestAuditMultiRound_LegacyStoreKeepsSingleRound(t *testing.T) {
	store := newFakeStore()
	seedCompletedAuditSession(t, store)
	hook := newMultiRoundHook(store, auditFailJSON)

	if _, err := hook.InterceptNonStream(context.Background(), auditReq("")); err != nil {
		t.Fatalf("first audit: %v", err)
	}
	// Legacy path: audit_result is set, the fix response is skipped.
	if _, err := hook.InterceptNonStream(context.Background(), auditReq(auditAutoFixAction)); err != nil {
		t.Fatalf("fix response: %v", err)
	}
	round, _ := auditRoundOf(store.sessions["s1"].AuditResult)
	if round != 1 {
		t.Fatalf("legacy store must stay single-round, got round %d", round)
	}
}
