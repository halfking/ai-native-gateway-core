# Hook 集成重构规划文档

> **版本**: v1.0  
> **日期**: 2026-06-29  
> **状态**: 方案 A 完成，方案 B 规划中

---

## 执行摘要

本文档规划 3 个核心域 Hook（Authentication, Identity, Session）的完整 Pipeline 集成方案。

**当前状态**:
- ✅ **Authentication Hook**: 已完成 Pipeline 集成（方案 A）
- ⚠️ **Identity Hook**: Hook 已实现，但逻辑需从 ChatHandler 提取
- ⚠️ **Session Hook**: Hook 已实现，但逻辑需从 ChatHandler 提取

**完成度**: 1/3 (33%)

---

## 一、Authentication Hook（已完成）✅

### 1.1 实现状态

**文件**: `domains/authentication/hook.go` (76 行)

**实现细节**:
- ✅ APIKeyAuthHook 完整实现
- ✅ 从 metadata["api_key"] 提取 key
- ✅ 调用 Verifier.Verify 验证
- ✅ 注入 env.APIKey 和 env.Authenticated

### 1.2 Pipeline 集成

**文件**: `cmd/gateway/main_pipeline.go`

**集成细节**:
```go
// Phase: Authentication (priority 10)
if deps.Config.EnableAuth {
    keyVerifier := authentication.NewKeyVerifier()
    p.AddStage(&pipeline.PipelineStage{
        Name: "authentication", 
        Phase: pipeline.PhaseAuthentication,
        Mode: pipeline.ModeSequential,
        Hooks: []pipeline.Hook{authentication.NewAPIKeyAuthHook(keyVerifier)},
    })
}
```

**API Key 提取**:
```go
// 从 Authorization: Bearer <token> header
if auth := r.Header.Get("Authorization"); auth != "" {
    if len(auth) > 7 && auth[:7] == "Bearer " {
        env.Metadata["api_key"] = auth[7:]
    }
}
// 从 X-API-Key header (fallback)
if apiKey := r.Header.Get("X-API-Key"); apiKey != "" {
    env.Metadata["api_key"] = apiKey
}
```

### 1.3 配置

**环境变量**:
- `LLM_GATEWAY_USE_V2_PIPELINE=true` - 启用 v2 Pipeline
- `LLM_GATEWAY_V2_AUTH=true` - 启用 Authentication Hook（默认 false）

**限制**:
- KeyVerifier 需要 DB 连接才能验证 key
- 当前为占位实现，生产环境需传入 DB pool from main.go

---

## 二、Identity Hook（需提取）⚠️

### 2.1 当前状态

**文件**: `domains/identity/hook.go` (79 行)

**Hook 状态**: ✅ 已实现（占位版本）

**问题**: 实际逻辑仍在 `domains/streaming/handler.go:1151`:
```go
clientID := identity.BuildIdentityFromRequest(r, tenant(keyInfo), appID(keyInfo), 
                                               apiKeyIDPtr(keyInfo), clientProfileFromKey(keyInfo))
identityHash := clientID.ShortID()
```

### 2.2 提取计划

#### Step 1: 增强 ClientIdentityHook 实现

**需要的依赖**:
```go
type ClientIdentityHook struct {
    // No additional dependencies needed - BuildIdentityFromRequest is a pure function
}
```

**实现逻辑**:
```go
func (h *ClientIdentityHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
    // 1. Extract tenant, appID, apiKeyID from env.APIKey
    var tenantID string
    var appID, apiKeyID *int
    if env.APIKey != nil {
        tenantID = env.APIKey.TenantID
        if id, err := strconv.Atoi(env.APIKey.ID); err == nil {
            apiKeyID = &id
        }
    }
    
    // 2. Extract client profile from HTTP headers
    clientProfile := extractClientProfile(env.HTTPRequest)
    
    // 3. Call BuildIdentityFromRequest
    clientID := BuildIdentityFromRequest(
        env.HTTPRequest, 
        tenantID, 
        appID, 
        apiKeyID, 
        clientProfile,
    )
    
    // 4. Store in metadata (env.Identity doesn't exist yet)
    env.Metadata["identity_hash"] = clientID.IdentityHash
    env.Metadata["client_identity"] = clientID
    
    return nil
}
```

#### Step 2: 从 ChatHandler 移除身份提取

**文件**: `domains/streaming/handler.go`

**需修改位置**:
- Line 1151: `clientID := identity.BuildIdentityFromRequest(...)`
- 改为: 从 `env.Metadata["client_identity"]` 读取（如果 Pipeline 已执行）
- 保留 fallback: 如果 metadata 中没有，仍然调用 BuildIdentityFromRequest

#### Step 3: 启用 Pipeline 集成

**文件**: `cmd/gateway/main_pipeline.go`

```go
// Phase: Client Identity (priority 20)
p.AddStage(&pipeline.PipelineStage{
    Name: "client_identity", 
    Phase: pipeline.PhasePreRouting, 
    Mode: pipeline.ModeSequential,
    Hooks: []pipeline.Hook{identity.NewClientIdentityHook()},
})
```

### 2.3 复杂度评估

| 维度 | 评估 |
|------|------|
| **代码量** | 中（修改 50-100 行） |
| **风险** | 低（identity 逻辑独立，无副作用） |
| **测试难度** | 低（纯函数，易测试） |
| **估计工时** | 1-2 小时 |

---

## 三、Session Hook（需重构）🔴

### 3.1 当前状态

**文件**: `domains/session/hook.go` (65 行)

**Hook 状态**: ✅ 已实现（部分功能）

**当前功能**:
- 从 store 加载会话
- 注入凭据偏好（粘性路由）

**问题**: Session 逻辑高度分散在 ChatHandler 中，包括：

1. **Session loading**: `sessionGetter.Get(ctx, sessionID)`
2. **Session creation**: `sessionGetter.CreateV2(ctx, apiKeyID, tenantID, deviceSeed, taskID)`
3. **Session reuse**: `FindRecentGatewaySession` (5-minute window)
4. **Session compression**: `sessionCompressor`
5. **Session audit**: `sessionAuditHook`
6. **Session routing**: `lastSystemSession`, `sessionPref`

### 3.2 提取计划（分阶段）

#### Phase 1: Session 加载和创建（2 小时）

**目标**: 将基础 session 加载/创建逻辑移到 Hook

**步骤**:
1. 增强 `SessionLoaderHook.Execute`:
   - 实现 session 加载逻辑
   - 实现 session 创建逻辑（when sessionID 为空）
   - 实现 5-minute reuse 逻辑

2. 从 ChatHandler 移除重复逻辑:
   - 保留 fallback（如果 Pipeline 未执行）
   - 逐步过渡到完全依赖 Hook

**代码位置**:
- `domains/streaming/handler.go`: 多处 session 相关调用
- 需要搜索: `sessionGetter`, `CreateV2`, `FindRecentGatewaySession`

#### Phase 2: Session 压缩集成（1 小时）

**目标**: 将 sessionCompressor 移到 Hook 或 hooks/compression

**当前位置**: `ChatHandler.sessionCompressor`

**重构方案**:
- 创建 `hooks/compression/session_compression_hook.go`
- 在 PostStreaming 阶段执行压缩
- 从 ChatHandler 移除 sessionCompressor 字段

#### Phase 3: Session 审计集成（已完成）

**当前位置**: `ChatHandler.sessionAuditHook`

**状态**: 已在 Pipeline 中注册（`hooks/sessionaudit`）

#### Phase 4: Session 路由集成（1 小时）

**目标**: 整合 lastSystemSession 和 sessionPref

**当前位置**: 
- `ChatHandler.lastSystemSession` (LastSystemSessionIndex)
- `ChatHandler.sessionPref` (SessionPreference)

**重构方案**:
- SessionLoaderHook 已支持 StickyRouter（凭据偏好）
- 需要增加 lastSystemSession 支持
- 考虑创建独立的 `SessionRoutingHook`

### 3.3 复杂度评估

| Phase | 代码量 | 风险 | 测试难度 | 估计工时 |
|-------|--------|------|---------|---------|
| Phase 1: 加载/创建 | 高（200+ 行） | 中 | 中 | 2 小时 |
| Phase 2: 压缩集成 | 中（100 行） | 低 | 低 | 1 小时 |
| Phase 3: 审计集成 | - | - | - | 已完成 |
| Phase 4: 路由集成 | 中（100 行） | 中 | 中 | 1 小时 |
| **总计** | | | | **4 小时** |

---

## 四、依赖关系分析

### 4.1 Hook 执行顺序

```
Authentication (priority 10)
    ↓
Identity (priority 20)
    ↓ (requires env.APIKey)
Session (priority 30)
    ↓ (requires identity for session creation)
Other Hooks (40+)
```

### 4.2 数据流

```
HTTP Request
    ↓
v2DispatchHandler: 提取 API Key → env.Metadata["api_key"]
    ↓
AuthenticationHook: 验证 key → env.APIKey, env.Authenticated
    ↓
IdentityHook: 构建身份 → env.Metadata["client_identity"]
    ↓
SessionHook: 加载/创建会话 → env.SessionID
    ↓
ChatHandler: 使用 env.APIKey, env.SessionID 处理请求
```

### 4.3 ChatHandler 依赖

**当前 ChatHandler 的 session 相关字段**:
```go
type ChatHandler struct {
    sessionGetter     interface { Get(), CreateV2(), BindAPIKey() }
    sessionCompressor *compression.SessionCompressor
    sessionAuditHook  *sessionaudithook.SessionAuditHook
    lastSystemSession *session.LastSystemSessionIndex
    sessionPref       *session.SessionPreference
    sessionReuseWindow time.Duration
}
```

**重构目标**: 将这些字段移到 Pipeline Hooks

---

## 五、测试策略

### 5.1 Authentication Hook 测试

**测试文件**: `domains/authentication/hook_test.go`

**测试用例**:
- ✅ API Key 验证成功
- ✅ API Key 验证失败
- ✅ API Key 缺失
- ✅ KeyVerifier 未启用

### 5.2 Identity Hook 测试

**测试文件**: `domains/identity/hook_test.go` (待创建)

**测试用例**:
- [ ] 从 HTTP headers 提取身份信息
- [ ] BuildIdentityFromRequest 正确调用
- [ ] Identity hash 正确计算
- [ ] Tenant 隔离正确

### 5.3 Session Hook 测试

**测试文件**: `domains/session/hook_test.go` (已存在)

**测试用例**:
- [ ] Session 加载成功
- [ ] Session 不存在时创建
- [ ] 5-minute reuse 逻辑
- [ ] 粘性路由偏好

### 5.4 E2E 测试

**测试文件**: `cmd/gateway/pipeline_integration_test.go` (待创建)

**测试场景**:
- [ ] 完整 Pipeline 流程（20+ Stages）
- [ ] Authentication → Identity → Session 链路
- [ ] Pipeline error 不阻塞 ChatHandler
- [ ] Feature flags 正确控制 Hook 启用/禁用

---

## 六、执行优先级

### 高优先级（立即执行）

1. **✅ Authentication Hook 集成** - 已完成
2. **Identity Hook 提取** - 1-2 小时
   - 风险低，收益高
   - 验证 Pipeline 机制可行性

### 中优先级（下个 Sprint）

3. **Session Hook Phase 1** - 2 小时
   - Session 加载/创建逻辑
   - 为后续 Phase 铺路

4. **E2E 测试** - 2 小时
   - 验证完整 Pipeline 流程
   - 确保 fallback 机制正常

### 低优先级（后续优化）

5. **Session Hook Phase 2-4** - 2 小时
   - 压缩、审计、路由集成
   - 完全去除 ChatHandler 依赖

6. **性能优化** - 1 小时
   - Pipeline overhead 测试
   - 缓存优化

---

## 七、风险评估

### 7.1 技术风险

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|---------|
| Pipeline overhead 影响延迟 | 低 | 中 | 性能基准测试，优化 Hook 执行 |
| ChatHandler 依赖复杂，难以提取 | 中 | 高 | 分阶段提取，保留 fallback |
| Session 逻辑分散，遗漏边界情况 | 中 | 中 | 完整单元测试+集成测试 |
| Backward compatibility 破坏 | 低 | 高 | 保留 fallback，逐步迁移 |

### 7.2 运维风险

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|---------|
| Feature flag 配置错误 | 低 | 中 | 清晰的文档和默认值 |
| 监控缺失，难以调试 | 中 | 中 | 增加 Pipeline 执行日志 |
| 回滚困难 | 低 | 高 | Feature flag 允许快速关闭 |

---

## 八、下一步行动

### 立即执行（1-2 小时）

1. **实现 Identity Hook 提取**
   - 增强 ClientIdentityHook.Execute
   - 从 ChatHandler 提取逻辑
   - 编写单元测试

2. **启用 Identity Hook 在 Pipeline**
   - 在 buildV2DispatchPipeline 中注册
   - 测试完整链路

### 短期计划（1 周）

3. **实现 Session Hook Phase 1**
   - Session 加载/创建逻辑
   - 5-minute reuse

4. **编写 E2E 测试**
   - 验证完整 Pipeline
   - 性能基准测试

### 中期规划（2-4 周）

5. **完成 Session Hook Phase 2-4**
   - 压缩、审计、路由集成

6. **ChatHandler 重构**
   - 移除 session 相关字段
   - 完全依赖 Pipeline

7. **文档和培训**
   - 操作手册
   - 团队培训

---

## 九、成功标准

### 功能完整性

- [ ] 3 个核心域 Hook 全部集成到 Pipeline
- [ ] ChatHandler 不再直接管理 authentication/identity/session
- [ ] Feature flags 正确控制所有 Hook

### 性能指标

- [ ] Pipeline overhead < 5ms (P99)
- [ ] 端到端延迟不增加 > 10ms
- [ ] 内存使用不增加 > 5%

### 质量指标

- [ ] 单元测试覆盖率 > 85%
- [ ] E2E 测试覆盖所有关键路径
- [ ] 无回归 bug

### 运维指标

- [ ] Feature flags 可动态切换
- [ ] 监控和日志完整
- [ ] 回滚时间 < 5 分钟

---

**文档结束**
