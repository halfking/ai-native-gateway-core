# vLLM 自托管方言规范

## 概述

- Base protocol: `openai-chat`（兼容模式）
- 方言: `DialectVLLM`
- 官方文档: https://docs.vllm.ai/en/latest/serving/openai_compatible_server.html
- 端点: `POST /v1/chat/completions`
- catalog code: `vllm`

## 兼容性

vLLM 提供 OpenAI 兼容的 HTTP 服务，支持 Chat Completions API。

## 与 OpenAI 的差异

### 1. `guided_choice` / `guided_regex` / `guided_grammar`

```json
{
  "guided_choice": ["positive", "negative", "neutral"],
  "guided_regex": "answer\\s*is\\s*\\d+",
  "guided_grammar": "..."
}
```

通过结构化输出引导模型按指定词表/正则/语法生成。

### 2. `top_k` 参数

```json
{"top_k": 40}
```

vLLM 在采样时支持 `top_k`。

### 3. `repetition_penalty`

```json
{"repetition_penalty": 1.1}
```

防止重复生成的惩罚系数。

### 4. `min_p`

```json
{"min_p": 0.05}
```

最小概率阈值采样。

### 5. `use_beam_search`

```json
{"use_beam_search": true}
```

启用 beam search（与 `temperature` 不兼容）。

### 6. 函数调用

与 OpenAI 一致，但需要模型本身支持 tool use。

### 7. LoRA 适配器

```
POST /v1/chat/completions
Headers:
  x-vllm-lora-adapter: my-lora
```

通过 HTTP header 切换 LoRA 适配器。

### 8. 多模态

vLLM 支持 LLaVA、Qwen-VL 等多模态模型：

```json
{
  "content": [
    {"type": "text", "text": "..."},
    {"type": "image_url", "image_url": {"url": "data:image/png;base64,..."}}
  ]
}
```

### 9. 错误码

vLLM 一般直接透传底层错误：

```json
{"error": {"message": "...", "type": "...", "code": 400}}
```

## IR 映射

| vLLM 字段 | IR 字段 |
|-----------|---------|
| `guided_choice` | `Request.GuidedChoice` |
| `guided_regex` | `Request.GuidedRegex` |
| `guided_grammar` | `Request.GuidedGrammar` |
| `top_k` | `Request.TopK` |
| `repetition_penalty` | `Request.RepetitionPenalty` |
| `min_p` | `Request.MinP` |
| `use_beam_search` | `Request.UseBeamSearch` |
| `x-vllm-lora-adapter` (header) | `Request.LoraAdapter` |

## 序列化注意

1. 引导参数（guided_*）是 vLLM 私有，仅在 vLLM 方言时输出。
2. LoRA 适配器需要通过 HTTP header 透传，IR 中存储到 `Request.LoraAdapter`，序列化时插入 header。
3. `top_k` 冲突时（Anthropic 也用 top_k），需保证 IR 字段能正确转换。
