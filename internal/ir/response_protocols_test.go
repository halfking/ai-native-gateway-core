package ir

import "testing"

func TestParseGeminiResponse_PreservesTextThinkingAndToolCall(t *testing.T) {
	response, err := ParseGeminiResponse([]byte(`{
		"modelVersion":"gemini-2.5-pro",
		"candidates":[{"content":{"role":"model","parts":[
			{"thought":"considering options"},
			{"text":"I'll check that."},
			{"functionCall":{"name":"get_weather","args":{"city":"Beijing"}}}
		]},"finishReason":"STOP"}],
		"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}
	}`))
	if err != nil {
		t.Fatalf("ParseGeminiResponse: %v", err)
	}
	if response.SourceProtocol != ProtocolGeminiGenerate || response.Role != "assistant" {
		t.Fatalf("protocol/role = %q/%q", response.SourceProtocol, response.Role)
	}
	if response.ReasoningContent != "considering options" {
		t.Errorf("ReasoningContent = %q", response.ReasoningContent)
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].Name != "get_weather" {
		t.Errorf("ToolCalls = %+v", response.ToolCalls)
	}
	if string(response.ToolCalls[0].InputRaw) != `{"city":"Beijing"}` {
		t.Errorf("tool args = %s", response.ToolCalls[0].InputRaw)
	}
	if response.Usage.TotalTokens != 15 || response.FinishReason != "stop" {
		t.Errorf("usage/finish = %+v/%q", response.Usage, response.FinishReason)
	}
}

func TestParseGeminiResponse_RejectsMalformedFunctionCall(t *testing.T) {
	_, err := ParseGeminiResponse([]byte(`{
		"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"args":{}}}]}}]
	}`))
	if err == nil {
		t.Fatal("ParseGeminiResponse accepted a function call without a name")
	}
}

func TestParseGeminiResponse_UsesDistinctIDsForRepeatedFunctionNames(t *testing.T) {
	response, err := ParseGeminiResponse([]byte(`{
		"candidates":[{"content":{"role":"model","parts":[
			{"functionCall":{"name":"get_weather","args":{"city":"Beijing"}}},
			{"functionCall":{"name":"get_weather","args":{"city":"Shanghai"}}}
		]}}]
	}`))
	if err != nil {
		t.Fatalf("ParseGeminiResponse: %v", err)
	}
	if len(response.ToolCalls) != 2 {
		t.Fatalf("ToolCalls = %+v", response.ToolCalls)
	}
	if response.ToolCalls[0].ID == response.ToolCalls[1].ID {
		t.Fatalf("repeated function calls share ID %q", response.ToolCalls[0].ID)
	}
}

func TestParseResponsesResponse_PreservesMessageReasoningAndToolCall(t *testing.T) {
	response, err := ParseResponsesResponse([]byte(`{
		"id":"resp_123","created_at":123,"model":"gpt-5",
		"status":"completed",
		"output":[
			{"type":"reasoning","summary":[{"type":"summary_text","text":"considering options"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"I'll check that."}]},
			{"type":"function_call","id":"fc_123","call_id":"call_123","name":"get_weather","arguments":"{\"city\":\"Beijing\"}"}
		],
		"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}
	}`))
	if err != nil {
		t.Fatalf("ParseResponsesResponse: %v", err)
	}
	if response.SourceProtocol != ProtocolOpenAIResponses || response.ID != "resp_123" {
		t.Fatalf("protocol/id = %q/%q", response.SourceProtocol, response.ID)
	}
	if response.ReasoningContent != "considering options" || response.Role != "assistant" {
		t.Errorf("reasoning/role = %q/%q", response.ReasoningContent, response.Role)
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].ID != "call_123" {
		t.Errorf("ToolCalls = %+v", response.ToolCalls)
	}
	if response.Usage.TotalTokens != 15 || response.FinishReason != "stop" {
		t.Errorf("usage/finish = %+v/%q", response.Usage, response.FinishReason)
	}
}

func TestParseResponsesResponse_RejectsIncompleteFunctionCall(t *testing.T) {
	_, err := ParseResponsesResponse([]byte(`{
		"output":[{"type":"function_call","name":"get_weather"}]
	}`))
	if err == nil {
		t.Fatal("ParseResponsesResponse accepted a function call without call_id")
	}
}

func TestParseResponsesResponse_MapsIncompleteReason(t *testing.T) {
	response, err := ParseResponsesResponse([]byte(`{
		"status":"incomplete",
		"incomplete_details":{"reason":"content_filter"}
	}`))
	if err != nil {
		t.Fatalf("ParseResponsesResponse: %v", err)
	}
	if response.FinishReason != "content_filter" {
		t.Errorf("FinishReason = %q, want content_filter", response.FinishReason)
	}
}

func TestParseResponsesResponse_RejectsFailedStatus(t *testing.T) {
	_, err := ParseResponsesResponse([]byte(`{"status":"failed"}`))
	if err == nil {
		t.Fatal("ParseResponsesResponse accepted a failed response")
	}
}
