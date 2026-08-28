package streaming

import vendorstrip "github.com/kaixuan/llm-gateway-go/internal/vendorstrip"

// StripDeepSeekFieldsBody is retained as a compatibility adapter. The
// canonical implementation lives in internal/vendorstrip.
func StripDeepSeekFieldsBody(body []byte) []byte {
	return vendorstrip.DefaultRegistry.Strip(body, vendorstrip.VendorDeepSeek)
}
