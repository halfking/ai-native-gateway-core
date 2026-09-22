# 方言解析（Dialect Resolution）

## 概述

网关需要根据 provider candidate 的 catalog code + protocol 决定使用哪个方言。

## 解析逻辑

```go
// internal/paramreg/dialect.go
func Resolve(catalogCode, protocol string) Dialect {
    // 1. 优先按 catalog code 解析
    if d := DialectForCatalogCode(catalogCode); d != DialectUnknown {
        return d
    }
    // 2. 回退到按 protocol 解析
    return DialectForProtocol(protocol)
}
```

## Catalog Code → Dialect 映射

| Catalog Code | Dialect | Base Protocol |
|--------------|---------|---------------|
| `openai` | `DialectOpenAIChat` | openai-chat |
| `azure-openai` | `DialectOpenAIChat` | openai-chat |
| `anthropic` | `DialectAnthropic` | anthropic-messages |
| `google-gemini` | `DialectGemini` | gemini-generate |
| `vertex-ai` | `DialectGemini` | gemini-generate |
| `deepseek` | `DialectDeepSeek` | openai-chat |
| `qwen` | `DialectQwen` | openai-chat |
| `dashscope` | `DialectQwen` | openai-chat |
| `aliyun` | `DialectQwen` | openai-chat |
| `zhipu` | `DialectGLM` | openai-chat |
| `glm` | `DialectGLM` | openai-chat |
| `bigmodel` | `DialectGLM` | openai-chat |
| `zai` | `DialectGLM` | openai-chat |
| `minimax` | `DialectMiniMax` | openai-chat |
| `moonshot` | `DialectKimi` | openai-chat |
| `kimi` | `DialectKimi` | openai-chat |
| `volcengine` | `DialectArk` | openai-chat |
| `ark` | `DialectArk` | openai-chat |
| `doubao` | `DialectArk` | openai-chat |
| `xai` | `DialectGrok` | openai-chat |
| `grok` | `DialectGrok` | openai-chat |
| `mistral` | `DialectMistral` | openai-chat |
| `openrouter` | `DialectOpenRouter` | openai-chat |
| `vllm` | `DialectVLLM` | openai-chat |
| `ollama` | `DialectOllama` | openai-chat |
| `llamacpp` | `DialectOpenAIChat` | openai-chat |
| `mlx` | `DialectOpenAIChat` | openai-chat |
| `lmstudio` | `DialectOpenAIChat` | openai-chat |
| `siliconflow` | `DialectOpenAIChat` | openai-chat |
| `groq` | `DialectOpenAIChat` | openai-chat |
| `fireworks` | `DialectOpenAIChat` | openai-chat |
| `together` | `DialectOpenAIChat` | openai-chat |
| `nvidia` | `DialectOpenAIChat` | openai-chat |
| `perplexity` | `DialectOpenAIChat` | openai-chat |
| `cohere` | `DialectOpenAIChat` | openai-chat |
| `github-copilot` | `DialectOpenAIChat` | openai-chat |
| `xiaomi` | `DialectOpenAIChat` | openai-chat |
| `evol` | `DialectOpenAIChat` | openai-chat |

## Protocol → Dialect 映射（回退）

| Protocol | Dialect |
|----------|---------|
| `openai-chat` | `DialectOpenAIChat` |
| `openai-responses` | `DialectResponses` |
| `anthropic-messages` | `DialectAnthropic` |
| `gemini-generate` | `DialectGemini` |
| `auto` | `DialectUnknown` |
| `manifest` | `DialectUnknown` |

## Dialect 继承关系

```go
var parentProtocol = map[Dialect]Dialect{
    DialectOpenAIChat: DialectUnknown,  // 基础协议
    DialectResponses: DialectUnknown,  // 基础协议
    DialectAnthropic: DialectUnknown,  // 基础协议
    DialectGemini: DialectUnknown,  // 基础协议
    DialectDeepSeek: DialectOpenAIChat,  // 继承 OpenAI
    DialectQwen: DialectOpenAIChat,
    DialectGLM: DialectOpenAIChat,
    DialectMiniMax: DialectOpenAIChat,
    DialectKimi: DialectOpenAIChat,
    DialectArk: DialectOpenAIChat,
    DialectGrok: DialectOpenAIChat,
    DialectMistral: DialectOpenAIChat,
    DialectOpenRouter: DialectOpenAIChat,
    DialectVLLM: DialectOpenAIChat,
    DialectOllama: DialectOpenAIChat,
}
```

继承语义：

1. **字段共享**：DeepSeek 的字段定义可以引用 OpenAI Chat 的字段。
2. **方言专属**：每个方言可以添加自己的私有字段（如 DeepSeek 的 `reasoning_effort`）。
3. **守卫规则**：paramreg 在序列化时按方言决定哪些字段保留/删除。

## 决策示例

### 场景 1：客户端发 OpenAI Chat，请求 DeepSeek 模型

```go
cand.CatalogCode = "deepseek"
cand.Protocol = "openai-chat"

dialect := paramreg.Resolve("deepseek", "openai-chat")
// 返回 DialectDeepSeek

// 出站序列化
bodyBytes = SerializeOpenAI(req)
// → OpenAI Chat 格式（DeepSeek 兼容）
bodyBytes = paramguard.Apply(bodyBytes, DialectDeepSeek)
// → 保留 reasoning_effort，删除不支持字段
```

### 场景 2：客户端发 Anthropic，请求 OpenAI 模型

```go
cand.CatalogCode = "openai"
cand.Protocol = "openai-chat"

dialect := paramreg.Resolve("openai", "openai-chat")
// 返回 DialectOpenAIChat

// 出站序列化
bodyBytes = SerializeOpenAI(req)
// → OpenAI Chat 格式
bodyBytes = paramguard.Apply(bodyBytes, DialectOpenAIChat)
// → 标准 OpenAI 字段守卫
```

### 场景 3：客户端发 OpenAI Responses，请求 Gemini

```go
cand.CatalogCode = "google-gemini"
cand.Protocol = "gemini-generate"

dialect := paramreg.Resolve("google-gemini", "gemini-generate")
// 返回 DialectGemini

// 出站序列化
bodyBytes = SerializeGemini(req)
// → Gemini generateContent 格式
bodyBytes = paramguard.Apply(bodyBytes, DialectGemini)
// → 删除内置工具（web_search/file_search/code_interpreter）
```

## DialectUnknown 的处理

```go
func Resolve(catalogCode, protocol string) Dialect {
    if d := DialectForCatalogCode(catalogCode); d != DialectUnknown {
        return d
    }
    return DialectForProtocol(protocol)
}
```

当返回 `DialectUnknown` 时：

- `paramreg.decide` 走最宽松路径（放行所有字段）
- `paramguard.Apply` 不做字段删除
- 上游 HTTP 400 时由网关错误处理兜底

**潜在风险**：

- 未知方言可能接受 IR 不支持的字段，导致上游 400
- 应在 catalog 配置阶段确保每个 catalog code 都有方言映射
