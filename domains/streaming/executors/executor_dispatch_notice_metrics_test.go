package executors

// executor_dispatch_notice_metrics_test.go covers the DispatchNotice drop
// accounting added to the executor transport bridge (executor_dispatch.go,
// bridgeDispatchNotice). Two drop situations are counted in
// metrics.DispatchNoticeDroppedTotal:
//
//   - non_streaming: the request is not a stream response — no `: thinking:`
//     SSE channel exists, the notice can never be surfaced;
//   - prestream_uninit: a streaming request whose preStream keepalive was
//     never initialized — the handler-side OnNodeJump closure silently
//     swallows the message behind its `if preStream != nil` guard.
//
// The instrumentation is pure observation: the bridge must keep making the
// exact same calls as the previous inline closure (behaviour parity is
// asserted below).

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/metrics"
)

// gatherDispatchNoticeDropped reads the current value matrix of
// llmgw_dispatch_notice_dropped_total from the default registry
// (same DefaultGatherer pattern as router_shadow_diff_test.go).
func gatherDispatchNoticeDropped(t *testing.T) map[[2]string]float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	out := map[[2]string]float64{}
	for _, family := range families {
		if family.GetName() != "llmgw_dispatch_notice_dropped_total" {
			continue
		}
		for _, m := range family.GetMetric() {
			var kind, reason string
			for _, l := range m.GetLabel() {
				switch l.GetName() {
				case "notice_kind":
					kind = l.GetValue()
				case "reason":
					reason = l.GetValue()
				}
			}
			out[[2]string{kind, reason}] = m.GetCounter().GetValue()
		}
	}
	return out
}

func droppedDelta(t *testing.T, before, after map[[2]string]float64, key [2]string) float64 {
	t.Helper()
	return after[key] - before[key]
}

// Non-streaming request without an OnNodeJump bridge: the notice is dropped
// at the bridge itself and must be counted under reason=non_streaming.
func TestBridgeDispatchNoticeDropsCountedNonStreaming(t *testing.T) {
	before := gatherDispatchNoticeDropped(t)

	bridge := bridgeDispatchNotice(&ExecParams{IsStream: false}) // OnNodeJump == nil
	bridge(dispatch.DispatchNotice{Kind: dispatch.NoticeKindRetry, Message: "重试中…"})

	after := gatherDispatchNoticeDropped(t)
	key := [2]string{"retry", metrics.DispatchNoticeDropReasonNonStreaming}
	if d := droppedDelta(t, before, after, key); d != 1 {
		t.Fatalf("non_streaming retry drop delta = %v, want 1", d)
	}
	// The prestream bucket must stay untouched for a non-streaming request.
	other := [2]string{"retry", metrics.DispatchNoticeDropReasonPreStreamUninit}
	if d := droppedDelta(t, before, after, other); d != 0 {
		t.Fatalf("prestream_uninit retry delta = %v, want 0", d)
	}
}

// Non-streaming request whose caller DID wire OnNodeJump (all three protocol
// entries share one ExecParams builder): the callback is still invoked for
// behaviour parity, but the handler-side closure discards the message because
// preStream is nil (preStream only exists for streaming requests). Classified
// reason=non_streaming.
func TestBridgeDispatchNoticeNonStreamingStillInvokesCallback(t *testing.T) {
	before := gatherDispatchNoticeDropped(t)
	var got []string

	bridge := bridgeDispatchNotice(&ExecParams{
		IsStream:          false,
		PreStreamPrepared: false,
		OnNodeJump:        func(msg string) { got = append(got, msg) },
	})
	bridge(dispatch.DispatchNotice{Kind: dispatch.NoticeKindNodeSwitch, Message: "切换节点…"})

	if len(got) != 1 || got[0] != "切换节点…" {
		t.Fatalf("OnNodeJump invocations = %v, want exactly [切换节点…] (behaviour parity)", got)
	}
	after := gatherDispatchNoticeDropped(t)
	key := [2]string{"node_switch", metrics.DispatchNoticeDropReasonNonStreaming}
	if d := droppedDelta(t, before, after, key); d != 1 {
		t.Fatalf("non_streaming node_switch drop delta = %v, want 1", d)
	}
}

// Streaming request with preStream uninitialized: the callback is invoked
// exactly as before (the handler closure drops it internally), and the drop is
// counted under reason=prestream_uninit.
func TestBridgeDispatchNoticeDropsCountedPrestreamUninit(t *testing.T) {
	before := gatherDispatchNoticeDropped(t)
	var got []string

	bridge := bridgeDispatchNotice(&ExecParams{
		IsStream:          true,
		PreStreamPrepared: false,
		OnNodeJump:        func(msg string) { got = append(got, msg) },
	})
	bridge(dispatch.DispatchNotice{Kind: dispatch.NoticeKindRetry, Message: "重试中…"})

	if len(got) != 1 || got[0] != "重试中…" {
		t.Fatalf("OnNodeJump invocations = %v, want exactly [重试中…] (behaviour parity)", got)
	}
	after := gatherDispatchNoticeDropped(t)
	key := [2]string{"retry", metrics.DispatchNoticeDropReasonPreStreamUninit}
	if d := droppedDelta(t, before, after, key); d != 1 {
		t.Fatalf("prestream_uninit retry drop delta = %v, want 1", d)
	}
	other := [2]string{"retry", metrics.DispatchNoticeDropReasonNonStreaming}
	if d := droppedDelta(t, before, after, other); d != 0 {
		t.Fatalf("non_streaming retry delta = %v, want 0", d)
	}
}

// Streaming request without any OnNodeJump bridge (internal sub-executions):
// counted as prestream_uninit — a streaming request whose thinking channel
// was not wired up.
func TestBridgeDispatchNoticeStreamingNilCallbackCountsPrestreamUninit(t *testing.T) {
	before := gatherDispatchNoticeDropped(t)

	bridge := bridgeDispatchNotice(&ExecParams{IsStream: true}) // OnNodeJump == nil
	bridge(dispatch.DispatchNotice{Kind: dispatch.NoticeKindQueued, Message: "排队等待中…"})

	after := gatherDispatchNoticeDropped(t)
	key := [2]string{"queued", metrics.DispatchNoticeDropReasonPreStreamUninit}
	if d := droppedDelta(t, before, after, key); d != 1 {
		t.Fatalf("prestream_uninit queued drop delta = %v, want 1", d)
	}
}

// Healthy path: streaming request with preStream initialized — the notice is
// delivered and NO drop counter moves anywhere.
func TestBridgeDispatchNoticeNoDropWhenPrestreamReady(t *testing.T) {
	before := gatherDispatchNoticeDropped(t)
	var got []string

	bridge := bridgeDispatchNotice(&ExecParams{
		IsStream:          true,
		PreStreamPrepared: true,
		OnNodeJump:        func(msg string) { got = append(got, msg) },
	})
	bridge(dispatch.DispatchNotice{Kind: dispatch.NoticeKindModelSwitch, Message: "模型切换…"})

	if len(got) != 1 || got[0] != "模型切换…" {
		t.Fatalf("OnNodeJump invocations = %v, want exactly [模型切换…]", got)
	}
	after := gatherDispatchNoticeDropped(t)
	for key, v := range after {
		if v != before[key] {
			t.Fatalf("no drop should be recorded; series %v moved %v -> %v", key, before[key], v)
		}
	}
}

// dispatchNoticeDropReason classification table.
func TestDispatchNoticeDropReasonClassification(t *testing.T) {
	cases := []struct {
		name   string
		params *ExecParams
		want   string
	}{
		{"nil params", nil, metrics.DispatchNoticeDropReasonNonStreaming},
		{"non-stream", &ExecParams{IsStream: false}, metrics.DispatchNoticeDropReasonNonStreaming},
		{"stream", &ExecParams{IsStream: true}, metrics.DispatchNoticeDropReasonPreStreamUninit},
	}
	for _, c := range cases {
		if got := dispatchNoticeDropReason(c.params); got != c.want {
			t.Errorf("%s: reason = %q, want %q", c.name, got, c.want)
		}
	}
}
