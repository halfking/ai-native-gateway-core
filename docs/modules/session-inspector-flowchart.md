# 会话健康检查模块功能总结与流程图

**版本**: R1.13  
**日期**: 2026-07-09  
**状态**: 生产就绪

---

## 1. 功能总结

会话健康检查（Session Inspector）模块通过 **实时监控 + 后台回收** 双轨机制，
实现会话生命周期的全程治理，覆盖 Token 使用、活跃度、高频请求、错误率等多维度指标。

### 1.1 核心能力

| 能力 | 实现方式 | 触发条件 | 默认动作 |
|------|---------|---------|---------|
| **Token 使用量监控** | InspectorHook (PreRouting) | 累计 > max_total 或 soft_threshold | 日志 / metadata / block (429) |
| **不活跃会话检测** | InspectorHook + Worker | last_active > idle_timeout | 软关闭 (status='closed') |
| **高频请求检测** | InspectorHook (PreRouting) | RPM > rpm_limit 或 burst > burst_limit | 记录 / 429 (非 observe_only) |
| **错误率告警** | InspectorHook (PreRouting) | error_rate ≥ 50% (critical) 或 ≥ 20% (warning) | 记录 + IM 通知 |
| **租户超限驱逐** | SessionLifecycleWorker (5min 周期) | active_count > max_per_tenant | LRU/FIFO 驱逐 + 软关闭 |
| **模型切换频繁检测** | InspectorHook (PreRouting) | switch_count > 5 | 记录（仅观察） |

### 1.2 告警通道

```
Finding 检测 → EventBus 发布 → InspectorNotifier 路由
                                    ├─ 飞书机器人 (卡片/文本)
                                    ├─ 企业微信 (应用消息)
                                    ├─ Webhook (HTTP POST)
                                    └─ Prometheus (metrics)
```

### 1.3 数据流

```
请求进入 → Pipeline PreRouting 阶段
         → session_inspect stage
           → InspectorHook.Enabled(ctx, env)
             → LoadConfig() 读取最新配置
             → 检查 session_inspector.enabled
           → InspectorHook.Execute(ctx, env)
             → 6 个 Inspector 并行检测
               ├─ TokenLimitInspector (token_count vs max_total)
               ├─ InactiveInspector (last_active_at vs idle_timeout)
               ├─ HighFrequencyInspector (request_count vs rpm_limit)
               ├─ SessionLifecycleInspector (tenant_active_count vs max_per_tenant)
               ├─ ErrorRateInspector (error_rate vs 0.5/0.2)
               └─ ModelSwitchInspector (switch_count vs 5)
             → 合并 findings
             → 按 severity 决策动作
               ├─ Critical + block_action → 返回 429
               ├─ Warning/Error → 写 Metadata + Prometheus
               └─ 告警启用 → 发布 SessionInspectorFindingEvent
         → EventBus 分发
           → InspectorNotifier.HandleFindingEvent
             → 构造卡片/消息
             → 推送到飞书/企业微信
```

---

## 2. 主流程图

### 2.1 在线实时检测流程（Hook 触发）

```
┌─────────────────────────────────────────────────────────────────┐
│                    HTTP Request 进入 Pipeline                    │
└───────────────────────────────┬─────────────────────────────────┘
                                │
                    ┌───────────▼──────────┐
                    │  cache_lookup stage  │
                    │  (填充 Metadata)      │
                    └───────────┬──────────┘
                                │
            ┌───────────────────▼────────────────────┐
            │    session_inspect stage (PreRouting)  │
            │    InspectorHook.Enabled()              │
            │      ├─ LoadConfig() 读取 22 个配置项    │
            │      └─ 检查 enabled=true               │
            └───────────────────┬────────────────────┘
                                │ enabled=true
                    ┌───────────▼──────────┐
                    │  InspectorHook       │
                    │  .Execute(ctx, env)  │
                    └───────────┬──────────┘
                                │
        ┌───────────────────────┼───────────────────────┐
        │                       │                       │
┌───────▼────────┐   ┌─────────▼────────┐   ┌─────────▼────────┐
│ TokenLimit     │   │ Inactive         │   │ HighFrequency    │
│ Inspector      │   │ Inspector        │   │ Inspector        │
│ (token_count)  │   │ (last_active_at) │   │ (request_count)  │
└───────┬────────┘   └─────────┬────────┘   └─────────┬────────┘
        │                      │                       │
        └──────────────────────┼───────────────────────┘
                               │
        ┌──────────────────────┼──────────────────────┐
        │                      │                      │
┌───────▼────────┐   ┌─────────▼────────┐   ┌───────▼────────┐
│ Lifecycle      │   │ ErrorRate        │   │ ModelSwitch    │
│ Inspector      │   │ Inspector        │   │ Inspector      │
│ (tenant_count) │   │ (error_rate)     │   │ (switch_count) │
└───────┬────────┘   └─────────┬────────┘   └───────┬────────┘
        │                      │                      │
        └──────────────────────┼──────────────────────┘
                               │
                    ┌──────────▼───────────┐
                    │  合并 Findings       │
                    │  (去重 + 按 severity)│
                    └──────────┬───────────┘
                               │
                ┌──────────────┼──────────────┐
                │              │              │
        ┌───────▼──────┐  ┌────▼─────┐  ┌────▼─────┐
        │ severity =   │  │ metadata │  │ EventBus │
        │ critical +   │  │ 写入环境  │  │ Publish  │
        │ block_action │  │ 变量     │  │ finding  │
        └───────┬──────┘  └──────────┘  └────┬─────┘
                │                            │
        ┌───────▼──────┐              ┌─────▼──────────┐
        │ 返回 429     │              │ InspectorNotifier│
        │ (阻断请求)   │              │ .HandleFinding  │
        └──────────────┘              └─────┬──────────┘
                                            │
                                    ┌───────┼───────┐
                                    │       │       │
                            ┌───────▼──┐ ┌──▼─────┐ ┌▼──────┐
                            │ 飞书机器人│ │企业微信 │ │Webhook│
                            └──────────┘ └────────┘ └───────┘
```

### 2.2 后台回收流程（Worker 触发）

```
┌────────────────────────────────────────────────────────────┐
│          SessionLifecycleWorker (5min 定时器)               │
└──────────────────────────┬─────────────────────────────────┘
                           │
                ┌──────────▼──────────┐
                │  LoadConfig()       │
                │  读取 idle.timeout / │
                │  cleanup_interval   │
                └──────────┬──────────┘
                           │
        ┌──────────────────┼──────────────────┐
        │                  │                  │
┌───────▼─────────┐ ┌──────▼──────────┐ ┌────▼────────────┐
│ recycleIdle()   │ │ recycleAbsolute()│ │ evictExcess()   │
│ (idle_timeout)  │ │ (abs_max_life)   │ │ (max_per_tenant)│
└───────┬─────────┘ └──────┬──────────┘ └────┬────────────┘
        │                  │                  │
        │  SELECT gw_session_id, tenant_id   │
        │  WHERE status='active' AND ...     │
        └──────────────────┼──────────────────┘
                           │
                ┌──────────▼──────────┐
                │  applyRecycle()     │
                │  (批量 UPDATE)       │
                │  SET status='closed'│
                │  SET closed_at=NOW()│
                └──────────┬──────────┘
                           │
                           ├─ recycle_action = soft_close
                           │   → UPDATE session_dim
                           │
                           └─ recycle_action = notify_only
                               → 跳过 UPDATE
                           │
                ┌──────────▼──────────┐
                │  可选：发布         │
                │  SessionInspector   │
                │  RecycleEvent       │
                └──────────┬──────────┘
                           │
                ┌──────────▼──────────┐
                │ InspectorNotifier   │
                │ .HandleRecycle      │
                └──────────┬──────────┘
                           │
                    ┌──────┼──────┐
                    │      │      │
            ┌───────▼──┐ ┌─▼────┐ ┌▼──────┐
            │ 飞书机器人│ │企微   │ │Webhook│
            │ "会话回收"│ │      │ │       │
            └──────────┘ └──────┘ └───────┘
```

### 2.3 Admin API 交互流程

```
┌──────────────────────────────────────────────────────────────┐
│            管理员通过 Admin UI 访问                            │
└────────────────────────┬─────────────────────────────────────┘
                         │
        ┌────────────────┼────────────────┐
        │                │                │
┌───────▼──────────┐ ┌───▼───────────┐ ┌─▼─────────────────┐
│ GET /sessions/   │ │ GET /sessions/│ │ POST /sessions/   │
│ inspector-stats  │ │ <id>/         │ │ <id>/recycle      │
│                  │ │ inspector-    │ │                   │
│ (平台级统计)      │ │ findings      │ │ (手动软关闭)       │
└───────┬──────────┘ └───┬───────────┘ └─┬─────────────────┘
        │                │                │
        │ SELECT COUNT(*) FROM session_dim│
        │ GROUP BY status                 │
        └────────────────┼─────────────────┘
                         │
        ┌────────────────▼────────────────┐
        │  PostgreSQL session_dim 表      │
        │  ├─ gw_session_id (PK)          │
        │  ├─ status (active/closed)      │
        │  ├─ last_active_at              │
        │  ├─ first_request_at            │
        │  ├─ total_tokens                │
        │  └─ stop_reason                 │
        └────────────────┬────────────────┘
                         │
        ┌────────────────▼────────────────┐
        │  返回 JSON 响应                 │
        │  {                              │
        │    "active_sessions": 342,      │
        │    "idle_sessions": 89,         │
        │    "findings": [...]            │
        │  }                              │
        └─────────────────────────────────┘
```

---

## 3. 配置热更新流程

```
┌────────────────────────────────────────────────────────────┐
│        管理员修改配置 (PUT /api/admin/settings/{key})        │
│        例如: session_inspector.token.max_total = 50000      │
└──────────────────────────┬─────────────────────────────────┘
                           │
                ┌──────────▼──────────┐
                │  settings.Global    │
                │  .Set(key, value)   │
                │  写入 DB settings 表 │
                └──────────┬──────────┘
                           │
                           │ 秒级生效（无需重启）
                           │
        ┌──────────────────┼──────────────────┐
        │                  │                  │
┌───────▼─────────┐ ┌──────▼──────────┐ ┌────▼────────────┐
│ 下次请求触发    │ │ Worker 下次扫描  │ │ Admin API 立即  │
│ InspectorHook   │ │ (5min 后)        │ │ 读取新值        │
│ .Enabled()      │ │                  │ │                 │
└───────┬─────────┘ └──────┬──────────┘ └────┬────────────┘
        │                  │                  │
        └──────────────────┼──────────────────┘
                           │
                ┌──────────▼──────────┐
                │  LoadConfig()       │
                │  ├─ settings.Global │
                │  │   .GetInt(key)   │
                │  ├─ 环境变量 (覆盖) │
                │  └─ 默认值 (兜底)   │
                └──────────┬──────────┘
                           │
                ┌──────────▼──────────┐
                │  返回 Config 结构体  │
                │  (新配置已生效)      │
                └─────────────────────┘
```

---

## 4. 事件驱动架构

```
┌────────────────────────────────────────────────────────────┐
│                    EventBus (MemoryBus)                     │
│              Buffer Size: 100 (异步非阻塞)                   │
└────────────────────────┬───────────────────────────────────┘
                         │
        ┌────────────────┼────────────────┐
        │                │                │
┌───────▼──────────┐ ┌───▼───────────┐ ┌─▼─────────────────┐
│ session_inspector│ │ session_      │ │ (future)          │
│ .finding         │ │ inspector     │ │ session_inspector │
│                  │ │ .recycle      │ │ .stats            │
└───────┬──────────┘ └───┬───────────┘ └─┬─────────────────┘
        │ 订阅           │ 订阅           │ 订阅
        │                │                │
┌───────▼──────────┐ ┌───▼───────────┐ ┌─▼─────────────────┐
│ InspectorNotifier│ │ InspectorNoti-│ │ (future)          │
│ .HandleFinding   │ │ fier          │ │ Redis 缓存同步     │
│                  │ │ .HandleRecycle│ │ Worker            │
└───────┬──────────┘ └───┬───────────┘ └───────────────────┘
        │                │
        └────────────────┼───────────────┐
                         │               │
        ┌────────────────┼───────────┐   │
        │                │           │   │
┌───────▼──────┐  ┌──────▼─────┐ ┌──▼───▼──┐
│ LarkBotChannel│  │ WeChatCh   │ │ Webhook │
│ .Send()       │  │ .Send()    │ │ POST    │
└───────────────┘  └────────────┘ └─────────┘
```

---

## 5. 数据库交互

### 5.1 表结构依赖

```sql
-- session_dim (主表，由 compression 模块创建)
CREATE TABLE session_dim (
    gw_session_id   TEXT PRIMARY KEY,
    tenant_id       TEXT NOT NULL,
    status          TEXT DEFAULT 'active',  -- active / closed
    first_request_at TIMESTAMP,
    last_active_at  TIMESTAMP,
    total_tokens    BIGINT DEFAULT 0,
    request_count   INT DEFAULT 0,
    error_count     INT DEFAULT 0,
    model_switch_count INT DEFAULT 0,
    stop_reason     TEXT,                   -- idle_timeout / manual / ...
    closed_at       TIMESTAMP
);

-- settings (配置表，由 settings 模块创建)
CREATE TABLE settings (
    setting_key     TEXT PRIMARY KEY,
    setting_value   TEXT,
    updated_at      TIMESTAMP DEFAULT NOW()
);
```

### 5.2 SQL 操作清单

| 操作 | SQL 示例 | 调用者 |
|------|---------|-------|
| 读取会话详情 | `SELECT * FROM session_dim WHERE gw_session_id=$1 AND tenant_id=$2` | Admin API |
| 统计平台级数据 | `SELECT status, COUNT(*) FROM session_dim GROUP BY status` | Admin API |
| 软关闭闲置会话 | `UPDATE session_dim SET status='closed', closed_at=NOW(), stop_reason=$1 WHERE ...` | Worker |
| 驱逐超限会话 | `UPDATE session_dim SET status='closed' ... WHERE gw_session_id IN (SELECT ... ORDER BY last_active_at LIMIT N)` | Worker |
| 读取配置 | `SELECT setting_value FROM settings WHERE setting_key=$1` | LoadConfig() |

---

## 6. Prometheus 指标体系

### 6.1 Hook 指标（实时）

```
llmgw_session_inspector_findings_total{code="TOKEN_LIMIT_EXCEEDED",severity="critical"} 12
llmgw_session_inspector_findings_total{code="SESSION_IDLE",severity="warning"} 45
llmgw_session_inspector_block_total{reason="token_limit"} 3
llmgw_session_inspector_hook_duration_seconds_bucket{le="0.01"} 890
```

### 6.2 Worker 指标（批量）

```
llmgw_session_lifecycle_recycled_total{reason="idle_timeout"} 234
llmgw_session_lifecycle_recycled_total{reason="absolute_max_lifetime"} 12
llmgw_session_lifecycle_soft_closed_total 246
llmgw_session_lifecycle_evicted_total{policy="lru"} 34
llmgw_session_lifecycle_scan_duration_seconds 0.523
```

### 6.3 Grafana 仪表盘示例

```
Panel 1: Finding 趋势（按 severity）
  - Query: rate(llmgw_session_inspector_findings_total[5m])
  - Legend: {{severity}}

Panel 2: 每日回收数量
  - Query: increase(llmgw_session_lifecycle_recycled_total[24h])
  - Legend: {{reason}}

Panel 3: Hook 执行延迟 P99
  - Query: histogram_quantile(0.99, llmgw_session_inspector_hook_duration_seconds_bucket)
```

---

## 7. 容灾与降级策略

### 7.1 单个 Inspector 失败

```
InspectorHook.Execute()
  → for inspector in inspectors:
      findings, err = inspector.Inspect(snap)
      if err != nil:
        slog.Warn("inspector failed", "inspector", inspector.Name(), "error", err)
        continue  // 继续下一个 inspector
  → 合并所有成功的 findings
  → 返回给 Pipeline（不中断主流程）
```

### 7.2 EventBus 满

```
EventBus.Publish(event)
  → 写入 buffered channel (cap=100)
  → 如果 channel 满
    → 返回 error（非阻塞）
    → InspectorHook 降级：仅写日志和 Prometheus
```

### 7.3 通知渠道失败

```
InspectorNotifier.HandleFindingEvent()
  → for channel in channels:
      err = channel.Send(ctx, msg)
      if err != nil:
        slog.Warn("channel send failed", "channel", channel, "error", err)
        // 不返回 error，避免阻塞 EventBus
```

### 7.4 数据库不可用

```
SessionLifecycleWorker.sweep()
  → rows, err = pool.Query(ctx, sql)
  → if err != nil:
      scanErrors.Inc()
      slog.Error("scan failed", "error", err)
      return  // 等下次周期重试（5min 后）
```

---

## 8. 性能优化设计

### 8.1 配置缓存

```go
// hook.go 中的设计
type InspectorHook struct {
    inspectors []Inspector
    config     *Config  // 缓存在 Enabled() 时读取的配置
}

func (h *InspectorHook) Enabled(ctx, env) bool {
    h.config = LoadConfig()  // 仅读一次
    return h.config.Enabled
}

func (h *InspectorHook) Execute(ctx, env) error {
    // 复用 h.config，避免再次查 DB
}
```

### 8.2 批量操作

```go
// Worker 中的批量 UPDATE
func (w *Worker) applyRecycle(ctx, candidates) {
    // 单条 SQL 更新多个会话（IN 子句）
    _, err := w.pool.Exec(ctx, `
        UPDATE session_dim 
        SET status='closed', closed_at=NOW(), stop_reason=$1
        WHERE gw_session_id = ANY($2)
    `, reason, candidates)
}
```

### 8.3 索引优化

```sql
-- session_dim 表建议索引
CREATE INDEX idx_session_status_active ON session_dim(status, last_active_at) 
  WHERE status='active';

CREATE INDEX idx_session_tenant_status ON session_dim(tenant_id, status);
```

---

## 9. 测试策略

### 9.1 单元测试覆盖

| 组件 | 测试文件 | 覆盖率 |
|------|---------|-------|
| Config | config_test.go | ✅ DefaultConfig / LoadConfig / helpers |
| Inspectors | inspectors_test.go | ✅ 6 个 Inspector 边界条件 |
| Hook | session_inspector_test.go | ✅ 编排逻辑 / 错误处理 |
| Worker | (需补充) | ⚠️ sweep/recycle 逻辑 |
| Admin API | (需补充) | ⚠️ 3 个新端点 HTTP 测试 |

### 9.2 集成测试场景

```
场景 1: Token 超限阻断
  - 创建会话并发送请求至 token_count=101000 (超 100000)
  - 验证返回 429 + 日志包含 "TOKEN_LIMIT_EXCEEDED"

场景 2: 闲置会话回收
  - 修改配置 idle.timeout=10s
  - 创建会话后等待 15s
  - Worker 扫描后验证 status='closed', stop_reason='idle_timeout'

场景 3: IM 通知推送
  - Mock 飞书 Webhook
  - 触发 Finding 事件
  - 验证收到 POST 请求且 body 包含 session_id
```

---

## 10. 运维指南

### 10.1 日常监控指标

```
# 每日检查
1. llmgw_session_lifecycle_recycled_total{reason="idle_timeout"} 
   → 超 1000/day 需排查是否配置过严格

2. llmgw_session_inspector_block_total 
   → 持续增长说明有恶意请求或配置不合理

3. llmgw_session_lifecycle_scan_errors_total 
   → 非零值说明 DB 连接异常

# 告警规则
- scan_duration > 10s → 数据库慢查询
- findings_total{severity="critical"} rate[5m] > 10 → 异常流量
```

### 10.2 常见问题排查

| 问题 | 排查步骤 | 解决方案 |
|-----|---------|---------|
| 会话被误杀 | 检查日志 `stop_reason` 字段 | 调整 `idle.timeout` 或 `max_per_tenant` |
| IM 通知未收到 | 检查 EventBus 订阅日志 | 验证 `gLarkCh != nil` 且 `Subscribe` 成功 |
| 配置修改不生效 | 检查 `settings` 表是否写入 | 等待下次请求触发 `LoadConfig()` |
| Worker 未运行 | 检查启动日志 | 验证 `dbConn != nil && dbConn.Enabled()` |

---

## 11. 未来优化方向

### 11.1 P1 优先级

1. **补充 Worker 单元测试** — 验证 sweep/recycle 逻辑
2. **补充 Admin API 测试** — HTTP 端点行为验证
3. **Worker EventBus 集成** — 通过适配器桥接 `LifecycleEvent` 与 `eventbus.Event`

### 11.2 P2 优先级

1. **交互式卡片升级** — InspectorNotifier 生成带按钮的飞书卡片（查看详情/立即回收）
2. **租户级路由** — 支持不同租户推送到不同 IM 群
3. **多实例 Worker 分布式锁** — 使用 Redis/PG advisory lock 避免重复扫描

### 11.3 P3 优先级

1. **动态阈值** — 基于历史数据自动调整 `rpm_limit` / `idle_timeout`
2. **预测性驱逐** — 基于 ML 模型预测会话闲置概率
3. **A/B 测试框架** — 不同租户使用不同配置策略并对比效果

---

**文档维护者**: Official-Deploy Team  
**最后更新**: 2026-07-09  
**相关文档**: 
- [session-inspector.md](./session-inspector.md) — 模块详细文档
- [session-inspector-audit.md](./session-inspector-audit.md) — 审计报告
