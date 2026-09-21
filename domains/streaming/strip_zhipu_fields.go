package streaming

import vendorstrip "github.com/kaixuan/llm-gateway-go/internal/vendorstrip"

// StripZhipuFieldsBody is retained as a compatibility adapter for Zhipu/GLM
// callers. The canonical implementation lives in internal/vendorstrip.
func StripZhipuFieldsBody(body []byte) []byte {
	return vendorstrip.DefaultRegistry.Strip(body, vendorstrip.VendorZhipu)
}
