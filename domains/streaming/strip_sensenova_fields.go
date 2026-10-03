package streaming

import vendorstrip "github.com/kaixuan/llm-gateway-go/internal/vendorstrip"

// StripSensenovaFieldsBody is retained as a compatibility adapter. The
// canonical implementation lives in internal/vendorstrip.
//
// SenseNova (token.sensenova.cn) is an OpenAI-compatible reseller whose glm-*
// models are Zhipu-backed and whose chunks are the Zhipu payload forwarded
// verbatim — including the bare top-level `request_id` that the direct Zhipu
// endpoint instead tags as `zhipu_request_id`. Before this adapter existed the
// `sensenova` catalog code had no sanitizer at all, so both tags reached the
// client (see strip_vendor_fields_test.go).
func StripSensenovaFieldsBody(body []byte) []byte {
	return vendorstrip.DefaultRegistry.Strip(body, vendorstrip.VendorSensenova)
}
