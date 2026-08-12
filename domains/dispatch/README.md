# Dispatch V2 多层队列调度架构

> 本包实现了设计文档 [57-多层队列调度架构设计方案.md](../../docs/会话优化v2/57-多层队列调度架构设计方案.md) 中描述的多层队列调度系统。

## 架构概览

```
HTTP Request
    ↓
Tier-1: Model Queue (按模型分队列)
    ↓
① Model Dispatcher (解析 auto, 选择凭据)
    ↓
Tier-2: Credential Queue (按凭据分队列 + Governor 限流)
    ↓
② Credential Forwarder (转发到上游)
    ↓ (失败)
③ Failover Mover (重试/切凭据/切模型)
    ↓
Tier-3: Metrics & Snapshot (可观测层)
```

## 文件清单与职责

| 文件 | 职责 | 设计文档对应 |
|-----|-----|-------------|
| `gate.go` | `dispatch_v2.enabled` 特性开关 + 原子缓存 | §3.3 feature-flag |
| `config.go` | 热配置加载 (`llmgw_dispatch_*`) | §3.3 hotconfig |
| `errors.go` | 错误定义和常量 (`maxAttempts`, `errPaceTimeout`) | §4.2 补充 |
| `queued_request.go` | `QueuedRequest` / `CredentialRef` / `ForwardOutcome` | §4.1, §4.2 |
| `governor.go` | 三模调速器 (concurrency/rpm/tpm/disabled) | §4.3 governor |
| `snapshot.go` | `modelQueue` 结构 + `Pipeline.Snapshot()` API | §4.2 model_queue.go + cred_queue.go 合并 |
| `pipeline.go` | `Pipeline` 核心调度器 + Submit 入口 | §4.2 |
| `dispatcher.go` | ① Model Dispatcher 执行器 | §2.2, §4.2 |
| `forwarder.go` | ② Credential Forwarder 执行器 (含 Tier-2 队列逻辑) | §2.2, §4.2 |
| `failover.go` | ③ Failover Mover 执行器 | §2.2, §4.2 |
| `metrics.go` | Prometheus 指标 (替代事件总线) | §4.2 stats.go, §6 |
| `dispatch_test.go` | 完整测试套件 (22 个测试) | §4.2 *_test.go |

## 与设计文档的差异

### 架构简化

设计文档要求独立的 `model_queue.go`, `cred_queue.go`, `stats.go` 文件以及事件总线实现。实际实现做了以下合理简化：

1. **队列结构合并**：
   - `modelQueue` 结构体定义在 `snapshot.go` (仅 12 行)
   - `credQueue` 逻辑直接在 `forwarder.go` 的 `credForwarder.queue` 中实现
   - **理由**：两者都是简单的有界 channel 包装，无需独立文件

2. **Tier-3 可观测层**：
   - 设计要求：`StatsEvent` + 事件总线 + `StatsAggregator` goroutine
   - 实际实现：直接使用 Prometheus `promauto` metrics + `Pipeline.Snapshot()` 查询接口
   - **理由**：Prometheus 是行业标准，性能更好，避免额外的事件分发开销

3. **错误集中管理**：
   - 额外添加 `errors.go` 集中定义 `maxAttempts`, `maxRetryBudget`, `errPaceTimeout` 等
   - **理由**：提高可维护性，避免散落在各文件中

### 功能完整性 ✅

所有设计目标和核心算法均已实现：
- ✅ 三层队列 + 四执行器
- ✅ 四种并发模式 (concurrency/rpm/tpm/disabled)
- ✅ 分层故障转移 (凭据→模型→拒绝)
- ✅ 首字节边界感知 (ADR-Disp-003)
- ✅ 有界队列 + overflow 策略
- ✅ Feature gate + kill-switch
- ✅ 完整测试覆盖 (22 tests passed)

## 核心不变量

1. **单所有者**：`QueuedRequest` 在任意时刻只被一个执行器持有 (hand-off)
2. **ResultCh 单发**：每个请求的 `ResultCh` 恰好发送一次
3. **有界队列**：Tier-1/Tier-2 全部有界，满则 overflow
4. **Stats 不阻塞**：Metrics 记录从不阻塞热路径
5. **ctx 取消贯穿**：客户端断开立即传播到所有执行器

## ADR 决策记录

- **ADR-Disp-001**: 凭据队列按 `credentialID` 建制（厂商限流账户级）
- **ADR-Disp-002**: dispatch 包不 import executors（通过回调解耦）
- **ADR-Disp-003**: 首字节后不跨凭据切换
- **ADR-Disp-004**: 迁移 479 的 .sql 仅作 source-of-truth；真正生效靠 db.go 的 ensure 钩子
- **ADR-Disp-005**: `dispatch_v2.enabled` 默认 ON，DangerLevel=Breaking

## 测试覆盖

- Governor 三模式 (concurrency/rpm/tpm) + 限流验证
- 故障转移全路径 (重试→切凭据→切模型→拒绝)
- 首字节边界 (pre-firstbyte 可切换, post-firstbyte 终止)
- 队列溢出 + 超时处理
- ctx 取消 + goroutine 安全关闭
- 并发竞争测试 (Submit/Stop race)

运行测试：
```bash
go test -v ./domains/dispatch/
```

## 可观测性

### Prometheus Metrics

- `dispatch_model_queue_depth` - Tier-1 模型队列深度
- `dispatch_model_queue_wait_seconds` - Tier-1 等待时长
- `dispatch_cred_queue_depth` - Tier-2 凭据队列深度
- `dispatch_in_flight` - 正在转发的请求数
- `dispatch_dequeued_total` - 出队计数
- `dispatch_forwarded_total{result}` - 转发结果 (success/fail_prefirstbyte/fail_postfirstbyte)
- `dispatch_overflow_total{reason}` - 溢出计数 (cred_queue_full/pace_timeout 等)
- `dispatch_failover_total{kind}` - 故障转移计数 (cred_retry/cred_switch/model_switch)

### Admin API

```bash
# 实时队列快照
curl http://localhost:8080/api/admin/dispatch/queues
```

返回：
```json
{
  "enabled": true,
  "wired": true,
  "models": [
    {"model": "gpt-4", "depth": 12}
  ],
  "credentials": [
    {"credential": 101, "mode": "rpm", "depth": 3}
  ]
}
```

## 集成点

### cmd/gateway/main.go
- `wireDispatchPipeline()` - 构建 Pipeline 并注入到 executor
- 路由注册：`/api/admin/dispatch/queues` → `handleDispatchQueues`

### cmd/gateway/main_settings.go
- `syncDispatchGateFromSettings()` - 启动时同步 `dispatch_v2.enabled` 到原子缓存

### admin/settings.go
- PUT `/api/admin/settings` - 即时同步 `dispatch_v2.enabled` 变更

### domains/streaming/executors/executor.go
- `Execute()` - 检查 `dispatch.IsDispatchEnabled()` 决定是否使用 Pipeline

## 配置

### 数据库设置 (settings_kv)

```sql
-- Feature gate (默认 ON)
INSERT INTO settings_kv (key, value) VALUES ('dispatch_v2.enabled', 'true');

-- 全局模型变更开关
INSERT INTO settings_kv (key, value) VALUES ('dispatch_v2.allow_model_change', 'true');
```

### Hotconfig (运行时可热更新)

```sql
-- 队列参数
llmgw_dispatch_max_queue_depth=1024          -- 默认队列深度
llmgw_dispatch_max_queue_wait_ms=5000        -- 默认最长等待
llmgw_dispatch_dispatcher_workers=8          -- Model Dispatcher 工作池
llmgw_dispatch_failover_workers=8            -- Failover Mover 工作池
llmgw_dispatch_retry_per_credential=1        -- 同凭据重试次数
```

### 凭据级配置 (credentials 表)

```sql
-- 每个凭据独立的并发模式和队列参数
UPDATE credentials SET
  concurrency_mode = 'rpm',           -- concurrency|rpm|tpm|disabled
  rpm_limit = 3500,                   -- rpm 模式：每分钟请求数上限
  tpm_limit = NULL,                   -- tpm 模式：每分钟 token 数上限
  max_queue_depth = 2048,             -- 覆盖全局默认值 (NULL=使用全局)
  max_queue_wait_ms = 10000           -- 覆盖全局默认值 (NULL=使用全局)
WHERE id = 101;
```

## Kill Switch

紧急关闭 V2 调度，回退到同步模式：

```bash
# 方法 1: Admin API
curl -X PUT http://localhost:8080/api/admin/settings \
  -H 'Content-Type: application/json' \
  -d '{"dispatch_v2.enabled": false}'

# 方法 2: 环境变量 (需重启)
export KILL_DISPATCH_V2=1
./llm-gateway-go

# 方法 3: 数据库直接修改 (需重启或等待热加载)
UPDATE settings_kv SET value = 'false' WHERE key = 'dispatch_v2.enabled';
```

## 参考文档

- [57-多层队列调度架构设计方案.md](../../docs/会话优化v2/57-多层队列调度架构设计方案.md) - 完整设计
- [56-LLM厂商并发模式参考.md](../../docs/会话优化v2/56-LLM厂商并发模式参考.md) - 厂商限流语义
- [31-当前实现基线与修正决策.md](../../docs/会话优化v2/31-当前实现基线与修正决策.md) - 历史背景
