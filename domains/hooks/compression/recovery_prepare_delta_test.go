package compression

// Regression pins for the 4xx recovery → next-Prepare contract (D03-①).
//
// Recover used to persist a SessionState whose outbound identity fields
// (LastOutboundHash / MsgCount / CompressedSnapshot / CompressedTokens) still
// described the pre-4xx body, and a rebuilt body whose summary message carried
// no [smm_v1:] marker line. Either one alone makes the next Prepare fail open:
// the hash/count guard rejects the state-vs-body mismatch, and the diff anchor
// cannot recognise an unmarked summary as compressed lineage. The client then
// gets its full history resent and the session recompresses what it had just
// compressed.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestRecoveryCoordinator_PersistedStateMatchesRebuiltBody pins the persisted
// state consistency for both strategies: after Recover, the cached state must
// describe the rebuilt body exactly, the way buildSessionState does for
// updateCache and CommitFinal.
func TestRecoveryCoordinator_PersistedStateMatchesRebuiltBody(t *testing.T) {
	for _, tc := range []struct {
		name       string
		summarizer SummaryFunc
	}{
		{name: "llm", summarizer: func(context.Context, []byte, string) (string, bool) {
			return "deterministic state-pin summary", true
		}},
		{name: "mechanical", summarizer: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tenant, sess := "tenant-state-pin", "gw-state-pin-"+tc.name
			cache := NewSessionCache(nil, nil)
			// Seed an established session so the state carries a non-empty
			// stale outbound identity — the production shape in which the
			// diff guard actually fires.
			sc := NewSessionCompressor(SessionCompressorDeps{Cache: cache})
			seed := []byte(`{"model":"m","messages":[{"role":"user","content":"seed turn"}]}`)
			sc.Prepare(ctx, seed, tenant, sess, "openai", 128000, false)

			rc := NewRecoveryCoordinator(RecoveryDeps{Cache: cache, Summarizer: tc.summarizer, MaxRetries: 2})
			res := rc.Recover(ctx, makeRecoveryTestBody(), "openai", 5000, tenant, sess, 0)
			if !res.ShouldRetry || res.CutMarker == nil {
				t.Fatalf("recovery failed: %+v", res)
			}

			state, cachedBody, err := cache.GetOrLoad(ctx, tenant, sess)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(cachedBody, res.NewBody) {
				t.Fatal("L1 body is not the rebuilt body")
			}
			if state.LastOutboundHash != sha256Hex(res.NewBody) {
				t.Fatalf("LastOutboundHash is stale: %s, want hash of rebuilt body", state.LastOutboundHash)
			}
			if state.MsgCount != countMessages(res.NewBody) {
				t.Fatalf("MsgCount = %d, want %d (rebuilt)", state.MsgCount, countMessages(res.NewBody))
			}
			if state.TokenEstimate != estimateBodyTokens(res.NewBody) {
				t.Fatalf("TokenEstimate = %d, want %d (rebuilt)", state.TokenEstimate, estimateBodyTokens(res.NewBody))
			}
			if state.CompressedMsgs != state.MsgCount || state.CompressedTokens != state.TokenEstimate {
				t.Fatalf("compressed family out of step: msgs=%d tokens=%d", state.CompressedMsgs, state.CompressedTokens)
			}
			if state.CompressedSnapshot != SnapshotForBody(res.NewBody) {
				t.Fatalf("CompressedSnapshot = %+v, want snapshot of rebuilt body", state.CompressedSnapshot)
			}
			if state.SummaryMarker != res.CutMarker.SummaryMarker {
				t.Fatalf("state marker %q != cut marker %q", state.SummaryMarker, res.CutMarker.SummaryMarker)
			}
			if state.SummaryMarker != "" && !bytesContainSummaryMarker(res.NewBody, state.SummaryMarker) {
				t.Fatalf("persisted marker %q not present in rebuilt body", state.SummaryMarker)
			}
		})
	}
}

// makeDeltaPinBody builds a no-system OpenAI fixture with unique per-turn
// contents. That is the canonical shape the diff anchor supports after a
// summary-marked compression (same shape as
// TestBuildOutbound_SummaryMarkerPreserved): the marked summary message is
// excluded from the anchor, and the retained suffix occurs contiguously and
// uniquely in the client's resent history. A leading system message would
// break the contiguity requirement for the proactive path as well — a
// pre-existing diff-engine limitation outside this pin's scope.
func makeDeltaPinBody() []byte {
	msgs := make([]map[string]any, 0, 24)
	for i := 0; i < 12; i++ {
		content := strings.Repeat("filler text for the compression window ", 40) + fmt.Sprintf("unique turn %02d", i)
		msgs = append(msgs,
			map[string]any{"role": "user", "content": content},
			map[string]any{"role": "assistant", "content": "reply to " + content},
		)
	}
	body, err := json.Marshal(map[string]any{"model": "m", "messages": msgs})
	if err != nil {
		panic(err)
	}
	return body
}

// TestRecoveryCoordinator_NextPrepareDeltaAppendsOntoRecoveredBody is the
// end-to-end half: the turn after a 4xx recovery must delta-append onto the
// recovered prefix instead of failing open to a new session.
func TestRecoveryCoordinator_NextPrepareDeltaAppendsOntoRecoveredBody(t *testing.T) {
	ctx := context.Background()
	const tenant, sess = "tenant-delta-pin", "gw-delta-pin01"
	cache := NewSessionCache(nil, nil)
	sc := NewSessionCompressor(SessionCompressorDeps{Cache: cache})
	// Seed an established session so the persisted state carries a non-empty
	// stale outbound identity — the production shape for a session that grew
	// long enough to hit context_length_exceeded, and the shape in which the
	// diff guard's hash leg actually fires.
	seed := []byte(`{"model":"m","messages":[{"role":"user","content":"seed turn"}]}`)
	sc.Prepare(ctx, seed, tenant, sess, "openai", 128000, false)
	rc := NewRecoveryCoordinator(RecoveryDeps{
		Cache: cache,
		Summarizer: func(context.Context, []byte, string) (string, bool) {
			return "deterministic recovery summary", true
		},
		MaxRetries: 2,
	})

	res := rc.Recover(ctx, makeDeltaPinBody(), "openai", 5000, tenant, sess, 0)
	if !res.ShouldRetry || res.NewBody == nil {
		t.Fatalf("recovery failed: %+v", res)
	}
	rebuiltMsgCount := countMessages(res.NewBody)

	// The client resends the full history plus one new turn (standard agent
	// loop behaviour that caused the 4xx in the first place).
	origMsgs, err := extractMessages(makeDeltaPinBody())
	if err != nil {
		t.Fatal(err)
	}
	grown := make([]map[string]any, 0, len(origMsgs)+1)
	for _, raw := range origMsgs {
		var m map[string]any
		if json.Unmarshal(raw, &m) != nil {
			t.Fatal("fixture message not an object")
		}
		grown = append(grown, m)
	}
	grown = append(grown, map[string]any{"role": "user", "content": "follow-up turn after recovery"})
	body2, err := json.Marshal(map[string]any{"model": "m", "messages": grown})
	if err != nil {
		t.Fatal(err)
	}

	next := sc.Prepare(ctx, body2, tenant, sess, "openai", 128000, false)
	if next.OutboundBody == nil || next.CompressionStrategy != "delta_append" {
		t.Fatalf("recovered session failed open to a full resend: strategy=%q outbound_nil=%v",
			next.CompressionStrategy, next.OutboundBody == nil)
	}
	if next.MsgCount != rebuiltMsgCount+1 {
		t.Fatalf("delta count mismatch: MsgCount=%d, want rebuilt %d + 1 new turn", next.MsgCount, rebuiltMsgCount)
	}
	out := string(next.OutboundBody)
	if !strings.Contains(out, "follow-up turn after recovery") {
		t.Error("new client turn missing from delta outbound")
	}
	if !strings.Contains(out, "deterministic recovery summary") {
		t.Error("recovered summary prefix lost from delta outbound")
	}
	if strings.Contains(out, "unique turn 00") {
		t.Error("dropped pre-cut history leaked back into the delta outbound")
	}
}
