# 客户端适配器设计文档

**版本**: v1.0  
**日期**: 2026-07-02  
**作者**: Kiro (AI Agent)

---

## 📋 概述

客户端适配器系统为不同的 AI 编程助手提供统一的适配层，通过设计模式实现低耦合、高扩展性的客户端支持。

### 设计目标

1. **统一接口**: 对外提供一致的 API，隐藏客户端差异
2. **易于扩展**: 新增客户端无需修改现有代码
3. **低耦合**: 适配器之间相互独立
4. **代码复用**: 通过基类提供默认实现

---

## 🏗️ 架构设计

### 采用的设计模式

#### 1. 适配器模式（Adapter Pattern）

**意图**: 将一个类的接口转换成客户期望的另一个接口

```go
// 统一接口
type ClientAdapter interface {
    PreprocessRequest(ctx, reqBody) (reqBody, error)
    PostprocessResponse(ctx, respBody) (respBody, error)
    // ...
}

// 具体适配器
type CursorAdapter struct {
    BaseClientAdapter  // 继承基类
}
```

**优点**:
- 复用现有代码
- 符合开闭原则
- 客户端特性隔离

#### 2. 工厂模式（Factory Pattern）

**意图**: 提供创建适配器的统一入口

```go
// 工厂函数
func GetClientAdapter(r *http.Request) ClientAdapter {
    clientType := extractClientType(r)
    return defaultRegistry.Get(clientType)
}
```

**优点**:
- 集中管理创建逻辑
- 隐藏实例化细节
- 便于切换实现

#### 3. 注册表模式（Registry Pattern）

**意图**: 集中管理所有适配器实例

```go
type ClientAdapterRegistry struct {
    adapters map[string]ClientAdapter
}

func init() {
    defaultRegistry.Register(NewCursorAdapter())
    defaultRegistry.Register(NewCopilotAdapter())
    // ...
}
```

**优点**:
- 单例管理
- 按需查找
- 支持运行时注册

#### 4. 模板方法模式（Template Method Pattern）

**意图**: 基类定义算法骨架，子类实现特定步骤

```go
type BaseClientAdapter struct {
    name string
}

// 提供默认实现
func (b *BaseClientAdapter) PreprocessRequest(ctx, reqBody) (reqBody, error) {
    return reqBody, nil  // 默认不处理
}

// 子类覆盖
func (c *CursorAdapter) PreprocessRequest(ctx, reqBody) (reqBody, error) {
    // Cursor 特定的预处理逻辑
    return enhancedReqBody, nil
}
```

**优点**:
- 代码复用
- 减少重复
- 灵活扩展

---

## 🎯 接口设计

### ClientAdapter 接口

```go
type ClientAdapter interface {
    // 标识
    Name() string
    
    // 请求生命周期
    PreprocessRequest(ctx, reqBody) (reqBody, error)
    PostprocessResponse(ctx, respBody) (respBody, error)
    ProcessStreamChunk(ctx, chunk) (chunk, error)
    ValidateRequest(ctx, reqBody) []error
    
    // 优化提示
    GetOptimizationHints() OptimizationHints
    
    // 特性开关
    ShouldEnableToolCallTracking() bool
    ShouldEnableStrictProtocol() bool
    
    // 配置参数
    GetMaxRetries() int
    GetTimeout() int
}
```

### OptimizationHints 结构

```go
type OptimizationHints struct {
    PreferLowLatency      bool  // 优先低延迟
    PreferHighQuality     bool  // 优先高质量
    ExpectsLongContext    bool  // 期望长上下文
    ExpectsMultiTurn      bool  // 期望多轮对话
    ExpectsToolCalls      bool  // 期望工具调用
    CacheEnabled          bool  // 启用缓存
    MaxConcurrentRequests int   // 最大并发数
}
```

---

## 📊 客户端特性对比

| 客户端 | 低延迟 | 长上下文 | 多轮对话 | Tool追踪 | 超时(秒) | 并发数 |
|--------|--------|---------|---------|---------|---------|--------|
| **Cursor** | ❌ | ✅ | ✅ | ✅ | 90 | 5 |
| **Windsurf** | ❌ | ✅ | ✅ | ✅ | 60 | 5 |
| **Copilot** | ✅ | ❌ | ❌ | ❌ | 30 | 10 |
| **VSCode** | ❌ | ❌ | ✅ | ✅ | 60 | 5 |
| **Zed** | ✅ | ❌ | ❌ | ❌ | 60 | 8 |
| **JetBrains** | ❌ | ✅ | ✅ | ✅ | 120 | 3 |
| **Generic** | ❌ | ❌ | ❌ | ❌ | 60 | ∞ |

---

## 🔄 使用流程

### 1. 请求处理流程

```
HTTP Request
    ↓
extractClientType() → 识别客户端类型
    ↓
GetClientAdapter() → 获取对应适配器
    ↓
adapter.PreprocessRequest() → 预处理请求
    ↓
adapter.ValidateRequest() → 验证请求
    ↓
adapter.GetOptimizationHints() → 获取路由提示
    ↓
[路由到上游模型]
    ↓
adapter.PostprocessResponse() → 后处理响应
    ↓
返回给客户端
```

### 2. 流式响应处理

```
SSE Stream
    ↓
for each chunk:
    adapter.ProcessStreamChunk(chunk)
    ↓
    修改/增强/过滤 chunk
    ↓
    发送给客户端
```

---

## 💡 实现示例

### 添加新客户端适配器

```go
// 1. 定义新适配器
type NewClientAdapter struct {
    BaseClientAdapter
}

// 2. 实现构造函数
func NewNewClientAdapter() *NewClientAdapter {
    return &NewClientAdapter{
        BaseClientAdapter: BaseClientAdapter{name: "newclient"},
    }
}

// 3. 覆盖需要定制的方法
func (a *NewClientAdapter) PreprocessRequest(ctx context.Context, reqBody map[string]any) (map[string]any, error) {
    // 特定的预处理逻辑
    return reqBody, nil
}

func (a *NewClientAdapter) GetOptimizationHints() OptimizationHints {
    return OptimizationHints{
        PreferLowLatency: true,
        // ...
    }
}

// 4. 注册到注册表
func init() {
    defaultRegistry.Register(NewNewClientAdapter())
}
```

**添加新适配器的步骤**:
1. 创建新结构体，嵌入 `BaseClientAdapter`
2. 实现构造函数
3. 覆盖需要定制的方法（可选）
4. 注册到默认注册表

**代码量**: 约50-100行（取决于定制程度）

### 在 Handler 中集成

```go
func (h *Handler) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
    // 1. 获取客户端适配器
    adapter := GetClientAdapter(r)
    
    // 2. 预处理请求
    reqBody := parseRequestBody(r)
    processedReq, err := adapter.PreprocessRequest(r.Context(), reqBody)
    if err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }
    
    // 3. 验证请求
    if errors := adapter.ValidateRequest(r.Context(), processedReq); len(errors) > 0 {
        http.Error(w, fmt.Sprintf("Validation errors: %v", errors), http.StatusBadRequest)
        return
    }
    
    // 4. 获取优化提示
    hints := adapter.GetOptimizationHints()
    
    // 5. 根据提示进行路由决策
    routeCtx := RouteContext{
        PreferLowLatency: hints.PreferLowLatency,
        ExpectsToolCalls: hints.ExpectsToolCalls,
        // ...
    }
    
    // 6. 启用特性开关
    if adapter.ShouldEnableToolCallTracking() {
        enableToolCallTracking(r.Context())
    }
    
    // 7. 设置超时
    ctx, cancel := context.WithTimeout(r.Context(), time.Duration(adapter.GetTimeout())*time.Second)
    defer cancel()
    
    // 8. 调用上游...
    response := callUpstream(ctx, processedReq, routeCtx)
    
    // 9. 后处理响应
    processedResp, _ := adapter.PostprocessResponse(r.Context(), response)
    
    // 10. 返回响应
    json.NewEncoder(w).Encode(processedResp)
}
```

---

## 🧪 测试策略

### 单元测试

```go
func TestCursorAdapter(t *testing.T) {
    adapter := NewCursorAdapter()
    
    // 测试属性
    assert.Equal(t, "cursor", adapter.Name())
    
    // 测试优化提示
    hints := adapter.GetOptimizationHints()
    assert.True(t, hints.ExpectsLongContext)
    assert.True(t, hints.ExpectsToolCalls)
    
    // 测试请求预处理
    reqBody := map[string]any{"messages": makeLongMessages(25)}
    processed, err := adapter.PreprocessRequest(ctx, reqBody)
    assert.NoError(t, err)
    assert.True(t, processed["_cursor_long_context"].(bool))
}
```

### 集成测试

```go
func TestClientAdapterIntegration(t *testing.T) {
    // 模拟 Cursor 请求
    req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
    req.Header.Set("User-Agent", "cursor/0.1")
    
    adapter := GetClientAdapter(req)
    assert.Equal(t, "cursor", adapter.Name())
    
    // 验证完整流程
    reqBody := createTestRequest()
    processed, _ := adapter.PreprocessRequest(ctx, reqBody)
    errors := adapter.ValidateRequest(ctx, processed)
    assert.Empty(t, errors)
}
```

### 性能测试

```go
func BenchmarkAdapterPreprocess(b *testing.B) {
    adapter := NewCursorAdapter()
    reqBody := createTestRequest()
    
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        adapter.PreprocessRequest(context.Background(), reqBody)
    }
}
```

---

## 📈 性能考虑

### 1. 内存开销

- **注册表**: 单例模式，所有适配器共享一个实例
- **适配器实例**: 每个客户端类型一个实例（7个）
- **预估内存**: < 1MB

### 2. CPU 开销

- **客户端识别**: O(1) map 查找
- **请求预处理**: O(n) 其中 n 是请求体大小
- **优化**: 预处理逻辑简单，避免深拷贝

### 3. 延迟影响

- **额外延迟**: < 1ms（预处理 + 验证）
- **可忽略**: 相比网络和模型推理时间

---

## 🔒 安全考虑

### 1. 输入验证

每个适配器的 `ValidateRequest()` 方法负责验证输入：

```go
func (a *CursorAdapter) ValidateRequest(ctx context.Context, reqBody map[string]any) []error {
    var errors []error
    
    // 检查必填字段
    if messages, ok := reqBody["messages"].([]any); !ok || len(messages) == 0 {
        errors = append(errors, fmt.Errorf("messages required"))
    }
    
    // 检查 tool_call_id
    // ...
    
    return errors
}
```

### 2. 注入防护

- 不执行客户端提供的代码
- 不使用 `eval()` 或反射执行
- 所有修改都在白名单内

### 3. 资源限制

- 并发数限制：`MaxConcurrentRequests`
- 超时控制：`GetTimeout()`
- 大小限制：在预处理中检查

---

## 📝 最佳实践

### 1. 添加新适配器

✅ **推荐**:
```go
// 继承基类，只覆盖需要的方法
type NewAdapter struct {
    BaseClientAdapter
}

func (a *NewAdapter) GetOptimizationHints() OptimizationHints {
    return OptimizationHints{/* 定制 */}
}
```

❌ **不推荐**:
```go
// 从零实现所有方法（代码重复）
type NewAdapter struct{}

func (a *NewAdapter) Name() string { return "new" }
func (a *NewAdapter) PreprocessRequest(...) { /* 重复逻辑 */ }
// ...
```

### 2. 预处理逻辑

✅ **推荐**:
```go
// 最小化修改，添加标记
func (a *Adapter) PreprocessRequest(ctx, reqBody) (reqBody, error) {
    if needsSpecialHandling(reqBody) {
        reqBody["_adapter_hint"] = "special"
    }
    return reqBody, nil
}
```

❌ **不推荐**:
```go
// 大量修改原始请求（破坏性）
func (a *Adapter) PreprocessRequest(ctx, reqBody) (reqBody, error) {
    return completelyRewriteRequest(reqBody), nil
}
```

### 3. 错误处理

✅ **推荐**:
```go
// 返回明确的错误信息
func (a *Adapter) ValidateRequest(ctx, reqBody) []error {
    var errors []error
    if missing := checkRequired(reqBody); missing != nil {
        errors = append(errors, fmt.Errorf("missing field: %s", missing))
    }
    return errors
}
```

❌ **不推荐**:
```go
// 直接 panic 或返回 generic error
func (a *Adapter) ValidateRequest(ctx, reqBody) []error {
    if !isValid(reqBody) {
        panic("invalid request")
    }
    return nil
}
```

---

## 🚀 未来扩展

### 短期（1个月）

1. **添加更多客户端**
   - Cline
   - Claude Code (Anthropic 官方)
   - Continue

2. **增强功能**
   - 请求指纹识别
   - 异常检测
   - 自动降级

### 中期（3个月）

1. **智能路由**
   - 基于客户端特性的动态路由
   - A/B 测试支持
   - 流量分配策略

2. **监控指标**
   - 每个客户端的成功率
   - 延迟分布
   - 错误类型统计

### 长期（6个月）

1. **自适应优化**
   - 机器学习驱动的参数调整
   - 自动发现客户端模式
   - 预测性优化

2. **插件系统**
   - 动态加载适配器
   - 热更新支持
   - 第三方扩展

---

## 📚 参考资料

### 设计模式

- **《设计模式：可复用面向对象软件的基础》** - GoF
- **《Head First 设计模式》** - Freeman & Freeman

### Go 最佳实践

- **Effective Go**: https://go.dev/doc/effective_go
- **Go Code Review Comments**: https://github.com/golang/go/wiki/CodeReviewComments

### 相关文档

- `docs/PRICING_AND_CLIENT_AUDIT_2026_07_02.md` - 客户端特性审计
- `docs/fixes/2026-06-23-tool-call-id-missing.md` - Tool ID 问题修复
- `domains/streaming/client_fingerprint.go` - 客户端识别

---

## ✅ 总结

### 优势

1. **低耦合**: 适配器之间相互独立
2. **易扩展**: 新增客户端代码量小（50-100行）
3. **可测试**: 每个适配器独立测试
4. **高性能**: 额外开销 < 1ms

### 适用场景

- ✅ 需要支持多种客户端
- ✅ 客户端行为差异大
- ✅ 需要针对性优化
- ✅ 协议需要适配

### 不适用场景

- ❌ 只有单一客户端
- ❌ 客户端行为完全一致
- ❌ 不需要定制化

---

**文档版本**: v1.0  
**最后更新**: 2026-07-02  
**维护者**: LLM Gateway Team
