package transport

import (
	"encoding/json"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/domain"
)

// IRExtensionExtractor 从客户端请求中提取非标参数。
//
// Phase 1 完整实现：只提取 IR 不处理的"非标准顶层字段" + 已知扩展 headers。
// 这些字段在 OpenAI→Anthropic→OpenAI 往返时会被 IR 丢弃，必须靠 ExtensionsBag 保留。
type IRExtensionExtractor struct{}

// NewIRExtensionExtractor 构造一个扩展属性提取器。
func NewIRExtensionExtractor() *IRExtensionExtractor { return &IRExtensionExtractor{} }

// extensionHeaders 是已知需要保留往返的请求 headers（不在 body 中的扩展属性）。
var extensionHeaders = []string{
	"anthropic-beta",
	"anthropic-version",
	"x-custom-header",
	"x-request-priority",
}

// Extract 提取扩展属性。
//
// 提取规则：
//   - body 中的非标准顶层字段 → ClientRaw（如 custom_field、extra_body 内的字段）
//   - 已知扩展 headers → Headers
func (e *IRExtensionExtractor) Extract(bodyBytes []byte, headers http.Header) (*domain.ExtensionsBag, error) {
	bag := &domain.ExtensionsBag{
		ClientRaw: make(map[string]json.RawMessage),
		Headers:   make(map[string]string),
		Custom:    make(map[string]any),
	}

	// 1. 提取 body 中的非标准顶层字段
	if len(bodyBytes) > 0 {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(bodyBytes, &raw); err == nil {
			for k, v := range raw {
				if !isStandardField(k) {
					bag.ClientRaw[k] = v
				}
			}
		}
	}

	// 2. 提取已知扩展 headers
	if headers != nil {
		for _, h := range extensionHeaders {
			if v := headers.Get(h); v != "" {
				bag.Headers[h] = v
			}
		}
	}

	return bag, nil
}

// IRExtensionRestorer 把扩展属性还原到目标 JSON 中。
type IRExtensionRestorer struct{}

// NewIRExtensionRestorer 构造一个扩展属性还原器。
func NewIRExtensionRestorer() *IRExtensionRestorer { return &IRExtensionRestorer{} }

// Restore 把 ExtensionsBag 中的扩展属性合并回 body。
//
// 合并规则：
//   - ClientRaw 中的字段，如果目标 body 没有同名 key，则写入（不覆盖已有字段）
//   - Headers 不合并进 body（headers 由 HTTP 层处理）
func (r *IRExtensionRestorer) Restore(bodyBytes []byte, extensions *domain.ExtensionsBag) ([]byte, error) {
	if extensions == nil || len(extensions.ClientRaw) == 0 {
		return bodyBytes, nil
	}

	// 解析目标 body
	var target map[string]json.RawMessage
	if len(bodyBytes) > 0 {
		if err := json.Unmarshal(bodyBytes, &target); err != nil {
			// 目标 body 不是有效 JSON 对象，无法合并，原样返回
			return bodyBytes, nil
		}
	}
	if target == nil {
		target = make(map[string]json.RawMessage)
	}

	// 合并：只写入目标不存在的字段（不覆盖 IR 已生成的标准字段）
	merged := false
	for k, v := range extensions.ClientRaw {
		if _, exists := target[k]; !exists {
			target[k] = v
			merged = true
		}
	}

	if !merged {
		return bodyBytes, nil
	}

	return json.Marshal(target)
}
