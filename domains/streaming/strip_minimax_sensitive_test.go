package streaming

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStripMinimaxFieldsBody_PreservesSensitiveTypeFields verifies that
// P2-MiniMax-2 fix preserves input_sensitive_type and output_sensitive_type
// for fine-grained content moderation classification (1-7 scale).
func TestStripMinimaxFieldsBody_PreservesSensitiveTypeFields(t *testing.T) {
	body := []byte(`{
		"id": "minimax-response",
		"model": "abab5.5-chat",
		"choices": [{
			"message": {
				"role": "assistant",
				"content": "Hello"
			},
			"finish_reason": "stop"
		}],
		"input_sensitive": false,
		"input_sensitive_type": 0,
		"output_sensitive": false,
		"output_sensitive_type": 0,
		"base_resp": {
			"status_code": 0,
			"status_msg": "success"
		},
		"request_id": "req_123",
		"created_by": "system"
	}`)

	stripped := StripMinimaxFieldsBody(body)

	var result map[string]any
	require.NoError(t, json.Unmarshal(stripped, &result))

	// Should preserve: input_sensitive, input_sensitive_type, output_sensitive, output_sensitive_type
	assert.Contains(t, result, "input_sensitive", "input_sensitive should be preserved")
	assert.Contains(t, result, "input_sensitive_type", "input_sensitive_type should be preserved (P2-MiniMax-2)")
	assert.Contains(t, result, "output_sensitive", "output_sensitive should be preserved")
	assert.Contains(t, result, "output_sensitive_type", "output_sensitive_type should be preserved (P2-MiniMax-2)")

	// Should strip: base_resp, request_id, created_by
	assert.NotContains(t, result, "base_resp", "base_resp should be stripped")
	assert.NotContains(t, result, "request_id", "request_id should be stripped")
	assert.NotContains(t, result, "created_by", "created_by should be stripped")
}

func TestStripMinimaxFieldsBody_SensitiveContentFlagged(t *testing.T) {
	body := []byte(`{
		"id": "minimax-response",
		"model": "abab5.5-chat",
		"choices": [{
			"message": {
				"role": "assistant",
				"content": "[FILTERED]"
			},
			"finish_reason": "stop"
		}],
		"input_sensitive": false,
		"input_sensitive_type": 0,
		"output_sensitive": true,
		"output_sensitive_type": 3,
		"base_resp": {
			"status_code": 0
		}
	}`)

	stripped := StripMinimaxFieldsBody(body)

	var result map[string]any
	require.NoError(t, json.Unmarshal(stripped, &result))

	// Verify fine-grained classification is preserved
	assert.Equal(t, true, result["output_sensitive"])
	assert.Equal(t, float64(3), result["output_sensitive_type"], "output_sensitive_type=3 should be preserved for audit/compliance")
}

func TestStripMinimaxFieldsBody_MultipleClassificationLevels(t *testing.T) {
	cases := []struct {
		name               string
		inputType          int
		outputType         int
		wantInputPreserved bool
		wantOutputPreserved bool
	}{
		{
			name:                "clean content (0)",
			inputType:           0,
			outputType:          0,
			wantInputPreserved:  true,
			wantOutputPreserved: true,
		},
		{
			name:                "mild violation (1)",
			inputType:           1,
			outputType:          1,
			wantInputPreserved:  true,
			wantOutputPreserved: true,
		},
		{
			name:                "moderate violation (3)",
			inputType:           3,
			outputType:          3,
			wantInputPreserved:  true,
			wantOutputPreserved: true,
		},
		{
			name:                "severe violation (7)",
			inputType:           7,
			outputType:          7,
			wantInputPreserved:  true,
			wantOutputPreserved: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{
				"id": "test",
				"choices": [{"message": {"role": "assistant", "content": "test"}}],
				"input_sensitive": ` + (map[bool]string{true: "true", false: "false"})[tc.inputType > 0] + `,
				"input_sensitive_type": ` + string(rune('0'+tc.inputType)) + `,
				"output_sensitive": ` + (map[bool]string{true: "true", false: "false"})[tc.outputType > 0] + `,
				"output_sensitive_type": ` + string(rune('0'+tc.outputType)) + `,
				"request_id": "should_be_stripped"
			}`)

			stripped := StripMinimaxFieldsBody(body)

			var result map[string]any
			require.NoError(t, json.Unmarshal(stripped, &result))

			if tc.wantInputPreserved {
				assert.Contains(t, result, "input_sensitive_type")
				assert.Equal(t, float64(tc.inputType), result["input_sensitive_type"])
			}

			if tc.wantOutputPreserved {
				assert.Contains(t, result, "output_sensitive_type")
				assert.Equal(t, float64(tc.outputType), result["output_sensitive_type"])
			}

			assert.NotContains(t, result, "request_id", "private fields should still be stripped")
		})
	}
}
