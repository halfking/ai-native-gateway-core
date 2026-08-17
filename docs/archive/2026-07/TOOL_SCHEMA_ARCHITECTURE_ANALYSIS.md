---
archived_from: (legacy) docs/archive/2026-07/TOOL_SCHEMA_ARCHITECTURE_ANALYSIS.md
archived_at: 2026-08-17
archived_by: docs-archive remediate v1.0
backup_ts: 20260817-190919
status: archived
note: legacy archive, frontmatter retroactively added
---

# LLM Gateway Tool Schema 架构分析报告

## 问题背景

在使用 claude-opus-4-8 模型时，遇到错误：
```
Error code: 400 - {'type': 'error', 'error': {'type': 'invalid_request_error', 'message': 'tools.0.input_schema.required: must be an array'}}
```

这表明从客户端发送的工具定义中，`input_schema.required` 字段的格式不正确（应该是数组但实际不是）。

## 架构分析

### 1. 请求处理流程

#### 1.1 入口层 (domains/streaming/executors/)
- **executor.go**: 主执行器，根据上游协议分发到不同的处理器
- **executor_anthropic.go**: Anthropic 协议专用执行器
  - `executeAnthropic()`: 主执行函数
  - `prepareAnthropicRequestBody()`: 准备请求体，包含格式转换

#### 1.2 协议转换层

**OpenAI → Anthropic 转换路径:**

1. **内部表示(IR)方式** (推荐，当 `e.IR` 已设置时):
   ```
   客户端 OpenAI 请求
     ↓
   internal/ir/parse_openai.go: ParseOpenAI()
     ↓
   InternalRequest (统一内部表示)
     ↓
   internal/ir/serialize_anthropic.go: SerializeAnthropic()
     ↓
   Anthropic 请求
   ```

2. **传统方式** (当 IR 未设置时):
   ```
   客户端 OpenAI 请求
     ↓
   domains/transformation/anthropic/chat_to_anthropic.go: ConvertChatRequestToAnthropic()
     ↓
   Anthropic 请求
   ```

### 2. Tool 定义转换逻辑

#### 2.1 核心转换函数

**文件**: `domains/transformation/anthropic/chat_to_anthropic.go`

**关键函数**:
```go
// 行 255-282: openAIToolToAnthropic
func openAIToolToAnthropic(tool map[string]any) (map[string]any, bool) {
    normalized := normalizeOpenAIToolDefinitions([]any{tool})
    // ...
    function, _ := toolMap["function"].(map[string]any)
    
    anthropicTool := map[string]any{"name": name}
    if description, ok := function["description"].(string); ok && description != "" {
        anthropicTool["description"] = description
    }
    
    // ⚠️ 关键转换点：parameters → input_schema
    if parameters, ok := function["parameters"]; ok {
        anthropicTool["input_schema"] = parameters  // 直接赋值，未验证 required 字段格式
    } else {
        anthropicTool["input_schema"] = map[string]any{"type": "object", "properties": map[string]any{}}
    }
    return anthropicTool, true
}
```

**文件**: `domains/streaming/tools_normalize.go`

**关键函数**:
```go
// 行 56-83: OpenAIToolToAnthropic (功能相同)
func OpenAIToolToAnthropic(tool map[string]any) (map[string]any, bool) {
    // ... 相同的转换逻辑
    if p, ok := fn["parameters"]; ok {
        anth["input_schema"] = p  // 直接赋值
    } else {
        anth["input_schema"] = map[string]any{"type": "object", "properties": map[string]any{}}
    }
    return anth, true
}
```

#### 2.2 Tool 规范化逻辑

**文件**: `domains/transformation/anthropic/chat_to_anthropic.go`

```go
// 行 204-253: normalizeOpenAIToolDefinitions
func normalizeOpenAIToolDefinitions(tools []any) []any {
    // 支持三种工具定义格式：
    // 1. OpenAI 标准格式: {type: "function", function: {name, parameters}}
    // 2. Anthropic 格式: {name, input_schema}
    // 3. 混合格式: {name, parameters}
    
    // ⚠️ 对于 input_schema 格式，直接复制为 parameters
    if schema, hasSchema := tool["input_schema"]; hasSchema {
        function := map[string]any{"name": name}
        if schema != nil {
            function["parameters"] = schema  // 直接使用，未验证
        }
        // ...
    }
}
```

### 3. IR (Internal Representation) 路径

#### 3.1 解析 Anthropic 工具定义

**文件**: `internal/ir/parse_anthropic.go`

```go
// 行 485-517: parseAnthropicTools
func parseAnthropicTools(raw json.RawMessage) ([]ToolDefinition, error) {
    // ...
    if s := marshalAnyToRaw(tool["input_schema"]); s != nil {
        td.Parameters = s  // 存储为 RawMessage，未验证内部结构
    } else {
        td.Parameters = marshalAnyToRaw(tool["parameters"])
    }
    // ...
}
```

#### 3.2 序列化为 Anthropic 格式

**文件**: `internal/ir/serialize_anthropic.go`

```go
// 行 513-529: serializeAnthropicTools
func serializeAnthropicTools(tools []ToolDefinition) []map[string]any {
    for _, tool := range tools {
        toolMap := map[string]any{"name": tool.Name}
        if tool.Description != "" {
            toolMap["description"] = tool.Description
        }
        if tool.Parameters != nil {
            toolMap["input_schema"] = tool.Parameters  // 直接输出，未验证
        }
        result = append(result, toolMap)
    }
    return result
}
```

### 4. 问题根因分析

**核心问题**: 整个工具转换链路中，**没有任何地方验证或修正 `input_schema.required` 字段的格式**。

#### 4.1 可能的问题来源

1. **客户端发送错误格式**:
   ```json
   {
     "type": "function",
     "function": {
       "name": "get_weather",
       "parameters": {
         "type": "object",
         "properties": {...},
         "required": "city"  // ❌ 错误：应该是 ["city"]
       }
     }
   }
   ```

2. **转换过程未验证**:
   - `openAIToolToAnthropic()` 直接复制 `parameters` 为 `input_schema`
   - 没有检查 `required` 是否为数组类型

3. **claude-opus-4-8 验证更严格**:
   - 旧版本 Claude 可能容忍错误格式
   - opus-4-8 严格按照 JSON Schema 规范验证

#### 4.2 验证缺失的位置

✅ **有验证的地方**:
- `domains/streaming/executors/request_validator.go`: 验证请求基本结构（model, messages）
- `domains/transformation/anthropic_message_fix.go`: 修复消息序列（role 转换）
- `internal/ir/serialize_anthropic.go`: 行 54-58 验证 tool_call 完整性

❌ **缺少验证的地方**:
- Tool 定义的 JSON Schema 格式验证
- `input_schema.required` 字段类型验证
- `input_schema.properties` 结构验证

### 5. 数据流追踪

```
客户端请求 (可能包含错误的 required 字段)
  ↓
domains/streaming/handler.go
  ↓
domains/streaming/executors/executor.go: Execute()
  ↓
domains/streaming/executors/executor_anthropic.go: executeAnthropic()
  ↓
domains/streaming/executors/executor_anthropic.go: prepareAnthropicRequestBody()
  ↓
[分支A: 使用 IR]
  internal/ir/parse_openai.go: ParseOpenAI()
    → parseOpenAITools() 解析 tools (未验证 required 格式)
  ↓
  internal/ir/serialize_anthropic.go: SerializeAnthropic()
    → serializeAnthropicTools() 输出 (未验证 required 格式)
  ↓
[分支B: 传统转换]
  domains/transformation/anthropic/chat_to_anthropic.go: ConvertChatRequestToAnthropic()
    → openAIToolToAnthropic() (未验证 required 格式)
  ↓
domains/streaming/tools_normalize.go: SanitizeAnthropicToolsInBody()
  → SanitizeAnthropicToolDefinitions() (未验证 required 格式)
  ↓
domains/transformation/anthropic_message_fix.go: FixAnthropicMessages()
  (仅处理消息序列，不处理 tools)
  ↓
发送到上游 Anthropic API
  ↓
❌ 400 Error: tools.0.input_schema.required: must be an array
```

### 6. 修复建议

#### 6.1 短期修复（最小侵入）

在 `openAIToolToAnthropic()` 和 `OpenAIToolToAnthropic()` 函数中添加验证：

```go
func openAIToolToAnthropic(tool map[string]any) (map[string]any, bool) {
    // ... 现有代码 ...
    
    if parameters, ok := function["parameters"]; ok {
        // 验证并修正 input_schema
        anthropicTool["input_schema"] = sanitizeJSONSchema(parameters)
    } else {
        anthropicTool["input_schema"] = map[string]any{"type": "object", "properties": map[string]any{}}
    }
    return anthropicTool, true
}

func sanitizeJSONSchema(schema any) any {
    schemaMap, ok := schema.(map[string]any)
    if !ok {
        return schema
    }
    
    // 修正 required 字段
    if required, exists := schemaMap["required"]; exists {
        switch r := required.(type) {
        case string:
            // 单个字符串 → 数组
            schemaMap["required"] = []string{r}
        case []any:
            // 转换为 []string
            strArray := make([]string, 0, len(r))
            for _, v := range r {
                if s, ok := v.(string); ok {
                    strArray = append(strArray, s)
                }
            }
            schemaMap["required"] = strArray
        case []string:
            // 已经是正确格式
        default:
            // 删除无效的 required 字段
            delete(schemaMap, "required")
        }
    }
    
    return schemaMap
}
```

#### 6.2 中期修复（IR 层统一处理）

在 `internal/ir/parse_openai.go` 的 `parseOpenAITools()` 中添加验证：

```go
func parseOpenAITools(raw json.RawMessage) ([]ToolDefinition, error) {
    // ... 现有代码 ...
    
    // 解析并验证 parameters
    if paramsRaw := marshalAnyToRaw(fn["parameters"]); paramsRaw != nil {
        validatedParams, err := validateAndFixJSONSchema(paramsRaw)
        if err != nil {
            return nil, fmt.Errorf("invalid tool schema for %s: %w", name, err)
        }
        td.Parameters = validatedParams
    }
    // ...
}
```

#### 6.3 长期修复（架构改进）

1. **添加专门的 Schema 验证层**:
   - 创建 `internal/schema/validator.go`
   - 实现完整的 JSON Schema Draft 7 验证
   - 在请求入口处验证

2. **增强错误信息**:
   - 检测到错误时，返回详细的字段路径和修复建议
   - 记录到结构化日志

3. **添加单元测试**:
   - 测试各种错误的 required 格式
   - 测试边界情况（null, undefined, 空数组等）

### 7. 测试用例

#### 7.1 错误输入示例

```json
{
  "model": "claude-opus-4-8",
  "messages": [...],
  "tools": [{
    "type": "function",
    "function": {
      "name": "get_weather",
      "parameters": {
        "type": "object",
        "properties": {
          "city": {"type": "string"}
        },
        "required": "city"  // ❌ 应该是 ["city"]
      }
    }
  }]
}
```

#### 7.2 预期转换结果

```json
{
  "model": "claude-opus-4-8",
  "messages": [...],
  "tools": [{
    "name": "get_weather",
    "input_schema": {
      "type": "object",
      "properties": {
        "city": {"type": "string"}
      },
      "required": ["city"]  // ✅ 修正为数组
    }
  }]
}
```

### 8. 相关文件清单

#### 核心转换文件
- `domains/transformation/anthropic/chat_to_anthropic.go` (第 255-282 行)
- `domains/streaming/tools_normalize.go` (第 56-83 行)
- `internal/ir/parse_openai.go` (第 177-220 行)
- `internal/ir/serialize_anthropic.go` (第 513-529 行)

#### 执行器文件
- `domains/streaming/executors/executor_anthropic.go` (第 376-501 行)
- `domains/streaming/executors/executor.go`

#### 验证文件
- `domains/streaming/executors/request_validator.go`
- `domains/streaming/executors/inline_validation.go`

## 总结

1. **根本原因**: 工具定义转换链路中缺少 JSON Schema 格式验证，特别是 `required` 字段类型验证
2. **影响范围**: 所有使用工具的 OpenAI → Anthropic 转换路径
3. **修复优先级**: 高（影响 claude-opus-4-8 等严格验证的模型）
4. **建议方案**: 在转换函数中添加 `sanitizeJSONSchema()` 函数，确保 `required` 字段始终为字符串数组

---
生成时间: 2026-07-17
分析工具: Claude Code Analysis
