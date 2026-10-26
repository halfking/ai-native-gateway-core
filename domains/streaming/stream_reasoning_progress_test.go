package streaming

import (
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

const reasoningProgressFrame = "data: {\"id\":\"r\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"thinking\"}}]}\n\n"

func TestEmptyGateReasoningProgressBeforeAnswer(t *testing.T) {
	withEarlyEmptyThreshold(t, 3)
	lines, outcome, wire := runEmptyGateForTest(t, reasoningProgressFrame,
		strings.Repeat(reasoningProgressFrame, 3)+earlyEmptyContentDeltaFrame+"data: [DONE]\n\n")
	if outcome != nil {
		t.Fatalf("reasoning progress must wait for the answer: %+v", outcome)
	}
	if !strings.Contains(strings.Join(lines, ""), `"content":"ok"`) {
		t.Fatalf("answer missing from buffered frames: %q", lines)
	}
	if wire != "" {
		t.Fatalf("gate wrote frames instead of returning them: %q", wire)
	}
}

func TestEmptyGateReasoningResetsEmptyCounter(t *testing.T) {
	withEarlyEmptyThreshold(t, 3)
	lines, outcome, wire := runEmptyGateForTest(t, emptyDeltaFrame,
		emptyDeltaFrame+reasoningProgressFrame+emptyDeltaFrame+emptyDeltaFrame+earlyEmptyContentDeltaFrame)
	if outcome != nil {
		t.Fatalf("reasoning must reset the two preceding empty deltas: %+v", outcome)
	}
	// This sequence hits the existing eight-line cap (blank SSE lines count).
	// The main loop will consume the answer; the gate must retain all four
	// empty deltas instead of failing on the first one after reasoning.
	if strings.Count(strings.Join(lines, ""), `"id":"empty"`) != 4 || wire != "" {
		t.Fatalf("gate did not reach the deltas after reasoning: lines=%q wire=%q", lines, wire)
	}
}

func TestEmptyGateReasoningOnlyFinishStillFailsOver(t *testing.T) {
	withEarlyEmptyThreshold(t, 3)
	finish := "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"length\"}]}\n\n"
	_, outcome, wire := runEmptyGateForTest(t, reasoningProgressFrame,
		reasoningProgressFrame+reasoningProgressFrame+finish)
	if outcome == nil || outcome.Kind != errorsx.KindEmptyResponse || !outcome.Resumable {
		t.Fatalf("reasoning without an answer must fail over at completion: %+v", outcome)
	}
	if outcome.Reason == "early_empty_detection" || wire != "" {
		t.Fatalf("reasoning must reach the terminal before judging it empty: %+v, wire=%q", outcome, wire)
	}
}
