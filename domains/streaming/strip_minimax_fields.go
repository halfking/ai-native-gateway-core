package streaming

import (
	"bytes"
	"encoding/json"
	vendorstrip "github.com/kaixuan/llm-gateway-go/internal/vendorstrip"
)

// StripMinimaxFieldsBody is retained as a compatibility adapter for existing
// streaming callers. The canonical implementation lives in internal/vendorstrip.
func StripMinimaxFieldsBody(body []byte) []byte {
	return vendorstrip.DefaultRegistry.Strip(body, vendorstrip.VendorMiniMax)
}

// NewMiniMaxStreamFieldStripper keeps failed tool continuations intact while
// retaining the ordinary vendor/reasoning cleanup for the rest of this stream.
func NewMiniMaxStreamFieldStripper() func([]byte) []byte {
	return vendorstrip.NewMiniMaxStreamStripper()
}

func newInferredStreamStripper() func([]byte) []byte {
	minimax := NewMiniMaxStreamFieldStripper()
	seen := false
	return func(body []byte) []byte {
		if bytes.Equal(bytes.TrimSpace(body), []byte("[DONE]")) {
			seen = false
			return minimax(body)
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(body, &fields) == nil && (fields["base_resp"] != nil || fields["nvext"] != nil || fields["input_sensitive"] != nil) {
			seen = true
		}
		// Vendor evidence may arrive after an opener (or after reader-created
		// content frames). Feed bounded lexical state without changing bytes
		// until a top-level vendor field actually identifies this response.
		cleaned := minimax(body)
		if seen {
			return cleaned
		}
		return body
	}
}
