# LangChain / LangGraph 请求格式要求

## 概述

LangChain 通过不同的 Chat Model 类接入不同的协议：

- `ChatOpenAI` → OpenAI Chat Completions
- `ChatAnthropic` → Anthropic Messages
- `ChatGoogleGenerativeAI` → Gemini generateContent

## LangChain 入站格式（按 Chat Model 类型）

### ChatOpenAI 配置

```python
from langchain_openai import ChatOpenAI
llm = ChatOpenAI(
    model="gpt-4o",
    api_key="any-string",
    base_url="https://gateway.example.com/v1",
)
```

请求符合 OpenAI Chat Completions 协议（见 [openai-sdk.md](openai-sdk.md)）。

### ChatAnthropic 配置

```python
from langchain_anthropic import ChatAnthropic
llm = ChatAnthropic(
    model="claude-3-5-sonnet-20241022",
    api_key="any-string",
    base_url="https://gateway.example.com",
)
```

请求符合 Anthropic Messages 协议（见 [anthropic-sdk.md](anthropic-sdk.md)）。

### ChatGoogleGenerativeAI 配置

```python
from langchain_google_genai import ChatGoogleGenerativeAI
llm = ChatGoogleGenerativeAI(
    model="gemini-1.5-pro",
    google_api_key="any-string",
)
```

请求符合 Gemini generateContent 协议（见 [../vendor-formats/gemini.md](../vendor-formats/gemini.md)）。

## LangChain 的特殊请求模式

### 1. Tool binding

```python
from langchain.tools import tool
@tool
def get_weather(location: str) -> str:
    """Get weather"""
    return f"sunny in {location}"

llm_with_tools = llm.bind_tools([get_weather])
```

LangChain 自动把 Python 函数签名转换为 tool schema。

### 2. Tool calling 响应解析

LangChain 期望 SDK 把上游的 tool_calls 解析为结构化的 ToolCall 对象：

```python
response = llm.invoke("What's the weather in SF?")
# response.tool_calls = [{"name": "get_weather", "args": {"location": "SF"}, "id": "call_xxx"}]
```

### 3. Structured output

```python
from pydantic import BaseModel

class Weather(BaseModel):
    location: str
    temperature: float

structured_llm = llm.with_structured_output(Weather)
```

LangChain 会自动设置 `response_format`。

### 4. 流式响应

LangChain 的 `stream()` / `astream()` 把上游 SSE/JSON 流转换为 `AIMessageChunk` 流：

```python
async for chunk in llm.astream("Hello"):
    print(chunk.content)
```

### 5. Multi-modal

LangChain 通过 `HumanMessage(content=[...])` 支持多模态：

```python
from langchain_core.messages import HumanMessage
msg = HumanMessage(content=[
    {"type": "text", "text": "What's in this image?"},
    {"type": "image_url", "image_url": {"url": "..."}}
])
```

## 网关对 LangChain 的特殊支持

### 1. tool_calls.id 完整性

LangChain 通过 `id` 字段把 tool_calls 与 ToolMessage 对应起来。网关必须保留上游的 tool_call_id，不能伪造或丢失。

### 2. Structured Output 转换

当上游不支持 `response_format` 时（如 Anthropic），网关需要：

- 客户端传 `response_format` → IR 记录
- 目标方言是 OpenAI → 透传 `response_format`
- 目标方言是 Anthropic → 注入 prompt 强制 JSON 输出 + 后处理
- 目标方言是 Gemini → 使用 `generationConfig.responseMimeType: "application/json"`

### 3. Tool Schema 兼容性

不同厂商的 tool schema 字段不同：

| 厂商 | 字段 |
|------|------|
| OpenAI | `parameters` |
| Anthropic | `input_schema` |
| Gemini | `parameters`（在 functionDeclaration 内） |

IR 需要在序列化时按目标方言重命名。

## 不兼容警告

- ❌ LangChain `with_structured_output`：非 OpenAI 厂商需要后处理
- ❌ LangChain 的某些 callback hook：流式 chunk 格式必须严格匹配
- ✅ `bind_tools`：所有厂商支持
- ✅ multi-modal：所有厂商支持
