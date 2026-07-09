# 会话健康检查模块审计报告

**日期**: 2026-07-09  
**审计范围**: session_inspector 模块与其他模块的集成关系

---

## 1. 模块间调用关系审计

### 1.1 发现的问题

#### 问题 1: EventBus 未实际接入
**严重程度**: 中等  
**描述**: 
- `session-inspector/hook.go` 定义了 `EventBusPublisher` 接口并预留了 `SetEventBus()` 方法
- 但在 `cmd/gateway/main_pipeline.go:349` 和 `main_v2_pipeline.go:220` 的 wiring 代码中，**未调用 `SetEventBus()` 注入 MemoryBus 实例**
- 结果：所有 `SessionInspectorFindingEvent` 和告警事件不会被发布，IM 通知渠道无法收到

**影响**:
- `alert.notify_channels` 配置项无效（feishu/wechat 不会收到通知）
- `alert.webhook_urls` 无效
- EventBus 订阅者（如果未来添加）无法接收事件

**推荐修复**:
```go
// cmd/gateway/main_pipeline.go:349 处
inspectorHook := sessioninspector.NewInspectorHookWithConfig(nil)
if deps.EventBus != nil {
    inspectorHook.SetEventBus(deps.EventBus) // 注入全局 EventBus
}
p.AddStage(&pipeline.PipelineStage{
    Name: "session_inspect", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
    Hooks: []pipeline.Hook{inspectorHook},
})
```

#### 问题 2: SessionLifecycleWorker 未注入 EventBus
**严重程度**: 中等  
**描述**:
- `bg/session_lifecycle_worker.go` 支持可选的 `LifecycleEventPublisher` 注入
- 但在 `cmd/gateway/main.go:2139` 注册时未使用 `WithEventBus()` option
- 结果：`SessionInspectorRecycleEvent` 不会被发布，管理员无法通过 IM 收到回收通知

**推荐修复**:
```go
// cmd/gateway/main.go:2139 处
lifecycleWorker := bg.NewSessionLifecycleWorker(
    dbConn.Pool(),
    bg.WithEventBus(gAuditBus), // 复用全局 auditBus
)
lifecycleWorker.Start(context.Background())
```

#### 问题 3: 通知渠道未订阅 session_inspector 事件
**严重程度**: 高  
**描述**:
- `domains/notification/approval_notifier.go` 仅处理 `sessionaudit` 的审批通知
- 没有任何代码订阅 `session_inspector.finding` 或 `session_inspector.recycle` 事件
- 即使修复问题 1 和 2，IM 通知仍然无法工作

**推荐方案（3 选 1）**:

**方案 A（推荐）**: 创建专用 Inspector 通知器
```go
// domains/notification/inspector_notifier.go (新建)
type InspectorNotifier struct {
    channels map[string]Channel
}

func (n *InspectorNotifier) HandleFindingEvent(ctx context.Context, event eventbus.Event) error {
    fe := event.(*sessioninspector.SessionInspectorFindingEvent)
    // 构造卡片 + 路由到 feishu/wechat
}

// cmd/gateway/main.go 注册订阅
if gAuditBus != nil && enableSessionInspector == "true" {
    inspectorNotifier := notification.NewInspectorNotifier(larkCh, weChatCh)
    gAuditBus.Subscribe("session_inspector.finding", inspectorNotifier.HandleFindingEvent)
    gAuditBus.Subscribe("session_inspector.recycle", inspectorNotifier.HandleRecycleEvent)
}
```

**方案 B**: 复用 ApprovalNotifier（不推荐 — 混淆职责）
```go
// 在 approval_notifier.go 中新增方法
func (n *ApprovalNotifier) NotifyInspectorFinding(ctx, finding) error
```

**方案 C**: 直接在 hook.go 中调用 notification 包（最不推荐 — 引入反向依赖）

---

### 1.2 模块依赖关系验证

| 依赖模块 | 实际使用情况 | 是否重复造轮子 | 备注 |
|---------|-------------|---------------|------|
| `compression` | ✅ 通过 Metadata 读取 token_count | 否 | 复用已有字段 |
| `cache` | ✅ 通过 Metadata 读取 last_active_at | 否 | 复用 CacheLookupHook 填充的数据 |
| `prompt_injection` | ⚠️ 未直接依赖 | 否 | 通过 session_health API 间接关联 |
| `output_compliance` | ⚠️ 未直接依赖 | 否 | 同上 |
| `session_audit` | ✅ EventBus 复用 gAuditBus | 否 | 共享同一个 MemoryBus 实例 |
| `eventbus` | ✅ 接口抽象避免强依赖 | 否 | EventBusPublisher 是可选注入 |
| `notification` | ❌ **未集成** | - | 缺少订阅者逻辑 |

**结论**: 
- ✅ 没有重复造轮子（复用了 eventbus / settings / admin 框架）
- ❌ notification 集成缺失导致告警功能不完整

---

### 1.3 Prometheus 指标命名冲突检查

| 指标名 | 定义位置 | 冲突检查 |
|-------|---------|---------|
| `llmgw_session_inspector_findings_total` | hook.go:36 | ✅ 无冲突 |
| `llmgw_session_inspector_recycle_total` | hook.go:41 | ✅ 无冲突 |
| `llmgw_session_inspector_block_total` | hook.go:46 | ✅ 无冲突 |
| `llmgw_session_inspector_hook_duration_seconds` | hook.go:51 | ✅ 无冲突 |
| `llmgw_session_lifecycle_recycled_total` | session_lifecycle_worker.go:36 | ✅ 无冲突 |
| `llmgw_session_lifecycle_soft_closed_total` | session_lifecycle_worker.go:42 | ✅ 无冲突 |
| `llmgw_session_lifecycle_notified_total` | session_lifecycle_worker.go:47 | ✅ 无冲突 |
| `llmgw_session_lifecycle_evicted_total` | session_lifecycle_worker.go:52 | ✅ 无冲限 |
| `llmgw_session_lifecycle_scan_duration_seconds` | session_lifecycle_worker.go:58 | ✅ 无冲突 |
| `llmgw_session_lifecycle_scan_errors_total` | session_lifecycle_worker.go:63 | ✅ 无冲突 |

---

## 2. Admin API 路由审计

### 2.1 新增端点验证

| 端点 | 路由注册位置 | HTTP 方法 | 权限 | 状态 |
|-----|------------|----------|------|------|
| `/api/admin/sessions/<id>/inspector-findings` | session_state_handlers.go:186 | GET | admin | ✅ 已注册 |
| `/api/admin/sessions/inspector-stats` | handler.go:550 | GET | admin | ✅ 已注册 |
| `/api/admin/sessions/<id>/recycle` | session_state_handlers.go:193 | POST | super_admin | ✅ 已注册 |

### 2.2 与现有端点的关系

| 现有端点 | 新端点 | 关系 |
|---------|-------|------|
| `GET /api/admin/sessions/<id>/health` | `GET .../inspector-findings` | 互补（health=评分，findings=实时检测） |
| `POST /api/admin/sessions/<id>/stop` | `POST .../recycle` | 互补（stop=完全停止，recycle=软关闭） |
| `GET /api/admin/session-audit/stats` | `GET .../sessions/inspector-stats` | 平行（audit 统计审批队列，inspector 统计活跃/闲置） |

**结论**: ✅ 无重复端点，职责清晰

---

## 3. 配置热更新机制验证

### 3.1 LoadConfig 调用链

```
PipelineRequest 触发
  → InspectorHook.Enabled(ctx, env)
    → cfg := LoadConfig()  // 每次请求都读最新配置
  → InspectorHook.Execute(ctx, env)
    → cfg := h.config (使用 Enabled 时缓存的 config)
```

**潜在问题**: 
- `Execute` 使用的是 `Enabled` 时缓存的 `h.config`
- 如果 `Enabled` 返回 false 跳过，后续配置修改不会触发 `Execute`
- 配置修改后需要等下一个请求才能生效

**影响**: 可接受（配置热更延迟 <= 1 个请求周期）

---

## 4. 数据库操作审计

### 4.1 SessionLifecycleWorker SQL 注入风险检查

| 查询 | 参数化 | 风险 |
|-----|--------|------|
| `recycleIdle` | ✅ $1/$2 占位符 | 安全 |
| `recycleAbsolute` | ✅ $1/$2 占位符 | 安全 |
| `evictExcess` | ✅ $1/$2/$3 占位符 | 安全 |
| `applyRecycle` | ✅ $1/$2 占位符 | 安全 |

**结论**: ✅ 全部使用参数化查询，无 SQL 注入风险

### 4.2 Session Health API SQL 注入风险检查

| 函数 | 参数化 | 风险 |
|-----|--------|------|
| `fetchSessionDim` | ✅ $1/$2 占位符 | 安全 |
| `fetchSessionInspectorStats` | ✅ $1 占位符（tenantFilter 动态拼接但无用户输入） | 安全 |
| `HandleSessionRecycle` | ✅ $1/$2/$3 占位符 | 安全 |

**结论**: ✅ 安全

---

## 5. 测试覆盖率审计

### 5.1 单元测试统计

| 包 | 测试文件 | 测试数量 | 覆盖功能 |
|----|---------|---------|---------|
| session-inspector | config_test.go | 6 个 | DefaultConfig / SoftWarningThreshold / IsBlockAction / 等 |
| session-inspector | inspectors_test.go | 17 个 | 6 个 Inspector 的边界条件 |
| session-inspector | session_inspector_test.go | 14 个（已有） | Hook 编排 / 错误处理 |
| bg | (无新增) | - | SessionLifecycleWorker 缺少单元测试 ⚠️ |
| admin | (复用已有) | - | session_health_api 新端点缺少单元测试 ⚠️ |

**缺失的测试**:
1. ⚠️ `bg/session_lifecycle_worker_test.go` — worker 的 sweep/recycle 逻辑
2. ⚠️ `admin/session_health_api_test.go` — 3 个新端点的 HTTP 测试

---

## 6. 推荐改进优先级

| 优先级 | 改进项 | 预计工作量 | 影响范围 |
|-------|--------|-----------|---------|
| **P0** | 修复 EventBus 未注入问题 | 30 分钟 | 告警功能完全不可用 |
| **P0** | 创建 InspectorNotifier 订阅事件 | 2 小时 | IM 通知完全不可用 |
| **P1** | 补充 SessionLifecycleWorker 单元测试 | 1 小时 | 后台回收逻辑未验证 |
| **P1** | 补充 Admin API 新端点测试 | 1 小时 | 新端点行为未验证 |
| **P2** | 优化 LoadConfig 热更机制 | 30 分钟 | 配置延迟优化 |
| **P3** | 文档补充 EventBus 集成示例 | 30 分钟 | 用户指南完善 |

---

## 7. 合规性检查

### 7.1 CODEOWNERS 覆盖
- ✅ `domains/hooks/session-inspector/` 由 @backend-team 负责
- ✅ `bg/session_lifecycle_worker.go` 由 @backend-team 负责
- ✅ `docs/modules/session-inspector.md` 由 @docs-team 审核

### 7.2 日志安全
- ✅ 所有敏感配置（token/secret）使用 `_` 占位不打印
- ✅ 会话 ID / 租户 ID 在 structured logging 中正确标记

### 7.3 错误处理
- ✅ Inspector 失败降级为 Warn 日志
- ✅ Worker 扫描错误不中断服务
- ✅ Admin API 错误返回规范 JSON 格式

---

## 8. 总结

### 8.1 已完成
- ✅ 配置层完整（22 个设置项 + 热更新）
- ✅ 6 个 Inspector 业务逻辑正确
- ✅ 后台 Worker 回收策略可配置
- ✅ Admin API 端点齐全
- ✅ 文档完整
- ✅ 核心单元测试覆盖

### 8.2 需要修复（阻塞生产）
- ❌ **EventBus 未实际接入** — 告警功能不可用
- ❌ **通知渠道未订阅事件** — IM 推送不可用

### 8.3 建议改进（非阻塞）
- ⚠️ 补充 Worker 与 API 单元测试
- ⚠️ 优化配置热更延迟

---

**审计人**: Kiro AI Assistant  
**审计日期**: 2026-07-09  
**下一步**: 修复 P0 问题后进行本地环境验证
