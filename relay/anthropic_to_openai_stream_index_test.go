package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAnthropicToOpenAIStream_IndexStaysSameAcrossChunks verifies index consistency
func TestAnthropicToOpenAIStream_IndexStaysSameAcrossChunks(t *testing.T) {
	anthropicSSE := `event: message_start
data: {"type":"message_start","message":{"id":"msg_abc","type":"message","role":"assistant","content":[],"model":"test","usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_123","name":"bash","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"pwd\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_stop
data: {"type":"message_stop"}
`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(anthropicSSE))
	}))
	defer upstream.Close()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", upstream.URL, nil)
	resp, _ := http.DefaultClient.Do(req)

	StreamAnthropicSSEToOpenAI(w, resp, "test", "test", "test-idx", nil, nil)

	body := w.Body.String()
	var indices []int
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") || strings.Contains(line, "[DONE]") {
			continue
		}
		var chunk map[string]interface{}
		json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk)
		if choices, ok := chunk["choices"].([]interface{}); ok && len(choices) > 0 {
			choice := choices[0].(map[string]interface{})
			if delta, ok := choice["delta"].(map[string]interface{}); ok {
				if tc, ok := delta["tool_calls"]; ok {
					b, _ := json.Marshal(tc)
					var tcs []map[string]interface{}
					json.Unmarshal(b, &tcs)
					for _, t := range tcs {
						if idx, ok := t["index"].(float64); ok {
							indices = append(indices, int(idx))
						}
					}
				}
			}
		}
	}

	if len(indices) < 2 {
		t.Fatalf("Expected at least 2 chunks, got %d", len(indices))
	}
	for i, idx := range indices {
		if idx != 0 {
			t.Errorf("Chunk %d has index %d, expected 0", i, idx)
		}
	}
	t.Logf("✅ All %d chunks have index=0", len(indices))
}
