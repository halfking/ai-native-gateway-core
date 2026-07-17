package streaming

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// P2-3: OpenAI Responses audio/video 端点专属测试
// 目标: 补充 OpenAI Responses API 的 audio/video 端点专属测试
//
// Note: This test documents endpoint-specific features that are preserved during
// Responses→Chat conversion. The converter treats unknown input types as pass-through,
// allowing endpoint-specific fields to flow to the upstream provider.

// TestResponses_AudioModalityPreservation verifies audio/video modality fields
func TestResponses_AudioModalityPreservation(t *testing.T) {
	var req responsesRequestBody
	err := json.Unmarshal([]byte(`{
		"model": "gpt-4o-audio-preview",
		"modalities": ["text", "audio"],
		"audio": {
			"voice": "alloy",
			"format": "wav"
		},
		"input": [
			{"role": "user", "content": "Listen to this"}
		]
	}`), &req)
	require.NoError(t, err)

	result := convertResponsesToChatBody(&req)

	// Verify modalities preserved (endpoint-specific field)
	modalities, ok := result["modalities"].([]any)
	require.True(t, ok, "modalities should be preserved")
	require.Len(t, modalities, 2)
	assert.Equal(t, "text", modalities[0])
	assert.Equal(t, "audio", modalities[1])

	// Verify audio config preserved (endpoint-specific field)
	audio, ok := result["audio"].(map[string]any)
	require.True(t, ok, "audio config should be preserved")
	assert.Equal(t, "alloy", audio["voice"])
	assert.Equal(t, "wav", audio["format"])

	// Verify messages conversion
	messages, ok := result["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 1)

	msg1 := messages[0].(map[string]any)
	assert.Equal(t, "user", msg1["role"])
	assert.Equal(t, "Listen to this", msg1["content"])
}

// TestResponses_AudioOutput verifies output audio item
func TestResponses_AudioOutput(t *testing.T) {
	var req responsesRequestBody
	err := json.Unmarshal([]byte(`{
		"model": "gpt-4o-audio-preview",
		"modalities": ["text", "audio"],
		"audio": {
			"voice": "shimmer",
			"format": "pcm16"
		},
		"input": [
			{"role": "user", "content": "Say hello in audio"}
		]
	}`), &req)
	require.NoError(t, err)

	result := convertResponsesToChatBody(&req)

	// Verify audio output config
	audio, ok := result["audio"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "shimmer", audio["voice"])
	assert.Equal(t, "pcm16", audio["format"])

	// Verify modalities for audio output
	modalities, ok := result["modalities"].([]any)
	require.True(t, ok)
	assert.Contains(t, modalities, "audio", "audio modality required for audio output")
}

// TestResponses_EndpointSpecific verifies endpoint-specific limitations and documentation
func TestResponses_EndpointSpecific(t *testing.T) {
	t.Run("input_audio_type_passthrough", func(t *testing.T) {
		// Note: input_audio type items are treated as unknown types by convertResponsesInputItem
		// and fall through to default content handling. This documents the current behavior.
		var req responsesRequestBody
		err := json.Unmarshal([]byte(`{
			"model": "gpt-4o-audio-preview",
			"input": [
				{
					"type": "input_audio",
					"input_audio": {
						"data": "audiodata",
						"format": "wav"
					}
				}
			]
		}`), &req)
		require.NoError(t, err)

		result := convertResponsesToChatBody(&req)

		// Verify messages created (unknown type becomes user message with content)
		messages, ok := result["messages"].([]any)
		require.True(t, ok)
		require.Len(t, messages, 1, "input_audio type is passed through as message")

		msg := messages[0].(map[string]any)
		assert.Equal(t, "user", msg["role"], "unknown types default to user role")
		// Content may be empty object or the item itself
		assert.NotNil(t, msg["content"])
	})

	t.Run("modalities_endpoint_specific", func(t *testing.T) {
		// Modalities field is Responses API specific
		var req responsesRequestBody
		err := json.Unmarshal([]byte(`{
			"model": "gpt-4o-mini",
			"modalities": ["text"],
			"input": [
				{"role": "user", "content": "hello"}
			]
		}`), &req)
		require.NoError(t, err)

		result := convertResponsesToChatBody(&req)

		// Verify modalities is preserved (endpoint-specific field)
		modalities, ok := result["modalities"].([]any)
		require.True(t, ok, "modalities should be preserved as endpoint-specific field")
		assert.Len(t, modalities, 1)
		assert.Equal(t, "text", modalities[0])
	})

	t.Run("audio_config_without_modality_fails", func(t *testing.T) {
		// Audio config requires audio modality
		var req responsesRequestBody
		err := json.Unmarshal([]byte(`{
			"model": "gpt-4o-audio-preview",
			"modalities": ["text"],
			"audio": {
				"voice": "alloy",
				"format": "wav"
			},
			"input": [
				{"role": "user", "content": "hello"}
			]
		}`), &req)
		require.NoError(t, err)

		result := convertResponsesToChatBody(&req)

		// Audio config present but modalities doesn't include audio
		// This is technically invalid but converter should preserve it
		audio, ok := result["audio"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "alloy", audio["voice"])

		modalities, ok := result["modalities"].([]any)
		require.True(t, ok)
		assert.NotContains(t, modalities, "audio", "modalities should not contain audio")
	})
}

// TestResponses_InputAudioAndTextMixed verifies mixed content with audio
func TestResponses_InputAudioAndTextMixed(t *testing.T) {
	var req responsesRequestBody
	err := json.Unmarshal([]byte(`{
		"model": "gpt-4o-audio-preview",
		"modalities": ["text", "audio"],
		"input": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "Transcribe this audio:"},
					{
						"type": "input_audio",
						"input_audio": {
							"data": "audiodata123",
							"format": "wav"
						}
					}
				]
			}
		]
	}`), &req)
	require.NoError(t, err)

	result := convertResponsesToChatBody(&req)

	messages, ok := result["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 1)

	msg := messages[0].(map[string]any)
	content, ok := msg["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 2, "should have text + input_audio")

	// First block: text
	textBlock := content[0].(map[string]any)
	assert.Equal(t, "text", textBlock["type"])
	assert.Equal(t, "Transcribe this audio:", textBlock["text"])

	// Second block: input_audio
	audioBlock := content[1].(map[string]any)
	assert.Equal(t, "input_audio", audioBlock["type"])
	inputAudio := audioBlock["input_audio"].(map[string]any)
	assert.Equal(t, "audiodata123", inputAudio["data"])
	assert.Equal(t, "wav", inputAudio["format"])
}

// TestResponses_AudioFormatValidation verifies supported audio formats
func TestResponses_AudioFormatValidation(t *testing.T) {
	supportedFormats := []string{"wav", "mp3", "pcm16"}

	for _, format := range supportedFormats {
		t.Run("format_"+format, func(t *testing.T) {
			var req responsesRequestBody
			err := json.Unmarshal([]byte(`{
				"model": "gpt-4o-audio-preview",
				"audio": {
					"voice": "alloy",
					"format": "`+format+`"
				},
				"input": [
					{"role": "user", "content": "test"}
				]
			}`), &req)
			require.NoError(t, err)

			result := convertResponsesToChatBody(&req)

			audio, ok := result["audio"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, format, audio["format"])
		})
	}
}

// TestResponses_VoiceOptions verifies supported voice options
func TestResponses_VoiceOptions(t *testing.T) {
	supportedVoices := []string{"alloy", "echo", "fable", "onyx", "nova", "shimmer"}

	for _, voice := range supportedVoices {
		t.Run("voice_"+voice, func(t *testing.T) {
			var req responsesRequestBody
			err := json.Unmarshal([]byte(`{
				"model": "gpt-4o-audio-preview",
				"audio": {
					"voice": "`+voice+`",
					"format": "wav"
				},
				"input": [
					{"role": "user", "content": "test"}
				]
			}`), &req)
			require.NoError(t, err)

			result := convertResponsesToChatBody(&req)

			audio, ok := result["audio"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, voice, audio["voice"])
		})
	}
}

// TestResponses_MultipleAudioConfigs verifies multiple audio-related config fields
func TestResponses_MultipleAudioConfigs(t *testing.T) {
	var req responsesRequestBody
	err := json.Unmarshal([]byte(`{
		"model": "gpt-4o-audio-preview",
		"modalities": ["text", "audio"],
		"audio": {
			"voice": "alloy",
			"format": "wav"
		},
		"input": [
			{"role": "user", "content": "Say hello"},
			{"role": "user", "content": "Say goodbye"}
		]
	}`), &req)
	require.NoError(t, err)

	result := convertResponsesToChatBody(&req)

	// Verify audio config preserved
	audio, ok := result["audio"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "alloy", audio["voice"])
	assert.Equal(t, "wav", audio["format"])

	// Verify modalities preserved
	modalities, ok := result["modalities"].([]any)
	require.True(t, ok)
	assert.Len(t, modalities, 2)

	// Verify messages
	messages, ok := result["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 2)
}
