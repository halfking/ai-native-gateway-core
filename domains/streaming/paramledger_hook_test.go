package streaming

// paramledger_hook_test.go — 流式参数回显还原测试（2026-09-22）。
// 出站被降档（x-high→high）后，上游 SSE 的 response.created/completed
// 生命周期帧回显 effort:"high"，写回客户端前应还原为 "x-high"；
// 内容帧不受影响。

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/paramledger"
)

func TestRestoreEchoFrameUnit(t *testing.T) {
	prev := ParamLedger()
	defer SetParamLedger(prev)

	l := paramledger.New(nil)
	l.Record("req-1", paramledger.Adjustment{
		Field: "reasoning.effort", Original: "x-high", Sent: "high",
		Action: paramledger.ActionClamp, Reason: "test",
	})
	SetParamLedger(l)

	raw := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"reasoning\":{\"effort\":\"high\"}}}\n\n"
	got := restoreEchoFrame([]byte(raw), "req-1")
	if !strings.Contains(string(got), `"effort":"x-high"`) {
		t.Fatalf("frame not restored: %s", got)
	}
	// 未记录的请求原样返回。
	untouched := restoreEchoFrame([]byte(raw), "req-other")
	if !strings.Contains(string(untouched), `"effort":"high"`) {
		t.Fatalf("unknown request frame must pass through: %s", untouched)
	}
	// nil 账本安全。
	SetParamLedger(nil)
	if got := restoreEchoFrame([]byte(raw), "req-1"); !strings.Contains(string(got), `"effort":"high"`) {
		t.Fatalf("nil ledger must pass through: %s", got)
	}
}

func TestStreamNativeResponsesSSERestoresEffortEcho(t *testing.T) {
	prev := ParamLedger()
	defer SetParamLedger(prev)

	l := paramledger.New(nil)
	l.Record("req-sse-1", paramledger.Adjustment{
		Field: "reasoning.effort", Original: "x-high", Sent: "high",
		Action: paramledger.ActionClamp, Reason: "test",
	})
	SetParamLedger(l)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"reasoning\":{\"effort\":\"high\"}}}\n\n")
		_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"reasoning\":{\"effort\":\"high\"}}}\n\n")
	}))
	defer upstream.Close()

	resp, err := http.Post(upstream.URL, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	rec := httptest.NewRecorder()
	outcome := StreamNativeResponsesSSE(context.Background(), rec, resp, "req-sse-1", nil, nil)
	if outcome.Interrupted {
		t.Fatalf("outcome interrupted: %+v", outcome)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"effort":"x-high"`) {
		t.Fatalf("client stream missing restored effort: %s", body)
	}
	if strings.Contains(body, `"effort":"high"`) {
		t.Fatalf("client stream still shows clamped effort: %s", body)
	}
	// 内容帧不被改写。
	if !strings.Contains(body, `"delta":"hi"`) {
		t.Fatalf("content frame lost: %s", body)
	}
	if !strings.Contains(body, "event: response.completed") {
		t.Fatalf("terminal frame lost: %s", body)
	}
	_ = time.Second
}
