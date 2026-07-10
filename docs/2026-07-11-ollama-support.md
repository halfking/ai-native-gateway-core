# Ollama 协议支持文档

**日期**: 2026-07-11  
**版本**: v1.0  
**状态**: ✅ 已实现

---

## 概述

llm-gateway-go 现已支持 Ollama 协议的核心字段白名单。所有 Ollama 专有字段均可正常通过白名单检查，并通过 ExtensionsBag 机制在协议转换时保留。

---

## 支持的 Ollama 字段

| 字段名       | 类型               | 说明                                       | 示例值                          |
|-------------|-------------------|-------------------------------------------|--------------------------------|
| `keep_alive` | string/duration   | 控制模型在内存中的保留时长                    | `"5m"`, `"300s"`               |
| `format`     | string            | 指定输出格式（主要用于强制 JSON 输出）         | `"json"`                       |
| `context`    | []int             | KV cache 上下文数组，用于多轮对话状态保持      | `[1, 2, 3, ...]`               |
| `raw`        | bool              | 跳过提示词模板化，直接发送原始 prompt         | `true`, `false`                |
| `template`   | string            | 自定义提示词模板（Ollama Modelfile 格式）     | `"{{ .System }}\n{{ .Prompt }}"` |

---

## 使用示例

### 1. 强制 JSON 输出

```bash
curl -X POST http://llmgo.kxpms.cn/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $OLLAMA_API_KEY" \
  -d '{
    "model": "llama3.2",
    "messages": [{"role": "user", "content": "返回一个 JSON 格式的天气预报"}],
    "format": "json"
  }'
```

### 2. 自定义模型保留时长

```bash
curl -X POST http://llmgo.kxpms.cn/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $OLLAMA_API_KEY" \
  -d '{
    "model": "qwen2.5",
    "messages": [{"role": "user", "content": "你好"}],
    "keep_alive": "10m"
  }'
```

### 3. 多轮对话上下文保持

```bash
# 第一轮请求（保存返回的 context）
response=$(curl -s -X POST http://llmgo.kxpms.cn/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $OLLAMA_API_KEY" \
  -d '{
    "model": "llama3.2",
    "messages": [{"role": "user", "content": "我叫张三"}]
  }')

context=$(echo $response | jq -r '.context')

# 第二轮请求（传入上一轮的 context）
curl -X POST http://llmgo.kxpms.cn/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $OLLAMA_API_KEY" \
  -d "{
    \"model\": \"llama3.2\",
    \"messages\": [{\"role\": \"user\", \"content\": \"我叫什么名字？\"}],
    \"context\": $context
  }"
```

### 4. 跳过模板化（原始 prompt）

```bash
curl -X POST http://llmgo.kxpms.cn/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $OLLAMA_API_KEY" \
  -d '{
    "model": "llama3.2",
    "messages": [{"role": "user", "content": "你好"}],
    "raw": true
  }'
```

### 5. 自定义提示词模板

```bash
curl -X POST http://llmgo.kxpms.cn/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $OLLAMA_API_KEY" \
  -d '{
    "model": "llama3.2",
    "messages": [{"role": "user", "content": "写一首诗"}],
    "template": "{{ .System }}\n\n用户: {{ .Prompt }}\n\n助手:"
  }'
```

---

## 实现细节

### 白名单机制

所有 Ollama 字段已添加到 `domains/transformation/fields.go` 的 `standardRequestFields` 白名单中：

```go
// Ollama 特有
"keep_alive": true, // 模型加载时长
"format":     true, // 输出格式 (json)
"context":    true, // KV cache 上下文数组
"raw":        true, // 跳过模板化
"template":   true, // 自定义模板
```

### 协议转换保证

- **Ollama → OpenAI**: Ollama 专有字段通过 ExtensionsBag 保留，不会被丢弃
- **OpenAI → Ollama**: 可携带 Ollama 字段透传到 Ollama 后端
- **混合路由**: 支持在 OpenAI 协议请求中携带 Ollama 字段，网关自动路由到 Ollama 提供商时字段保持完整

---

## 测试覆盖

测试文件: `domains/transformation/fields_ollama_test.go`

```go
func TestOllamaFieldsInWhitelist(t *testing.T) {
    ollamaFields := []string{"keep_alive", "format", "context", "raw", "template"}
    
    for _, field := range ollamaFields {
        if !isStandardField(field) {
            t.Errorf("Ollama field %q should be in whitelist but was not found", field)
        }
    }
}
```

**测试结果**: ✅ 全部通过

---

## 兼容性

- ✅ 不影响 OpenAI 协议
- ✅ 不影响 Anthropic 协议
- ✅ 与现有 ExtensionsBag 机制无冲突
- ✅ 所有现有测试全部通过（133 个测试用例）

---

## 相关文件

| 文件路径                                           | 说明                  |
|--------------------------------------------------|---------------------|
| `domains/transformation/fields.go`               | 白名单定义文件          |
| `domains/transformation/fields_ollama_test.go`   | Ollama 字段测试        |
| `domains/transformation/extension.go`            | ExtensionsBag 实现    |

---

## 参考资料

- [Ollama API 官方文档](https://github.com/ollama/ollama/blob/main/docs/api.md)
- [Ollama Modelfile 语法](https://github.com/ollama/ollama/blob/main/docs/modelfile.md)
- [llm-gateway-go ExtensionsBag 设计](./architecture-decisions.md#extensionsbag)

---

## 更新日志

### v1.0 (2026-07-11)

- ✅ 添加 5 个 Ollama 核心字段到白名单
- ✅ 创建专用测试 `fields_ollama_test.go`
- ✅ 验证所有现有测试通过（133 个用例）
- ✅ 文档化使用示例和实现细节
