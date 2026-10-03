package streaming

import vendorstrip "github.com/kaixuan/llm-gateway-go/internal/vendorstrip"

// StripZhipuStreamChunkFieldsBody sanitizes a Zhipu-family *streaming chunk*.
//
// It differs from StripZhipuFieldsBody in exactly one field: the bare
// top-level `request_id`. A Zhipu response body may legitimately expose
// `request_id` to clients (pinned by strip_realworld_test.go), but
// `chat.completion.chunk` has no such field in the OpenAI spec, and the bare
// form is the only vendor tag a reseller-forwarded GLM chunk carries — Xcode's
// Coding Assistant failed the whole event on it (2026-10-01).
func StripZhipuStreamChunkFieldsBody(body []byte) []byte {
	return vendorstrip.DefaultRegistry.Strip(body, vendorstrip.VendorZhipuStreamChunk)
}
