// Package compression - diff_two_segment_test.go (R38 diff 引擎专项)
//
// findTwoSegmentAnchor 的行为级钉测：B-track 布局（首条 user 头段 + 连续
// 尾段，中间被压缩）命中与 fail-open 边界。与 anthropic_failopen_repro_test.go
// 的区别：repro 文件钉「产线重建体端到端命中」（翻转靶），本文件钉引擎
// 安全判据（尾段唯一性 / 头段血统证据 / 分解歧义 / 多轮持续性）——这些是
// 两段式放宽 fail-open 后必须仍然保守的面。
//
// 变异靶标注（承重实证用，R38 变异矩阵 M1/M2/M3 对应）：
//   - M1 删 findTwoSegmentAnchor 调用（relaxed 只留连续）→ 全部 TwoSegment
//     命中类钉测红；
//   - M2 删 containsSeqBefore（头段血统证据）→ HeadSegment 钉测红；
//   - M3 尾段取首个命中而非唯一（uniqueContiguousIndex 改 first-match）→
//     AmbiguousTail 钉测红。
package compression

import (
	"encoding/json"
	"strings"
	"testing"
)

// twoSegBody builds an OpenAI-shaped body from raw message contents, marking
// the summary slot with the smm_v1 prefix (the marked production shape).
func twoSegBody(t *testing.T, msgs []map[string]string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"model": "m", "messages": msgs})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestFindDeltaAnchor_TwoSegment_BTrackLayoutAnchors pins the core gain: a
// compressed outbound whose retained block is [first-user head + recent tail]
// anchors on the unique tail occurrence, and the merged outbound carries the
// full retained block plus the client delta.
func TestFindDeltaAnchor_TwoSegment_BTrackLayoutAnchors(t *testing.T) {
	// lastOutbound: [marker summary, u1, u3, a3] — B-track production layout.
	last := twoSegBody(t, []map[string]string{
		summaryMsg("prior turns"),
		userMsg("u1 hello"),
		userMsg("u3 and now"),
		assistantMsg("a3 ok"),
	})
	client := twoSegBody(t, []map[string]string{
		userMsg("u1 hello"), assistantMsg("a1 hi"),
		userMsg("u2 mid"), assistantMsg("a2 mid"),
		userMsg("u3 and now"), assistantMsg("a3 ok"),
		userMsg("u4 new"),
	})
	res, err := BuildOutboundMessages(client, &SessionState{SchemaVersion: 1}, last, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if res.IsNewSess || res.DeltaCount != 1 {
		t.Fatalf("two-segment anchor: expected hit with DeltaCount=1, got IsNewSess=%v DeltaCount=%d", res.IsNewSess, res.DeltaCount)
	}
	msgs, _ := extractMessages(res.Body)
	// Hardcoded expectation: marker + u1 + u3 + a3 + u4 = 5 messages, in order.
	if len(msgs) != 5 {
		t.Fatalf("two-segment merge: expected 5 msgs (marker,u1,u3,a3,u4), got %d", len(msgs))
	}
	if !isSummaryMarkerMsg(msgs[0]) ||
		!strings.Contains(string(msgs[1]), "u1 hello") ||
		!strings.Contains(string(msgs[2]), "u3 and now") ||
		!strings.Contains(string(msgs[3]), "a3 ok") ||
		!strings.Contains(string(msgs[4]), "u4 new") {
		t.Fatalf("two-segment merge: retained block or delta lost/reordered: %s", msgs)
	}
}

// TestFindDeltaAnchor_TwoSegment_MultiRoundKeepsAnchoring pins the compounding
// gain: after a delta merge the merged body (marker + head + tail + delta)
// must keep anchoring on the next round. This is the fail-open death spiral
// the project exists to break — one compression used to cost delta on every
// subsequent turn forever.
func TestFindDeltaAnchor_TwoSegment_MultiRoundKeepsAnchoring(t *testing.T) {
	last := twoSegBody(t, []map[string]string{
		summaryMsg("prior turns"),
		userMsg("u1 hello"),
		userMsg("u3 and now"),
		assistantMsg("a3 ok"),
	})
	rounds := [][]map[string]string{
		{userMsg("u1 hello"), assistantMsg("a1 hi"), userMsg("u2 mid"), assistantMsg("a2 mid"),
			userMsg("u3 and now"), assistantMsg("a3 ok"), userMsg("r1")},
		{userMsg("u1 hello"), assistantMsg("a1 hi"), userMsg("u2 mid"), assistantMsg("a2 mid"),
			userMsg("u3 and now"), assistantMsg("a3 ok"), userMsg("r1"), assistantMsg("r1a")},
		{userMsg("u1 hello"), assistantMsg("a1 hi"), userMsg("u2 mid"), assistantMsg("a2 mid"),
			userMsg("u3 and now"), assistantMsg("a3 ok"), userMsg("r1"), assistantMsg("r1a"), userMsg("r2")},
	}
	for i, clientMsgs := range rounds {
		res, err := BuildOutboundMessages(twoSegBody(t, clientMsgs), &SessionState{SchemaVersion: 1}, last, "openai")
		if err != nil {
			t.Fatalf("round %d: %v", i+1, err)
		}
		// Each round the previous delta is already part of the retained tail
		// segment, so exactly the newest message is new every time.
		if res.IsNewSess || res.DeltaCount != 1 {
			t.Fatalf("round %d: expected DeltaCount=1, got IsNewSess=%v DeltaCount=%d", i+1, res.IsNewSess, res.DeltaCount)
		}
		last = res.Body
	}
	// Final merged body: marker + u1 + u3 + a3 + r1 + r1a + r2 = 7.
	msgs, _ := extractMessages(last)
	if len(msgs) != 7 {
		t.Fatalf("multi-round merge: expected 7 msgs, got %d", len(msgs))
	}
}

// TestFindDeltaAnchor_TwoSegment_AmbiguousTailFailsOpen: when the tail segment
// occurs twice in the client history the anchor is ambiguous and the engine
// must fail open (uniqueness discipline of the contiguous branch carries over).
// The client breaks contiguity of the whole retained block (u1,x,a,b) so the
// contiguous branch cannot absorb the case before the two-segment walk.
func TestFindDeltaAnchor_TwoSegment_AmbiguousTailFailsOpen(t *testing.T) {
	// lastComparable = [u1, a, b]; client repeats [a, b] after both x and y.
	last := twoSegBody(t, []map[string]string{
		summaryMsg("s"),
		userMsg("u1"),
		userMsg("a"),
		assistantMsg("b"),
	})
	client := twoSegBody(t, []map[string]string{
		userMsg("u1"), userMsg("x"), userMsg("a"), assistantMsg("b"),
		userMsg("y"), userMsg("a"), assistantMsg("b"),
	})
	res, err := BuildOutboundMessages(client, &SessionState{SchemaVersion: 1}, last, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsNewSess {
		t.Fatalf("ambiguous tail: expected fail-open, got DeltaCount=%d", res.DeltaCount)
	}
}

// TestFindDeltaAnchor_TwoSegment_MissingHeadFailsOpen: the head segment (first
// user / A-track system) is lineage evidence. A client history that contains
// the tail but not the head is a different session and must fail open.
func TestFindDeltaAnchor_TwoSegment_MissingHeadFailsOpen(t *testing.T) {
	last := twoSegBody(t, []map[string]string{
		summaryMsg("s"),
		userMsg("u1 original"),
		userMsg("u3 tail"),
		assistantMsg("a3 tail"),
	})
	client := twoSegBody(t, []map[string]string{
		userMsg("some other session"), assistantMsg("x"),
		userMsg("u3 tail"), assistantMsg("a3 tail"),
		userMsg("new"),
	})
	res, err := BuildOutboundMessages(client, &SessionState{SchemaVersion: 1}, last, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsNewSess {
		t.Fatalf("missing head: expected fail-open, got DeltaCount=%d", res.DeltaCount)
	}
}

// TestFindDeltaAnchor_TwoSegment_HeadAfterTailDoesNotCount: a head occurrence
// at or after the tail window is not lineage evidence for that anchor.
func TestFindDeltaAnchor_TwoSegment_HeadAfterTailDoesNotCount(t *testing.T) {
	last := twoSegBody(t, []map[string]string{
		summaryMsg("s"),
		userMsg("h"),
		userMsg("t1"),
		assistantMsg("t2"),
	})
	// [h] only occurs after the unique [t1,t2] window.
	client := twoSegBody(t, []map[string]string{
		userMsg("x"), userMsg("t1"), assistantMsg("t2"), userMsg("h"),
	})
	res, err := BuildOutboundMessages(client, &SessionState{SchemaVersion: 1}, last, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsNewSess {
		t.Fatalf("head-after-tail: expected fail-open, got DeltaCount=%d", res.DeltaCount)
	}
}

// TestFindDeltaAnchor_TwoSegment_SingleRetainedMsgCannotSplit: a one-message
// retained block has no two-segment decomposition and stays fail-open on
// ambiguity (guards the CompressedDuplicateAnchorFailsOpen semantics).
func TestFindDeltaAnchor_TwoSegment_SingleRetainedMsgCannotSplit(t *testing.T) {
	last := twoSegBody(t, []map[string]string{
		summaryMsg("s"),
		userMsg("A"),
	})
	client := twoSegBody(t, []map[string]string{
		userMsg("A"), assistantMsg("mid"), userMsg("A"), userMsg("new"),
	})
	res, err := BuildOutboundMessages(client, &SessionState{SchemaVersion: 1}, last, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsNewSess {
		t.Fatalf("single retained msg ambiguity: expected fail-open, got DeltaCount=%d", res.DeltaCount)
	}
}

// TestFindDeltaAnchor_AnthropicStringSystemSummaryDetected: an Anthropic
// rebuild with a string-shape system (orig + prefix + summary, unmarked) is
// recognised as compressed lineage via the top-level system field — the
// messages[] array carries no marker at all.
func TestFindDeltaAnchor_AnthropicStringSystemSummaryDetected(t *testing.T) {
	body := anthroBody(t, "You are Claude.", []string{
		"u1 hello", "a1 hi", "u2 how are you", "a2 fine", "u3 and now", "a3 ok",
	})
	ret, err := extractAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, ok := RebuildAnthropicAfterSummary(body, "summary text", ret, 1)
	if !ok {
		t.Fatal("rebuild must succeed")
	}
	// Precondition: the summary really is mid-string (the unmarked string shape).
	var top map[string]json.RawMessage
	if err := json.Unmarshal(rebuilt, &top); err != nil {
		t.Fatal(err)
	}
	var sys string
	if err := json.Unmarshal(top["system"], &sys); err != nil {
		t.Fatalf("precondition: system must stay string shape, got %s", top["system"])
	}
	if !strings.Contains(sys, "You are Claude.") || !strings.Contains(sys, "summary text") {
		t.Fatalf("precondition: string system must carry orig+summary, got %q", sys)
	}
	if !hasAnthropicSystemSummary(rebuilt) {
		t.Fatal("precondition: deep detection must fire for mid-string summary")
	}
	next := anthroBody(t, "You are Claude.", []string{
		"u1 hello", "a1 hi", "u2 how are you", "a2 fine", "u3 and now", "a3 ok", "u4 new question",
	})
	res, err := BuildOutboundMessages(next, &SessionState{SchemaVersion: 1}, rebuilt, "anthropic-messages")
	if err != nil {
		t.Fatal(err)
	}
	if res.IsNewSess || res.DeltaCount != 1 {
		t.Fatalf("anthropic string-system summary: expected anchor with DeltaCount=1, got IsNewSess=%v DeltaCount=%d", res.IsNewSess, res.DeltaCount)
	}
	// The merged outbound keeps the compressed system field (summary preserved).
	var mergedTop map[string]json.RawMessage
	if err := json.Unmarshal(res.Body, &mergedTop); err != nil {
		t.Fatal(err)
	}
	var mergedSys string
	if err := json.Unmarshal(mergedTop["system"], &mergedSys); err != nil || !strings.Contains(mergedSys, "summary text") {
		t.Fatalf("merged body must preserve the compressed system field, got %s", mergedTop["system"])
	}
}

// TestFindDeltaMarkerRecognition_UnmarkedDynCtxIsMarker pins the recognition
// split: isSummaryMarkerMsg keeps its marked-only contract (smm_v1 prefix —
// marker_idempotency Scenario C and the retain pin depend on it), while the
// anchor walk separately recognises the rebuilders' unmarked summary
// prefixes via isUnmarkedGatewaySummaryMsg. Both generations must be excluded
// from the comparable retained block.
func TestFindDeltaMarkerRecognition_UnmarkedDynCtxIsMarker(t *testing.T) {
	// The prefixes carry real newlines, so the messages must be built via
	// Marshal — splicing them into a JSON string literal produces invalid JSON.
	mustMsg := func(content string) json.RawMessage {
		b, err := json.Marshal(map[string]string{"role": "user", "content": content})
		if err != nil {
			t.Fatal(err)
		}
		return json.RawMessage(b)
	}
	dynCtx := mustMsg(CompressionSummaryPrefix + "summary body")
	if isSummaryMarkerMsg(dynCtx) {
		t.Fatal("isSummaryMarkerMsg contract is marked-only; unmarked content must stay false (Scenario C / retain pin depend on it)")
	}
	if !isUnmarkedGatewaySummaryMsg(dynCtx) {
		t.Fatal("unmarked CompressionSummaryPrefix content must be recognised by isUnmarkedGatewaySummaryMsg for the anchor walk")
	}
	smart := mustMsg(smartWindowSummaryPrefix + "summary body")
	if !isUnmarkedGatewaySummaryMsg(smart) {
		t.Fatal("unmarked smartWindowSummaryPrefix content must be recognised by isUnmarkedGatewaySummaryMsg")
	}
	marked := mustMsg("[smm_v1:abcdef]\n" + CompressionSummaryPrefix + "summary body")
	if !isSummaryMarkerMsg(marked) {
		t.Fatal("marked summary content must stay recognised by isSummaryMarkerMsg")
	}
}
