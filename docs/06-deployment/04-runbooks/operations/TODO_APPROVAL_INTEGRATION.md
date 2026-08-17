# 审批流程集成 TODO

本文档列出了审批流程剩余的集成工作。

## 已完成 ✅

### E2: SessionCache v6 扩展 + 审批 Hook + 恢复处理器
- [x] SessionState v6 字段扩展（8个新字段）
- [x] ApprovalHook (PreRouting，priority=110)
- [x] CacheUpdateHook (PostRouting，priority=400)
- [x] ApprovalResumeHandler
- [x] PendingStoreAdapter (ApprovalPendingWriter)
- [x] PendingStoreResponder (ClientResponder)
- [x] LLMCallerFunc (LLMCaller adapter)
- [x] 单元测试（覆盖率 80%+）

**Commits:**
- `3e204884`: SessionCache v6 扩展 + 审批 Hook + 恢复处理器
- `8d97a287`: 实现审批恢复 adapters

## 待完成 ⏳

### 1. main.go 集成 (高优先级)

#### 1.1 创建依赖实例

在 `cmd/gateway/main.go` 中添加：

```go
// 在 approvalMgr 创建后
var (
    approvalMgr        *sessionaudit.ApprovalManager
    approvalHook       *sessionaudithook.ApprovalHook
    cacheUpdateHook    *sessionaudithook.CacheUpdateHook
    approvalResumeHdlr *session.ApprovalResumeHandler
)

// 创建 SessionCache
sessionCache := compression.NewSessionCache(redisClientForCache, dbConn.Pool())

// 创建 CacheUpdateHook
cacheUpdateHook = sessionaudithook.NewCacheUpdateHook(sessionCache)

// 创建 ApprovalHook（需要 Notifier 和 EventBus）
// 假设已有 approvalNotifier 和 auditBus
approvalHook = sessionaudithook.NewApprovalHook(
    approvalMgr,
    auditBus,
    approvalNotifier,
    cacheUpdateHook,
    15*time.Minute, // approval timeout
)

// 创建 pending.Store（如果还没有）
pendingStore := pending.NewStore(redisClientForPending)

// 创建 adapters
pendingWriter := session.NewPendingStoreAdapter(pendingStore)
clientResponder := session.NewPendingStoreResponder(pendingStore)

// 创建 LLMCaller（包装 streaming.ChatHandler）
llmCaller := session.LLMCallerFunc(func(ctx context.Context, snap *sessionaudit.RequestSnapshot) error {
    // TODO: 实现从 snapshot 恢复 LLM 调用的逻辑
    // 1. 反序列化 snap.BodyBytes 为请求对象
    // 2. 重建 http.Request
    // 3. 调用 streaming.ChatHandler
    // 4. 写响应到 pending.Store
    return nil
})

// 创建 ApprovalResumeHandler
approvalResumeHdlr = session.NewApprovalResumeHandler(
    sessionCache,
    approvalMgr,
    llmCaller,
    clientResponder,
    pendingWriter,
)
```

#### 1.2 注入到 v2 Pipeline (可选)

如果启用 `LLM_GATEWAY_USE_V2_PIPELINE=true`，在 `cmd/gateway/main_v2_pipeline.go` 中添加：

```go
// buildV2Pipeline 中，在 session_inspect 之后添加
if deps.ApprovalMgr != nil {
    p.AddStage(&pipeline.PipelineStage{
        Name: "session_audit", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
        Hooks: []pipeline.Hook{
            deps.ApprovalHook,
        },
    })
}

// 在 PostResponse 阶段添加
if deps.CacheUpdateHook != nil {
    p.AddStage(&pipeline.PipelineStage{
        Name: "cache_update", Phase: pipeline.PhasePostResponse, Mode: pipeline.ModeSequential,
        Hooks: []pipeline.Hook{
            deps.CacheUpdateHook,
        },
    })
}
```

**依赖修改:**
- `v2PipelineDeps` 结构体需要添加 `ApprovalHook` 和 `CacheUpdateHook` 字段
- `newV2PipelineDeps` 需要接收这些参数

#### 1.3 v1 集成（兼容旧代码）

保持现有的 `SessionAuditHookV1` 集成不变，或者逐步迁移到新的 Hook 架构。

### 2. Admin API endpoint (高优先级)

#### 2.1 添加 resume 端点

在 `admin/approval_handler.go` 中添加：

```go
// POST /api/admin/approvals/:id/resume
func (h *ApprovalHandler) HandleResume(c echo.Context) error {
    approvalID := c.Param("id")
    tenantID := getTenantIDFromContext(c) // 从认证中获取

    if err := h.resumeHandler.ResumeAfterApproval(c.Request().Context(), approvalID, tenantID); err != nil {
        if errors.Is(err, session.ErrResumeNotPending) {
            return c.JSON(400, map[string]string{"error": "approval not in pending state"})
        }
        if errors.Is(err, session.ErrResumeSnapshotMissing) {
            return c.JSON(500, map[string]string{"error": "snapshot missing"})
        }
        return c.JSON(500, map[string]string{"error": err.Error()})
    }

    return c.JSON(200, map[string]string{"status": "resumed"})
}
```

#### 2.2 注册路由

在 `cmd/gateway/main.go` 或 admin handler 初始化代码中：

```go
adminHandler.SetApprovalResumeHandler(approvalResumeHdlr)

// 在 Echo router 中
e.POST("/api/admin/approvals/:id/resume", adminHandler.HandleResume)
```

### 3. LLMCaller 实现细节 (中优先级)

`LLMCallerFunc` 需要实现从 `RequestSnapshot` 恢复 LLM 调用的逻辑：

```go
llmCaller := session.LLMCallerFunc(func(ctx context.Context, snap *sessionaudit.RequestSnapshot) error {
    // 1. 反序列化请求体
    var req map[string]any
    if err := json.Unmarshal(snap.BodyBytes, &req); err != nil {
        return fmt.Errorf("unmarshal request: %w", err)
    }

    // 2. 重建 HTTP 请求
    body := bytes.NewReader(snap.BodyBytes)
    httpReq, _ := http.NewRequestWithContext(ctx, "POST", "/v1/chat/completions", body)
    httpReq.Header.Set("Content-Type", "application/json")
    httpReq.Header.Set("X-Session-ID", snap.SessionID)
    httpReq.Header.Set("X-Tenant-ID", snap.TenantID)
    httpReq.Header.Set("X-Client-IP", snap.ClientInfo.IP)
    httpReq.Header.Set("User-Agent", snap.ClientInfo.UserAgent)

    // 3. 创建 ResponseRecorder
    rec := httptest.NewRecorder()

    // 4. 调用 streaming.ChatHandler
    chatHandler.HandleChat(rec, httpReq)

    // 5. 将响应写入 pending.Store
    resp := &pending.Response{
        SessionID:     snap.SessionID,
        TenantID:      snap.TenantID,
        RequestID:     snap.RequestID,
        Status:        pending.StatusCompleted,
        Body:          rec.Body.String(),
        ContentType:   rec.Header().Get("Content-Type"),
        CreatedAt:     time.Now().Unix(),
        CompletedAt:   time.Now().Unix(),
        BytesBuffered: rec.Body.Len(),
        IsStream:      strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream"),
    }
    return pendingStore.Save(ctx, resp)
})
```

**注意事项：**
- 需要考虑流式响应的处理
- 需要处理 provider 选择（sticky routing）
- 需要处理认证/授权（tenant_id 验证）

### 4. 集成测试 (中优先级)

#### 4.1 端到端测试

创建 `tests/integration/approval_flow_test.go`：

```go
func TestApprovalFlow_E2E(t *testing.T) {
    // 1. 发起包含敏感内容的请求
    // 2. 验证返回 ErrApprovalRequired
    // 3. 验证 approval_queue 中有记录
    // 4. 验证 SessionState 中有 approval_id
    // 5. Admin 批准请求
    // 6. 调用 /api/admin/approvals/:id/resume
    // 7. 验证 LLM 调用完成
    // 8. 验证响应写入 pending.Store
    // 9. Client 调用 GET /v1/sessions/:id/pending-response
    // 10. 验证得到完整响应
}
```

#### 4.2 性能测试

- 审批流程的延迟影响
- pending.Store 的读写性能
- SessionCache 的命中率

### 5. 监控和可观测性 (低优先级)

#### 5.1 Metrics

- `approval_requests_total{decision}` - 审批请求数（pass/warn/need_approval）
- `approval_pending_duration_seconds` - 审批等待时长
- `approval_resume_total{status}` - 恢复请求数（success/failed）
- `cache_update_total{status}` - 缓存更新数

#### 5.2 Logging

- ApprovalHook: 记录 approval_id, session_id, tenant_id, decision, score
- CacheUpdateHook: 记录缓存更新成功/失败
- ApprovalResumeHandler: 记录恢复请求的详细信息

#### 5.3 Tracing

- 在 OpenTelemetry span 中添加 approval 相关的 attributes

### 6. 文档更新 (低优先级)

#### 6.1 架构文档

- 更新 `docs/architecture/approval-flow.md`
- 添加 SessionCache v6 字段说明
- 添加审批流程时序图

#### 6.2 运维文档

- 审批超时配置（`LLM_GATEWAY_APPROVAL_TIMEOUT`）
- Redis 键空间说明
- 故障排查指南

#### 6.3 API 文档

- POST /api/admin/approvals/:id/resume
- GET /v1/sessions/:id/pending-response
- 错误码说明

## 验收标准

- [ ] main.go 集成完成，服务可以启动
- [ ] 审批流程端到端测试通过
- [ ] v1 和 v2 pipeline 都支持审批
- [ ] Admin API 可以手动触发 resume
- [ ] Client 可以通过 pending-response 轮询得到结果
- [ ] 测试覆盖率 ≥ 80%
- [ ] 文档更新完成
- [ ] 代码 review 通过
- [ ] 部署到 test 环境验证

## 参考资料

- **SessionCache v6 设计**: `domains/hooks/compression/session_cache.go` L98-L124
- **ApprovalHook**: `domains/hooks/sessionaudit/approval_hook.go`
- **CacheUpdateHook**: `domains/hooks/sessionaudit/cache_update_hook.go`
- **ApprovalResumeHandler**: `domains/session/approval_resume.go`
- **Adapters**: `domains/session/adapters.go`
- **Pending Store**: `pending/pending.go`

## 注意事项

1. **向后兼容**：SessionCache v6 字段全部 omitempty，SchemaVersion 保持 1
2. **事务一致性**：approval_queue 和 SessionState 的状态需要保持一致
3. **错误处理**：ErrApprovalRequired 是 sentinel，需要在上层正确处理
4. **超时处理**：approval timeout 后需要自动 reject 并通知 client
5. **租户隔离**：所有操作都需要验证 tenant_id

## 联系人

- **负责人**: [你的名字]
- **审查人**: [Tech Lead]
- **截止日期**: [TBD]
