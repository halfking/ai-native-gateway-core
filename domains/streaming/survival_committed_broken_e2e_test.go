package streaming

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// survival_committed_broken_e2e_test.go — R58 §11.6 real-machine fault
// injection (245, 2026-09-23) as a pinned regression.
//
// Live shape observed on 245 before the fix: a single-candidate model whose
// upstream streams chunks past the holdback window (committed) and then
// breaks without [DONE] gets the survival terminal (gateway_survival_resume_
// blocked envelope + one data: [DONE]) — and THEN, after [DONE], a second
// error frame ("No available provider ... All 1 candidates failed") written
// by the shared post-execute error path. Two terminals on one wire violates
// the §11.6 one-terminal contract and can corrupt strict SSE parsers.
//
// The fix: runSurvivalCoordinator wraps the returned error in
// errSurvivalTerminalRendered once the Terminal seam has settled, and the
// chat error path routes subsequent writes into a blackhole writer.
//
// This test pins the wire contract: after the survival terminal, silence.

// commitThenBreakExecutor streams slow content chunks on the first attempt
// (crossing the holdback window so the gate commits them) and then fails;
// every later attempt fails immediately. Exhausted+Tried mirror the
// production ExecuteError shape that drives the model_not_found prewarm
// branch — the exact branch that stacked the second terminal on 245.
type commitThenBreakExecutor struct {
	calls int64
}

const commitThenBreakChunks = 10

func (e *commitThenBreakExecutor) Execute(params *executors.ExecParams) (*executors.ExecuteResult, error) {
	n := atomic.AddInt64(&e.calls, 1)
	if n == 1 && params != nil && params.W != nil {
		// Slow-drip past the 5s holdback window so the tail chunks commit.
		for i := 1; i <= commitThenBreakChunks; i++ {
			frame := fmt.Sprintf("data: {\"id\":\"chatcmpl-fi\",\"object\":\"chat.completion.chunk\",\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"chunk%d \"},\"finish_reason\":null}]}\n\n", i)
			if _, err := params.W.Write([]byte(frame)); err != nil {
				break
			}
			time.Sleep(650 * time.Millisecond)
		}
	}
	return nil, &executors.ExecuteError{
		LastKind:  errorsx.KindUpstreamDown,
		LastErr:   fmt.Errorf("synthetic upstream broken after commit (attempt %d)", n),
		Exhausted: true,
		Tried:     1,
	}
}

func newCommittedBrokenHandler(t *testing.T) (*ChatHandler, *commitThenBreakExecutor) {
	t.Helper()
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.executor = &executors.Executor{}
	h.setRequestKeyVerifierForTest(noNodesKeyVerifier{})
	h.provider = noNodesResolver{}
	exec := &commitThenBreakExecutor{}
	h.survivalAttemptExec = exec
	h.SetRequestSurvival(func(string) bool { return true }, SurvivalOptions{
		Deadline:          5 * time.Minute,
		RetryBase:         100 * time.Millisecond,
		RetryInterval:     100 * time.Millisecond,
		MaxRetries:        2,
		NightMaxRetries:   2,
		KeepaliveInterval: 50 * time.Millisecond,
	})
	return h, exec
}

func TestSurvivalCommittedBroken_NoSecondTerminalAfterDone(t *testing.T) {
	h, exec := newCommittedBrokenHandler(t)

	srv := httptest.NewServer(h)
	defer srv.Close()

	body := strings.NewReader(`{"model":"fi-1168-neutral","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/v1/chat/completions", body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (bytes were committed, status cannot change)", resp.StatusCode)
	}

	// Slow-drip attempt (~6.5s) + terminal; give the drain generous room.
	wire := streamRead(t, resp.Body, 1<<20, 20*time.Second)

	if got := atomic.LoadInt64(&exec.calls); got < 1 {
		t.Fatalf("executor calls = %d, want at least 1", got)
	}
	if !strings.Contains(wire, "chunk1 ") {
		t.Fatalf("committed chunks missing from wire (first 500 bytes: %.500q)", wire)
	}
	if !strings.Contains(wire, "gateway_survival_resume_blocked") {
		t.Fatalf("survival terminal envelope missing from wire (tail 500 bytes: %q)", tailBytes(wire, 500))
	}
	if n := strings.Count(wire, "data: [DONE]"); n != 1 {
		t.Fatalf("data: [DONE] count = %d, want exactly 1\nwire tail: %q", n, tailBytes(wire, 500))
	}

	// The core §11.6 assertion: after the terminal [DONE], the wire is
	// silent — no second error frame may follow.
	doneIdx := strings.LastIndex(wire, "data: [DONE]")
	after := wire[doneIdx+len("data: [DONE]"):]
	if strings.Contains(after, "data: {") {
		t.Fatalf("second terminal stacked after [DONE]: %q", after)
	}
	if strings.Contains(wire, "model_not_found") {
		t.Fatalf("late exhaustion frame reached the wire (the exact 245 leak this test pins)")
	}
}

func tailBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
