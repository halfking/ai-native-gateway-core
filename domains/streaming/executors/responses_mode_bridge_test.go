package executors

// responses_mode_bridge_test.go — unit gates for the chat→responses half of the
// protocol adapter.
//
// Fixtures are transcribed from the live vapEUR upstream (2026-10-02,
// credential 126 hxt-local), not invented, so the converter is pinned to the
// wire shapes the relay actually emits:
//
//   - gpt-5.3-codex /v1/chat/completions → 400 {"code":4006,"message":"The
//     requested operation is unsupported."}
//   - claude-sonnet-5 /v1/responses     → 400 {"error":{"message":"该供应商不支持
//     Responses API","code":"unsupported_operation"}}
//   - gpt-5.3-codex /v1/responses       → 200 (message item + usage)

import (
	"encoding/json"
	"strings"
	"testing"
)

const upstreamChatUnsupportedBody = `{"code":4006,"message":"The requested operation is unsupported.","data":null}`

const upstreamResponsesUnsupportedBody = `{"error":{"message":"该供应商不支持 Responses API","type":"invalid_request_error","code":"unsupported_operation"}}`

// transcribed from a live gpt-5.3-codex non-stream /v1/responses 200, trimmed to
// the fields the converter consumes.
const upstreamCodexResponsesBody = `{
  "id": "resp_0c7d53a46ec052b6006abf2cebb26c8193b9191a8c45d79b35",
  "object": "response",
  "created_at": 1790913771,
  "status": "completed",
  "model": "gpt-5.3-codex",
  "output": [
    {
      "id": "msg_0c7d53a46ec052b6006abf2cec55fc819396019c39134b0cef",
      "type": "message",
      "status": "completed",
      "content": [{"type": "output_text", "annotations": [], "logprobs": [], "text": "Hi!"}],
      "phase": "final_answer",
      "role": "assistant"
    }
  ],
  "usage": {
    "input_tokens": 8,
    "input_tokens_details": {"cache_write_tokens": 0, "cached_tokens": 0},
    "output_tokens": 8,
    "output_tokens_details": {"reasoning_tokens": 0},
    "total_tokens": 16
  },
  "incomplete_details": null
}`

func TestChatRequiresResponses(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
		why    string
	}{
		{
			name: "vapeur code 4006 generic verdict", status: 400,
			body: upstreamChatUnsupportedBody, want: true,
			why: "the real gpt-5.3-codex chat rejection; re-probing cannot heal it",
		},
		{
			name: "explicit english", status: 400,
			body: `{"error":{"message":"Please use /v1/responses instead"}}`, want: true,
		},
		{
			name: "explicit CJK", status: 400,
			body: `{"error":{"message":"该模型仅支持 Responses API"}}`, want: true,
		},
		{
			name: "param shaped rejection", status: 400,
			body: `{"error":{"message":"Unsupported parameter: 'messages' on responses api"}}`, want: false,
			why: "a request-shape bug; switching wire format would mask it",
		},
		{
			name: "param shaped CJK", status: 400,
			body: `{"error":{"message":"不支持该参数 messages"}}`, want: false,
		},
		{
			name: "auth failure", status: 401,
			body: `{"error":{"message":"The requested operation is unsupported."}}`, want: false,
			why: "401 is outside the status set — a bad key is not a protocol gap",
		},
		{
			name: "permission denied", status: 403,
			body: `{"error":{"message":"No permission to access model: grok-4.7"}}`, want: false,
		},
		{
			name: "rate limited", status: 429,
			body: `{"error":{"message":"The requested operation is unsupported."}}`, want: false,
		},
		{
			name: "empty body", status: 400, body: "", want: false,
			why: "no evidence → never spend a doomed retry",
		},
		{
			name: "unrelated 400", status: 400,
			body: `{"error":{"message":"context length exceeded"}}`, want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := chatRequiresResponses(c.status, c.body); got != c.want {
				t.Fatalf("chatRequiresResponses(%d, %q) = %v, want %v (%s)",
					c.status, c.body, got, c.want, c.why)
			}
		})
	}
}

// The two directions must disagree on vapEUR's two real verdicts, otherwise a
// provider that legitimately supports one leg would be downgraded into the other.
func TestBridgeDirectionsAreNotSymmetric(t *testing.T) {
	if !chatRequiresResponses(400, upstreamChatUnsupportedBody) {
		t.Fatal("chat leg: vapEUR code 4006 must be recognised as responses-only")
	}
	if chatRequiresResponses(400, upstreamResponsesUnsupportedBody) {
		t.Fatal("chat leg: the responses-unsupported verdict must NOT trigger a chat→responses retry")
	}
	if !responsesCapabilityUnsupportedBody(400, upstreamResponsesUnsupportedBody) {
		t.Fatal("responses leg: the 该供应商不支持 Responses API verdict must be recognised")
	}
	if responsesCapabilityUnsupportedBody(400, upstreamChatUnsupportedBody) {
		t.Fatal("responses leg: the chat 4006 verdict is not a responses-API verdict")
	}
}

func TestChatBodyToResponsesBody(t *testing.T) {
	t.Run("plain message with model override", func(t *testing.T) {
		chat := []byte(`{"model":"client-alias","messages":[{"role":"user","content":"say hi"}],"max_tokens":16}`)
		got, ok := chatBodyToResponsesBody(chat, "gpt-5.3-codex")
		if !ok {
			t.Fatal("conversion returned ok=false for a plain chat body")
		}
		var m map[string]any
		if err := json.Unmarshal(got, &m); err != nil {
			t.Fatalf("converted body is not JSON: %v (%s)", err, got)
		}
		if m["model"] != "gpt-5.3-codex" {
			t.Fatalf("model = %v, want the candidate raw model gpt-5.3-codex", m["model"])
		}
		if _, hasInput := m["input"]; !hasInput {
			t.Fatalf("converted body has no input[]: %s", got)
		}
		if _, hasMessages := m["messages"]; hasMessages {
			t.Fatalf("converted body still carries chat messages[]: %s", got)
		}
		if _, hasMaxTokens := m["max_tokens"]; hasMaxTokens {
			t.Fatalf("converted body still carries chat max_tokens: %s", got)
		}
	})

	t.Run("tools are flattened", func(t *testing.T) {
		chat := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],
			"tools":[{"type":"function","function":{"name":"get_time","description":"t","parameters":{"type":"object","properties":{}}}}]}`)
		got, ok := chatBodyToResponsesBody(chat, "gpt-5.3-codex")
		if !ok {
			t.Fatal("conversion failed for a body with tools")
		}
		if !strings.Contains(string(got), `"get_time"`) {
			t.Fatalf("converted body lost the tool name: %s", got)
		}
	})

	t.Run("empty body", func(t *testing.T) {
		if _, ok := chatBodyToResponsesBody(nil, "m"); ok {
			t.Fatal("empty body must not convert")
		}
	})

	t.Run("malformed body", func(t *testing.T) {
		if _, ok := chatBodyToResponsesBody([]byte("{not json"), "m"); ok {
			t.Fatal("malformed body must not convert (caller keeps the original failure)")
		}
	})
}

func TestResponsesBodyToChatBody(t *testing.T) {
	got, ok := responsesBodyToChatBody([]byte(upstreamCodexResponsesBody), "gpt-5.3-codex")
	if !ok {
		t.Fatal("conversion returned ok=false for a real upstream Responses body")
	}
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("converted body is not JSON: %v (%s)", err, got)
	}
	if m["object"] != "chat.completion" {
		t.Fatalf("object = %v, want chat.completion", m["object"])
	}
	if m["model"] != "gpt-5.3-codex" {
		t.Fatalf("model = %v, want the client model echoed back", m["model"])
	}
	choices, _ := m["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices = %v, want exactly 1", m["choices"])
	}
	ch, _ := choices[0].(map[string]any)
	msg, _ := ch["message"].(map[string]any)
	if msg == nil {
		t.Fatalf("choices[0].message missing: %s", got)
	}
	if msg["role"] != "assistant" {
		t.Fatalf("message.role = %v, want assistant", msg["role"])
	}
	if txt, _ := msg["content"].(string); !strings.Contains(txt, "Hi") {
		t.Fatalf("message.content = %q, want the upstream text", txt)
	}
	if _, leaked := m["output"]; leaked {
		t.Fatalf("converted chat body still carries responses output[]: %s", got)
	}
	usage, _ := m["usage"].(map[string]any)
	if usage == nil {
		t.Fatalf("converted chat body lost usage: %s", got)
	}
}

func TestResponsesBodyToChatBodyRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"", "   ", "{not json", `{"unrelated":true}`} {
		if _, ok := responsesBodyToChatBody([]byte(bad), "m"); ok {
			t.Fatalf("body %q must not convert (caller must surface the upstream body unchanged)", bad)
		}
	}
}

// An upstream error envelope that arrived on a 2xx must never be laundered into
// an empty chat completion: the client would see a successful but content-less
// answer where the upstream had actually failed.
func TestResponsesBodyToChatBodyNeverLaundersErrors(t *testing.T) {
	errEnvelope := `{"object":"response","id":"resp_1","status":"failed",
		"error":{"code":"server_error","message":"upstream exploded"},
		"output":[]}`
	if _, ok := responsesBodyToChatBody([]byte(errEnvelope), "m"); ok {
		t.Fatal("a failed Responses envelope must not convert into a chat completion")
	}

	// A structurally valid Responses object carrying no output item is equally
	// unsafe — it would render as `choices` with empty content.
	emptyOutput := `{"object":"response","id":"resp_2","status":"completed","output":[]}`
	if _, ok := responsesBodyToChatBody([]byte(emptyOutput), "m"); ok {
		t.Fatal("an empty-output Responses body must not convert (it would answer the client with nothing)")
	}

	// object != "response" even with output present — not a Responses body.
	wrongObject := `{"object":"chat.completion","choices":[{"message":{"role":"assistant","content":"x"}}]}`
	if _, ok := responsesBodyToChatBody([]byte(wrongObject), "m"); ok {
		t.Fatal("a chat.completion body must not be fed through the responses converter")
	}
}

// Round-trip guard: chat → responses → chat must preserve the user text, which
// is the property the whole bridge exists to provide for a chat-shaped client.
func TestChatResponsesChatRoundTrip(t *testing.T) {
	chat := []byte(`{"model":"gpt-5.3-codex","messages":[{"role":"user","content":"say hi"}],"max_tokens":32}`)
	responsesBody, ok := chatBodyToResponsesBody(chat, "gpt-5.3-codex")
	if !ok {
		t.Fatal("request conversion failed")
	}
	// Feed the recorded upstream response back through the reverse conversion.
	back, ok := responsesBodyToChatBody([]byte(upstreamCodexResponsesBody), "gpt-5.3-codex")
	if !ok {
		t.Fatal("response conversion failed")
	}
	if !strings.Contains(string(responsesBody), "say hi") {
		t.Fatalf("responses request lost the user text: %s", responsesBody)
	}
	if !strings.Contains(string(back), "Hi") {
		t.Fatalf("chat response lost the assistant text: %s", back)
	}
}
