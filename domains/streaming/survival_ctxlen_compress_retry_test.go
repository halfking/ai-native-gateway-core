package streaming

// R36 (2026-09-17 audit) — pins the uncommitted context-length
// compress-and-retry ladder (closes R35-gap 遗留#2: the survival budget
// never worked for KindContextLength because the central policy hard-pins
// it FailTerminal regardless of commit state).
//
// Architecture note: the override lives in SurvivalCoordinator.Run via
// survivalCtxLenCompressRetryDue, NOT in the shared decision layer
// (centralActionForTaskWithHistory) — the durable recovery worker aggregates
// the same kinds but re-runs attempts from the original snapshot without a
// body-rewrite hook, so a decision-layer retry would loop on the identical
// oversized body there (pinned by TestDurableRecoveryWorkerTerminalDecisionFails).

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSurvivalCtxLenCompressRetryDue(t *testing.T) {
	uncommitted := &AttemptResult{
		CommitState:       CommitStateNone,
		CandidateOutcomes: []CandidateOutcome{{Kind: errorsx.KindContextLength}},
	}
	terminal := TaskDecision{Action: TaskActionFailTerminal, Reason: string(errorsx.KindContextLength)}

	// The ladder fires exactly once: uncommitted + terminal + context-length.
	assert.True(t, survivalCtxLenCompressRetryDue(false, terminal, uncommitted))
	assert.False(t, survivalCtxLenCompressRetryDue(true, terminal, uncommitted), "one-shot: no second compress retry")

	// Committed output keeps the terminal (central commit-block rule).
	committed := &AttemptResult{
		CommitState:       CommitStateContent,
		CandidateOutcomes: []CandidateOutcome{{Kind: errorsx.KindContextLength}},
	}
	assert.False(t, survivalCtxLenCompressRetryDue(false, terminal, committed))

	// Other terminal kinds keep their semantics (content filter is not
	// recoverable by shrinking the body).
	otherKind := &AttemptResult{
		CommitState:       CommitStateNone,
		CandidateOutcomes: []CandidateOutcome{{Kind: errorsx.KindContentFilter}},
	}
	assert.False(t, survivalCtxLenCompressRetryDue(false, terminal, otherKind))

	// Non-terminal decisions pass through untouched.
	assert.False(t, survivalCtxLenCompressRetryDue(false,
		TaskDecision{Action: TaskActionRetryNow}, uncommitted))
}

func TestSurvivalAttemptHasKind(t *testing.T) {
	r := &AttemptResult{CandidateOutcomes: []CandidateOutcome{
		{Kind: errorsx.KindTimeout},
		{Kind: errorsx.KindContextLength},
	}}
	assert.True(t, survivalAttemptHasKind(r, errorsx.KindContextLength))
	assert.True(t, survivalAttemptHasKind(r, errorsx.KindTimeout))
	assert.False(t, survivalAttemptHasKind(r, errorsx.KindAuth))
	assert.False(t, survivalAttemptHasKind(nil, errorsx.KindContextLength))
}

func TestSurvivalCompressBodyForRetry(t *testing.T) {
	// Messages-shaped body with heavy old turns: must shrink.
	var msgs []map[string]any
	msgs = append(msgs, map[string]any{"role": "system", "content": "be helpful"})
	for i := 0; i < 400; i++ {
		msgs = append(msgs,
			map[string]any{"role": "user", "content": fmt.Sprintf("turn %d: %s", i, strings.Repeat("lorem ipsum dolor sit amet ", 20))},
			map[string]any{"role": "assistant", "content": fmt.Sprintf("reply %d: %s", i, strings.Repeat("consectetur adipiscing elit ", 20))},
		)
	}
	body, err := json.Marshal(map[string]any{"model": "x", "messages": msgs})
	require.NoError(t, err)

	trimmed, ok := survivalCompressBodyForRetry(body)
	require.True(t, ok, "messages-shaped oversized body must compress")
	assert.Less(t, len(trimmed), len(body))
	// The wire shape survives the trim.
	var out struct {
		Messages []json.RawMessage `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(trimmed, &out))
	assert.NotEmpty(t, out.Messages)

	// Responses-protocol shape (no "messages") returns unchanged → not ok.
	responsesBody := []byte(`{"model":"x","input":"hello","stream":true}`)
	_, ok = survivalCompressBodyForRetry(responsesBody)
	assert.False(t, ok, "non-messages bodies must not be retried with the same bytes")

	// Empty/nil bodies never retry.
	_, ok = survivalCompressBodyForRetry(nil)
	assert.False(t, ok)
}
