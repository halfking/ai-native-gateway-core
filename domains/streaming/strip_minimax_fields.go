package streaming

import vendorstrip "github.com/kaixuan/llm-gateway-go/internal/vendorstrip"

// StripMinimaxFieldsBody is retained as a compatibility adapter for existing
// streaming callers. The canonical implementation lives in internal/vendorstrip.
func StripMinimaxFieldsBody(body []byte) []byte {
	return vendorstrip.DefaultRegistry.Strip(body, vendorstrip.VendorMiniMax)
}
