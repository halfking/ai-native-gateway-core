package streaming

import "strings"

// DispatchStripVendorFields conditionally strips vendor-specific private
// fields based on the provider's catalog_code (P0-3, 2026-07-11).
//
// Only the matching stripper runs; unknown providers (including openai)
// preserve all fields unchanged. This ensures that same-named fields like
// "request_id" are only removed when the response comes from the vendor
// that owns that field shape.
//
// Catalog code mapping:
//   - "minimax"  → StripMinimaxFieldsBody
//   - "zhipu"    → StripZhipuFieldsBody
//   - "deepseek" → StripDeepSeekFieldsBody
//   - "doubao"   → StripDoubaoFieldsBody
//   - all others → no-op (return body unchanged)
func DispatchStripVendorFields(body []byte, catalogCode string) []byte {
	if len(body) == 0 {
		return body
	}
	// Normalize catalog code to lowercase for case-insensitive matching.
	code := strings.ToLower(strings.TrimSpace(catalogCode))
	switch code {
	case "minimax":
		return StripMinimaxFieldsBody(body)
	case "zhipu", "glm":
		return StripZhipuFieldsBody(body)
	case "deepseek":
		return StripDeepSeekFieldsBody(body)
	case "doubao", "volcano":
		return StripDoubaoFieldsBody(body)
	default:
		// OpenAI, Anthropic, unknown providers: no stripping.
		return body
	}
}
