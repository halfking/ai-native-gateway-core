package compression

import "github.com/kaixuan/llm-gateway-go/domains/transformation" //nolint:depguard

// trimMessagesBody is the actual delegator for compressMechanical.
// Kept in its own file so the dispatcher (compression.go) doesn't carry
// the transform import for readers focused on mode dispatch logic.
func trimMessagesBody(body []byte, contextWindow int) []byte {
	return transformation.CompressMessagesIfNeeded(body, contextWindow)
}
