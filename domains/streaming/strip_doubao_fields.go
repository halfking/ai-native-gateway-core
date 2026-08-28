package streaming

import (
	"strings"

	vendorstrip "github.com/kaixuan/llm-gateway-go/internal/vendorstrip"
)

// IsDoubaoCatalog reports whether a candidate is the official Doubao
// provider. Aggregated Volcengine Coding credentials must not use this
// policy because they can return GLM, DeepSeek, MiniMax, or other payloads.
func IsDoubaoCatalog(catalogCode string) bool {
	return strings.EqualFold(strings.TrimSpace(catalogCode), vendorstrip.VendorDoubao)
}

// StripDoubaoFieldsBody is retained as a compatibility adapter. The canonical
// implementation lives in internal/vendorstrip.
func StripDoubaoFieldsBody(body []byte) []byte {
	return vendorstrip.DefaultRegistry.Strip(body, vendorstrip.VendorDoubao)
}
