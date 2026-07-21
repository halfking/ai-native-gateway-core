package streaming

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConvertResponsesInputItemWithError_Media(t *testing.T) {
	cases := []struct {
		name string
		item map[string]any
		want string
	}{
		{
			name: "input_text",
			item: map[string]any{"type": "input_text", "text": "describe"},
			want: "text",
		},
		{
			name: "input_image",
			item: map[string]any{"type": "input_image", "image_url": "https://example.test/a.png"},
			want: "image_url",
		},
		{
			name: "input_audio",
			item: map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": "AAA", "format": "wav"}},
			want: "input_audio",
		},
		{
			name: "input_file",
			item: map[string]any{"type": "input_file", "input_file": map[string]any{"file_id": "file_1"}},
			want: "file",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			message, ok, err := convertResponsesInputItemWithError(tc.item)
			require.NoError(t, err)
			require.True(t, ok)
			content := message["content"].([]any)
			block := content[0].(map[string]any)
			if tc.want == "image_url" {
				if block["type"] != tc.want {
					t.Fatalf("block=%v, want image_url", block)
				}
				return
			}
			if block["type"] != tc.want {
				t.Fatalf("block=%v, want %s", block, tc.want)
			}
		})
	}
}

func TestConvertResponsesToChatBodyWithError_RejectsMalformedMedia(t *testing.T) {
	var req responsesRequestBody
	require.NoError(t, json.Unmarshal([]byte(`{"model":"gpt-4o","input":[{"type":"input_audio"}]}`), &req))
	_, err := convertResponsesToChatBodyWithError(&req)
	if err == nil {
		t.Fatal("expected unsupported_modality error")
	}
	if got := err.Error(); got == "" || !strings.Contains(got, "unsupported_modality") {
		t.Fatalf("error=%q, want unsupported_modality", got)
	}
}
