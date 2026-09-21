# 任务4: IR层测试强化 - 详细提示词

## 任务4.1: Fuzzing测试实现

### 执行提示词

```markdown
# 任务：为IR层添加Fuzzing测试

## 背景
IR层负责解析和转换多种LLM协议（OpenAI、Anthropic、Gemini、Vertex等），需要处理各种边界情况和畸形输入。Fuzzing测试可以自动发现：
- Panic和崩溃
- 内存泄漏
- 解析错误
- 数据丢失

## Go Fuzzing基础

Go 1.18+内置fuzzing支持，语法：

```go
func FuzzMyFunction(f *testing.F) {
    // 添加种子语料
    f.Add("valid input 1")
    f.Add("valid input 2")
    
    // Fuzz目标
    f.Fuzz(func(t *testing.T, input string) {
        result := MyFunction(input)
        // 断言不变量
        if result == nil && input != "" {
            t.Error("unexpected nil for non-empty input")
        }
    })
}
```

运行：
```bash
# 运行fuzzing（默认无限期）
go test -fuzz=FuzzMyFunction

# 限制时间
go test -fuzz=FuzzMyFunction -fuzztime=30s

# 并行度
go test -fuzz=FuzzMyFunction -parallel=8

# 查看语料库
ls testdata/fuzz/FuzzMyFunction/
```

## 实施步骤

### 1. 项目结构

```
internal/ir/
├── parse_openai.go
├── parse_openai_test.go
├── parse_openai_fuzz_test.go    # 新增
├── parse_anthropic.go
├── parse_anthropic_test.go
├── parse_anthropic_fuzz_test.go # 新增
├── serialize_openai.go
├── serialize_openai_fuzz_test.go # 新增
└── testdata/
    └── fuzz/
        ├── FuzzParseOpenAIRequest/
        ├── FuzzParseAnthropicRequest/
        └── FuzzSerializeOpenAI/
```

### 2. OpenAI解析器Fuzzing

**文件**: `internal/ir/parse_openai_fuzz_test.go`

```go
//go:build go1.18
// +build go1.18

package ir

import (
    "encoding/json"
    "testing"
)

// FuzzParseOpenAIRequest 测试OpenAI请求解析的鲁棒性
func FuzzParseOpenAIRequest(f *testing.F) {
    // 添加有效的种子语料
    f.Add([]byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`))
    f.Add([]byte(`{"model":"gpt-3.5-turbo","messages":[{"role":"system","content":"You are helpful"},{"role":"user","content":"Hi"}],"temperature":0.7}`))
    f.Add([]byte(`{"model":"gpt-4","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"https://example.com/img.jpg"}}]}]}`))
    
    // 添加边界情况
    f.Add([]byte(`{}`))  // 空对象
    f.Add([]byte(`{"model":""}`))  // 空model
    f.Add([]byte(`{"model":"gpt-4","messages":[]}`))  // 空messages
    f.Add([]byte(`{"model":"gpt-4","messages":[{"role":"","content":""}]}`))  // 空role/content
    
    // 添加畸形JSON
    f.Add([]byte(`{"model":"gpt-4","messages":[{`))  // 不完整
    f.Add([]byte(`{"model":"gpt-4","messages":null}`))  // null值
    
    f.Fuzz(func(t *testing.T, data []byte) {
        // 基本不变量：解析器不应panic
        defer func() {
            if r := recover(); r != nil {
                t.Errorf("ParseOpenAIRequest panicked: %v\nInput: %s", r, string(data))
            }
        }()
        
        // 尝试解析
        ir, err := ParseOpenAIRequest(data)
        
        // 不变量1: 如果输入是有效JSON，不应返回错误（即使字段缺失）
        var raw map[string]interface{}
        if json.Unmarshal(data, &raw) == nil {
            // 有效JSON，某些字段可能缺失但不应panic
            
            // 不变量2: 如果解析成功，模型名不应为空
            if err == nil && ir != nil {
                if ir.Model == "" {
                    t.Errorf("parsed IR has empty model\nInput: %s", string(data))
                }
                
                // 不变量3: 如果有messages，长度应>0
                if len(ir.Messages) == 0 && raw["messages"] != nil {
                    t.Errorf("messages lost during parsing\nInput: %s", string(data))
                }
                
                // 不变量4: 温度应在有效范围内
                if ir.Temperature != nil {
                    temp := *ir.Temperature
                    if temp < 0 || temp > 2 {
                        t.Errorf("temperature out of range: %f\nInput: %s", temp, string(data))
                    }
                }
                
                // 不变量5: MaxTokens如果存在应>0
                if ir.MaxTokens != nil && *ir.MaxTokens <= 0 {
                    t.Errorf("invalid MaxTokens: %d\nInput: %s", *ir.MaxTokens, string(data))
                }
            }
        }
        
        // 不变量6: 错误应该是描述性的，不是泛泛的"parse failed"
        if err != nil {
            errStr := err.Error()
            if errStr == "" || errStr == "error" {
                t.Errorf("non-descriptive error: %v\nInput: %s", err, string(data))
            }
        }
    })
}

// FuzzParseOpenAIRequestMessages 专门测试消息解析
func FuzzParseOpenAIRequestMessages(f *testing.F) {
    // 文本消息
    f.Add([]byte(`[{"role":"user","content":"hello"}]`))
    
    // 多模态消息
    f.Add([]byte(`[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"http://x.com/a.jpg"}}]}]`))
    
    // 工具调用
    f.Add([]byte(`[{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"NYC\"}"}}]}]`))
    
    // 边界情况
    f.Add([]byte(`[]`))  // 空数组
    f.Add([]byte(`[{}]`))  // 空消息对象
    f.Add([]byte(`[{"role":"user"}]`))  // 缺少content
    f.Add([]byte(`[{"content":"hello"}]`))  // 缺少role
    
    f.Fuzz(func(t *testing.T, data []byte) {
        defer func() {
            if r := recover(); r != nil {
                t.Errorf("message parsing panicked: %v\nInput: %s", r, string(data))
            }
        }()
        
        var rawMessages []map[string]interface{}
        if err := json.Unmarshal(data, &rawMessages); err != nil {
            return  // 无效JSON，跳过
        }
        
        // 构造完整请求
        fullReq := map[string]interface{}{
            "model":    "gpt-4",
            "messages": rawMessages,
        }
        reqBytes, _ := json.Marshal(fullReq)
        
        ir, err := ParseOpenAIRequest(reqBytes)
        if err != nil {
            return  // 解析失败是可以的
        }
        
        // 不变量：消息数量应该匹配（或合理缩减）
        if len(ir.Messages) > len(rawMessages) {
            t.Errorf("IR has more messages than input: IR=%d, Input=%d", len(ir.Messages), len(rawMessages))
        }
        
        // 检查每条消息
        for i, msg := range ir.Messages {
            // 不变量：role不应为空
            if msg.Role == "" {
                t.Errorf("message[%d] has empty role", i)
            }
            
            // 不变量：Content存在时不应为nil
            if msg.Content != nil {
                // 文本content不应为空字符串（除非原始就是）
                if len(msg.Content.Parts) == 0 && rawMessages[i]["content"] != nil {
                    rawContent := rawMessages[i]["content"]
                    if str, ok := rawContent.(string); ok && str != "" {
                        t.Errorf("message[%d] lost non-empty content", i)
                    }
                }
            }
        }
    })
}

// FuzzParseOpenAIRequestWithExtensions 测试扩展字段处理
func FuzzParseOpenAIRequestWithExtensions(f *testing.F) {
    // 包含未知字段
    f.Add([]byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"custom_field":"value","nested":{"unknown":123}}`))
    
    f.Fuzz(func(t *testing.T, data []byte) {
        defer func() {
            if r := recover(); r != nil {
                t.Errorf("parsing with extensions panicked: %v", r)
            }
        }()
        
        var raw map[string]interface{}
        if json.Unmarshal(data, &raw) != nil {
            return
        }
        
        ir, err := ParseOpenAIRequest(data)
        if err != nil || ir == nil {
            return
        }
        
        // 不变量：未知字段应该保留在Extensions中
        // 检查是否有非标准字段
        standardFields := map[string]bool{
            "model": true, "messages": true, "temperature": true,
            "max_tokens": true, "stream": true, "tools": true,
            "tool_choice": true, "frequency_penalty": true,
            "presence_penalty": true, "top_p": true, "n": true,
            "stop": true, "user": true, "response_format": true,
        }
        
        for key := range raw {
            if !standardFields[key] {
                // 应该在Extensions中
                if ir.Extensions == nil || ir.Extensions[key] == nil {
                    t.Errorf("extension field %q not preserved", key)
                }
            }
        }
    })
}
```

### 3. Anthropic解析器Fuzzing

**文件**: `internal/ir/parse_anthropic_fuzz_test.go`

```go
package ir

import (
    "encoding/json"
    "testing"
)

func FuzzParseAnthropicRequest(f *testing.F) {
    // Claude特定格式
    f.Add([]byte(`{"model":"claude-3-opus-20240229","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`))
    f.Add([]byte(`{"model":"claude-3-sonnet-20240229","max_tokens":2048,"messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"..."}}]}]}`))
    
    // system作为顶级字段
    f.Add([]byte(`{"model":"claude-3-opus-20240229","max_tokens":1024,"system":"You are helpful","messages":[{"role":"user","content":"hi"}]}`))
    
    f.Fuzz(func(t *testing.T, data []byte) {
        defer func() {
            if r := recover(); r != nil {
                t.Errorf("ParseAnthropicRequest panicked: %v\nInput: %s", r, string(data))
            }
        }()
        
        ir, err := ParseAnthropicRequest(data)
        
        var raw map[string]interface{}
        if json.Unmarshal(data, &raw) == nil {
            // Anthropic必须有max_tokens
            if err == nil && ir != nil {
                if ir.MaxTokens == nil {
                    t.Errorf("Anthropic IR missing MaxTokens\nInput: %s", string(data))
                }
                
                // 检查system是否正确转换
                if sysStr, ok := raw["system"].(string); ok && sysStr != "" {
                    foundSystem := false
                    for _, msg := range ir.Messages {
                        if msg.Role == "system" {
                            foundSystem = true
                            break
                        }
                    }
                    if !foundSystem {
                        t.Errorf("system field not converted to message\nInput: %s", string(data))
                    }
                }
            }
        }
    })
}
```

### 4. 序列化器Fuzzing

**文件**: `internal/ir/serialize_openai_fuzz_test.go`

```go
package ir

import (
    "encoding/json"
    "testing"
)

// FuzzSerializeOpenAI 测试序列化不会丢失数据
func FuzzSerializeOpenAI(f *testing.F) {
    // 添加有效的IR种子
    f.Add([]byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`))
    
    f.Fuzz(func(t *testing.T, inputJSON []byte) {
        defer func() {
            if r := recover(); r != nil {
                t.Errorf("SerializeOpenAI panicked: %v", r)
            }
        }()
        
        // 解析输入为IR
        ir, err := ParseOpenAIRequest(inputJSON)
        if err != nil || ir == nil {
            return  // 无效输入，跳过
        }
        
        // 序列化回OpenAI格式
        output, err := SerializeOpenAI(ir)
        if err != nil {
            t.Errorf("serialization failed: %v\nIR: %+v", err, ir)
            return
        }
        
        // 不变量：输出应该是有效JSON
        var outMap map[string]interface{}
        if err := json.Unmarshal(output, &outMap); err != nil {
            t.Errorf("serialized output is invalid JSON: %v\nOutput: %s", err, string(output))
            return
        }
        
        // 不变量：再次解析应该得到相同的IR（幂等性）
        ir2, err := ParseOpenAIRequest(output)
        if err != nil {
            t.Errorf("re-parsing serialized output failed: %v", err)
            return
        }
        
        // 比较关键字段
        if ir.Model != ir2.Model {
            t.Errorf("model mismatch: %q vs %q", ir.Model, ir2.Model)
        }
        
        if len(ir.Messages) != len(ir2.Messages) {
            t.Errorf("message count mismatch: %d vs %d", len(ir.Messages), len(ir2.Messages))
        }
        
        // 温度应该保持（允许浮点误差）
        if ir.Temperature != nil && ir2.Temperature != nil {
            diff := *ir.Temperature - *ir2.Temperature
            if diff < -0.001 || diff > 0.001 {
                t.Errorf("temperature mismatch: %f vs %f", *ir.Temperature, *ir2.Temperature)
            }
        }
    })
}

// FuzzRoundTripOpenAI 测试往返转换
func FuzzRoundTripOpenAI(f *testing.F) {
    f.Add([]byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}],"temperature":0.7,"max_tokens":100}`))
    
    f.Fuzz(func(t *testing.T, input []byte) {
        defer func() {
            if r := recover(); r != nil {
                t.Errorf("round-trip panicked: %v", r)
            }
        }()
        
        // 第一次解析
        ir1, err := ParseOpenAIRequest(input)
        if err != nil {
            return
        }
        
        // 序列化
        serialized, err := SerializeOpenAI(ir1)
        if err != nil {
            t.Errorf("serialization failed: %v", err)
            return
        }
        
        // 第二次解析
        ir2, err := ParseOpenAIRequest(serialized)
        if err != nil {
            t.Errorf("re-parsing failed: %v", err)
            return
        }
        
        // 再次序列化
        serialized2, err := SerializeOpenAI(ir2)
        if err != nil {
            t.Errorf("second serialization failed: %v", err)
            return
        }
        
        // 不变量：两次序列化的输出应该相同（稳定性）
        var map1, map2 map[string]interface{}
        json.Unmarshal(serialized, &map1)
        json.Unmarshal(serialized2, &map2)
        
        // 简化比较：至少model和message count应该一致
        if map1["model"] != map2["model"] {
            t.Errorf("model differs after round-trip")
        }
    })
}
```

### 5. 跨协议转换Fuzzing

**文件**: `internal/ir/cross_protocol_fuzz_test.go`

```go
package ir

import "testing"

// FuzzOpenAIToAnthropic 测试OpenAI -> IR -> Anthropic转换
func FuzzOpenAIToAnthropic(f *testing.F) {
    f.Add([]byte(`{"model":"gpt-4","messages":[{"role":"system","content":"Be helpful"},{"role":"user","content":"Hello"}],"max_tokens":100}`))
    
    f.Fuzz(func(t *testing.T, openaiInput []byte) {
        defer func() {
            if r := recover(); r != nil {
                t.Errorf("cross-protocol conversion panicked: %v", r)
            }
        }()
        
        // OpenAI -> IR
        ir, err := ParseOpenAIRequest(openaiInput)
        if err != nil || ir == nil {
            return
        }
        
        // IR -> Anthropic
        anthropicOutput, err := SerializeAnthropic(ir)
        if err != nil {
            // 某些OpenAI特性可能不支持，这是可以的
            return
        }
        
        // 验证Anthropic输出
        irBack, err := ParseAnthropicRequest(anthropicOutput)
        if err != nil {
            t.Errorf("Anthropic output is invalid: %v", err)
            return
        }
        
        // 不变量：消息数量应该相同或合理变化（system可能合并）
        originalMsgCount := len(ir.Messages)
        convertedMsgCount := len(irBack.Messages)
        
        // 允许system消息的转换差异
        if convertedMsgCount > originalMsgCount+1 || convertedMsgCount < originalMsgCount-1 {
            t.Errorf("message count changed unexpectedly: %d -> %d", originalMsgCount, convertedMsgCount)
        }
    })
}

// FuzzAnthropicToOpenAI 反向测试
func FuzzAnthropicToOpenAI(f *testing.F) {
    f.Add([]byte(`{"model":"claude-3-opus-20240229","max_tokens":1024,"system":"Be helpful","messages":[{"role":"user","content":"Hi"}]}`))
    
    f.Fuzz(func(t *testing.T, anthropicInput []byte) {
        defer func() {
            if r := recover(); r != nil {
                t.Errorf("Anthropic->OpenAI panicked: %v", r)
            }
        }()
        
        ir, err := ParseAnthropicRequest(anthropicInput)
        if err != nil {
            return
        }
        
        openaiOutput, err := SerializeOpenAI(ir)
        if err != nil {
            t.Errorf("serialization to OpenAI failed: %v", err)
            return
        }
        
        // 验证OpenAI输出
        irBack, err := ParseOpenAIRequest(openaiOutput)
        if err != nil {
            t.Errorf("OpenAI output is invalid: %v", err)
        }
        
        // 基本不变量检查
        if irBack != nil && irBack.Model == "" {
            t.Error("converted model is empty")
        }
    })
}
```

### 6. 运行Fuzzing

#### 本地运行

```bash
# 运行单个fuzz测试30秒
go test -fuzz=FuzzParseOpenAIRequest -fuzztime=30s ./internal/ir

# 运行所有fuzz测试（每个10秒）
go test -fuzz=. -fuzztime=10s ./internal/ir

# 并行运行（8核）
go test -fuzz=FuzzParseOpenAIRequest -parallel=8 ./internal/ir

# 使用特定语料库
go test -fuzz=FuzzParseOpenAIRequest -fuzzminimizetime=1m ./internal/ir
```

#### CI集成

**文件**: `.github/workflows/fuzz.yml`

```yaml
name: Fuzzing

on:
  schedule:
    - cron: '0 2 * * *'  # 每天凌晨2点运行
  workflow_dispatch:  # 手动触发

jobs:
  fuzz:
    name: Fuzz Testing
    runs-on: ubuntu-latest
    timeout-minutes: 60
    
    strategy:
      matrix:
        target:
          - FuzzParseOpenAIRequest
          - FuzzParseAnthropicRequest
          - FuzzSerializeOpenAI
          - FuzzOpenAIToAnthropic
    
    steps:
      - uses: actions/checkout@v4
      
      - uses: actions/setup-go@v5
        with:
          go-version: '1.21'
      
      - name: Run fuzzing
        run: |
          go test -fuzz=${{ matrix.target }} -fuzztime=10m ./internal/ir
        continue-on-error: true  # 发现问题不立即失败
      
      - name: Upload crash artifacts
        if: failure()
        uses: actions/upload-artifact@v4
        with:
          name: fuzz-crashes-${{ matrix.target }}
          path: |
            internal/ir/testdata/fuzz/${{ matrix.target }}/*
          retention-days: 30
      
      - name: Notify on crash
        if: failure()
        uses: 8398a7/action-slack@v3
        with:
          status: 'failure'
          text: 'Fuzzing found crashes in ${{ matrix.target }}'
          webhook_url: ${{ secrets.SLACK_WEBHOOK }}
```

### 7. 修复发现的问题

#### 常见Fuzzing发现的问题

**A. 未检查的nil指针**

```go
// ❌ Fuzzing发现的问题
func parseMessage(data map[string]interface{}) Message {
    content := data["content"].(string)  // panic if not string!
    return Message{Content: &Content{Text: content}}
}

// ✅ 修复
func parseMessage(data map[string]interface{}) (Message, error) {
    contentRaw, ok := data["content"]
    if !ok {
        return Message{}, errors.New("missing content")
    }
    
    var content string
    switch v := contentRaw.(type) {
    case string:
        content = v
    case nil:
        content = ""
    default:
        return Message{}, fmt.Errorf("invalid content type: %T", contentRaw)
    }
    
    return Message{Content: &Content{Text: content}}, nil
}
```

**B. 整数溢出**

```go
// ❌ Fuzzing发现的问题
func parseMaxTokens(data map[string]interface{}) int {
    return int(data["max_tokens"].(float64))  // 大数字可能溢出
}

// ✅ 修复
func parseMaxTokens(data map[string]interface{}) (int, error) {
    tokensRaw, ok := data["max_tokens"]
    if !ok {
        return 0, nil  // 可选字段
    }
    
    tokensFloat, ok := tokensRaw.(float64)
    if !ok {
        return 0, fmt.Errorf("max_tokens must be number")
    }
    
    if tokensFloat < 0 || tokensFloat > math.MaxInt32 {
        return 0, fmt.Errorf("max_tokens out of range: %f", tokensFloat)
    }
    
    return int(tokensFloat), nil
}
```

**C. 无限递归**

```go
// ❌ Fuzzing发现的问题
func parseNested(data map[string]interface{}) {
    if nested, ok := data["nested"].(map[string]interface{}); ok {
        parseNested(nested)  // 可能无限递归
    }
}

// ✅ 修复
func parseNested(data map[string]interface{}, depth int) error {
    const maxDepth = 10
    if depth > maxDepth {
        return errors.New("maximum nesting depth exceeded")
    }
    
    if nested, ok := data["nested"].(map[string]interface{}); ok {
        return parseNested(nested, depth+1)
    }
    return nil
}
```

**D. 字符串处理边界**

```go
// ❌ Fuzzing发现的问题
func truncate(s string, n int) string {
    return s[:n]  // panic if n > len(s)
}

// ✅ 修复
func truncate(s string, n int) string {
    if n < 0 {
        n = 0
    }
    if n > len(s) {
        n = len(s)
    }
    return s[:n]
}
```

### 8. 语料库管理

#### 导入有效语料

```bash
# 从实际生产日志导出样本
./scripts/export_request_samples.sh > testdata/corpus/production_samples.txt

# 添加到fuzzing语料库
while IFS= read -r line; do
    echo "f.Add([]byte(\`$line\`))" 
done < testdata/corpus/production_samples.txt
```

#### 最小化语料库

```bash
# 合并和最小化语料库
go test -fuzz=FuzzParseOpenAIRequest -fuzzminimizetime=5m ./internal/ir
```

#### 共享语料库

```yaml
# .github/workflows/fuzz-corpus.yml
- name: Cache fuzz corpus
  uses: actions/cache@v3
  with:
    path: internal/ir/testdata/fuzz/
    key: fuzz-corpus-${{ github.sha }}
    restore-keys: |
      fuzz-corpus-
```

## 验收标准

- [ ] 至少5个fuzz测试函数实现
- [ ] 覆盖所有主要解析器和序列化器
- [ ] 每个fuzz测试定义至少10个种子
- [ ] CI每日运行fuzzing（每个目标至少10分钟）
- [ ] 发现的崩溃自动上传artifacts
- [ ] 至少修复一个fuzzing发现的真实问题
- [ ] 文档化fuzzing策略

## 交付物

1. Fuzz测试文件（*_fuzz_test.go）
2. CI workflow配置
3. 修复报告（fuzzing发现的问题）
4. 语料库管理脚本
5. 文档：`docs/testing/fuzzing-guide.md`

## 时间估算
- Fuzz测试编写: 1天
- CI集成: 0.5天
- 初次运行和问题修复: 1天
- 语料库建设: 0.5天
- 文档编写: 0.5天
```

---

## 任务4.2: 属性测试实现

### 执行提示词

```markdown
# 任务：为IR层添加属性测试（Property-Based Testing）

## 背景
属性测试（也称基于属性的测试）关注系统应该满足的**不变量**和**属性**，而不是具体的输入输出。对于IR层特别有用，因为需要保证：
- 往返转换无损（parse -> serialize -> parse）
- 跨协议转换保留语义
- 所有输入都能处理（不panic）

## 工具选择
**gopter**: Go的属性测试库（类似Haskell的QuickCheck）

```bash
go get github.com/leandro-lugaresi/hub
```

## 实施步骤

### 1. 安装依赖

```bash
go get github.com/leandro-lugaresi/hub@latest
```

更新`go.mod`:
```go
require (
    github.com/leandro-lugaresi/hub v1.1.1
)
```

### 2. 属性测试基础

**文件**: `internal/ir/properties_test.go`

```go
package ir

import (
    "encoding/json"
    "testing"
    
    "github.com/leandro-lugaresi/hub"
    "github.com/leandro-lugaresi/hub/gen"
)

// TestProperty_RoundTripIsLossless 测试往返转换无损
func TestProperty_RoundTripIsLossless(t *testing.T) {
    properties := hub.NewProperties(hub.DefaultTestParameters())
    
    properties.Property("OpenAI round-trip preserves model", hub.ForAll(
        genValidOpenAIRequest(),
        func(reqBytes []byte) bool {
            // 第一次解析
            ir1, err := ParseOpenAIRequest(reqBytes)
            if err != nil {
                return true  // 解析失败是可以的
            }
            
            // 序列化
            serialized, err := SerializeOpenAI(ir1)
            if err != nil {
                t.Logf("serialization failed: %v", err)
                return false
            }
            
            // 第二次解析
            ir2, err := ParseOpenAIRequest(serialized)
            if err != nil {
                t.Logf("re-parsing failed: %v", err)
                return false
            }
            
            // 属性：模型名应该保持不变
            return ir1.Model == ir2.Model
        },
    ))
    
    properties.Property("OpenAI round-trip preserves message count", hub.ForAll(
        genValidOpenAIRequest(),
        func(reqBytes []byte) bool {
            ir1, err := ParseOpenAIRequest(reqBytes)
            if err != nil {
                return true
            }
            
            serialized, err := SerializeOpenAI(ir1)
            if err != nil {
                return true
            }
            
            ir2, err := ParseOpenAIRequest(serialized)
            if err != nil {
                return false
            }
            
            // 属性：消息数量应该保持不变
            return len(ir1.Messages) == len(ir2.Messages)
        },
    ))
    
    properties.Property("OpenAI round-trip preserves temperature", hub.ForAll(
        genValidOpenAIRequest(),
        func(reqBytes []byte) bool {
            ir1, err := ParseOpenAIRequest(reqBytes)
            if err != nil || ir1.Temperature == nil {
                return true
            }
            
            serialized, err := SerializeOpenAI(ir1)
            if err != nil {
                return true
            }
            
            ir2, err := ParseOpenAIRequest(serialized)
            if err != nil || ir2.Temperature == nil {
                return false
            }
            
            // 属性：温度应该保持（允许浮点误差）
            diff := *ir1.Temperature - *ir2.Temperature
            return diff > -0.001 && diff < 0.001
        },
    ))
    
    properties.TestingRun(t)
}

// genValidOpenAIRequest 生成有效的OpenAI请求
func genValidOpenAIRequest() hub.Gen {
    return gen.Struct(
        reflect.TypeOf(map[string]interface{}{}),
        map[string]hub.Gen{
            "model": gen.OneConstOf(
                "gpt-4",
                "gpt-4-turbo",
                "gpt-3.5-turbo",
            ),
            "messages": gen.SliceOf(gen.Struct(
                reflect.TypeOf(map[string]interface{}{}),
                map[string]hub.Gen{
                    "role": gen.OneConstOf("system", "user", "assistant"),
                    "content": gen.AlphaString(),
                },
            ), gen.IntRange(1, 10)),  // 1-10条消息
            "temperature": gen.PtrOf(gen.Float64Range(0, 2)),
            "max_tokens": gen.PtrOf(gen.IntRange(1, 4096)),
        },
    ).Map(func(data map[string]interface{}) []byte {
        b, _ := json.Marshal(data)
        return b
    })
}
```

### 3. 跨协议转换属性

**文件**: `internal/ir/cross_protocol_properties_test.go`

```go
package ir

import (
    "testing"
    
    "github.com/leandro-lugaresi/hub"
)

// TestProperty_CrossProtocolPreservesSemantics 测试跨协议转换保留语义
func TestProperty_CrossProtocolPreservesSemantics(t *testing.T) {
    properties := hub.NewProperties(hub.DefaultTestParameters())
    
    properties.Property("OpenAI->Anthropic preserves core fields", hub.ForAll(
        genValidOpenAIRequest(),
        func(openaiReq []byte) bool {
            ir, err := ParseOpenAIRequest(openaiReq)
            if err != nil {
                return true
            }
            
            anthropicReq, err := SerializeAnthropic(ir)
            if err != nil {
                return true  // 某些特性可能不支持
            }
            
            irBack, err := ParseAnthropicRequest(anthropicReq)
            if err != nil {
                return false
            }
            
            // 属性1: 模型信息应该保留（可能映射）
            if irBack.Model == "" {
                return false
            }
            
            // 属性2: 消息数量应该合理（允许system消息转换）
            msgCountDiff := len(irBack.Messages) - len(ir.Messages)
            if msgCountDiff < -1 || msgCountDiff > 1 {
                t.Logf("message count changed too much: %d -> %d", 
                    len(ir.Messages), len(irBack.Messages))
                return false
            }
            
            // 属性3: MaxTokens应该保留（Anthropic必需）
            if irBack.MaxTokens == nil {
                t.Log("MaxTokens lost in Anthropic conversion")
                return false
            }
            
            return true
        },
    ))
    
    properties.Property("Anthropic->OpenAI->Anthropic is stable", hub.ForAll(
        genValidAnthropicRequest(),
        func(anthropicReq1 []byte) bool {
            ir1, err := ParseAnthropicRequest(anthropicReq1)
            if err != nil {
                return true
            }
            
            openaiReq, err := SerializeOpenAI(ir1)
            if err != nil {
                return false
            }
            
            ir2, err := ParseOpenAIRequest(openaiReq)
            if err != nil {
                return false
            }
            
            anthropicReq2, err := SerializeAnthropic(ir2)
            if err != nil {
                return false
            }
            
            ir3, err := ParseAnthropicRequest(anthropicReq2)
            if err != nil {
                return false
            }
            
            // 属性：两次Anthropic IR应该等价
            return ir1.Model == ir3.Model &&
                len(ir1.Messages) == len(ir3.Messages)
        },
    ))
    
    properties.TestingRun(t)
}

func genValidAnthropicRequest() hub.Gen {
    return gen.Struct(
        reflect.TypeOf(map[string]interface{}{}),
        map[string]hub.Gen{
            "model": gen.OneConstOf(
                "claude-3-opus-20240229",
                "claude-3-sonnet-20240229",
                "claude-3-haiku-20240307",
            ),
            "max_tokens": gen.IntRange(1, 4096),
            "messages": gen.SliceOf(gen.Struct(
                reflect.TypeOf(map[string]interface{}{}),
                map[string]hub.Gen{
                    "role": gen.OneConstOf("user", "assistant"),
                    "content": gen.AlphaString(),
                },
            ), gen.IntRange(1, 10)),
            "temperature": gen.PtrOf(gen.Float64Range(0, 1)),
        },
    ).Map(func(data map[string]interface{}) []byte {
        b, _ := json.Marshal(data)
        return b
    })
}
```

### 4. 序列化幂等性属性

**文件**: `internal/ir/serialization_properties_test.go`

```go
package ir

import (
    "encoding/json"
    "testing"
    
    "github.com/leandro-lugaresi/hub"
)

// TestProperty_SerializationIsIdempotent 测试序列化幂等性
func TestProperty_SerializationIsIdempotent(t *testing.T) {
    properties := hub.NewProperties(hub.DefaultTestParameters())
    
    properties.Property("serialize twice produces same output", hub.ForAll(
        genInternalRequest(),
        func(ir *InternalRequest) bool {
            // 第一次序列化
            out1, err := SerializeOpenAI(ir)
            if err != nil {
                return true
            }
            
            // 解析回来
            irMid, err := ParseOpenAIRequest(out1)
            if err != nil {
                t.Logf("re-parse failed: %v", err)
                return false
            }
            
            // 第二次序列化
            out2, err := SerializeOpenAI(irMid)
            if err != nil {
                t.Logf("second serialize failed: %v", err)
                return false
            }
            
            // 属性：两次输出应该相同（JSON等价）
            var map1, map2 map[string]interface{}
            json.Unmarshal(out1, &map1)
            json.Unmarshal(out2, &map2)
            
            // 简化比较：检查关键字段
            return map1["model"] == map2["model"] &&
                len(map1["messages"].([]interface{})) == len(map2["messages"].([]interface{}))
        },
    ))
    
    properties.TestingRun(t)
}

// genInternalRequest 生成IR结构
func genInternalRequest() hub.Gen {
    return gen.Struct(
        reflect.TypeOf(&InternalRequest{}),
        map[string]hub.Gen{
            "Model": gen.OneConstOf("gpt-4", "gpt-3.5-turbo"),
            "Messages": gen.SliceOf(genMessage(), gen.IntRange(1, 5)),
            "Temperature": gen.PtrOf(gen.Float64Range(0, 2)),
            "MaxTokens": gen.PtrOf(gen.IntRange(1, 4096)),
            "Stream": gen.PtrOf(gen.Bool()),
        },
    )
}

func genMessage() hub.Gen {
    return gen.Struct(
        reflect.TypeOf(Message{}),
        map[string]hub.Gen{
            "Role": gen.OneConstOf("system", "user", "assistant"),
            "Content": gen.PtrOf(genContent()),
        },
    )
}

func genContent() hub.Gen {
    return gen.Struct(
        reflect.TypeOf(Content{}),
        map[string]hub.Gen{
            "Parts": gen.SliceOf(genContentPart(), gen.IntRange(1, 3)),
        },
    )
}

func genContentPart() hub.Gen {
    return gen.OneGenOf(
        // 文本part
        gen.Struct(
            reflect.TypeOf(ContentPart{}),
            map[string]hub.Gen{
                "Type": gen.Const("text"),
                "Text": gen.PtrOf(gen.AlphaString()),
            },
        ),
        // 图片part
        gen.Struct(
            reflect.TypeOf(ContentPart{}),
            map[string]hub.Gen{
                "Type": gen.Const("image_url"),
                "ImageURL": gen.PtrOf(gen.Struct(
                    reflect.TypeOf(&ImageURL{}),
                    map[string]hub.Gen{
                        "URL": gen.RegexMatch("https://example.com/[a-z]+\\.jpg"),
                    },
                )),
            },
        ),
    )
}
```

### 5. 错误处理属性

**文件**: `internal/ir/error_handling_properties_test.go`

```go
package ir

import (
    "testing"
    
    "github.com/leandro-lugaresi/hub"
    "github.com/leandro-lugaresi/hub/gen"
)

// TestProperty_ParserNeverPanics 测试解析器永不panic
func TestProperty_ParserNeverPanics(t *testing.T) {
    properties := hub.NewProperties(hub.DefaultTestParameters())
    
    properties.Property("OpenAI parser never panics", hub.ForAll(
        gen.SliceOf(gen.UInt8(), gen.IntRange(0, 10000)),  // 任意字节序列
        func(data []byte) (result bool) {
            defer func() {
                if r := recover(); r != nil {
                    t.Errorf("ParseOpenAIRequest panicked: %v\nInput: %s", r, string(data))
                    result = false
                }
            }()
            
            // 调用解析器，不关心结果
            _, _ = ParseOpenAIRequest(data)
            result = true
            return
        },
    ))
    
    properties.Property("Anthropic parser never panics", hub.ForAll(
        gen.SliceOf(gen.UInt8(), gen.IntRange(0, 10000)),
        func(data []byte) (result bool) {
            defer func() {
                if r := recover(); r != nil {
                    t.Errorf("ParseAnthropicRequest panicked: %v", r)
                    result = false
                }
            }()
            
            _, _ = ParseAnthropicRequest(data)
            result = true
            return
        },
    ))
    
    properties.TestingRun(t)
}

// TestProperty_ErrorsAreDescriptive 测试错误消息的描述性
func TestProperty_ErrorsAreDescriptive(t *testing.T) {
    properties := hub.NewProperties(hub.DefaultTestParameters())
    
    properties.Property("parse errors contain useful info", hub.ForAll(
        gen.SliceOf(gen.UInt8(), gen.IntRange(1, 1000)),
        func(data []byte) bool {
            _, err := ParseOpenAIRequest(data)
            if err == nil {
                return true  // 成功解析
            }
            
            errStr := err.Error()
            
            // 属性：错误消息不应为空
            if errStr == "" {
                t.Log("error message is empty")
                return false
            }
            
            // 属性：错误消息不应该是泛泛的
            genericErrors := []string{"error", "failed", "invalid"}
            isGeneric := false
            for _, ge := range genericErrors {
                if errStr == ge {
                    isGeneric = true
                    break
                }
            }
            
            if isGeneric {
                t.Logf("error is too generic: %q", errStr)
                return false
            }
            
            return true
        },
    ))
    
    properties.TestingRun(t)
}
```

### 6. 数据不变量属性

**文件**: `internal/ir/invariants_properties_test.go`

```go
package ir

import (
    "testing"
    
    "github.com/leandro-lugaresi/hub"
)

// TestProperty_IRInvariants 测试IR数据结构的不变量
func TestProperty_IRInvariants(t *testing.T) {
    properties := hub.NewProperties(hub.DefaultTestParameters())
    
    properties.Property("parsed IR always has valid model", hub.ForAll(
        genValidOpenAIRequest(),
        func(reqBytes []byte) bool {
            ir, err := ParseOpenAIRequest(reqBytes)
            if err != nil {
                return true
            }
            
            // 不变量：模型名非空
            if ir.Model == "" {
                t.Log("IR has empty model")
                return false
            }
            
            return true
        },
    ))
    
    properties.Property("parsed IR has valid temperature range", hub.ForAll(
        genValidOpenAIRequest(),
        func(reqBytes []byte) bool {
            ir, err := ParseOpenAIRequest(reqBytes)
            if err != nil || ir.Temperature == nil {
                return true
            }
            
            // 不变量：温度在0-2之间
            temp := *ir.Temperature
            return temp >= 0 && temp <= 2
        },
    ))
    
    properties.Property("parsed IR has positive max_tokens", hub.ForAll(
        genValidOpenAIRequest(),
        func(reqBytes []byte) bool {
            ir, err := ParseOpenAIRequest(reqBytes)
            if err != nil || ir.MaxTokens == nil {
                return true
            }
            
            // 不变量：max_tokens > 0
            return *ir.MaxTokens > 0
        },
    ))
    
    properties.Property("messages preserve role", hub.ForAll(
        genValidOpenAIRequest(),
        func(reqBytes []byte) bool {
            ir, err := ParseOpenAIRequest(reqBytes)
            if err != nil {
                return true
            }
            
            // 不变量：所有消息都有role
            for i, msg := range ir.Messages {
                if msg.Role == "" {
                    t.Logf("message[%d] has empty role", i)
                    return false
                }
                
                // 不变量：role是有效值
                validRoles := map[string]bool{
                    "system": true, "user": true, "assistant": true, "tool": true,
                }
                if !validRoles[msg.Role] {
                    t.Logf("message[%d] has invalid role: %q", i, msg.Role)
                    return false
                }
            }
            
            return true
        },
    ))
    
    properties.TestingRun(t)
}
```

### 7. CI集成

**文件**: `.github/workflows/property-tests.yml`

```yaml
name: Property Tests

on:
  push:
    branches: [ main, develop ]
  pull_request:
    branches: [ main, develop ]
  schedule:
    - cron: '0 3 * * *'  # 每天凌晨3点运行长测试

jobs:
  property-tests:
    name: Property-Based Testing
    runs-on: ubuntu-latest
    timeout-minutes: 30
    
    steps:
      - uses: actions/checkout@v4
      
      - uses: actions/setup-go@v5
        with:
          go-version: '1.21'
      
      - name: Run property tests (quick)
        if: github.event_name == 'pull_request'
        run: |
          go test -v -run TestProperty ./internal/ir
        env:
          GOPTER_ITERATIONS: 100  # PR时少量迭代
      
      - name: Run property tests (thorough)
        if: github.event_name == 'schedule'
        run: |
          go test -v -run TestProperty ./internal/ir
        env:
          GOPTER_ITERATIONS: 10000  # 定时任务时大量迭代
      
      - name: Upload failure cases
        if: failure()
        uses: actions/upload-artifact@v4
        with:
          name: property-test-failures
          path: |
            /tmp/gopter-*
          retention-days: 14
```

### 8. 配置测试参数

```go
// 在测试中配置
func TestMain(m *testing.M) {
    // 从环境变量读取迭代次数
    iterations := 100
    if iter := os.Getenv("GOPTER_ITERATIONS"); iter != "" {
        if n, err := strconv.Atoi(iter); err == nil {
            iterations = n
        }
    }
    
    hub.DefaultTestParameters().MinSuccessfulTests = iterations
    
    os.Exit(m.Run())
}
```

## 验收标准

- [ ] 至少10个属性测试实现
- [ ] 覆盖往返转换、跨协议转换、序列化幂等性
- [ ] 所有属性测试通过（至少100次迭代）
- [ ] CI集成（PR时快速检查，定时任务深度测试）
- [ ] 至少发现并修复一个属性违反问题
- [ ] 文档化属性测试策略

## 交付物

1. 属性测试文件（*_properties_test.go）
2. 生成器函数（gen*.go）
3. CI workflow配置
4. 修复报告（属性测试发现的问题）
5. 文档：`docs/testing/property-testing-guide.md`

## 时间估算
- 属性测试编写: 1.5天
- 生成器实现: 1天
- CI集成: 0.5天
- 问题修复: 0.5天
- 文档编写: 0.5天
```

---

## 总结

任务4的两个子任务为IR层提供了全面的测试覆盖：

### 任务4.1: Fuzzing测试
- **覆盖面**: 自动发现边界情况和畸形输入
- **目标**: Panic、崩溃、内存泄漏
- **工具**: Go内置fuzzing
- **策略**: 持续运行，语料库积累

### 任务4.2: 属性测试
- **覆盖面**: 验证系统不变量和属性
- **目标**: 数据完整性、幂等性、等价性
- **工具**: gopter
- **策略**: 大量随机测试用例

**预期效果**:
- IR层bug减少90%+
- 协议兼容性问题提前发现
- 回归问题自动检测

**下一步**: 继续生成任务5-10的详细提示词。
