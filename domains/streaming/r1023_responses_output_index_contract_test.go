package streaming

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 2026-10-13 — r1023：Responses 桥的两条线协议契约修正。
//
// 客户端（ZCode 内嵌 AI-SDK openai-responses，2026-10-13 解包确认）对
// /v1/responses 流的两条硬契约：
//
//  1. **output_index 唯一**：每个 output item 按输出数组位置编号；delta
//     事件按 output_index 关联 item。旧实现把 OpenAI tool_calls[].index
//     （几乎恒 0）直接当 output_index 用，与预声明的 message item（0）
//     冲突——两个不同 item 在线上共用一个索引。
//  2. **output_item.done 的 function_call item.status 必须是字面量
//     "completed"**：AI-SDK 用 Zod literal 校验该字段，任何其它值（如
//     length 截断时的 "incomplete"）让整条流解析失败。截断语义由终态
//     envelope 的 status + incomplete_details 承载。
//
// 判据与被测函数同层：直接驱动 StreamOpenAIToResponsesSSE /
// StreamAnthropicSSEToResponses（httptest 假上游），逐帧解析客户端侧
// SSE 断言契约。

type r1023Frame struct {
	event string
	data  map[string]any
}

func r1023ParseSSE(t *testing.T, body string) []r1023Frame {
	t.Helper()
	var frames []r1023Frame
	cur := ""
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "event: ") {
			cur = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			var d map[string]any
			require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d), "frame not JSON: %s", line)
			frames = append(frames, r1023Frame{event: cur, data: d})
		}
	}
	return frames
}

// r1023AssertOutputIndexContract walks the emitted frames and enforces:
//   - every output_item.added gets a DISTINCT output_index;
//   - the message item owns index 0, tool items ≥ 1;
//   - function_call_arguments.delta / .done / output_item.done carry the SAME
//     output_index as their item's output_item.added (keyed by item_id);
//   - every output_item.done item with type function_call has
//     status == "completed".
func r1023AssertOutputIndexContract(t *testing.T, frames []r1023Frame) {
	t.Helper()
	addedIdx := map[string]float64{} // item_id -> output_index (function_call)
	usedIdx := map[float64]string{}  // output_index -> item type
	for _, f := range frames {
		switch f.event {
		case "response.output_item.added":
			item, _ := f.data["item"].(map[string]any)
			require.NotNil(t, item, "output_item.added without item")
			idx, _ := f.data["output_index"].(float64)
			typ, _ := item["type"].(string)
			if prev, clash := usedIdx[idx]; clash {
				t.Fatalf("output_index %.0f reused by two items (%s and %s)", idx, prev, typ)
			}
			usedIdx[idx] = typ
			if typ == "function_call" && idx == 0 {
				t.Fatalf("function_call item emitted at output_index 0 (collides with the pre-declared message item)")
			}
			if id, _ := item["id"].(string); typ == "function_call" {
				addedIdx[id] = idx
			}
		case "response.function_call_arguments.delta", "response.function_call_arguments.done":
			itemID, _ := f.data["item_id"].(string)
			idx, _ := f.data["output_index"].(float64)
			if want, ok := addedIdx[itemID]; ok && want != idx {
				t.Fatalf("%s for item %s carries output_index %.0f, but the item was added at %.0f", f.event, itemID, idx, want)
			}
		case "response.output_item.done":
			item, _ := f.data["item"].(map[string]any)
			require.NotNil(t, item, "output_item.done without item")
			if typ, _ := item["type"].(string); typ == "function_call" {
				st, _ := item["status"].(string)
				if st != "completed" {
					t.Fatalf("output_item.done function_call item.status = %q, want literal \"completed\" (AI-SDK Zod contract)", st)
				}
			}
		}
	}
}

// TestR1023OpenAIToResponsesOutputIndexUnique — 文本 + 两个工具调用的混合流
// （OpenAI chat 上游，tool_calls index 0 与 1）：message 占 0，工具依次占
// 1、2，各事件帧的 output_index 与 item 一致。
func TestR1023OpenAIToResponsesOutputIndexUnique(t *testing.T) {
	upstreamBody := strings.Join([]string{
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"let me run"},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"shell","arguments":"{\"co"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"mmand\":\"ls\"}"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"read","arguments":"{\"path\":\"/tmp\"}"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n",
		`data: [DONE]` + "\n\n",
	}, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	rec := httptest.NewRecorder()
	out := StreamOpenAIToResponsesSSE(context.Background(), rec, resp, "m", "m", "req-r1023-openai", nil, nil)
	require.False(t, out.Interrupted)

	frames := r1023ParseSSE(t, rec.Body.String())
	require.NotEmpty(t, frames)
	r1023AssertOutputIndexContract(t, frames)

	// 前提在场：必须真的出现了 message + 两个 function_call item。
	sawMessage, sawTools := false, 0
	for _, f := range frames {
		if f.event == "response.output_item.added" {
			typ, _ := f.data["item"].(map[string]any)["type"].(string)
			switch typ {
			case "message":
				sawMessage = true
			case "function_call":
				sawTools++
			}
		}
	}
	assert.True(t, sawMessage, "message item missing — contract check vacuous")
	assert.Equal(t, 2, sawTools, "expected both tool calls as distinct items")
}

// TestR1023AnthropicToResponsesOutputIndexUnique — 同一契约的 Anthropic 上游
// 方向（content_block index → output_index 不得与 message 冲突）。
func TestR1023AnthropicToResponsesOutputIndexUnique(t *testing.T) {
	upstreamBody := strings.Join([]string{
		`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"shell","input":{}}}` + "\n\n",
		`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls\"}"}}` + "\n\n",
		`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n",
		`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}` + "\n\n",
		`event: message_stop` + "\n" + `data: {"type":"message_stop"}` + "\n\n",
	}, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	rec := httptest.NewRecorder()
	out := StreamAnthropicSSEToResponses(context.Background(), rec, resp, "m", "m", "req-r1023-anthropic", nil, nil)
	require.False(t, out.Interrupted)

	frames := r1023ParseSSE(t, rec.Body.String())
	require.NotEmpty(t, frames)
	r1023AssertOutputIndexContract(t, frames)
	sawTool := false
	for _, f := range frames {
		if f.event == "response.output_item.added" {
			if typ, _ := f.data["item"].(map[string]any)["type"].(string); typ == "function_call" {
				sawTool = true
			}
		}
	}
	assert.True(t, sawTool, "function_call item missing — contract check vacuous")
}

// TestR1023TruncatedToolCallItemStatusLiteral — finish_reason=length 的截断
// 工具调用：output_item.done 的 item.status 仍必须是 "completed"（截断由
// 终态 envelope 承载），否则 AI-SDK Zod literal 校验整流失败。
func TestR1023TruncatedToolCallItemStatusLiteral(t *testing.T) {
	upstreamBody := strings.Join([]string{
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_t","type":"function","function":{"name":"shell","arguments":"{\"command\":\"lo"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}` + "\n\n",
		`data: [DONE]` + "\n\n",
	}, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	rec := httptest.NewRecorder()
	out := StreamOpenAIToResponsesSSE(context.Background(), rec, resp, "m", "m", "req-r1023-length", nil, nil)
	require.False(t, out.Interrupted)

	frames := r1023ParseSSE(t, rec.Body.String())
	sawDone := false
	for _, f := range frames {
		if f.event != "response.output_item.done" {
			continue
		}
		item, _ := f.data["item"].(map[string]any)
		if typ, _ := item["type"].(string); typ == "function_call" {
			sawDone = true
			st, _ := item["status"].(string)
			assert.Equal(t, "completed", st, "truncated function_call item status must stay the literal \"completed\"")
		}
	}
	assert.True(t, sawDone, "no function_call output_item.done seen — premise absent")
	// 终态 envelope 保留截断语义。
	assert.Contains(t, rec.Body.String(), `"status":"incomplete"`)
}
