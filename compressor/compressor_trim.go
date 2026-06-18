package compressor

import "github.com/kaixuan/llm-gateway-go/transform"

// trimMessagesBody is the actual delegator for compressMechanical.
// Kept in its own file so the dispatcher (compressor.go) doesn't carry
// the transform import for readers focused on mode dispatch logic.
func trimMessagesBody(body []byte, contextWindow int) []byte {
	return transform.CompressMessagesIfNeeded(body, contextWindow)
}
