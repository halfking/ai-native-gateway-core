package modelname

import "testing"

func TestInferModality(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		expected string
	}{
		// OpenAI - Vision
		{"gpt-4o exact", "gpt-4o", "vision"},
		{"gpt-4o variant", "gpt-4o-2024-08-06", "vision"},
		{"gpt-4-turbo", "gpt-4-turbo", "vision"},
		{"gpt-4-turbo variant", "gpt-4-turbo-2024-04-09", "vision"},
		{"o1-preview", "o1-preview", "vision"},
		{"o1-mini", "o1-mini", "vision"},
		{"o3-mini", "o3-mini", "vision"},

		// OpenAI - Audio
		{"whisper-1", "whisper-1", "audio"},
		{"tts-1", "tts-1", "audio"},
		{"gpt-4o-audio-preview", "gpt-4o-audio-preview", "audio"},

		// OpenAI - Text
		{"gpt-3.5-turbo", "gpt-3.5-turbo", "text"},
		{"gpt-3.5-turbo variant", "gpt-3.5-turbo-0125", "text"},

		// OpenAI - Embedding
		{"text-embedding-3-small", "text-embedding-3-small", "embedding"},
		{"text-embedding-ada-002", "text-embedding-ada-002", "embedding"},
		{"embedding-001", "embedding-001", "embedding"},

		// Anthropic - Vision
		{"claude-3-opus", "claude-3-opus-20240229", "vision"},
		{"claude-3-sonnet", "claude-3-sonnet-20240229", "vision"},
		{"claude-3-5-sonnet", "claude-3-5-sonnet-20241022", "vision"},
		{"claude-sonnet-4", "claude-sonnet-4", "vision"},
		{"claude-opus-4", "claude-opus-4", "vision"},

		// Anthropic - Text
		{"claude-2", "claude-2", "text"},
		{"claude-2.1", "claude-2.1", "text"},
		{"claude-instant-1.2", "claude-instant-1.2", "text"},

		// Google Gemini - Multimodal
		{"gemini-1.5-pro", "gemini-1.5-pro", "multimodal"},
		{"gemini-2.0-flash", "gemini-2.0-flash-exp", "multimodal"},
		{"gemini-2.5-flash", "gemini-2.5-flash", "multimodal"},
		{"gemini-2.5-flash-image", "gemini-2.5-flash-image", "multimodal"}, // image variant (Phase 4 T-21 gap fix)
		{"gemini-2.5-pro", "gemini-2.5-pro", "multimodal"},
		{"gemini-3-flash", "gemini-3-flash", "multimodal"},
		{"gemini-ultra", "gemini-ultra", "multimodal"},

		// Google Gemini - Vision
		{"gemini-pro-vision", "gemini-pro-vision", "vision"},

		// Google Gemini - Text
		{"gemini-pro", "gemini-pro", "text"},
		{"gemma-2-9b", "gemma-2-9b-it", "text"},

		// Meta Llama - Vision
		{"llama-3.2-11b-vision", "llama-3.2-11b-vision-instruct", "vision"},
		{"llama-3.2-90b-vision", "llama-3.2-90b-vision-instruct", "vision"},

		// Meta Llama - Text
		{"llama-3.2-1b", "llama-3.2-1b", "text"},
		{"llama-3.2-3b", "llama-3.2-3b-instruct", "text"},
		{"llama-3.1-8b", "llama-3.1-8b-instruct", "text"},
		{"llama-3-70b", "llama-3-70b-instruct", "text"},

		// Mistral - Vision
		{"pixtral-12b", "pixtral-12b-2409", "vision"},
		{"pixtral-large", "pixtral-large-latest", "vision"},

		// Mistral - Text
		{"mistral-large-2411", "mistral-large-2411", "text"},
		{"mixtral-8x7b", "mixtral-8x7b-instruct-v0.1", "text"},
		{"ministral-8b", "ministral-8b-latest", "text"},

		// Mistral - Embedding
		{"mistral-embed", "mistral-embed", "embedding"},

		// Chinese Models - GLM
		{"glm-4v", "glm-4v", "vision"},
		{"glm-4v-plus", "glm-4v-plus", "vision"},
		{"glm-4", "glm-4", "text"},
		{"glm-3-turbo", "glm-3-turbo", "text"},
		{"chatglm3", "chatglm3-6b", "text"},

		// Chinese Models - Qwen
		{"qwen-vl-max", "qwen-vl-max", "vision"},
		{"qwen2-vl-7b", "qwen2-vl-7b-instruct", "vision"},
		{"qwen-audio-turbo", "qwen-audio-turbo", "audio"},
		{"qwen2.5-72b", "qwen2.5-72b-instruct", "text"},
		{"qwen2-7b", "qwen2-7b-instruct", "text"},

		// Chinese Models - Doubao
		{"doubao-vision-pro", "doubao-vision-pro-32k", "vision"},
		{"doubao-pro-32k", "doubao-pro-32k", "text"},
		{"seed-chat", "seed-chat-v1", "text"},

		// Chinese Models - Yi
		{"yi-vision", "yi-vision", "vision"},
		{"yi-vl-plus", "yi-vl-plus", "vision"},
		{"yi-34b-chat", "yi-34b-chat-0205", "text"},

		// Chinese Models - Others
		{"deepseek-chat", "deepseek-chat", "text"},
		{"minimax-pro", "minimax-pro", "text"},
		{"baichuan2-turbo", "baichuan2-turbo", "text"},
		{"spark-v3.5", "spark-v3.5", "text"},
		{"ernie-bot-4", "ernie-bot-4", "text"},
		{"ernie-4.0-vision", "ernie-4.0-vision-8k", "vision"},
		{"hunyuan-pro", "hunyuan-pro", "text"},
		{"hunyuan-vision", "hunyuan-vision", "vision"},

		// StepFun
		{"step-1v-32k", "step-1v-32k", "vision"},
		{"step-2-16k", "step-2-16k", "text"},

		// Cohere
		{"command-r-plus", "command-r-plus-08-2024", "text"},
		{"embed-english-v3.0", "embed-english-v3.0", "embedding"},
		{"rerank-english-v3.0", "rerank-english-v3.0", "text"},

		// xAI Grok
		{"grok-vision-beta", "grok-vision-beta", "vision"},
		{"grok-beta", "grok-beta", "text"},

		// NVIDIA
		{"llama-3.1-nemotron-70b-instruct", "llama-3.1-nemotron-70b-instruct", "text"},

		// Generic fallback patterns
		{"custom-vision-model", "custom-vision-model", "vision"},
		{"custom-vl-model", "custom-vl-model", "vision"},
		{"custom-audio-model", "custom-audio-model", "audio"},
		{"custom-embedding-model", "custom-embedding-model", "embedding"},

		// Unknown models default to text
		{"unknown-model", "unknown-model-v1", "text"},
		{"random-123", "random-123", "text"},

		// Edge cases
		{"empty", "", "text"},
		{"whitespace", "   ", "text"},
		{"case insensitive", "GPT-4O", "vision"},
		{"case insensitive 2", "Claude-3-Opus", "vision"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InferModality(tt.model)
			if got != tt.expected {
				t.Errorf("InferModality(%q) = %q, want %q", tt.model, got, tt.expected)
			}
		})
	}
}

func TestInferModalityPriorityOrder(t *testing.T) {
	// Test that exact match takes priority over wildcard
	tests := []struct {
		name     string
		model    string
		expected string
	}{
		// Exact match should win over prefix/suffix
		{"exact match priority", "gpt-4o", "vision"},

		// Prefix match should work when no exact match
		{"prefix match", "gpt-4o-mini", "vision"}, // matches gpt-4o-*

		// Suffix match should work
		{"suffix match embedding", "custom-embedding", "embedding"}, // matches *embedding*

		// Contains match should work as fallback
		{"contains vision", "my-vision-model", "vision"}, // matches *-vision-*
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InferModality(tt.model)
			if got != tt.expected {
				t.Errorf("InferModality(%q) = %q, want %q", tt.model, got, tt.expected)
			}
		})
	}
}
