# 任务1: IR层测试覆盖增强 - 详细提示词

## 任务1.1: 添加Fuzzing测试

### 执行提示词

```markdown
# 任务：为IR层添加Fuzzing测试

## 背景
当前IR层负责在不同LLM协议之间转换数据，但缺少针对畸形输入的鲁棒性测试。需要使用Go的`testing`包和fuzzing功能来验证解析器的健壮性。

## 目标文件
1. `internal/ir/parse_openai.go` - OpenAI协议解析器
2. `internal/ir/parse_anthropic.go` - Anthropic协议解析器  
3. `internal/ir/parse_gemini.go` - Gemini协议解析器
4. `internal/ir/parse_generic.go` - 通用协议解析器

## 具体要求

### 1. 创建Fuzz测试文件
为每个解析器创建对应的`*_fuzz_test.go`文件：
- `internal/ir/parse_openai_fuzz_test.go`
- `internal/ir/parse_anthropic_fuzz_test.go`
- `internal/ir/parse_gemini_fuzz_test.go`
- `internal/ir/parse_generic_fuzz_test.go`

### 2. 实现Fuzz测试函数

#### OpenAI示例框架
```go
package ir

import (
    "testing"
)

func FuzzParseOpenAIRequest(f *testing.F) {
    // 添加种子语料库
    f.Add([]byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`))
    f.Add([]byte(`{"model":"gpt-4","messages":[]}`))
    f.Add([]byte(`{}`))
    f.Add([]byte(`{"model":"","messages":[{"role":"","content":""}]}`))
    
    // 添加畸形输入种子
    f.Add([]byte(`{"model":"gpt-4","messages":[{"role":"user","content":"`+string(make([]byte, 10000))+`"}]}`)) // 超大内容
    f.Add([]byte(`{"model":"gpt-4"` )) // 不完整JSON
    f.Add([]byte(`null`))
    f.Add([]byte(``)) // 空输入
    
    f.Fuzz(func(t *testing.T, data []byte) {
        // 执行解析，不应崩溃
        req, err := ParseOpenAIRequest(data)
        
        // 基本断言
        if err == nil {
            // 如果解析成功，验证基本约束
            if req != nil {
                // 验证关键字段不为nil
                if req.Messages == nil {
                    t.Error("Parsed request has nil Messages")
                }
            }
        }
        
        // 关键：无论是否报错，不应该panic
    })
}

func FuzzParseOpenAIResponse(f *testing.F) {
    // 响应解析的fuzz测试
    f.Add([]byte(`{"id":"chatcmpl-123","object":"chat.completion","created":1234567890,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}]}`))
    f.Add([]byte(`{"id":"","choices":[]}`))
    f.Add([]byte(`{"error":{"message":"Invalid API key","type":"invalid_request_error"}}`))
    
    f.Fuzz(func(t *testing.T, data []byte) {
        resp, err := ParseOpenAIResponse(data)
        
        if err == nil && resp != nil {
            // 验证解析后的数据完整性
            if resp.Choices != nil && len(resp.Choices) > 0 {
                for _, choice := range resp.Choices {
                    if choice.Message == nil {
                        t.Error("Choice has nil Message")
                    }
                }
            }
        }
    })
}
```

### 3. 边界条件覆盖

每个fuzz测试需覆盖以下场景：

#### a. 数据大小边界
- 空payload (`[]byte{}`)
- 单字符 (`[]byte("x")`)
- 超大payload (>10MB，测试内存限制)
- 恰好在边界的大小 (64KB, 1MB等)

#### b. JSON结构异常
- 不完整的JSON (`{"key":`)
- 深度嵌套 (>100层，测试栈溢出)
- 超长key名 (>10000字符)
- 特殊字符: `\x00`, `\xFF`, unicode控制字符
- 重复key

#### c. 类型错误
- 期望string但给了number: `{"model": 123}`
- 期望array但给了object: `{"messages": {}}`
- 期望object但给了null: `{"message": null}`
- 期望number但给了string: `{"temperature": "hot"}`

#### d. 字段缺失或冗余
- 必需字段缺失: `{"messages": []}` (无model)
- 只有未知字段: `{"unknown_field": "value"}`
- 字段顺序混乱
- 大量冗余字段 (1000+个)

### 4. 添加种子语料库文件

创建 `testdata/fuzz` 目录，添加真实生产案例：

```bash
testdata/fuzz/
├── openai/
│   ├── corpus/
│   │   ├── valid_simple.txt
│   │   ├── valid_with_tools.txt
│   │   ├── valid_multimodal.txt
│   │   ├── malformed_incomplete.txt
│   │   └── malformed_wrong_types.txt
├── anthropic/
│   └── corpus/
├── gemini/
│   └── corpus/
└── generic/
    └── corpus/
```

每个文件包含一个测试用例的原始字节。

### 5. 运行Fuzz测试

#### 本地运行（开发阶段）
```bash
# 运行5分钟
go test -fuzz=FuzzParseOpenAIRequest -fuzztime=5m ./internal/ir

# 运行特定次数
go test -fuzz=FuzzParseOpenAIRequest -fuzztime=100000x ./internal/ir

# 使用种子语料库
go test -fuzz=FuzzParseOpenAIRequest -fuzztime=5m ./internal/ir
```

#### CI集成（持续验证）
在 `.github/workflows/fuzz.yml` 中添加：

```yaml
name: Fuzz Testing

on:
  schedule:
    - cron: '0 2 * * *'  # 每天凌晨2点运行
  workflow_dispatch:      # 支持手动触发

jobs:
  fuzz:
    runs-on: ubuntu-latest
    timeout-minutes: 60
    
    steps:
      - uses: actions/checkout@v3
      
      - name: Setup Go
        uses: actions/setup-go@v4
        with:
          go-version: '1.21'
          
      - name: Run IR Fuzzing
        run: |
          # 每个fuzz测试运行10分钟
          go test -fuzz=FuzzParseOpenAIRequest -fuzztime=10m ./internal/ir || true
          go test -fuzz=FuzzParseOpenAIResponse -fuzztime=10m ./internal/ir || true
          go test -fuzz=FuzzParseAnthropicRequest -fuzztime=10m ./internal/ir || true
          go test -fuzz=FuzzParseAnthropicResponse -fuzztime=10m ./internal/ir || true
          go test -fuzz=FuzzParseGeminiRequest -fuzztime=10m ./internal/ir || true
          go test -fuzz=FuzzParseGeminiResponse -fuzztime=10m ./internal/ir || true
          
      - name: Upload crash reports
        if: failure()
        uses: actions/upload-artifact@v3
        with:
          name: fuzz-crash-reports
          path: |
            internal/ir/testdata/fuzz/*/
```

### 6. 处理发现的问题

当fuzz测试发现崩溃时：

```bash
# 复现特定失败
go test -run=FuzzParseOpenAIRequest/abc123def456
```

修复后添加为回归测试：
```go
func TestParseOpenAIRequest_RegressionFuzz123(t *testing.T) {
    // 从testdata/fuzz/.../abc123def456复制过来的输入
    crashInput := []byte(`...`)
    
    req, err := ParseOpenAIRequest(crashInput)
    
    // 应该优雅处理，不崩溃
    if err != nil {
        // 记录错误但不应panic
        t.Logf("Expected error: %v", err)
    }
}
```

### 7. 性能基准

添加性能基准测试（可选）：

```go
func BenchmarkParseOpenAIRequest(b *testing.B) {
    data := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`)
    
    b.ReportAllocs()
    b.ResetTimer()
    
    for i := 0; i < b.N; i++ {
        _, err := ParseOpenAIRequest(data)
        if err != nil {
            b.Fatal(err)
        }
    }
}
```

## 验收标准

- [ ] 每个协议有至少2个fuzz测试函数（请求+响应）
- [ ] 每个fuzz测试至少10个种子用例
- [ ] 本地运行10分钟无崩溃
- [ ] CI每天自动运行
- [ ] 发现的崩溃有对应的回归测试
- [ ] 文档更新（README或testing.md中说明如何运行fuzz测试）

## 预期输出

1. 8个新的fuzz测试文件
2. `testdata/fuzz/` 语料库目录
3. `.github/workflows/fuzz.yml` CI配置
4. 文档更新

## 时间估算
- 编写fuzz测试: 2天
- 添加种子语料库: 0.5天
- CI集成: 0.5天
- 修复发现的问题: 1天（预留）
```

---

## 任务1.2: 属性测试（Property-Based Testing）

### 执行提示词

```markdown
# 任务：实现IR层Round-Trip属性测试

## 背景
属性测试验证代码在大量随机输入下是否保持某些不变性。对于IR层，核心属性是：**往返转换数据一致性**（Parse → Serialize → Parse 应恢复原始数据）。

## 依赖安装
```bash
go get github.com/leanovate/gopter@latest
go get github.com/leanovate/gopter/gen@latest
go get github.com/leanovate/gopter/prop@latest
```

## 实现步骤

### 1. 创建测试文件
`internal/ir/property_test.go`

### 2. 定义生成器（Generators）

```go
package ir

import (
    "testing"
    "time"
    
    "github.com/leanovate/gopter"
    "github.com/leanovate/gopter/gen"
    "github.com/leanovate/gopter/prop"
)

// 生成随机InternalRequest
func genInternalRequest() gopter.Gen {
    return gopter.CombineGens(
        gen.Identifier(),        // Model
        genMessages(),           // Messages
        gen.Float64Range(0, 2),  // Temperature
        gen.Float64Range(0, 1),  // TopP
        gen.IntRange(1, 4096),   // MaxTokens
        gen.Bool(),              // Stream
        genTools(),              // Tools (可选)
    ).Map(func(values []interface{}) *InternalRequest {
        return &InternalRequest{
            Model:       values[0].(string),
            Messages:    values[1].([]Message),
            Temperature: ptr(values[2].(float64)),
            TopP:        ptr(values[3].(float64)),
            MaxTokens:   ptr(values[4].(int)),
            Stream:      values[5].(bool),
            Tools:       values[6].([]Tool),
        }
    })
}

// 生成随机消息列表
func genMessages() gopter.Gen {
    return gen.SliceOf(genMessage()).
        SuchThat(func(v interface{}) bool {
            msgs := v.([]Message)
            return len(msgs) > 0 && len(msgs) <= 100 // 合理长度
        })
}

// 生成单条消息
func genMessage() gopter.Gen {
    return gopter.CombineGens(
        gen.OneConstOf("user", "assistant", "system"), // Role
        genContent(),                                   // Content
    ).Map(func(values []interface{}) Message {
        return Message{
            Role:    values[0].(string),
            Content: values[1],
        }
    })
}

// 生成消息内容（文本或多模态）
func genContent() gopter.Gen {
    return gen.OneGenOf(
        gen.AlphaString().Map(func(s string) interface{} { return s }), // 纯文本
        genMultimodalContent(), // 多模态内容
    )
}

// 生成多模态内容
func genMultimodalContent() gopter.Gen {
    return gen.SliceOf(genContentPart(), 1, 5).
        Map(func(v interface{}) interface{} {
            return v
        })
}

// 生成内容片段
func genContentPart() gopter.Gen {
    return gen.OneGenOf(
        // 文本片段
        gen.AlphaString().Map(func(s string) ContentPart {
            return ContentPart{Type: "text", Text: s}
        }),
        // 图片URL
        gen.Const("https://example.com/image.jpg").Map(func(url string) ContentPart {
            return ContentPart{
                Type:     "image_url",
                ImageURL: &ImageURL{URL: url},
            }
        }),
    )
}

// 生成工具列表（可选）
func genTools() gopter.Gen {
    return gen.OneGenOf(
        gen.Const([]Tool(nil)), // 无工具
        gen.SliceOf(genTool(), 1, 3), // 1-3个工具
    )
}

// 生成单个工具定义
func genTool() gopter.Gen {
    return gopter.CombineGens(
        gen.Identifier(), // 工具名
        gen.AlphaString(), // 描述
    ).Map(func(values []interface{}) Tool {
        return Tool{
            Type: "function",
            Function: &FunctionDefinition{
                Name:        values[0].(string),
                Description: values[1].(string),
                Parameters: map[string]interface{}{
                    "type": "object",
                    "properties": map[string]interface{}{
                        "query": map[string]interface{}{
                            "type": "string",
                        },
                    },
                },
            },
        }
    })
}

// 辅助函数
func ptr[T any](v T) *T {
    return &v
}
```

### 3. 实现Round-Trip属性测试

```go
// 测试OpenAI协议的往返一致性
func TestProperty_OpenAIRoundTrip(t *testing.T) {
    properties := gopter.NewProperties(nil)
    
    properties.Property("OpenAI Request Round-Trip", prop.ForAll(
        func(req *InternalRequest) bool {
            // 1. 序列化为OpenAI格式
            serialized, err := SerializeToOpenAI(req)
            if err != nil {
                t.Logf("Serialize error: %v", err)
                return false
            }
            
            // 2. 解析回InternalRequest
            parsed, err := ParseOpenAIRequest(serialized)
            if err != nil {
                t.Logf("Parse error: %v", err)
                return false
            }
            
            // 3. 验证关键字段一致性
            return assertRequestsEqual(req, parsed)
        },
        genInternalRequest(),
    ))
    
    properties.TestingRun(t, gopter.ConsoleReporter(false))
}

// 测试Anthropic协议的往返一致性
func TestProperty_AnthropicRoundTrip(t *testing.T) {
    properties := gopter.NewProperties(nil)
    
    properties.Property("Anthropic Request Round-Trip", prop.ForAll(
        func(req *InternalRequest) bool {
            serialized, err := SerializeToAnthropic(req)
            if err != nil {
                return false
            }
            
            parsed, err := ParseAnthropicRequest(serialized)
            if err != nil {
                return false
            }
            
            return assertRequestsEqual(req, parsed)
        },
        genInternalRequest(),
    ))
    
    properties.TestingRun(t, gopter.ConsoleReporter(false))
}

// 测试跨协议转换的不变性
func TestProperty_CrossProtocolInvariants(t *testing.T) {
    properties := gopter.NewProperties(nil)
    
    properties.Property("OpenAI → Anthropic → OpenAI preserves semantics", prop.ForAll(
        func(req *InternalRequest) bool {
            // OpenAI → IR → Anthropic
            openaiData, _ := SerializeToOpenAI(req)
            ir1, _ := ParseOpenAIRequest(openaiData)
            anthropicData, _ := SerializeToAnthropic(ir1)
            
            // Anthropic → IR → OpenAI
            ir2, _ := ParseAnthropicRequest(anthropicData)
            finalData, _ := SerializeToOpenAI(ir2)
            
            // 再次解析
            final, _ := ParseOpenAIRequest(finalData)
            
            // 验证语义一致性（可能不是完全相等，但核心字段应保留）
            return assertSemanticEquivalence(req, final)
        },
        genInternalRequest(),
    ))
    
    properties.TestingRun(t, gopter.ConsoleReporter(false))
}
```

### 4. 实现比较逻辑

```go
// 断言两个InternalRequest核心字段相等
func assertRequestsEqual(a, b *InternalRequest) bool {
    if a == nil && b == nil {
        return true
    }
    if a == nil || b == nil {
        return false
    }
    
    // 模型必须一致
    if a.Model != b.Model {
        return false
    }
    
    // 消息数量一致
    if len(a.Messages) != len(b.Messages) {
        return false
    }
    
    // 消息内容一致
    for i := range a.Messages {
        if !assertMessagesEqual(&a.Messages[i], &b.Messages[i]) {
            return false
        }
    }
    
    // 参数一致（允许浮点误差）
    if !floatEqual(a.Temperature, b.Temperature, 0.0001) {
        return false
    }
    
    if !floatEqual(a.TopP, b.TopP, 0.0001) {
        return false
    }
    
    // Stream标志一致
    if a.Stream != b.Stream {
        return false
    }
    
    // 工具定义一致
    if len(a.Tools) != len(b.Tools) {
        return false
    }
    
    return true
}

// 断言两条消息相等
func assertMessagesEqual(a, b *Message) bool {
    if a.Role != b.Role {
        return false
    }
    
    // 内容比较（处理文本和多模态）
    switch aContent := a.Content.(type) {
    case string:
        bContent, ok := b.Content.(string)
        return ok && aContent == bContent
        
    case []ContentPart:
        bContent, ok := b.Content.([]ContentPart)
        if !ok || len(aContent) != len(bContent) {
            return false
        }
        for i := range aContent {
            if !assertContentPartsEqual(&aContent[i], &bContent[i]) {
                return false
            }
        }
        return true
        
    default:
        return false
    }
}

// 断言内容片段相等
func assertContentPartsEqual(a, b *ContentPart) bool {
    if a.Type != b.Type {
        return false
    }
    
    switch a.Type {
    case "text":
        return a.Text == b.Text
    case "image_url":
        return a.ImageURL != nil && b.ImageURL != nil &&
            a.ImageURL.URL == b.ImageURL.URL
    default:
        return true // 未知类型暂时认为相等
    }
}

// 语义等价（更宽松的比较，允许协议特定差异）
func assertSemanticEquivalence(a, b *InternalRequest) bool {
    // 核心语义：模型、消息内容、主要参数
    if a.Model != b.Model {
        return false
    }
    
    if len(a.Messages) != len(b.Messages) {
        return false
    }
    
    // 只比较消息的role和文本内容
    for i := range a.Messages {
        if a.Messages[i].Role != b.Messages[i].Role {
            return false
        }
        
        // 提取文本内容比较
        aText := extractTextContent(&a.Messages[i])
        bText := extractTextContent(&b.Messages[i])
        if aText != bText {
            return false
        }
    }
    
    return true
}

// 提取消息的文本内容
func extractTextContent(msg *Message) string {
    switch content := msg.Content.(type) {
    case string:
        return content
    case []ContentPart:
        var text string
        for _, part := range content {
            if part.Type == "text" {
                text += part.Text
            }
        }
        return text
    default:
        return ""
    }
}

// 浮点数比较（允许误差）
func floatEqual(a, b *float64, epsilon float64) bool {
    if a == nil && b == nil {
        return true
    }
    if a == nil || b == nil {
        return false
    }
    diff := *a - *b
    if diff < 0 {
        diff = -diff
    }
    return diff < epsilon
}
```

### 5. 配置测试参数

```go
func TestProperty_Configure(t *testing.T) {
    parameters := gopter.DefaultTestParameters()
    
    // 自定义迭代次数
    parameters.MinSuccessfulTests = 1000 // 默认100
    
    // 缩小失败用例
    parameters.MaxShrinkCount = 1000
    
    // 设置随机种子（可重现）
    parameters.Rng.Seed(time.Now().UnixNano())
    
    properties := gopter.NewProperties(parameters)
    
    properties.Property("Example", prop.ForAll(
        func(req *InternalRequest) bool {
            return true
        },
        genInternalRequest(),
    ))
    
    properties.TestingRun(t)
}
```

### 6. CI集成

在 `.github/workflows/test.yml` 中添加：

```yaml
- name: Run Property Tests
  run: |
    go test -v -run TestProperty ./internal/ir -timeout 10m
```

## 验收标准

- [ ] 至少5个属性测试用例
- [ ] 每个测试至少1000次迭代
- [ ] 覆盖主要协议转换路径
- [ ] 失败时能自动缩小到最小复现用例
- [ ] 集成到CI流水线

## 预期发现的问题类型

1. **字段丢失**: 某些协议不支持的字段在往返后丢失
2. **精度损失**: 浮点数序列化后精度改变
3. **顺序变化**: 数组元素顺序不保证
4. **类型转换**: 数字与字符串互转
5. **空值处理**: null vs undefined vs 空字符串

## 时间估算
- 编写生成器: 1天
- 实现属性测试: 1.5天
- 比较逻辑: 0.5天
- 修复发现的问题: 1天
```

---

## 任务1.3: 文档化IR生命周期契约

### 执行提示词

```markdown
# 任务：编写IR层生命周期文档

## 目标
为开发人员提供清晰的IR层使用指南，说明其设计理念、生命周期、性能特性和最佳实践。

## 文档结构

创建 `docs/architecture/ir-lifecycle.md`，包含以下章节：

### 1. 概述

```markdown
# IR层生命周期与设计契约

## 什么是IR层？

IR（Intermediate Representation，中间表示）层是LLM Gateway的核心抽象层，负责在不同LLM供应商的协议格式之间进行无损或语义保留的转换。

**核心职责**:
- 统一表示不同供应商的请求/响应格式
- 提供协议无关的业务逻辑处理接口
- 支持扩展字段保留，确保未来兼容性

**设计原则**:
1. **瞬态性**: IR对象仅存在于内存中，不持久化
2. **不可变性**: IR对象创建后不可修改（推荐）
3. **零拷贝**: 尽量减少数据复制
4. **协议中立**: 不偏向任何特定供应商
```

### 2. 数据流向

```markdown
## 数据流向图

### 请求流向
```
客户端 (OpenAI格式)
    ↓
[HTTP Handler]
    ↓
[ParseOpenAIRequest] ────→ InternalRequest (IR)
    ↓
[业务逻辑: 认证、路由、限流]
    ↓
[SerializeToAnthropic] ←── InternalRequest (IR)
    ↓
上游LLM (Anthropic格式)
```

### 响应流向
```
上游LLM (Anthropic格式)
    ↓
[ParseAnthropicResponse] ──→ InternalResponse (IR)
    ↓
[业务逻辑: 日志记录、计费]
    ↓
[SerializeToOpenAI] ←────── InternalResponse (IR)
    ↓
客户端 (OpenAI格式)
```

### 存储路径（元数据）
```
InternalRequest (IR)
    ↓
[ExtractMetadata] ──→ RequestMetadata
    ↓
[WriteToDB] ──→ request_logs_hot (8小时)
    ↓
[PromoteTask] ──→ request_logs (分区表, 长期存储)
```

**关键点**:
- IR对象本身**不存储**到数据库
- 只存储**元数据**（RequestID, Model, TokenCount等）
- 请求体/响应体可选择性存储（body字段，JSONB压缩）
```

### 3. 生命周期详解

```markdown
## IR对象生命周期

### 阶段1: 创建（Parse）
```go
// 从客户端请求创建IR
req, err := ir.ParseOpenAIRequest(bodyBytes)
// req 现在存在于栈/堆内存
```

**内存位置**: 通常在堆上分配（escape analysis决定）

### 阶段2: 传递（Pass-Through）
```go
// IR在各模块间传递（仅传指针）
authenticatedReq := authenticator.Validate(req)
routedReq := router.SelectBackend(authenticatedReq)
```

**注意**: 传递指针而非值，避免大对象复制

### 阶段3: 转换（Transform）
```go
// 转换为上游格式
anthropicBody, err := ir.SerializeToAnthropic(req)
// anthropicBody 是 []byte，准备发送
```

**优化**: 使用 `bytes.Buffer` 池减少分配

### 阶段4: 销毁（GC）
```go
// 请求处理完成后，IR对象自动垃圾回收
// 无需显式释放
```

**性能**: 单次请求通常分配 <10KB，GC压力小

## 内存管理策略

### 对象池（sync.Pool）

高频对象使用池复用：

```go
var requestPool = sync.Pool{
    New: func() interface{} {
        return &InternalRequest{
            Messages: make([]Message, 0, 10), // 预分配容量
        }
    },
}

// 获取
req := requestPool.Get().(*InternalRequest)

// 使用完毕后重置并归还
req.Reset()
requestPool.Put(req)
```

**适用对象**:
- `InternalRequest`
- `InternalResponse`
- `bytes.Buffer` (序列化缓冲区)

### 避免内存泄漏

**反模式**:
```go
// ❌ 错误：在goroutine中持有IR引用
go func() {
    time.Sleep(1 * time.Hour)
    log.Println(req.Model) // req无法被GC
}()
```

**正确做法**:
```go
// ✅ 正确：只传递需要的字段
model := req.Model
go func() {
    time.Sleep(1 * time.Hour)
    log.Println(model)
}()
```
```

### 4. 协议映射矩阵

```markdown
## 协议字段映射

### 请求字段映射

| IR字段 | OpenAI | Anthropic | Gemini | 说明 |
|--------|--------|-----------|--------|------|
| Model | `model` | `model` | `model` | 直接映射 |
| Messages | `messages` | `messages` | `contents` | 结构不同，需转换 |
| Temperature | `temperature` | `temperature` | `generationConfig.temperature` | 位置不同 |
| MaxTokens | `max_tokens` | `max_tokens` | `generationConfig.maxOutputTokens` | 名称/位置不同 |
| Stream | `stream` | `stream` | `stream` | 直接映射 |
| Tools | `tools` | `tools` | `tools` | OpenAI与Anthropic兼容，Gemini需转换 |
| TopP | `top_p` | `top_p` | `generationConfig.topP` | 位置不同 |
| Stop | `stop` | `stop_sequences` | `generationConfig.stopSequences` | 名称不同 |

### 已知限制

1. **Anthropic不支持**:
   - `frequency_penalty` / `presence_penalty`
   - `logprobs`
   - 处理: 存储在 `Extensions` map，返回时恢复

2. **Gemini特殊字段**:
   - `safetySettings` (安全设置)
   - 处理: 映射到 `Extensions["gemini_safety_settings"]`

3. **工具调用差异**:
   - OpenAI: `function_call` + `tool_choice`
   - Anthropic: `tool_choice` (简化版)
   - 处理: 尽力转换，不支持时报错

### 扩展机制

不支持的字段通过 `Extensions` 保留：

```go
type InternalRequest struct {
    // ... 标准字段
    
    Extensions map[string]interface{} `json:"extensions,omitempty"`
}

// 示例：保留OpenAI的logprobs
req.Extensions["openai_logprobs"] = true
```
```

### 5. 性能特性

```markdown
## 性能基准

### 解析性能（Benchmark）

```
BenchmarkParseOpenAIRequest-8     	  100000	     12340 ns/op	    4096 B/op	      10 allocs/op
BenchmarkParseAnthropicRequest-8  	   90000	     13120 ns/op	    4200 B/op	      11 allocs/op
BenchmarkParseGeminiRequest-8     	   85000	     14580 ns/op	    4500 B/op	      12 allocs/op
```

### 序列化性能

```
BenchmarkSerializeToOpenAI-8      	  120000	     10230 ns/op	    3072 B/op	       8 allocs/op
BenchmarkSerializeToAnthropic-8   	  110000	     11050 ns/op	    3200 B/op	       9 allocs/op
```

### 内存使用

典型请求IR对象大小：
- 简单文本请求: ~2KB
- 带工具定义: ~8KB
- 多模态(图片): ~15KB (包含Base64)

### 优化建议

1. **预分配切片容量**
```go
Messages: make([]Message, 0, 10) // 预计10条消息
```

2. **复用缓冲区**
```go
var bufPool = sync.Pool{
    New: func() interface{} {
        return bytes.NewBuffer(make([]byte, 0, 4096))
    },
}
```

3. **避免反射**
- 使用代码生成而非 `encoding/json` 反射（可选）
- 考虑 `jsoniter` 或 `easyjson`
```

### 6. 测试策略

```markdown
## 测试覆盖策略

### 1. 单元测试
- 每个Parse/Serialize函数独立测试
- 覆盖正常、边界、异常输入

### 2. Fuzz测试
- 畸形JSON
- 超大payload
- 特殊字符

### 3. 属性测试
- Round-trip一致性
- 跨协议转换语义保留

### 4. 集成测试
- 真实协议端到端
- 使用mock server验证上游请求格式

### 5. 性能测试
- Benchmark跟踪性能回归
- 内存profiling检测泄漏
```

### 7. 最佳实践

```markdown
## 开发者指南

### ✅ 推荐做法

1. **传递指针**
```go
func ProcessRequest(req *InternalRequest) error
```

2. **不修改IR对象**
```go
// 需要修改时，创建副本
newReq := *req
newReq.Model = "gpt-4"
```

3. **使用对象池**
```go
req := requestPool.Get().(*InternalRequest)
defer func() {
    req.Reset()
    requestPool.Put(req)
}()
```

4. **错误处理**
```go
req, err := ParseOpenAIRequest(body)
if err != nil {
    return fmt.Errorf("parse error: %w", err)
}
```

### ❌ 反模式

1. **持久化IR对象**
```go
// ❌ 不要这样做
json.Marshal(req) // 然后存到DB
```

2. **在goroutine中持有引用**
```go
// ❌ 可能导致内存泄漏
go func() {
    time.Sleep(time.Hour)
    use(req)
}()
```

3. **修改共享IR**
```go
// ❌ 并发不安全
req.Model = "new-model" // 如果多个goroutine使用同一个req
```

### 添加新协议

步骤：
1. 在 `internal/ir/types.go` 添加必要字段到IR
2. 实现 `ParseXXXRequest` / `ParseXXXResponse`
3. 实现 `SerializeToXXX`
4. 添加单元测试、fuzz测试、round-trip测试
5. 更新协议映射矩阵文档
6. 更新集成测试
```

## 验收标准

- [ ] 文档包含完整的7个章节
- [ ] 至少2个数据流向图（Mermaid或图片）
- [ ] 协议映射矩阵完整（至少3个协议）
- [ ] 性能基准数据（通过实际benchmark获取）
- [ ] 最佳实践章节包含代码示例

## 时间估算
- 文档编写: 1.5天
- 绘制流程图: 0.5天
```

---

## 总结

以上提示词覆盖了任务1的三个子任务：

1. **Fuzzing测试**: 详细的实现指南、种子用例、CI集成
2. **属性测试**: 完整的生成器、属性定义、比较逻辑
3. **文档化**: 结构化的文档大纲、代码示例、图表

每个提示词都可以直接复制给执行者（人或AI代理），包含：
- 背景和目标
- 具体实现步骤
- 代码框架
- 验收标准
- 时间估算

**下一步**:
继续生成任务2-10的详细提示词文档。
