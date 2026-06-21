package relay

import (
	"testing"
)

func TestIsOpenAIFormatData(t *testing.T) {
	tests := []struct {
		name     string
		data     string
		expected bool
	}{
		{
			name:     "valid_anthropic_message_start",
			data:     `{"type":"message_start","message":{"id":"msg_1","role":"assistant"}}`,
			expected: false,
		},
		{
			name:     "valid_anthropic_content_delta",
			data:     `{"type":"content_block_delta","index":0,"delta":{"type":"text","text":"Hello"}}`,
			expected: false,
		},
		{
			name:     "openai_with_choices",
			data:     `{"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
			expected: true,
		},
		{
			name:     "openai_empty_choices",
			data:     `{"id":"chatcmpl-2","choices":[]}`,
			expected: true,
		},
		{
			name:     "openai_with_created_timestamp",
			data:     `{"id":"chatcmpl-3","created":1782052812,"model":"glm-5.2"}`,
			expected: true,
		},
		{
			name:     "openai_object_signature",
			data:     `{"id":"chatcmpl-4","object":"chat.completion","model":"gpt-4"}`,
			expected: true,
		},
		{
			name:     "mixed_format_with_choices",
			data:     `{"type":"","choices":[],"model":"glm-5.2"}`,
			expected: true,
		},
		{
			name:     "anthropic_text_containing_word_choices",
			data:     `{"type":"content_block_delta","delta":{"type":"text","text":"You have multiple choices here"}}`,
			expected: false, // Should NOT be caught - "choices" is in text content, not a field
		},
		{
			name:     "anthropic_with_created_at",
			data:     `{"type":"message_start","message":{"id":"msg_1","created_at":"2024-01-01T00:00:00Z"}}`,
			expected: false, // created_at with string, not created with number
		},
		{
			name:     "empty_data",
			data:     ``,
			expected: false,
		},
		{
			name:     "short_data",
			data:     `{"a":1}`,
			expected: false,
		},
		{
			name:     "ping_event",
			data:     `{"type":"ping"}`,
			expected: false,
		},
		{
			name:     "error_event",
			data:     `{"type":"error","error":{"type":"invalid_request_error","message":"Bad request"}}`,
			expected: false,
		},
		{
			name:     "openai_streaming_done",
			data:     `{"id":"chatcmpl-5","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"created":1234567890}`,
			expected: true,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isOpenAIFormatData([]byte(tt.data))
			if result != tt.expected {
				t.Errorf("isOpenAIFormatData() = %v, want %v\nData: %s", 
					result, tt.expected, tt.data)
			}
		})
	}
}

func TestTruncateForLog(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "shorter_than_max",
			input:  "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "equal_to_max",
			input:  "hello",
			maxLen: 5,
			want:   "hello",
		},
		{
			name:   "longer_than_max",
			input:  "hello world",
			maxLen: 5,
			want:   "hello...",
		},
		{
			name:   "much_longer",
			input:  "this is a very long string that needs truncation",
			maxLen: 10,
			want:   "this is a ...",
		},
		{
			name:   "empty_string",
			input:  "",
			maxLen: 10,
			want:   "",
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateForLog(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateForLog() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Benchmark the detector to ensure it's fast enough for hot path
func BenchmarkIsOpenAIFormatData(b *testing.B) {
	testCases := [][]byte{
		[]byte(`{"type":"content_block_delta","delta":{"type":"text","text":"Hello"}}`),
		[]byte(`{"id":"chatcmpl-1","choices":[{"delta":{"content":"hi"}}]}`),
		[]byte(`{"id":"chatcmpl-2","object":"chat.completion","created":1234567890}`),
	}
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, data := range testCases {
			_ = isOpenAIFormatData(data)
		}
	}
}
