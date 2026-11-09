// estimator.go (2026-11-10) — calibrated default token estimator for bodies
// whose bucket has no exact Counter.
//
// 标定来源：domains/streaming/executors/input_token_estimate.go 的
// EstimateAnthropicInputTokens（charsPerToken=3.5、无空白长串 blob=5.0、
// 与仓内 transformation.EstimateTokens 同源）。raw bytes-as-tokens 对 CJK
// 高估 ~3x、对代码高估 ~2x，是 2026-11-10 平台 422 事故的执法误差来源。
//
// 估计目标不是精确 tokenizer，而是「量级正确的保守估计」：文本按字符
// /3.5，无空白长串（base64/hex blob，≥4096B）按字节/5（接近解码成本），
// 加 JSON 框架开销。仅对「窗口已知（catalog 或上游发现证据）」的桶执法；
// 窗口未知时 advisory（容量裁决归上游）。
package contextbudget

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const (
	estimateCharsPerToken     = 3.5
	estimateBlobBytesPerToken = 5.0
	estimateBlobMinBytes      = 4096
)

// estimateJSONStringTokens walks the JSON string values and estimates tokens:
// regular text at chars/3.5, opaque no-whitespace blobs at bytes/5. JSON
// framing overhead (request + per-node) is added by the caller from
// inspect()'s node count — the same framing the legacy byte bound used.
func estimateJSONStringTokens(body []byte) float64 {
	total := 0.0
	var visit func(v any)
	visit = func(v any) {
		switch x := v.(type) {
		case string:
			total += estimateStringTokens(x)
		case []any:
			for _, item := range x {
				visit(item)
			}
		case map[string]any:
			for _, val := range x {
				visit(val)
			}
		}
	}
	var root any
	if json.Unmarshal(body, &root) != nil {
		// Malformed bodies are rejected by inspect() before estimation; this
		// branch is a defensive floor, not a permission to under-count.
		return float64(len(body) / int(estimateBlobBytesPerToken))
	}
	visit(root)
	return total
}

// estimateStringTokens scores a single JSON string value. Blob detection
// mirrors the executors-side heuristic: no whitespace, at least 4KiB —
// base64/hex payloads whose token cost tracks decoded bytes, not chars/3.5.
func estimateStringTokens(s string) float64 {
	if len(s) >= estimateBlobMinBytes && !hasWhitespace(s) {
		return float64(len(s)) / estimateBlobBytesPerToken
	}
	runes := utf8.RuneCountInString(s)
	return float64(runes) / estimateCharsPerToken
}

func hasWhitespace(s string) bool {
	return strings.ContainsAny(s, " \t\r\n")
}
