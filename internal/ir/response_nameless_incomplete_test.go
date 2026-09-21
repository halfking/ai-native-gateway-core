package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// ─── audit #11a: all-nameless tool_calls turns degrade, never breach ────────

func TestSerializeOpenAIResponse_NamelessOnlyToolCallsDegradeFinishReason(t *testing.T) {
	irResp := &InternalResponse{
		Role:         "assistant",
		FinishReason: "tool_calls",
		ToolCalls: []ResponseToolCall{
			{ID: "call_1", Name: "", Arguments: `{"a":1}`},
		},
	}
	body, err := SerializeOpenAIResponse(irResp, "client-model")
	if err != nil {
		t.Fatalf("SerializeOpenAIResponse: %v", err)
	}
	var out struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				ToolCalls []map[string]any `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Choices) != 1 {
		t.Fatalf("choices = %d", len(out.Choices))
	}
	if got := out.Choices[0].FinishReason; got != "stop" {
		t.Fatalf("finish_reason = %q, want degraded %q", got, "stop")
	}
	if len(out.Choices[0].Message.ToolCalls) != 0 {
		t.Fatalf("nameless call leaked to wire: %v", out.Choices[0].Message.ToolCalls)
	}
}

func TestSerializeOpenAIResponse_NamedToolCallKeepsToolCallsFinish(t *testing.T) {
	irResp := &InternalResponse{
		Role:         "assistant",
		FinishReason: "tool_calls",
		ToolCalls: []ResponseToolCall{
			{ID: "call_1", Name: "", Arguments: `{}`},            // dropped
			{ID: "call_2", Name: "get_weather", Arguments: `{}`}, // emitted
		},
	}
	body, err := SerializeOpenAIResponse(irResp, "client-model")
	if err != nil {
		t.Fatalf("SerializeOpenAIResponse: %v", err)
	}
	if !strings.Contains(string(body), `"finish_reason":"tool_calls"`) {
		t.Fatalf("mixed nameless+named must keep tool_calls finish: %s", body)
	}
}

func TestSerializeAnthropicResponse_NamelessOnlyToolCallsDegradeStopReason(t *testing.T) {
	irResp := &InternalResponse{
		Role:         "assistant",
		FinishReason: "tool_calls",
		ToolCalls: []ResponseToolCall{
			{ID: "call_1", Name: "", Arguments: `{}`},
		},
	}
	body, err := SerializeAnthropicResponse(irResp, "client-model")
	if err != nil {
		t.Fatalf("SerializeAnthropicResponse: %v", err)
	}
	if !strings.Contains(string(body), `"stop_reason":"end_turn"`) {
		t.Fatalf("stop_reason want end_turn (degraded), body: %s", body)
	}
	if strings.Contains(string(body), `"stop_reason":"tool_use"`) {
		t.Fatalf("all-nameless tool_calls turn must not surface stop_reason=tool_use: %s", body)
	}
}

func TestSerializeAnthropicResponse_NamedToolCallKeepsToolUseStop(t *testing.T) {
	irResp := &InternalResponse{
		Role:         "assistant",
		FinishReason: "tool_calls",
		ToolCalls: []ResponseToolCall{
			{ID: "call_1", Name: "get_weather", Arguments: `{}`},
		},
	}
	body, err := SerializeAnthropicResponse(irResp, "client-model")
	if err != nil {
		t.Fatalf("SerializeAnthropicResponse: %v", err)
	}
	if !strings.Contains(string(body), `"stop_reason":"tool_use"`) {
		t.Fatalf("named tool call must keep stop_reason=tool_use: %s", body)
	}
}

func TestSerializeResponsesResponse_NamelessOnlyToolCallsDegradeStatus(t *testing.T) {
	irResp := &InternalResponse{
		Role:         "assistant",
		FinishReason: "tool_calls",
		ToolCalls: []ResponseToolCall{
			{ID: "call_1", Name: "", Arguments: `{}`},
		},
	}
	body, err := SerializeResponsesResponse(irResp, "client-model")
	if err != nil {
		t.Fatalf("SerializeResponsesResponse: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["status"] != "incomplete" {
		t.Fatalf("status = %v, want incomplete (no false success for a fully-suppressed tool turn)", out["status"])
	}
	output, _ := out["output"].([]any)
	if len(output) != 0 {
		t.Fatalf("output = %v, want empty", output)
	}
	// finish reason "tool_calls" carries no explicit incomplete cause → the
	// incomplete_details field is omitted entirely.
	if _, ok := out["incomplete_details"]; ok {
		t.Fatalf("incomplete_details must be omitted without an explicit reason: %s", body)
	}
}

// ─── audit #11b: incomplete_details.reason alignment ─────────────────────────

func TestSerializeResponsesResponse_IncompleteDetailsReason(t *testing.T) {
	cases := []struct {
		finishReason string
		wantStatus   string
		wantReason   string // "" → field omitted
	}{
		{finishReason: "length", wantStatus: "incomplete", wantReason: "max_output_tokens"},
		{finishReason: "max_tokens", wantStatus: "incomplete", wantReason: "max_output_tokens"},
		{finishReason: "content_filter", wantStatus: "incomplete", wantReason: "content_filter"},
		{finishReason: "stop", wantStatus: "completed", wantReason: ""},
		{finishReason: "tool_calls", wantStatus: "completed", wantReason: ""},
	}
	for _, tc := range cases {
		t.Run(tc.finishReason, func(t *testing.T) {
			irResp := &InternalResponse{
				Role:         "assistant",
				FinishReason: tc.finishReason,
				Content:      []ResponseContentBlock{{Type: "text", Text: "partial"}},
			}
			body, err := SerializeResponsesResponse(irResp, "client-model")
			if err != nil {
				t.Fatalf("SerializeResponsesResponse: %v", err)
			}
			var out map[string]any
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if out["status"] != tc.wantStatus {
				t.Fatalf("status = %v, want %v", out["status"], tc.wantStatus)
			}
			details, ok := out["incomplete_details"]
			if tc.wantReason == "" {
				if ok {
					t.Fatalf("incomplete_details must be omitted for %q: %v", tc.finishReason, details)
				}
				return
			}
			detailsMap, ok := details.(map[string]any)
			if !ok {
				t.Fatalf("incomplete_details missing for %q: %s", tc.finishReason, body)
			}
			if detailsMap["reason"] != tc.wantReason {
				t.Fatalf("reason = %v, want %q", detailsMap["reason"], tc.wantReason)
			}
		})
	}
}

// Serialize→Parse must round-trip: incomplete_details.reason vocabulary maps
// back onto the same finish reason (parse-side mapResponsesStatus).
func TestIncompleteReasonRoundTripWithParser(t *testing.T) {
	cases := map[string]string{
		"length":         "length",
		"content_filter": "content_filter",
	}
	for finish, want := range cases {
		body, err := SerializeResponsesResponse(&InternalResponse{
			Role:         "assistant",
			FinishReason: finish,
			Content:      []ResponseContentBlock{{Type: "text", Text: "x"}},
		}, "m")
		if err != nil {
			t.Fatalf("serialize %q: %v", finish, err)
		}
		parsed, err := ParseResponsesResponse(body)
		if err != nil {
			t.Fatalf("parse %q: %v", finish, err)
		}
		if parsed.FinishReason != want {
			t.Fatalf("round-trip finish for %q = %q, want %q", finish, parsed.FinishReason, want)
		}
	}
}
