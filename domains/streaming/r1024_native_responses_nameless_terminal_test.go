package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// r1024（2026-10-13）：vapeur 的 Responses SSE 不发 `event:` 行，事件名只在
// data JSON 的 type 字段里。此前终态判定（terminal 只看 event.Name）失效：
// response.completed 已写给客户端后，上游关连接被误判为
// native_responses_read_error 流中断，客户端在完整响应之后又收到
// response.failed，且失败计入凭据健康（探针 3 连败停节点 → no_candidate）。
//
// 判据用真实线帧形状（2026-10-13 生产捕获，去掉非载荷字段）：无 event: 行、
// data.type 承载事件名，terminal 后紧跟连接关闭。
func TestR1024NamelessResponsesTerminalSurvivesConnClose(t *testing.T) {
	frames := []string{
		`data: {"type":"response.created","response":{"id":"r1","status":"in_progress","output":[]}}` + "\n\n",
		`data: {"type":"response.output_item.added","item":{"type":"message","role":"assistant"},"output_index":0}` + "\n\n",
		`data: {"type":"response.output_text.delta","delta":"ok","output_index":0}` + "\n\n",
		`data: {"type":"response.output_text.done","text":"ok","output_index":0}` + "\n\n",
		`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"total_tokens":9}}}` + "\n\n",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, f := range frames {
			_, _ = w.Write([]byte(f))
			flusher.Flush()
		}
		// 终态之后立刻关连接（vapeur 实测形态：不带 [DONE]，直接 EOF）。
		panic(http.ErrAbortHandler)
	}))
	defer srv.Close()

	rec := httptest.NewRecorder()
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("upstream: %v", err)
	}
	outcome := StreamNativeResponsesSSE(context.Background(), rec, resp, "req-r1024", nil, nil)
	wire := rec.Body.String()

	if outcome.Interrupted {
		t.Fatalf("terminal-then-close must be a clean end: %+v", outcome)
	}
	if !strings.Contains(wire, `"type":"response.completed"`) {
		t.Fatal("completed frame missing on the wire")
	}
	if strings.Contains(wire, "response.failed") {
		t.Fatal("spurious response.failed after completed terminal")
	}
}

// 前提在场配对：终态**之前**的连接关闭仍必须是中断（修复不得放过真中断）。
func TestR1024NamelessResponsesMidStreamCloseStillInterrupts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"par"}` + "\n\n"))
		flusher.Flush()
		panic(http.ErrAbortHandler) // 无终态，直接断
	}))
	defer srv.Close()

	rec := httptest.NewRecorder()
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("upstream: %v", err)
	}
	outcome := StreamNativeResponsesSSE(context.Background(), rec, resp, "req-r1024b", nil, nil)
	if !outcome.Interrupted {
		t.Fatalf("pre-terminal close must stay interrupted: %+v", outcome)
	}
	if outcome.Reason != "native_responses_read_error" {
		t.Fatalf("reason=%s want native_responses_read_error", outcome.Reason)
	}
}

// 单元层：无 event: 行时 Name 从 payload.type 回填；带 event: 行的不覆盖。
func TestR1024FinishEventBackfillsNameFromPayload(t *testing.T) {
	ev, err := finishNativeResponsesEvent(NativeResponsesEvent{}, []string{`{"type":"response.completed","response":{}}`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Name != "response.completed" {
		t.Fatalf("name=%q want response.completed", ev.Name)
	}
	if ev.Class != FrameClassTerminal {
		t.Fatalf("class=%v want terminal", ev.Class)
	}
	ev, err = finishNativeResponsesEvent(NativeResponsesEvent{Name: "response.completed"}, []string{`{"type":"response.completed"}`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Name != "response.completed" {
		t.Fatalf("explicit name must be kept, got %q", ev.Name)
	}
	// [DONE] 与空 data 不回填、不报错。
	ev, err = finishNativeResponsesEvent(NativeResponsesEvent{}, []string{"[DONE]"}, nil)
	if err != nil || ev.Name != "" {
		t.Fatalf("done frame: %+v %v", ev, err)
	}
	_ = time.Second
}
