# LLM Gateway 架构重构指南

## 概述

本文档描述了 llm-gateway-go 的会话传输与转换架构重构，旨在实现：

1. **统一数据格式标准（IR）**：所有协议转换都通过 Internal Representation
2. **明确的会话状态机**：清晰的状态转换和回调机制
3. **完整的客户端适配器**：适配器参与完整的数据处理生命周期
4. **附件独立管理**：附件提取→存储→引用转换与消息处理解耦

## 新增核心模块

### 1. 会话状态机 (`domains/session/state_machine.go`)

**目的**：提供清晰的会话生命周期状态管理

**7 个核心状态**：
```
INITIAL → RECEIVING_FROM_CLIENT → PENDING_TO_LLM → SENDING_TO_LLM →
RECEIVING_FROM_LLM → PENDING_TO_CLIENT → SENDING_TO_CLIENT → COMPLETED
```

**使用示例**：
```go
sm := session.NewStateMachine()

// 注册状态转换回调
sm.RegisterCallback(session.StateReceivingFromClient, func(ctx context.Context, sc *session.SessionContext) error {
    // 会话审计
    return sessionAudit.Check(ctx, sc)
})

sm.RegisterCallback(session.StatePendingToLLM, func(ctx context.Context, sc *session.SessionContext) error {
    // 会话压缩
    return sessionCompressor.Compress(ctx, sc)
})

// 执行状态转换
err := sm.Transition(ctx, sessionCtx, session.StateReceivingFromClient, "request_received")
```

### 2. 会话上下文 (`domains/session/context.go`)

**目的**：贯穿整个请求生命周期的数据容器

**核心字段**：
```go
type SessionContext struct {
    // 基础标识
    SessionID, RequestID, TenantID string
    
    // 状态
    State SessionState
    Transitions []StateTransition
    
    // 数据快照
    ClientRawBody []byte
    ClientIR *ir.InternalRequest
    UpstreamBody []byte
    LLMResponseIR *ir.InternalResponse
    ClientFinalBody []byte
    
    // 附件（只存元数据）
    Attachments []attachments.AttachmentMetadata
    
    // 元数据
    Metadata map[string]any
}
```

**使用示例**：
```go
// 创建会话上下文
sc := session.NewSessionContext(r)
sc.SessionID = extractSessionID(r)
sc.RequestID = generateRequestID()
sc.ClientType = "cursor"

// 在整个生命周期中传递
transport.TransformRequest(ctx, sc)
executor.Execute(ctx, sc)
transport.TransformResponse(ctx, sc)
```

### 3. 统一转换层 (`domains/transformation/unified_transport.go`)

**目的**：废弃 Legacy Transport 和 anthropic_bridge，统一所有协议转换

**核心方法**：
```go
// 请求转换：Client → Upstream
func (t *UnifiedTransport) TransformRequest(ctx context.Context, sc *session.SessionContext) error

// 响应转换：Upstream → Client
func (t *UnifiedTransport) TransformResponse(ctx context.Context, sc *session.SessionContext, upstreamBody []byte) error

// 流式转换：统一处理
func (t *UnifiedTransport) TransformStream(ctx context.Context, sc *session.SessionContext, upstreamResp *http.Response, clientWriter http.ResponseWriter) error
```

**转换流程**：
```
Client Request (JSON)
    ↓
Protocol Detect → "openai-chat"
    ↓
ir.ParseOpenAI() → InternalRequest
    ↓
Model Mapping + Transformations
    ↓
ir.SerializeAnthropic() → Anthropic JSON
    ↓
Upstream Request
```

### 4. 附件 IR 转换器 (`domains/attachments/ir_transformer.go`)

**目的**：将 IR 中的 base64 附件转换为存储 URL 引用

**核心方法**：
```go
// 转换请求 IR 中的附件
func (t *IRTransformer) TransformRequest(ctx context.Context, requestID string, ir *ir.InternalRequest) ([]AttachmentMetadata, error)

// 持久化附件元数据到数据库（只存 hash/path，不存文件内容）
func (t *IRTransformer) PersistMetadata(ctx context.Context, db interface{}, requestID string, attachments []AttachmentMetadata) error
```

**存储原则**：
- ✅ 附件文件存储在文件系统/OSS/S3
- ✅ 数据库只存储：`{hash, path, size, content_type, created_at}`
- ✅ 使用 SHA256 去重，相同内容只存储一份
- ❌ 不在数据库中存储 base64 或二进制数据

**使用示例**：
```go
transformer := attachments.NewIRTransformer(storage, "https://gateway.example.com/attachments")

// 转换 IR 中的附件引用
attachmentsMeta, err := transformer.TransformRequest(ctx, requestID, sc.ClientIR)
if err != nil {
    return err
}
sc.Attachments = attachmentsMeta

// 持久化元数据到数据库
err = transformer.PersistMetadata(ctx, db, requestID, attachmentsMeta)
```

### 5. 增强的客户端适配器 (`domains/streaming/client_adapter_v2.go`)

**目的**：让适配器在 IR 层面修改数据，而非仅处理 JSON

**新接口**：
```go
// IR 转换接口
type IRTransformer interface {
    TransformRequestIR(ctx context.Context, ir *ir.InternalRequest) (*ir.InternalRequest, error)
    TransformResponseIR(ctx context.Context, ir *ir.InternalResponse) (*ir.InternalResponse, error)
}

// 会话感知接口
type SessionAwareAdapter interface {
    OnSessionStart(ctx context.Context, sessionID string, metadata map[string]any) error
    OnSessionEnd(ctx context.Context, sessionID string, metadata map[string]any) error
}
```

**适配器示例**（Cursor）：
```go
func (a *CursorAdapter) TransformRequestIR(ctx context.Context, req *ir.InternalRequest) (*ir.InternalRequest, error) {
    // 1. 补充缺失的 tool_call_id
    for i := range req.Messages {
        if req.Messages[i].Role == "assistant" {
            for j := range req.Messages[i].ToolCalls {
                if req.Messages[i].ToolCalls[j].ID == "" {
                    req.Messages[i].ToolCalls[j].ID = fmt.Sprintf("cursor_call_%d", j)
                }
            }
        }
    }
    
    // 2. 长上下文标记
    if len(req.Messages) > 20 {
        if req.Metadata == nil {
            req.Metadata = &ir.Metadata{}
        }
        if req.Metadata.Other == nil {
            req.Metadata.Other = make(map[string]string)
        }
        req.Metadata.Other["_cursor_long_context"] = "true"
    }
    
    return req, nil
}
```

## 迁移步骤

### Phase 1: 集成新模块到现有代码（渐进式）

#### 1.1 在 main.go 中初始化新组件

```go
// 创建状态机
stateMachine := session.NewStateMachine()

// 注册回调
stateMachine.RegisterCallback(session.StateReceivingFromClient, func(ctx context.Context, sc *session.SessionContext) error {
    // 会话审计回调
    if sessionAuditHook != nil {
        return sessionAuditHook.Check(ctx, sc)
    }
    return nil
})

stateMachine.RegisterCallback(session.StatePendingToLLM, func(ctx context.Context, sc *session.SessionContext) error {
    // 会话压缩回调
    if sessionCompressor != nil {
        return sessionCompressor.Prepare(ctx, sc)
    }
    return nil
})

// 创建统一转换层
unifiedTransport := transformation.NewUnifiedTransport()

// 创建附件转换器
attachmentTransformer := attachments.NewIRTransformer(
    attachmentStorage,
    os.Getenv("ATTACHMENT_BASE_URL"),
)

// 注入到 handler
chatHandler.SetStateMachine(stateMachine)
chatHandler.SetUnifiedTransport(unifiedTransport)
chatHandler.SetAttachmentTransformer(attachmentTransformer)
```

#### 1.2 修改 ChatHandler.ServeHTTP（示例）

```go
func (h *ChatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    
    // 1. 创建会话上下文
    sc := session.NewSessionContext(r)
    sc.SessionID = extractSessionID(r)
    sc.RequestID = generateRequestID()
    sc.TenantID = extractTenantID(ctx)
    sc.ClientType = identifyClientType(r)
    
    // 2. 读取请求体
    body, err := io.ReadAll(r.Body)
    if err != nil {
        http.Error(w, "failed to read body", http.StatusBadRequest)
        return
    }
    sc.ClientRawBody = body
    
    // 3. 状态转换：INITIAL → RECEIVING_FROM_CLIENT
    if err := h.stateMachine.Transition(ctx, sc, session.StateReceivingFromClient, "request_received"); err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    
    // 4. 协议转换：Client → IR
    if err := h.unifiedTransport.TransformRequest(ctx, sc); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }
    
    // 5. 附件处理
    if h.attachmentTransformer != nil {
        attachments, _ := h.attachmentTransformer.TransformRequest(ctx, sc.RequestID, sc.ClientIR)
        sc.Attachments = attachments
    }
    
    // 6. 客户端适配器转换
    if adapter := GetClientAdapter(sc.ClientType); adapter != nil {
        if transformer, ok := adapter.(IRTransformer); ok {
            sc.UpstreamIR, _ = transformer.TransformRequestIR(ctx, sc.ClientIR)
        }
    }
    
    // 7. 状态转换：RECEIVING_FROM_CLIENT → PENDING_TO_LLM
    if err := h.stateMachine.Transition(ctx, sc, session.StatePendingToLLM, "parsed"); err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    
    // 8. 路由选择
    candidates, _ := h.providerClient.GetCandidates(ctx, sc.ClientIR.Model)
    if len(candidates) == 0 {
        http.Error(w, "no available providers", http.StatusServiceUnavailable)
        return
    }
    selected := candidates[0]
    sc.CredentialID = selected.CredentialID
    sc.UpstreamProtocol = selected.Protocol
    sc.UpstreamModel = selected.RawModel
    
    // 9. 序列化上游请求
    sc.UpstreamBody, _ = ir.SerializeRequest(sc.UpstreamProtocol, sc.UpstreamIR)
    
    // 10. 状态转换：PENDING_TO_LLM → SENDING_TO_LLM
    if err := h.stateMachine.Transition(ctx, sc, session.StateSendingToLLM, "routed"); err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    
    // 11. 调用上游
    upstreamResp, err := h.executor.Execute(ctx, sc)
    if err != nil {
        sc.MarkError(err)
        h.stateMachine.Transition(ctx, sc, session.StateError, err.Error())
        http.Error(w, err.Error(), http.StatusBadGateway)
        return
    }
    
    // 12. 状态转换：SENDING_TO_LLM → RECEIVING_FROM_LLM
    h.stateMachine.Transition(ctx, sc, session.StateReceivingFromLLM, "llm_responded")
    
    // 13. 响应处理
    if sc.IsStreaming {
        // 流式转换
        h.unifiedTransport.TransformStream(ctx, sc, upstreamResp, w)
    } else {
        // 非流式转换
        upstreamBody, _ := io.ReadAll(upstreamResp.Body)
        h.unifiedTransport.TransformResponse(ctx, sc, upstreamBody)
        w.Write(sc.ClientFinalBody)
    }
    
    // 14. 状态转换：RECEIVING_FROM_LLM → COMPLETED
    h.stateMachine.Transition(ctx, sc, session.StateCompleted, "done")
    
    // 15. 持久化附件元数据
    if len(sc.Attachments) > 0 {
        h.attachmentTransformer.PersistMetadata(ctx, h.db, sc.RequestID, sc.Attachments)
    }
}
```

### Phase 2: 废弃旧代码

将以下文件移动到 `_to_be_deleted/` 目录：

1. **Legacy Transport**：
   - `domains/transformation/legacy_transport.go`
   
2. **Anthropic Bridge**（已被 UnifiedTransport 替代）：
   - `domains/streaming/anthropic_bridge.go`

3. **旧的适配器实现**（可选，如果完全重写）：
   - 保留 `domains/streaming/client_adapter.go`（接口定义）
   - 迁移 `domains/streaming/client_adapter_impl.go` 中的实现到 `client_adapter_v2.go`

### Phase 3: 测试验证

```bash
# 运行单元测试
go test ./domains/session/...
go test ./domains/transformation/...
go test ./domains/attachments/...

# 运行集成测试
go test ./cmd/gateway/... -tags=integration

# 性能测试
go test -bench=. ./domains/transformation/...
```

## 架构优势

### Before vs After

| 方面 | Before | After |
|------|--------|-------|
| **协议转换** | 3套系统（IR/Legacy/Bridge） | 1套统一系统（IR） |
| **会话状态** | 隐式，分散在多处 | 显式状态机，7个清晰状态 |
| **客户端适配** | 只处理元数据 | 完整参与IR转换 |
| **附件处理** | 与消息处理混杂 | 独立提取→存储→引用 |
| **流式处理** | 独立代码路径 | 与非流式统一管道 |
| **扩展性** | O(N²) | O(N) |

### 关键改进点

1. **维护成本降低**：协议转换逻辑从3处合并为1处
2. **可调试性提升**：SessionContext 记录完整的数据快照和状态转换历史
3. **扩展性增强**：添加新协议只需实现 Parser 和 Serializer
4. **附件性能优化**：去重存储，数据库只存元数据
5. **客户端定制能力**：适配器可在 IR 层面精确控制数据格式

## 常见问题

### Q1: 如何兼容现有代码？

**A**: 新模块是渐进式的，可以逐步集成：
1. 先集成状态机和会话上下文（不影响现有逻辑）
2. 逐步将转换逻辑从 Legacy/Bridge 迁移到 UnifiedTransport
3. 通过 feature flag 控制新旧代码路径

### Q2: 性能是否有影响？

**A**: 
- **IR 转换**：通过 JSON 序列化/反序列化，性能损失 < 5%
- **附件处理**：异步存储，不阻塞请求转发
- **状态机**：内存操作，overhead 可忽略

### Q3: 如何处理旧数据库记录？

**A**: 
- 新字段向后兼容：`attachments` JSONB 列，旧记录为 NULL
- 状态转换历史：新增 `request_logs.state_transitions` JSONB 列（可选）

### Q4: 如何回滚？

**A**: 
1. 保留 `_to_be_deleted/` 目录中的旧代码
2. 通过环境变量 `USE_UNIFIED_TRANSPORT=false` 切回 Legacy
3. 灰度部署时保持双路径运行 1 周

## 后续优化建议

1. **性能优化**：使用对象池减少 IR 分配
2. **监控增强**：为每个状态转换添加 metrics
3. **错误恢复**：状态机支持 checkpoint 和 resume
4. **分布式追踪**：SessionContext 集成 OpenTelemetry
5. **配置化**：状态机回调通过配置文件动态注册

## 参考资料

- [Internal Representation 设计文档](./docs/ir-design.md)
- [客户端适配器开发指南](./docs/client-adapters.md)
- [附件存储架构](./docs/attachment-storage.md)
- [状态机模式最佳实践](https://refactoring.guru/design-patterns/state)
