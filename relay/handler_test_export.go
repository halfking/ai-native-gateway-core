package relay

import "github.com/kaixuan/llm-gateway-go/telemetry"

// DetectEmptyStreamResponse is a test-only export of detectEmptyStreamResponse
// for integration test verification. This allows tests/integration package to
// audit the fix without duplicating the detection logic.
func DetectEmptyStreamResponse(m map[string]any, reqLog *telemetry.RequestLogEntry) bool {
	return detectEmptyStreamResponse(m, reqLog)
}
