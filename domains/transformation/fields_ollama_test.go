package transformation

import "testing"

// TestOllamaFieldsInWhitelist 验证 Ollama 协议的核心字段已在白名单中。
//
// Ollama 协议的 5 个核心字段需要被识别为标准字段：
//   - keep_alive: 控制模型在内存中的保留时长
//   - format: 指定输出格式（如 "json"）
//   - context: KV cache 上下文数组（多轮对话）
//   - raw: 跳过提示词模板化
//   - template: 自定义提示词模板
func TestOllamaFieldsInWhitelist(t *testing.T) {
	ollamaFields := []string{"keep_alive", "format", "context", "raw", "template"}

	for _, field := range ollamaFields {
		if !isStandardField(field) {
			t.Errorf("Ollama field %q should be in whitelist but was not found", field)
		}
	}
}
