package streaming

import "github.com/kaixuan/llm-gateway-go/internal/ir"

const (
	modalityText       = "text"
	modalityVision     = "vision"
	modalityAudio      = "audio"
	modalityVideo      = "video"
	modalityMultimodal = "multimodal"
)

// detectRequestModality derives the routing modality from OpenAI, Anthropic,
// Gemini, or Responses-shaped request bodies.
func detectRequestModality(bodyBytes []byte) string {
	return ir.DetectRequestCapabilities(bodyBytes).PrimaryModality()
}
