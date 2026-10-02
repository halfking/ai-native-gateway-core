package executors

// responses_stream_bridge.go — the streaming half of the chat→responses
// adapter (see responses_mode_bridge.go for the non-stream half and the
// incident that motivated both).
//
// The gateway could read chat SSE and could forward Responses SSE verbatim, but
// it had no Responses-SSE → chat-SSE reader, so a chat-shaped streaming client
// had no way to consume a Responses-only model. This file adds that reader.
//
// Rather than teach the chat stream handler about a second wire format, the
// conversion happens on the BODY: the upstream Responses stream is piped
// through this translator and the existing chat StreamChat reader consumes the
// result unchanged. That keeps all existing chat SSE handling (usage
// accumulation, [DONE], telemetry, interruption accounting) on its existing,
// well-tested path.
//
// Event shapes are transcribed from live vapEUR streams captured 2026-10-02
// (credential 126, gpt-5.3-codex), including the tool-call variant:
//
//	response.created / in_progress              → envelope, carries id+model
//	response.output_item.added (function_call)  → {item:{id,call_id,name}}
//	response.output_text.delta                  → {delta,item_id}
//	response.function_call_arguments.delta      → {delta,item_id}
//	response.completed                           → {response:{usage}}
//
// One relay quirk is load-bearing: vapEUR emits NO `event:` lines at all —
// every frame is a bare `data:` line whose JSON carries "type". OpenAI's
// canonical form uses `event: <type>` followed by `data: {...}`. Both are
// accepted; see parseResponsesSSEFrame.

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"
)

// responsesToChatTranslator turns Responses SSE text into chat SSE text.
type responsesToChatTranslator struct {
	model    string
	chatID   string
	created  int64
	roleSent bool

	toolIndex    map[string]int // Responses item_id → chat tool_calls index
	nextTool     int
	toolCallSeen bool
	done         bool
}

func newResponsesToChatTranslator(model string) *responsesToChatTranslator {
	return &responsesToChatTranslator{
		model:     model,
		chatID:    "chatcmpl-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		created:   time.Now().Unix(),
		toolIndex: map[string]int{},
	}
}

// chatChunk is the minimum shape a chat SSE frame needs.
type chatChunk struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
	// R32 (P2-B): terminal-frame usage. Upstream Responses streams deliver
	// usage unconditionally in response.completed; dropping it made every
	// bridged stream bill zero tokens. StreamChat accumulates usage from the
	// chunk.Usage field on the existing chat path.
	Usage *chatUsage `json:"usage,omitempty"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chatChoice struct {
	Index        int       `json:"index"`
	Delta        chatDelta `json:"delta"`
	FinishReason *string   `json:"finish_reason"`
}

type chatDelta struct {
	Role      string         `json:"role,omitempty"`
	Content   string         `json:"content,omitempty"`
	ToolCalls []chatToolCall `json:"tool_calls,omitempty"`
}

type chatToolCall struct {
	Index    int        `json:"index"`
	ID       string     `json:"id,omitempty"`
	Type     string     `json:"type,omitempty"`
	Function chatToolFn `json:"function"`
}

type chatToolFn struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

func (t *responsesToChatTranslator) frame(delta chatDelta, finish *string) []byte {
	return t.frameUsage(delta, finish, nil)
}

func (t *responsesToChatTranslator) frameUsage(delta chatDelta, finish *string, usage *chatUsage) []byte {
	c := chatChunk{
		ID: t.chatID, Object: "chat.completion.chunk",
		Created: t.created, Model: t.model,
		Choices: []chatChoice{{Index: 0, Delta: delta, FinishReason: finish}},
		Usage: usage,
	}
	b, err := json.Marshal(c)
	if err != nil {
		return nil
	}
	return append(append([]byte("data: "), b...), '\n', '\n')
}

func strPtr(s string) *string { return &s }

// handle converts one parsed Responses event into zero or more chat frames.
func (t *responsesToChatTranslator) handle(eventType string, payload []byte) []byte {
	if t.done {
		return nil
	}
	// Envelope events carry the id/model the client should see.
	if eventType == "response.created" || eventType == "response.in_progress" {
		var env struct {
			Response struct {
				ID        string `json:"id"`
				Model     string `json:"model"`
				CreatedAt int64  `json:"created_at"`
			} `json:"response"`
		}
		if json.Unmarshal(payload, &env) == nil {
			if env.Response.Model != "" {
				t.model = env.Response.Model
			}
			if env.Response.CreatedAt > 0 {
				t.created = env.Response.CreatedAt
			}
		}
		if !t.roleSent {
			t.roleSent = true
			return t.frame(chatDelta{Role: "assistant"}, nil)
		}
		return nil
	}

	switch eventType {
	case "response.output_text.delta":
		var d struct {
			Delta string `json:"delta"`
		}
		if json.Unmarshal(payload, &d) != nil || d.Delta == "" {
			return nil
		}
		if !t.roleSent {
			t.roleSent = true
			// First frame carries role and content together, the shape every
			// OpenAI SDK expects for the first chunk.
			return t.frame(chatDelta{Role: "assistant", Content: d.Delta}, nil)
		}
		return t.frame(chatDelta{Content: d.Delta}, nil)

	case "response.output_item.added":
		var d struct {
			Item struct {
				ID     string `json:"id"`
				Type   string `json:"type"`
				CallID string `json:"call_id"`
				Name   string `json:"name"`
			} `json:"item"`
		}
		if json.Unmarshal(payload, &d) != nil || d.Item.Type != "function_call" {
			return nil
		}
		idx := t.nextTool
		t.nextTool++
		if d.Item.ID != "" {
			t.toolIndex[d.Item.ID] = idx
		}
		t.toolCallSeen = true
		if !t.roleSent {
			t.roleSent = true
		}
		return t.frame(chatDelta{ToolCalls: []chatToolCall{{
			Index: idx,
			ID:    d.Item.CallID,
			Type:  "function",
			Function: chatToolFn{
				Name: d.Item.Name,
			},
		}}}, nil)

	case "response.function_call_arguments.delta":
		var d struct {
			Delta  string `json:"delta"`
			ItemID string `json:"item_id"`
		}
		if json.Unmarshal(payload, &d) != nil || d.Delta == "" {
			return nil
		}
		idx, ok := t.toolIndex[d.ItemID]
		if !ok {
			// Arguments arrived before the item announcement; allocate so the
			// fragment is not dropped.
			idx = t.nextTool
			t.nextTool++
			t.toolIndex[d.ItemID] = idx
			t.toolCallSeen = true
		}
		return t.frame(chatDelta{ToolCalls: []chatToolCall{{
			Index:    idx,
			Function: chatToolFn{Arguments: d.Delta},
		}}}, nil)

	case "response.completed", "response.incomplete", "response.failed":
		var env struct {
			Response struct {
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
					TotalTokens  int `json:"total_tokens"`
				} `json:"usage"`
			} `json:"response"`
		}
		// R32 (P2-B): usage used to be parsed and discarded (`_ =`), so every
		// bridged stream recorded zero tokens. Re-emit it in chat shape on the
		// terminal frame; a malformed payload degrades to a zero-valued usage
		// rather than killing the stream.
		_ = json.Unmarshal(payload, &env)
		usage := &chatUsage{
			PromptTokens:     env.Response.Usage.InputTokens,
			CompletionTokens: env.Response.Usage.OutputTokens,
			TotalTokens:      env.Response.Usage.TotalTokens,
		}
		reason := "stop"
		if t.toolCallSeen {
			reason = "tool_calls"
		}
		out := t.frameUsage(chatDelta{}, strPtr(reason), usage)
		t.done = true
		return append(out, []byte("data: [DONE]\n\n")...)

	case "error":
		// Surface the upstream failure as an error frame rather than closing
		// silently: a truncated stream that looks like a clean stop is the
		// worst possible outcome for a streaming client.
		t.done = true
		return append(t.frame(chatDelta{}, strPtr("stop")),
			[]byte("data: [DONE]\n\n")...)
	}
	// response.output_text.done / content_part.* / output_item.done /
	// function_call_arguments.done carry no information the deltas did not
	// already deliver.
	return nil
}

// responsesSSEFrame is one parsed SSE event.
type responsesSSEFrame struct {
	EventType string
	Data      []byte
}

// parseResponsesSSEFrame accepts BOTH SSE dialects observed in the wild:
//
//	canonical OpenAI : "event: response.output_text.delta\ndata: {...}"
//	relay (vapeur)   : "data: {...}" with "type" inside the JSON
//
// A frame with no event name is resolved from the payload's "type" field. This
// is the single reason the streaming bridge works against vapEUR at all — a
// parser that required the `event:` line would silently swallow every frame.
func parseResponsesSSEFrame(eventName string, data []byte) responsesSSEFrame {
	f := responsesSSEFrame{EventType: strings.TrimSpace(eventName), Data: data}
	if f.EventType == "" {
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &probe) == nil {
			f.EventType = probe.Type
		}
	}
	return f
}

// responsesToChatStream converts an upstream Responses SSE body into chat SSE.
//
// Implemented as a pipe so the existing chat StreamChat reader can consume the
// result without knowing the conversion happened. The returned ReadCloser must
// be Closed by the caller, which unblocks the goroutine.
func responsesToChatStream(ctx context.Context, upstream io.ReadCloser, model string) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		defer func() { _ = upstream.Close() }()
		defer func() { _ = pw.Close() }()

		t := newResponsesToChatTranslator(model)
		sc := bufio.NewScanner(upstream)
		// Individual Responses frames are small; the envelope carries the full
		// tool schema and can exceed bufio's 64 KiB default on tool-heavy
		// requests (measured: response.created with 6 tools ≈ 1.2 KiB, but a
		// large toolset scales it).
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

		var (
			eventName string
			dataBuf   []byte
		)
		flush := func() {
			if len(dataBuf) == 0 && eventName == "" {
				return
			}
			frame := parseResponsesSSEFrame(eventName, dataBuf)
			if out := t.handle(frame.EventType, frame.Data); len(out) > 0 {
				if _, err := pw.Write(out); err != nil {
					return
				}
			}
			eventName = ""
			dataBuf = nil
		}

		for sc.Scan() {
			line := strings.TrimRight(sc.Text(), "\r")
			switch {
			case line == "":
				flush()
			case strings.HasPrefix(line, ":"):
				// SSE comment / keep-alive.
			case strings.HasPrefix(line, "event:"):
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if payload == "[DONE]" {
					// Upstream signalled end of stream.
					if out := t.handle("response.completed", nil); len(out) > 0 {
						_, _ = pw.Write(out)
					}
					t.done = true
					flush()
					return
				}
				if len(dataBuf) > 0 {
					dataBuf = append(dataBuf, '\n')
				}
				dataBuf = append(dataBuf, payload...)
			}
		}
		flush()
		if !t.done {
			// Truncated upstream: still close the chat stream properly so the
			// client is not left waiting on an unterminated body.
			if out := t.handle("response.completed", nil); len(out) > 0 {
				_, _ = pw.Write(out)
			}
		}
	}()
	return pr
}
