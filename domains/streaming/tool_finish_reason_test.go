package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func toolFinishReasons(t *testing.T, wire string) map[int]string {
	t.Helper()
	out := map[int]string{}
	for _, line := range strings.Split(wire, "\n") {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var chunk struct {
			Choices []struct {
				Index  int     `json:"index"`
				Finish *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			t.Fatal(err)
		}
		for _, c := range chunk.Choices {
			if c.Finish != nil && *c.Finish != "" {
				out[c.Index] = *c.Finish
			}
		}
	}
	return out
}

func TestToolFinishReasonChatLiveStream(t *testing.T) {
	withEarlyEmptyThreshold(t, 0)
	norm := NewNormalizer() // production shares this instance across requests
	for _, reason := range []string{"stop", "", "length", "content_filter"} {
		t.Run(reason, func(t *testing.T) {
			body := `data: {"id":"x","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n" +
				`data: {"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"shell","arguments":"{}"}}]},"finish_reason":null},{"index":1,"delta":{"content":"plain"},"finish_reason":null}]}` + "\n\n" +
				fmt.Sprintf(`data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":%q},{"index":1,"delta":{},"finish_reason":"stop"}]}`, reason) + "\n\ndata: [DONE]\n\n"
			rec := httptest.NewRecorder()
			out := StreamChat(context.Background(), rec, &http.Response{Body: io.NopCloser(strings.NewReader(body))}, "test", "test", norm)
			if out.Interrupted {
				t.Fatalf("stream interrupted: %+v", out)
			}
			want := reason
			if reason == "" || reason == "stop" {
				want = "tool_calls"
			}
			got := toolFinishReasons(t, rec.Body.String())
			if got[0] != want || got[1] != "stop" {
				t.Fatalf("finish reasons = %v, want 0:%s, 1:stop; wire=%s", got, want, rec.Body.String())
			}
		})
	}
	// A prior tool request must not mark a later ordinary request as a tool turn.
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"plain\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	rec := httptest.NewRecorder()
	StreamChat(context.Background(), rec, &http.Response{Body: io.NopCloser(strings.NewReader(body))}, "test", "test", norm)
	if got := toolFinishReasons(t, rec.Body.String())[0]; got != "stop" {
		t.Fatalf("request state leaked: %q", got)
	}
}

func TestToolFinishReasonNonStreamNormalizer(t *testing.T) {
	for _, name := range []string{"shell", ""} {
		body := fmt.Sprintf(`{"choices":[{"index":0,"message":{"tool_calls":[{"function":{"name":%q,"arguments":"{}"}}]},"finish_reason":"stop"}]}`, name)
		got := string(NewNormalizer().NormalizeChunk([]byte(body), false))
		want := "stop"
		if name != "" {
			want = "tool_calls"
		}
		if !strings.Contains(got, `"finish_reason":"`+want+`"`) {
			t.Fatalf("normalized = %s, want %s", got, want)
		}
	}
	plain := []byte(`{"choices":[{"message":{"content":"plain"}}]}`)
	if got := NewNormalizer().NormalizeChunk(plain, false); string(got) != string(plain) {
		t.Fatalf("ordinary response changed: %s", got)
	}
}

func TestToolFinishReasonChatNoSpaceSSE(t *testing.T) {
	withEarlyEmptyThreshold(t, 0)
	body := "data:{\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"shell\",\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\n" +
		"data:{\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata:[DONE]\n\n"
	rec := httptest.NewRecorder()
	out := StreamChat(context.Background(), rec, &http.Response{Body: io.NopCloser(strings.NewReader(body))}, "test", "test", NewNormalizer())
	if out.Interrupted || toolFinishReasons(t, rec.Body.String())[0] != "tool_calls" {
		t.Fatalf("no-space SSE lost tool finish: %+v wire=%s", out, rec.Body.String())
	}
}

func TestToolFinishReasonNamelessStreamIsEmptyFailover(t *testing.T) {
	// r1028b: fully empty phantoms (no name, no arguments) are stripped;
	// with no other agent-visible output the Magpie empty gate must fail
	// over. Note: arguments-only frames like {"arguments":"{}"} are kept
	// as streaming continuations — use the true MiniMax empty slot here.
	withEarlyEmptyThreshold(t, 0)
	body := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"","type":"function","function":{"name":"","arguments":""}}]},"finish_reason":null}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
	rec := httptest.NewRecorder()
	out := StreamChat(context.Background(), rec, &http.Response{Body: io.NopCloser(strings.NewReader(body))}, "test", "test", NewNormalizer())
	if !out.Interrupted {
		t.Fatalf("nameless-only stream must fail over as empty, got %+v wire=%s", out, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "tool_calls") {
		t.Fatalf("phantom tool_calls leaked to wire: %s", rec.Body.String())
	}
}

func TestToolFinishReasonAnthropicToChatLiveStream(t *testing.T) {
	for _, reason := range []string{"end_turn", "max_tokens"} {
		body := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"test\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"shell\",\"input\":{\"command\":\"pwd\"}}}\n\n" +
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			fmt.Sprintf("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":4}}\n\n", reason) +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
		rec := httptest.NewRecorder()
		out := StreamAnthropicSSEToOpenAI(context.Background(), rec, &http.Response{Body: io.NopCloser(strings.NewReader(body))}, "test", "test", "test", nil, nil)
		if out.Interrupted {
			t.Fatalf("Q3 interrupted: %+v wire=%s", out, rec.Body.String())
		}
		want := "tool_calls"
		if reason == "max_tokens" {
			want = "length"
		}
		if got := toolFinishReasons(t, rec.Body.String())[0]; got != want {
			t.Fatalf("Q3 finish=%q want=%q wire=%s", got, want, rec.Body.String())
		}
	}
}

func TestToolFinishReasonQ2PreservesTruncation(t *testing.T) {
	for _, reason := range []string{"stop", "", "length"} {
		body := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"shell","arguments":"{}"}}]},"finish_reason":null}]}` + "\n\n" + fmt.Sprintf(`data: {"choices":[{"delta":{},"finish_reason":%q}]}`, reason) + "\n\ndata: [DONE]\n\n"
		rec := httptest.NewRecorder()
		out := StreamOpenAIToAnthropicSSE(context.Background(), rec, &http.Response{Body: io.NopCloser(strings.NewReader(body))}, "test", "test", "test", nil, nil)
		if out.Interrupted {
			t.Fatalf("stream interrupted: %+v", out)
		}
		want := "tool_use"
		if reason == "length" {
			want = "max_tokens"
		}
		if !strings.Contains(rec.Body.String(), `"stop_reason":"`+want+`"`) {
			t.Fatalf("Q2 reason=%s wire=%s", reason, rec.Body.String())
		}
	}
}
