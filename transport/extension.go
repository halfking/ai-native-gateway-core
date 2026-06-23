package transport

import (
	"encoding/json"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/domain"
)

// IRExtensionExtractor 从客户端请求中提取非标参数。
//
// 简单实现：从 JSON body 中提取所有非标准字段，存入 ClientRaw。
// 完整实现（Phase 1）会按协议差异做精细提取（extra_body / anthropic-beta 等）。
type IRExtensionExtractor struct{}

// Extract 提取扩展属性。
func (e *IRExtensionExtractor) Extract(bodyBytes []byte, headers http.Header) (*domain.ExtensionsBag, error) {
	bag := &domain.ExtensionsBag{
		ClientRaw: make(map[string]json.RawMessage),
		Headers:   make(map[string]string),
		Custom:    make(map[string]any),
	}

	if len(bodyBytes) > 0 {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(bodyBytes, &raw); err == nil {
			for k, v := range raw {
				bag.ClientRaw[k] = v
			}
		}
	}

	// 提取已知扩展 headers
	if headers != nil {
		for _, h := range []string{"anthropic-beta", "x-custom-header"} {
			if v := headers.Get(h); v != "" {
				bag.Headers[h] = v
			}
		}
	}

	return bag, nil
}

// IRExtensionRestorer 把扩展属性还原到响应体中。
type IRExtensionRestorer struct{}

// Restore 把 ExtensionsBag 合并到响应 JSON 中。
func (r *IRExtensionRestorer) Restore(bodyBytes []byte, extensions *domain.ExtensionsBag) ([]byte, error) {
	if extensions == nil || extensions.IsZero() {
		return bodyBytes, nil
	}
	// Phase 1 完整实现：把 ClientRaw 合并回响应体
	// 当前 MVP：直接返回原 body（避免破坏响应结构）
	return bodyBytes, nil
}
