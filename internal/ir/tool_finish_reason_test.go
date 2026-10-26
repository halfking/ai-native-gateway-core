package ir

import (
	"encoding/json"
	"testing"
)

func TestToolFinishReasonSerializers(t *testing.T) {
	for _, reason := range []string{"", "stop", "length", "content_filter", "network_error"} {
		for _, name := range []string{"shell", ""} {
			t.Run(reason+"/"+name, func(t *testing.T) {
				resp := &InternalResponse{Role: "assistant", FinishReason: reason, ToolCalls: []ResponseToolCall{{ID: "call_1", Name: name, Arguments: `{}`}}}
				want := reason
				if want == "" {
					want = "stop"
				}
				if name != "" && (reason == "" || reason == "stop") {
					want = "tool_calls"
				}
				body, err := SerializeOpenAIResponse(resp, "test")
				if err != nil {
					t.Fatal(err)
				}
				var chat struct {
					Choices []struct {
						Finish string `json:"finish_reason"`
					} `json:"choices"`
				}
				if err := json.Unmarshal(body, &chat); err != nil {
					t.Fatal(err)
				}
				if chat.Choices[0].Finish != want {
					t.Errorf("Chat finish = %q, want %q", chat.Choices[0].Finish, want)
				}
				body, err = SerializeAnthropicResponse(resp, "test")
				if err != nil {
					t.Fatal(err)
				}
				var ant struct {
					Stop string `json:"stop_reason"`
				}
				if err := json.Unmarshal(body, &ant); err != nil {
					t.Fatal(err)
				}
				if ant.Stop != mapFinishReasonToAnthropic(want) {
					t.Errorf("Anthropic stop = %q, want %q", ant.Stop, mapFinishReasonToAnthropic(want))
				}
			})
		}
	}
}
