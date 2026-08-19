package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// 2026-08-19 observability pass: every survival-coordinator decision is
// now greppable by request_id via slog. Pin the retry-now + terminal
// scenarios so the contract holds.
//
// TestSurvivalCoordinator_LogsCoverAttemptDiscardAndTerminal runs a
// transient failure → retry → succeed sequence and asserts:
//   - one "survival_attempt_outcome" line per attempt
//   - one "survival_attempt_discarded" line for the discarded attempt
//   - one "survival_task_ended" line at terminal
func TestSurvivalCoordinator_LogsCoverAttemptDiscardAndTerminal(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	exec := &scriptedExecutor{
		// First attempt: transient (will be discarded, retried).
		errs: []error{
			transientFailure(),
			nil, // second attempt succeeds
		},
		results: []*executors.ExecuteResult{
			nil,
			{
				LatencyMs:    42,
				RequestBody:  []byte(`{"model":"glm-5.2"}`),
				InboundBody:  []byte(`{"model":"glm-5.2"}`),
				Candidate:    provider.Candidate{ProviderID: 12763, CredentialID: 36, RawModel: "glm-5.2"},
				RoutingTracker: executors.NewRoutingAttemptsTracker(),
			},
		},
	}
	h := newCoordHarness(exec)
	params := &executors.ExecParams{
		RequestID: "req-obs-disposable",
		Model:     "glm-5.2",
		BodyBytes: []byte(`{"model":"glm-5.2"}`),
		Capture:   audit.NewStreamCapture(),
	}
	res := h.coordinator().Run(harnessCtx(), h.sw, params)
	if !res.Succeed {
		t.Fatalf("coordinator did not succeed: decision=%+v err=%v", res.Decision, res.FinalAttempt.FinalError)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), `"survival_task_ended"`) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	gotOutcome := false
	gotDiscarded := false
	gotTaskEnded := false
	sawProviderRawModel := false
	dec := json.NewDecoder(strings.NewReader(buf.String()))
	for dec.More() {
		var rec map[string]any
		if err := dec.Decode(&rec); err != nil {
			continue
		}
		switch rec["msg"] {
		case "survival_attempt_outcome":
			gotOutcome = true
			if rec["provider_id"] == nil {
				t.Errorf("survival_attempt_outcome missing provider_id: %v", rec)
			}
			if raw, ok := rec["raw_model"].(string); ok && raw == "glm-5.2" {
				sawProviderRawModel = true
			}
		case "survival_attempt_discarded":
			gotDiscarded = true
			if _, ok := rec["buffer_bytes"]; !ok {
				t.Errorf("survival_attempt_discarded missing buffer_bytes: %v", rec)
			}
		case "survival_task_ended":
			gotTaskEnded = true
			if rec["succeed"] != true {
				t.Errorf("survival_task_ended succeed=%v want true", rec["succeed"])
			}
		}
	}
	if !gotOutcome {
		t.Errorf("missing survival_attempt_outcome in log: %s", buf.String())
	}
	if !sawProviderRawModel {
		t.Errorf("survival_attempt_outcome never carried raw_model=glm-5.2: %s", buf.String())
	}
	if !gotDiscarded {
		t.Errorf("missing survival_attempt_discarded in log: %s", buf.String())
	}
	if !gotTaskEnded {
		t.Errorf("missing survival_task_ended in log: %s", buf.String())
	}
}

// TestSurvivalCoordinator_LogsCaptureResumeBlocked verifies the
// gateway_survival_resume_blocked / gateway request survival ended:
// committed_output failure class emits a survival_resume_blocked slog
// line with the provider/raw_model correlation.
func TestSurvivalCoordinator_LogsCaptureResumeBlocked(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	h := newCoordHarness(nil)
	// Inject an ExecuteAttempt impl that returns a transient error AND
	// promotes the per-attempt gate to CommitStateContent so the
	// aggregator sees committed && hasRetry → flips to
	// TaskActionResumeBlocked + Reason="committed_output".
	executor := committedAttemptExecutor{
		providerID:   14,
		rawModel:     "minimax-m3",
		credentialID: 21,
	}
	co := &SurvivalCoordinator{
		Exec: executor,
		Protocol: ProtocolOpenAIChat,
		Options: SurvivalOptions{
			Deadline:   30 * time.Minute,
			RetryBase:  1 * time.Millisecond,
			RetryMax:   1 * time.Millisecond,
			MaxRetries: 5,
		},
		Now:   func() time.Time { return time.Unix(0, 0) },
		Sleep: func(ctx context.Context, d time.Duration) error { return nil },
		Terminal: func(d TaskDecision, committed bool) {
			h.terminals = append(h.terminals, d)
			h.committeds = append(h.committeds, committed)
		},
	}
	params := &executors.ExecParams{
		RequestID: "req-resume-blocked",
		Model:     "minimax-m3",
		BodyBytes: []byte(`{"model":"minimax-m3"}`),
		Capture:   audit.NewStreamCapture(),
	}
	res := co.Run(harnessCtx(), h.sw, params)
	if res.Decision.Action != TaskActionResumeBlocked {
		t.Fatalf("action=%s want resume_blocked", res.Decision.Action.String())
	}
	if res.Decision.Reason != "committed_output" {
		t.Fatalf("reason=%q want committed_output", res.Decision.Reason)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), `"survival_resume_blocked"`) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(buf.String(), `"survival_resume_blocked"`) {
		t.Fatalf("missing survival_resume_blocked in log: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"provider_id":14`) {
		t.Errorf("survival_resume_blocked missing provider_id=14: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"raw_model":"minimax-m3"`) {
		t.Errorf("survival_resume_blocked missing raw_model=minimax-m3: %s", buf.String())
	}
}

// committedAttemptExecutor is a tiny AttemptExecutor that writes one
// content chunk (driving the per-attempt gate to CommitStateContent) and
// then returns a transient *executors.ExecuteError carrying the provider
// attribution the survival coordinator logs.
//
// The combination — committed=true, hasRetry=true — is exactly the input
// that AggregateTaskOutcome flips to TaskActionResumeBlocked +
// Reason="committed_output" (attempt_outcome.go:235-241), which is the
// failure class that produces the SSE envelope "gateway request survival
// ended: committed_output" + "gateway_survival_resume_blocked".
type committedAttemptExecutor struct {
	providerID   int
	rawModel     string
	credentialID int
}

func (c committedAttemptExecutor) Execute(params *executors.ExecParams) (*executors.ExecuteResult, error) {
	// params.W is the *GateWriter wrapped around the AttemptCommitGate by
	// ExecuteAttempt. Writing a content chunk advances the gate to
	// CommitStateContent; the gate then refuses any subsequent Discard.
	if params != nil && params.W != nil {
		// OpenAI Chat protocol: data: {role:assistant, content:"hi"}\n\n
		// The leading "data: " + closing blank line is what makes the gate
		// classify it as semantic content (commit).
		_, _ = params.W.Write([]byte(`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"))
	}
	return nil, &executors.ExecuteError{
		LastKind: errorsx.KindTransient,
		Attempts: []executors.AttemptRecord{{
			ProviderID:   c.providerID,
			CredentialID: c.credentialID,
			Kind:         errorsx.KindTransient,
			RawModel:     c.rawModel,
		}},
	}
}

// silence unused-import warning for errors when no test references it directly.
var _ = errors.New

func harnessCtx() context.Context { return context.Background() }