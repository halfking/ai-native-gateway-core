package executors

// executor_dispatch_notice_metrics_test.go covers the DispatchNotice drop
// accounting added to the executor transport bridge (executor_dispatch.go,
// bridgeDispatchNotice). Transport by request shape:
//
//   - non_streaming: a request without a wired FailoverNotices collector has
//     no `: thinking:` SSE channel and no header digest — the notice can
//     never be surfaced. With a collector wired, the header channel takes
//     over and nothing is dropped (asserted below);
//   - prestream_uninit: a streaming request whose preStream keepalive was
//     never initialized — the handler-side OnNodeJump closure silently
//     swallows the message behind its `if preStream != nil` guard.
//
// The instrumentation is pure observation: the bridge must keep making the
// exact same calls as the previous inline closure (behaviour parity is
// asserted below).

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
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

// Streaming requests keep the SSE side-band: even with a collector wired,
// the X-Gateway-Failover-Notice header must never be set (the preStream
// response may already be committed; SSE clients read `: thinking:`).
func TestBridgeDispatchNoticeStreamingNeverSetsHeader(t *testing.T) {
	w := httptest.NewRecorder()
	bridge := bridgeDispatchNotice(&ExecParams{
		W:                 w,
		IsStream:          true,
		PreStreamPrepared: true,
		FailoverNotices:   NewFailoverNoticeCollector(),
		OnNodeJump:        func(string) {},
	})
	bridge(dispatch.DispatchNotice{Kind: dispatch.NoticeKindRetry, Message: "重试中…"})

	if v := w.Header().Get(FailoverNoticeHeader); v != "" {
		t.Fatalf("streaming request must not set FailoverNoticeHeader, got %q", v)
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

// decodeFailoverNoticeHeader decodes the base64(JSON) FailoverNoticeHeader
// digest from a test recorder back into notice structs.
func decodeFailoverNoticeHeader(t *testing.T, w *httptest.ResponseRecorder) []dispatch.DispatchNotice {
	t.Helper()
	enc := w.Header().Get(FailoverNoticeHeader)
	if enc == "" {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("FailoverNoticeHeader is not valid base64: %v", err)
	}
	var notices []dispatch.DispatchNotice
	if err := json.Unmarshal(raw, &notices); err != nil {
		t.Fatalf("FailoverNoticeHeader payload is not valid notice JSON: %v", err)
	}
	return notices
}

// Non-streaming request with a wired collector: the notice is surfaced via
// the X-Gateway-Failover-Notice header digest instead of being dropped —
// no drop counter may move. The header must be set while mutable (before any
// WriteHeader), which the recorder models by never having committed.
func TestBridgeDispatchNoticeNonStreamingSurfacesViaHeader(t *testing.T) {
	before := gatherDispatchNoticeDropped(t)
	w := httptest.NewRecorder()
	var got []string

	bridge := bridgeDispatchNotice(&ExecParams{
		W:               w,
		IsStream:        false,
		FailoverNotices: NewFailoverNoticeCollector(),
		OnNodeJump:      func(msg string) { got = append(got, msg) },
	})
	bridge(dispatch.DispatchNotice{Kind: dispatch.NoticeKindNodeSwitch, Message: "切换节点…", Attempt: 2})
	bridge(dispatch.DispatchNotice{Kind: dispatch.NoticeKindRetry, Message: "重试中…"})

	notices := decodeFailoverNoticeHeader(t, w)
	if len(notices) != 2 {
		t.Fatalf("header digest holds %d notices, want 2", len(notices))
	}
	if notices[0].Kind != dispatch.NoticeKindNodeSwitch || notices[1].Kind != dispatch.NoticeKindRetry {
		t.Fatalf("header digest order wrong: %v / %v", notices[0].Kind, notices[1].Kind)
	}
	if notices[0].Attempt != 2 {
		t.Fatalf("structured fields lost in digest: attempt = %d, want 2", notices[0].Attempt)
	}
	if len(got) != 2 {
		t.Fatalf("OnNodeJump invocations = %v, want 2 (behaviour parity)", got)
	}
	after := gatherDispatchNoticeDropped(t)
	for key, v := range after {
		if v != before[key] {
			t.Fatalf("surfaced notice must not be counted as dropped; series %v moved %v -> %v", key, before[key], v)
		}
	}
}

// The header digest must stay a single, HTTP-safe header value: no raw
// newlines may leak from notice messages (they would corrupt the response).
func TestFailoverNoticeHeaderIsHTTPSafe(t *testing.T) {
	w := httptest.NewRecorder()
	bridge := bridgeDispatchNotice(&ExecParams{
		W:               w,
		IsStream:        false,
		FailoverNotices: NewFailoverNoticeCollector(),
	})
	bridge(dispatch.DispatchNotice{Kind: dispatch.NoticeKindQueued, Message: "line1\nline2\r\nline3"})

	v := w.Header().Get(FailoverNoticeHeader)
	if v == "" || strings.ContainsAny(v, "\n\r") {
		t.Fatalf("header value must be non-empty and header-safe, got %q", v)
	}
	notices := decodeFailoverNoticeHeader(t, w)
	if len(notices) != 1 || notices[0].Message != "line1\nline2\r\nline3" {
		t.Fatalf("digest must preserve the message verbatim after base64, got %+v", notices)
	}
}

// Bounded digest: only the most recent failoverNoticeMaxCount notices are
// kept, and over-long messages are truncated rune-safe.
func TestFailoverNoticeCollectorBoundsAndTruncates(t *testing.T) {
	c := NewFailoverNoticeCollector()
	longMsg := strings.Repeat("长", failoverNoticeMaxMessage+50)
	for i := 0; i < failoverNoticeMaxCount+5; i++ {
		c.Add(dispatch.DispatchNotice{Kind: dispatch.NoticeKindRetry, Message: longMsg, Attempt: i})
	}

	w := httptest.NewRecorder()
	w.Header().Set(FailoverNoticeHeader, c.HeaderValue())
	notices := decodeFailoverNoticeHeader(t, w)
	if len(notices) != failoverNoticeMaxCount {
		t.Fatalf("collector kept %d notices, want %d", len(notices), failoverNoticeMaxCount)
	}
	if notices[0].Attempt != 5 || notices[len(notices)-1].Attempt != failoverNoticeMaxCount+4 {
		t.Fatalf("collector must keep the most recent notices, got attempts %d..%d",
			notices[0].Attempt, notices[len(notices)-1].Attempt)
	}
	if got := len([]rune(notices[0].Message)); got != failoverNoticeMaxMessage {
		t.Fatalf("message truncated to %d runes, want %d", got, failoverNoticeMaxMessage)
	}
}

// A collector that saw nothing renders no header value — the executor must
// not set an empty X-Gateway-Failover-Notice on clean responses.
func TestFailoverNoticeHeaderValueEmptyWhenNoNotices(t *testing.T) {
	if v := NewFailoverNoticeCollector().HeaderValue(); v != "" {
		t.Fatalf("empty collector HeaderValue = %q, want empty", v)
	}
	var nilCollector *FailoverNoticeCollector
	if v := nilCollector.HeaderValue(); v != "" {
		t.Fatalf("nil collector HeaderValue = %q, want empty", v)
	}
}
