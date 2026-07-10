package transformation

import "testing"

// TestOllamaFieldsNotInWhitelist 验证 Ollama 协议的核心字段不在标准字段白名单中，
// 而是通过 Extensions 机制透传。
//
// Ollama 协议的 5 个核心字段需要被 Extensions 机制捕获并透传：
//   - keep_alive: 控制模型在内存中的保留时长
//   - format: 指定输出格式（如 "json"）
//   - context: KV cache 上下文数组（多轮对话）
//   - raw: 跳过提示词模板化
//   - template: 自定义提示词模板
//
// 这些字段不在 standardRequestFields 中，因此 isStandardField() 返回 false，
// 触发 IRExtensionExtractor 提取到 Extensions，实现透传。
func TestOllamaFieldsNotInWhitelist(t *testing.T) {
	ollamaFields := []string{"keep_alive", "format", "context", "raw", "template"}

	for _, field := range ollamaFields {
		if isStandardField(field) {
			t.Errorf("Ollama field %q should NOT be in standard fields (should use Extensions), but was found", field)
		}
	}
}
