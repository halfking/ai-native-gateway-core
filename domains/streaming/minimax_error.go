package streaming

import (
	"github.com/kaixuan/llm-gateway-go/errorsx"
	vendorstrip "github.com/kaixuan/llm-gateway-go/internal/vendorstrip"
)

// ParseMiniMaxBaseResp is retained as a compatibility adapter.
func ParseMiniMaxBaseResp(body []byte) (statusCode int, statusMsg string, isError bool) {
	return vendorstrip.ParseMiniMaxBaseResp(body)
}

// ClassifyMiniMaxStatusCode is retained as a compatibility adapter.
func ClassifyMiniMaxStatusCode(code int) errorsx.ErrorKind {
	return vendorstrip.ClassifyMiniMaxStatusCode(code)
}

// FormatMiniMaxError is retained as a compatibility adapter.
func FormatMiniMaxError(statusCode int, statusMsg string) string {
	return vendorstrip.FormatMiniMaxError(statusCode, statusMsg)
}
