//go:build integration && integration_debug

package integration

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
	gatewayURL := strings.TrimRight(os.Getenv("GATEWAY_URL"), "/")
	apiKey := os.Getenv("LLM_GATEWAY_API_KEY")
	if gatewayURL == "" || apiKey == "" {
		t.Skip("set GATEWAY_URL and LLM_GATEWAY_API_KEY to run GLM-5.2 live gateway test")
	}
	endpoint := gatewayURL + "/v1/chat/completions"

	// Test configurations
	testCases := []struct {
		name        string
		endpoint    string
		model       string
		contentType string
		stream      bool
	}{
		{
			name:        "glm-5.2_openai_format_non_stream",
			endpoint:    endpoint,
			model:       "glm-5.2",
			contentType: "application/json",
			stream:      false,
		},
		{
			name:        "glm-5.2_openai_format_stream",
			endpoint:    endpoint,
			model:       "glm-5.2",
			contentType: "application/json",
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
			req.Header.Set("Authorization", "Bearer "+apiKey)

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
			if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
				t.Fatalf("gateway returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
			}

			// Read response
			if tc.stream {
				t.Log("Processing streaming response...")
				chunkCount, err := validateGLM52Stream(resp.Body)
				if err != nil {
					t.Error(err)
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

func validateGLM52Stream(r io.Reader) (int, error) {
	scanner := newSSEScanner(r)
	chunkCount := 0
	sawDone := false
	sawContent := false
	for scanner.Scan() {
		line := scanner.Text()
		chunkCount++
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			sawDone = true
			break
		}

		var chunk struct {
			Choices []struct {
				Delta    map[string]any `json:"delta"`
				Finished string         `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return chunkCount, fmt.Errorf("parse chunk %d as JSON: %w", chunkCount, err)
		}
		if len(chunk.Choices) == 0 {
			return chunkCount, fmt.Errorf("chunk %d has empty choices array", chunkCount)
		}
		if chunk.Choices[0].Delta == nil {
			return chunkCount, fmt.Errorf("chunk %d has no choices[0].delta", chunkCount)
		}
		if content, ok := chunk.Choices[0].Delta["content"].(string); ok && content != "" {
			sawContent = true
		}
	}
	if err := scanner.Err(); err != nil {
		return chunkCount, fmt.Errorf("SSE scanner: %w", err)
	}
	if !sawContent {
		return chunkCount, fmt.Errorf("stream contained no choices[0].delta.content event")
	}
	if !sawDone {
		return chunkCount, fmt.Errorf("stream did not terminate with data: [DONE]")
	}
	return chunkCount, nil
}

func TestValidateGLM52Stream(t *testing.T) {
	tests := []struct {
		name    string
		stream  string
		wantErr string
	}{
		{
			name:   "valid completion",
			stream: "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n",
		},
		{
			name:    "missing content",
			stream:  "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n",
			wantErr: "no choices[0].delta.content",
		},
		{
			name:    "missing done marker",
			stream:  "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n",
			wantErr: "did not terminate",
		},
		{
			name:    "missing delta",
			stream:  "data: {\"choices\":[{}]}\n\ndata: [DONE]\n",
			wantErr: "has no choices[0].delta",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateGLM52Stream(strings.NewReader(tt.stream))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateGLM52Stream() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateGLM52Stream() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

// TestGLM52StreamEventParsing tests SSE event parsing for glm-5.2
func TestGLM52StreamEventParsing(t *testing.T) {
	testEvents := []struct {
		name       string
		raw        string
		wantValid  bool
		wantOpenAI bool
		wantEmpty  bool
		wantMixed  bool
	}{
		{"anthropic_message_start", `data: {"type":"message_start","message":{"id":"msg_1","role":"assistant"}}`, true, false, false, false},
		{"openai_chunk", `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"}}]}`, false, true, false, false},
		{"empty_choices", `data: {"id":"chatcmpl-2","object":"chat.completion.chunk","choices":[]}`, false, true, true, false},
		{"mixed_empty_type", `data: {"type":"","choices":[],"model":"glm-5.2"}`, false, true, true, true},
	}

	for _, event := range testEvents {
		t.Run(event.name, func(t *testing.T) {
			t.Logf("Testing event: %s", event.raw)

			// Parse the event data
			dataStr := strings.TrimPrefix(event.raw, "data: ")
			var data map[string]interface{}
			if err := json.Unmarshal([]byte(dataStr), &data); err != nil {
				t.Errorf("Failed to parse event: %v", err)
				return
			}

			// Check for problematic patterns
			eventType, hasType := data["type"].(string)
			choices, hasChoices := data["choices"].([]interface{})

			t.Logf("Event type: %q, has choices: %v", eventType, hasChoices)

			isEmpty := hasChoices && len(choices) == 0
			isOpenAI := hasChoices
			isMixed := isOpenAI && hasType && eventType == ""
			isValid := hasType && eventType != ""
			if isValid != event.wantValid || isOpenAI != event.wantOpenAI || isEmpty != event.wantEmpty || isMixed != event.wantMixed {
				t.Errorf("classification = valid:%v openai:%v empty:%v mixed:%v, want valid:%v openai:%v empty:%v mixed:%v", isValid, isOpenAI, isEmpty, isMixed, event.wantValid, event.wantOpenAI, event.wantEmpty, event.wantMixed)
			}
		})
	}
}
