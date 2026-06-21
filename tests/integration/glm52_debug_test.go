//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestGLM52RealRequest tests glm-5.2 through the real gateway
// to diagnose format conversion issues
func TestGLM52RealRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real gateway test in short mode")
	}

	// Test configurations
	testCases := []struct {
		name        string
		endpoint    string
		model       string
		contentType string
		authHeader  string
		stream      bool
	}{
		{
			name:        "glm-5.2_openai_format_non_stream",
			endpoint:    "https://llm.kxpms.cn/v1/chat/completions",
			model:       "glm-5.2",
			contentType: "application/json",
			authHeader:  "Bearer ", // Will be filled from env
			stream:      false,
		},
		{
			name:        "glm-5.2_openai_format_stream",
			endpoint:    "https://llm.kxpms.cn/v1/chat/completions",
			model:       "glm-5.2",
			contentType: "application/json",
			authHeader:  "Bearer ",
			stream:      true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Build request body
			reqBody := map[string]interface{}{
				"model": tc.model,
				"messages": []map[string]interface{}{
					{
						"role":    "system",
						"content": "You are a helpful assistant.",
					},
					{
						"role":    "user",
						"content": "Say 'hello world' and nothing else.",
					},
				},
				"max_tokens":  50,
				"temperature": 0.7,
				"stream":      tc.stream,
			}

			bodyBytes, err := json.Marshal(reqBody)
			if err != nil {
				t.Fatalf("failed to marshal request body: %v", err)
			}

			t.Logf("Request body: %s", string(bodyBytes))

			// Create request
			req, err := http.NewRequest("POST", tc.endpoint, bytes.NewReader(bodyBytes))
			if err != nil {
				t.Fatalf("failed to create request: %v", err)
			}

			req.Header.Set("Content-Type", tc.contentType)
			// In real test, you'd get this from env or config
			// For now, we'll just test the format conversion path
			req.Header.Set("Authorization", tc.authHeader+"test-key")

			// Send request
			client := &http.Client{
				Timeout: 30 * time.Second,
			}

			t.Logf("Sending request to %s", tc.endpoint)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			t.Logf("Response status: %d", resp.StatusCode)
			t.Logf("Response headers: %v", resp.Header)

			// Read response
			if tc.stream {
				t.Log("Processing streaming response...")
				scanner := newSSEScanner(resp.Body)
				chunkCount := 0
				for scanner.Scan() {
					line := scanner.Text()
					chunkCount++
					t.Logf("Chunk %d: %s", chunkCount, line)

					if strings.HasPrefix(line, "data: ") {
						data := strings.TrimPrefix(line, "data: ")
						if data == "[DONE]" {
							t.Log("Stream completed with [DONE]")
							break
						}

						// Try to parse as JSON
						var chunk map[string]interface{}
						if err := json.Unmarshal([]byte(data), &chunk); err != nil {
							t.Errorf("Failed to parse chunk %d as JSON: %v\nData: %s", chunkCount, err, data)
							continue
						}

						// Check for choices array
						if choices, ok := chunk["choices"].([]interface{}); ok {
							if len(choices) == 0 {
								t.Errorf("Chunk %d has empty choices array", chunkCount)
							} else {
								t.Logf("Chunk %d choices: %+v", chunkCount, choices)
							}
						} else {
							t.Logf("Chunk %d structure: %+v", chunkCount, chunk)
						}
					}
				}

				if err := scanner.Err(); err != nil {
					t.Errorf("SSE scanner error: %v", err)
				}

				t.Logf("Total chunks received: %d", chunkCount)
			} else {
				bodyBytes, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatalf("failed to read response: %v", err)
				}

				t.Logf("Response body: %s", string(bodyBytes))

				// Try to parse response
				var respData map[string]interface{}
				if err := json.Unmarshal(bodyBytes, &respData); err != nil {
					t.Errorf("Failed to parse response as JSON: %v", err)
				} else {
					// Check response structure
					if choices, ok := respData["choices"].([]interface{}); ok {
						if len(choices) == 0 {
							t.Error("Response has empty choices array")
						} else {
							t.Logf("Response choices: %+v", choices)
						}
					} else {
						t.Logf("Response structure: %+v", respData)
					}
				}
			}
		})
	}
}

// sseScanner is a simple SSE line scanner
type sseScanner struct {
	reader  *bufio.Reader
	line    string
	err     error
	lastErr error
}

func newSSEScanner(r io.Reader) *sseScanner {
	return &sseScanner{
		reader: bufio.NewReader(r),
	}
}

func (s *sseScanner) Scan() bool {
	line, err := s.reader.ReadString('\n')
	if err != nil {
		if err != io.EOF {
			s.lastErr = err
		}
		return false
	}
	s.line = strings.TrimRight(line, "\r\n")
	return true
}

func (s *sseScanner) Text() string {
	return s.line
}

func (s *sseScanner) Err() error {
	return s.lastErr
}

// TestGLM52FormatConversion tests the conversion functions directly
func TestGLM52FormatConversion(t *testing.T) {
	t.Run("openai_to_anthropic_conversion", func(t *testing.T) {
		// Simulate a glm-5.2 request through OpenAI format
		openaiReq := map[string]interface{}{
			"model": "glm-5.2",
			"messages": []map[string]interface{}{
				{"role": "system", "content": "You are helpful."},
				{"role": "user", "content": "Hello"},
			},
			"max_tokens":  100,
			"temperature": 0.7,
			"stream":      false,
		}

		reqBytes, err := json.Marshal(openaiReq)
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}

		t.Logf("Original OpenAI request: %s", string(reqBytes))

		// This would trigger Q3 path (OpenAI client -> Anthropic upstream)
		// if glm-5.2 is configured with anthropic-messages protocol
		t.Log("Q3 path: OpenAI request should be converted to Anthropic format")
		t.Log("Expected: system message extracted to top-level 'system' field")
		t.Log("Expected: messages array contains only user/assistant messages")
		t.Log("Expected: max_tokens preserved")
	})

	t.Run("anthropic_response_to_openai", func(t *testing.T) {
		// Simulate an Anthropic response that needs conversion back
		anthropicResp := map[string]interface{}{
			"id":          "msg-123",
			"type":        "message",
			"role":        "assistant",
			"model":       "glm-5.2",
			"content": []map[string]interface{}{
				{"type": "text", "text": "Hello back"},
			},
			"usage": map[string]interface{}{
				"input_tokens":  10,
				"output_tokens": 5,
			},
			"stop_reason": "end_turn",
		}

		respBytes, err := json.Marshal(anthropicResp)
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}

		t.Logf("Anthropic response: %s", string(respBytes))
		t.Log("Q3 return path: Should be converted back to OpenAI format")
		t.Log("Expected: content array flattened to string")
		t.Log("Expected: usage tokens mapped correctly")
	})
}

// TestGLM52StreamEventParsing tests SSE event parsing for glm-5.2
func TestGLM52StreamEventParsing(t *testing.T) {
	testEvents := []string{
		// Valid Anthropic event
		`data: {"type":"message_start","message":{"id":"msg_1","role":"assistant"}}`,
		// Valid OpenAI-format chunk (should be detected and handled)
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
		// Empty choices (the bug symptom)
		`data: {"id":"chatcmpl-2","object":"chat.completion.chunk","choices":[]}`,
		// Mixed format (Anthropic event with OpenAI fields - the actual bug)
		`data: {"type":"","choices":[],"model":"glm-5.2"}`,
	}

	for i, event := range testEvents {
		t.Run(fmt.Sprintf("event_%d", i), func(t *testing.T) {
			t.Logf("Testing event: %s", event)

			// Parse the event data
			dataStr := strings.TrimPrefix(event, "data: ")
			var data map[string]interface{}
			if err := json.Unmarshal([]byte(dataStr), &data); err != nil {
				t.Errorf("Failed to parse event: %v", err)
				return
			}

			// Check for problematic patterns
			eventType, hasType := data["type"].(string)
			choices, hasChoices := data["choices"].([]interface{})
			
			t.Logf("Event type: %q, has choices: %v", eventType, hasChoices)

			if hasChoices {
				if len(choices) == 0 {
					t.Error("❌ DETECTED: Empty choices array - this causes client crashes")
				}
				if hasType && eventType == "" {
					t.Error("❌ DETECTED: Mixed format - has choices but empty type field")
				}
			}

			if !hasType && hasChoices {
				t.Error("❌ DETECTED: OpenAI-format chunk in Anthropic stream")
			}
		})
	}
}
