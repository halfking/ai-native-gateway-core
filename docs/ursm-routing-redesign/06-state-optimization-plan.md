# 状态管理优化方案 — URSM Phase 3

> ⚠️ 本文档已被 `08-unified-resource-pipeline.md` 取代为统一方案。
> 保留作为 Phase 3 的历史记录, 新设计以 08 为准。

> 本文档基于 2026-07-13 完整代码审计撰写。审计覆盖了所有状态读写路径、
> 导入依赖和运行时接线，确保方案无遗漏。

## 1. 现状分析

### 1.1 当前状态管理全景（3条独立写入路径）

经审计确认，当前存在 **3 条独立的状态写入路径**，互不感知：

```
路径 A: credential.Writer (写 DB credentials / credential_model_bindings 表)
  executor.go: writeCredentialStateOnError / restoreCredentialState (按错误类型过滤)
  bg/passive_probe_listener.go (被动失败监听 30s)
  → 独立于 credentialstate / credentialhealth

路径 B: credentialstate.Manager + credentialfpslot (写 Redis + credential_state_log)
  executor.go: StateObserver.UpdateOnSuccess/OnFailure
  executor.go: RouteNodeRecorder (包装 credentialfpslot.RecordNodeSuccess/Failure)
  bg/credential_probe_v2.go: UpdateFromProbe
  bg/active_probe_worker.go: UpdateFromProbe
  router.go: FpSlots.GetNodeState (**路由时直接调用**)
  → 3 层缓存 (mem→redis→DB) + Redis Lua 节点状态

路径 C: credentialhealth (写 Redis 滑动窗口 + 独立 DB 检查)
  HealthTracker.OnSuccess → Recorder (Redis LIST, 100条)
  HealthTracker.OnError → Recorder + Tuner (调整 concurrency_limit_auto) + Checker (写 credential_model_bindings)
  bg/health_auto_recover.go / bg/call_history_aggregator.go / bg/concurrency_auto_scaleup.go
  → 完全独立于 A 和 B

其他:
  URSM.RecordRequest()     → 在建，尚未接入
  UnifiedProbeScheduler    → 已废弃，main.go 置 nil
  FailureLogger            → candidate_failure_logs (保留)
  bg/credential_recovery.go → 纯 SQL 恢复循环 (保留, 适应新方案)
```

```
请求/探测事件
  │
  ├─► executor.go (每次请求)
  │     ├─ HealthTracker.OnSuccess/OnError         → Redis llmgw:callhist:* (LIST, 100条)
  │     ├─ StateObserver.UpdateOnSuccess/OnFailure  → credentialstate (mem→redis→DB)
  │     ├─ URSM.RecordRequest()                     → ursm (mem→redis→DB)
  │     ├─ RouteNodeRecorder.RecordSuccess/Failure   → credentialfpslot (Redis Lua)
  │     ├─ writeCredentialStateOnError               → credential.Writer → DB
  │     ├─ UnifiedProbeScheduler.OnRealRequest        → 已废弃
  │     └─ FailureLogger                              → candidate_failure_logs (DB)
  │
  ├─► bg/credential_probe_v2.go (1h)
  │     └─ writeHealth → credentials 表 + cache + stateManager
  │
  ├─► bg/model_probe.go (10s)
  │     └─ applyResult → model_probe_state + credential_model_bindings + cache
  │
  ├─► bg/active_probe_worker.go (按需)
  │     └─ StateManager.UpdateFromProbe → credentialstate
  │
  ├─► bg/passive_probe_listener.go (30s)
  │     └─ passive_probe_state 表
  │
  └─► credentialhealth.checker (per-request)
        └─ credential_model_bindings.available = FALSE
```

### 1.2 关键问题

| # | 问题 | 影响 |
|---|------|------|
| 1 | **同一 (cred, model) 状态在 N 处存储** — memCache, Redis credstate, Redis callhist, Redis fp_node, credential_state_log, model_probe_state, credentials 表, credential_model_bindings | 状态漂移、写入放大、排障困难 |
| 2 | **三层缓存 (mem→redis→DB) 过度设计** — 每次写入 mem + redis + batchWriter 重复调用 | 延迟增加、复杂度高 |
| 3 | **6 个探测系统功能重叠** — CredentialProbeV2, ModelProbeRunner, ActiveProbeWorker, HealthTracker, PassiveProbeListener, UnifiedProbeScheduler(已废弃) | 维护负担、行为不一致 |
| 4 | **同一次请求状态写 4-5 次** — executor 成功路径写 HealthTracker + StateObserver + URSM + RouteNodeRecorder + maybe writeCredentialStateOnError | CPU/网络浪费 |
| 5 | **credentialhealth 的 Redis LIST 滑动窗口** — 100 条 JSON 存储，每次 append 都 LPUSH+LTRIM+EXPIRE | 存储膨胀、读时反序列化全部 |

### 1.3 依赖审计关键发现（2026-07-13）

经全量代码审计（覆盖 31 个文件、跨包引用、运行时接线），发现以下计划必须覆盖的依赖：

| # | 发现 | 影响 | 处理 |
|---|------|------|------|
| 1 | **router.go 直接调用 `FpSlots.GetNodeState`**（line 426）— 路由 P2C 评分需获取 concurrency_count | 若删除 credentialfpslot，StateRecorder 必须提供 `GetNodeState(credID, model) → {concurrency, ...}` 接口 | StateRecorder 新增 `GetNodeState` 方法 |
| 2 | **credential.Writer (Path A) 独立运作** — executor.go 的 `writeCredentialStateOnError` / `restoreCredentialState` 直接写 DB `credentials` 表 | 仅合并 Path B+C 仍有 1 条独立写路径。不影响路由决策，但影响 `bg/credential_recovery.go` | Phase B 将 credential.Writer 也迁入 StateRecorder |
| 3 | **`bg/credential_recovery.go` 纯 SQL 恢复循环** — 扫描 DB 表恢复冷却/限流凭据，不依赖 credentialstate/fpslot | 若状态主存转到 Redis，恢复循环需同时处理 Redis 状态 | Phase B 扩展 recovery 循环 |
| 4 | **`UnifiedProbeScheduler` 主流程已死** — main.go line 876 置 nil，不可恢复 | 直接删除 | P0 可执行 |
| 5 | **`domains/routing/health_tracker.go` 未被外部引用** — 177 行死代码 | 直接删除 | P0 可执行 |

### 1.4 写入路径对应关系

```
executor.go 成功路径 (当前 ~8 次写入):
  HealthTracker.OnSuccess             → Path C (Redis LIST)
  StateObserver.UpdateOnSuccess       → Path B (credentialstate 3层)
  URSM.RecordRequest()                → Path B (batch_writer DB)
  RouteNodeRecorder.RecordSuccess     → Path B (credentialfpslot Lua)
  restoreCredentialState              → Path A (credential.Writer → DB)
  UnifiedProbeScheduler.OnRealRequest → dead
  ↓
优化后: 1 次 StateRecorder.Record(success=true)

executor.go 失败路径 (当前 ~10 次写入):
  HealthTracker.OnError + Tuner.Check  → Path C
  StateObserver.UpdateOnFailure        → Path B
  URSM.RecordRequest()                 → Path B
  RouteNodeRecorder.RecordFailure      → Path B
  writeCredentialStateOnError          → Path A (按错误类型过滤)
  FailureLogger                        → candidate_failure_logs (保留)
  UnifiedProbeScheduler.OnRealRequest  → dead
  ↓
优化后: 1 次 StateRecorder.Record(success=false, errorKind, latencyMs)
         + 1 次 FailureLogger (保留)

router.go 读路径 (当前 4 层缓存):
  IsAvailable() → 4 层 LayerCache (mem→redis→DB)
  FpSlots.GetNodeState() → Redis JSON
  ↓
优化后: IsAvailable() + GetNodeState() → Redis HGETALL (单次)
```

## 2. 目标架构

### 核心理念：Redis 存"当前状态"、DB 记"状态变化"

```
写路径（简化后）:
  请求结束 / 探测完成
    │
    ▼
  StateRecorder.Record(ctx, Record{
      CredentialID, Model, Success, LatencyMs, ErrorKind
  })
    │
    ├─► Redis HMSET (实时状态，直接覆盖)
    │     Key: ursm:node:{credID}:{model}
    │     Fields: available, consecutive_fail, last_error, latency_avg, updated_at
    │     TTL: 7d
    │
    └─► IF 状态发生转换 (可用↔不可用):
          └─► DB INSERT state_change_log (7天保留)

读路径（简化后）:
  路由时 IsAvailable(credID, model):
    │
    ├─► Redis HGETALL ursm:node:{credID}:{model}
    │     ├─ 不存在 → 默认可用 (fail-open)
    │     └─ 存在 → 本地判断:
    │           available==true && consecutive_fail < 2 → true
    │           disabled_until > now → false
    │           其他 → true
    │
    └─► Redis 不可用 → 默认可用
```

### 2.1 状态转换条件

```
                  ┌──────────────┐
    1次成功       │              │       连续2次失败
  ◄───────────────│   AVAILABLE  │──────────────────►
                  │              │
                  └──────────────┘
                  │              │
                  │  UNAVAILABLE │
                  │              │
                  └──────────────┘

  规则:
  - 不可用 → 可用: 1 次成功请求 / 1 次探测成功即可转换
  - 可用 → 不可用: 必须连续 2 次失败（不含 KindCanceled / IsClientBug）
  - 从不可用状态恢复时: 清除 consecutive_fail 计数
```

### 2.2 Redis 数据模型

```redis
# 当前实时状态 (HASH)
Key: ursm:node:{credentialID}:{model}
Type: HASH
TTL: 604800 (7d, 自动清理不活跃节点)

Fields:
  available          → "1" / "0"          # 路由核心决策字段
  consecutive_fail   → "0"                # 连续失败次数（仅计数非客户端错误）
  last_error         → "rate_limited"     # 最近错误类型
  last_success_at    → "1718000000"       # unix ts
  last_failure_at    → "1718000100"       # unix ts
  disabled_until     → "1718000500"       # unix ts, 冷却到期时间
  latency_avg_ms     → "850"              # 指数移动平均延迟
  updated_at         → "1718000100"       # unix ts

# 分布式锁 (可选，写入时防并发)
Key: ursm:lock:{credentialID}:{model}
Type: STRING
TTL: 5
NX: true
```

### 2.3 DB 审计表

```sql
-- state_change_log: 只有状态转换时才写入
CREATE TABLE IF NOT EXISTS state_change_log (
    id              BIGSERIAL PRIMARY KEY,
    credential_id   INT NOT NULL,
    raw_model_name  TEXT NOT NULL,
    
    -- 事件信息
    event_type      TEXT NOT NULL,  -- 'available→unavailable' | 'unavailable→available'
    reason          TEXT NOT NULL,  -- 'consecutive_2_failures' | 'request_success' | 'probe_recovered'
    error_kind      TEXT,           -- 触发转换的错误类型（失败时）
    consecutive_fail INT DEFAULT 0, -- 转换时的连续失败数
    
    -- 快照（记录转换时的完整状态）
    snapshot JSONB,                 -- {available, latency_avg, last_error, ...}
    
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 7天清理索引
CREATE INDEX idx_scl_created_at ON state_change_log(created_at);

-- 按凭据查询
CREATE INDEX idx_scl_credential ON state_change_log(credential_id, raw_model_name, created_at DESC);

-- 定时清理 (pg_cron 或应用层定时任务):
-- DELETE FROM state_change_log WHERE created_at < NOW() - INTERVAL '7 days'
```

### 2.4 事件类型定义

| event_type | 触发条件 | reason |
|---|---|---|
| `available→unavailable` | 连续失败 ≥ 2 | `consecutive_2_failures` |
| `available→unavailable` | 探测标记不可用 | `probe_failed` |
| `available→unavailable` | 管理员手动禁用 | `manual_disabled` |
| `unavailable→available` | 单次成功请求 | `request_recovered` |
| `unavailable→available` | 探测恢复 | `probe_recovered` |
| `unavailable→available` | 管理员手动启用 | `manual_enabled` |

## 3. 详细设计

### 3.1 StateRecorder — 统一写入入口

```go
// domains/ursm/recorder.go — 统一状态记录器

package ursm

type RecordRequest struct {
    CredentialID int
    Model        string
    Success      bool
    LatencyMs    int
    ErrorKind    errorsx.ErrorKind
    RequestID    string
    Source       string // "request" | "probe_v2" | "model_probe" | "manual"
}

type StateRecorder struct {
    redis redis.UniversalClient
    db    *pgxpool.Pool
}

// Record 是唯一的状态写入入口
// 1. 从 Redis 读取当前状态（原子 HGETALL）
// 2. 应用状态转换规则
// 3. Lua 脚本原子写入 Redis（HMSET + 条件判断 disabled_until）
// 4. 如有状态转换，异步写入 DB state_change_log
func (r *StateRecorder) Record(ctx context.Context, req RecordRequest) error
```

### 3.2 Lua 脚本 — 原子状态更新

```lua
-- KEYS[1] = ursm:node:{credID}:{model}
-- ARGV[1] = "success" | "failure"
-- ARGV[2] = error_kind (空字符串表示成功)
-- ARGV[3] = now (unix秒)
-- ARGV[4] = latency_ms
-- ARGV[5] = fail_threshold (连续失败阈值，默认2)
-- ARGV[6] = cooldown_seconds (冷却时间，默认300)

local key = KEYS[1]
local kind = ARGV[1]
local error_kind = ARGV[2]
local now = tonumber(ARGV[3])
local latency_ms = tonumber(ARGV[4])
local fail_threshold = tonumber(ARGV[5]) or 2
local cooldown = tonumber(ARGV[6]) or 300

-- 读取当前状态
local raw = redis.call('HGETALL', key)
local state = {}
for i = 1, #raw, 2 do
    state[raw[i]] = raw[i+1]
end

-- 初始化默认值
local available = state['available']
local consecutive_fail = tonumber(state['consecutive_fail'] or '0')
local disabled_until = tonumber(state['disabled_until'] or '0')
local latency_avg = tonumber(state['latency_avg_ms'] or '0')

-- 如果冷却已到期，自动恢复
if disabled_until > 0 and now >= disabled_until then
    available = '1'
    consecutive_fail = 0
    disabled_until = 0
end

local state_changed = false
local from_state = available
local transition_reason = ''

if kind == 'success' then
    -- 成功: 设置可用，重置计数
    consecutive_fail = 0
    available = '1'
    disabled_until = 0
    state['last_success_at'] = tostring(now)
    -- 检查是否发生了 unavailable → available 转换
    if from_state == '0' then
        state_changed = true
        transition_reason = 'request_recovered'
    end
elseif kind == 'failure' then
    consecutive_fail = consecutive_fail + 1
    state['last_failure_at'] = tostring(now)
    state['last_error'] = error_kind
    -- 连续失败 >= 阈值: 标记不可用
    if consecutive_fail >= fail_threshold then
        available = '0'
        disabled_until = now + cooldown
        -- 检查是否发生了 available → unavailable 转换
        if from_state == '1' then
            state_changed = true
            transition_reason = 'consecutive_' .. fail_threshold .. '_failures'
        end
    end
end

-- 更新延迟 EMA (指数移动平均)
if latency_ms > 0 then
    if latency_avg == 0 then
        latency_avg = latency_ms
    else
        latency_avg = math.floor((latency_avg * 3 + latency_ms) / 4)
    end
    state['latency_avg_ms'] = tostring(latency_avg)
end

state['available'] = available
state['consecutive_fail'] = tostring(consecutive_fail)
state['disabled_until'] = tostring(disabled_until)
state['updated_at'] = tostring(now)

-- 原子写入 Redis
local args = {}
for k, v in pairs(state) do
    table.insert(args, k)
    table.insert(args, v)
end
redis.call('HMSET', key, unpack(args))
redis.call('EXPIRE', key, 604800)  -- 7d TTL

-- 返回状态转换信息 (给调用方写 DB 用)
return {tostring(state_changed), transition_reason, tostring(consecutive_fail), tostring(from_state), available}
```

### 3.3 读取接口

```go
// NodeReadState 从 Redis 读取的即时状态（供路由使用）
type NodeReadState struct {
    Available       bool
    ConsecutiveFail int
    LastError       string
    DisabledUntil   int64
    LatencyAvgMs    int
}

// IsAvailable 路由时调用 — 只读 Redis，无 DB 查询
func (r *StateRecorder) IsAvailable(ctx context.Context, credID int, model string) (bool, string) {
    key := fmt.Sprintf("ursm:node:%d:%s", credID, model)
    
    data, err := r.redis.HGetAll(ctx, key).Result()
    if err != nil || len(data) == 0 {
        return true, ""  // fail-open
    }
    
    now := time.Now().Unix()
    disabledUntil, _ := strconv.ParseInt(data["disabled_until"], 10, 64)
    consecutiveFail, _ := strconv.Atoi(data["consecutive_fail"])
    
    // 冷却到期自动恢复
    if disabledUntil > 0 && now >= disabledUntil {
        return true, ""
    }
    
    if data["available"] == "0" {
        return false, data["last_error"]
    }
    
    // available == "1" 但连续失败未归零（静默期）
    if consecutiveFail >= 2 {
        return false, data["last_error"]
    }
    
    return true, ""
}
```

### 3.4 DB 异步写入

```go
// 在 Lua 脚本返回 state_changed=true 时异步调用
func (r *StateRecorder) logStateChange(ctx context.Context, req RecordRequest, transition *Transition) {
    snapshot := map[string]interface{}{
        "available":       transition.ToState,
        "consecutive_fail": transition.ConsecutiveFail,
        "error_kind":      req.ErrorKind,
        "source":          req.Source,
        "latency_ms":      req.LatencyMs,
    }
    
    snapshotJSON, _ := json.Marshal(snapshot)
    
    _, err := r.db.Exec(ctx, `
        INSERT INTO state_change_log
            (credential_id, raw_model_name, event_type, reason, error_kind, 
             consecutive_fail, snapshot, created_at)
        VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
    `, req.CredentialID, req.Model,
        transition.EventType,
        transition.Reason,
        stringPtrIfNotEmpty(string(req.ErrorKind)),
        transition.ConsecutiveFail,
        snapshotJSON,
    )
    if err != nil {
        slog.Warn("failed to log state change", "error", err)
    }
}
```

### 3.5 7天数据保留策略

| 数据 | 位置 | 保留策略 | 清理方式 |
|------|------|---------|---------|
| 当前状态 | Redis HASH | 7d TTL | Redis 自动过期 |
| 状态转换日志 | DB state_change_log | 7d | `DELETE WHERE created_at < NOW() - '7d'` |
| 探测运行日志 | DB model_probe_runs | 7d | 同上 |
| 候选失败日志 | DB candidate_failure_logs | 7d | 同上（已有列式存储） |
| 共识状态 | DB model_probe_state | 永久（当前快照） | 保留 |

## 4. 待废弃模块迁移计划

### 4.1 废弃清单

| 包/模块 | 当前用途 | 替代方案 | 优先级 |
|---------|---------|---------|--------|
| `domains/credentialstate/` (整个包) | mem→redis→DB 三层状态 | `ursm.StateRecorder` (Redis 直接读写) | P0 |
| `credentialhealth/recorder.go` | Redis LIST 滑动窗口 | `ursm.StateRecorder.Record` (HMSET 直接覆盖) | P0 |
| `credentialhealth/checker.go` | 80% 失败率检测 → DB | `ursm.StateRecorder` 连续2次失败即转换 | P1 |
| `credentialhealth/tuner.go` | 并发限自动调整 | 保留，移入 `domains/ursm/tuner.go` | P2 |
| `credentialfpslot/node_state.go` | Redis JSON 节点状态 | `ursm.NodeReadState` (HASH) | P0 |
| `bg/active_probe_worker.go` | 错误触发的主动探测 | 简化，由 `StateRecorder` 直接触发探测 | P1 |
| `bg/passive_probe_listener.go` | 被动失败监听 → DB | `state_change_log` 分析代替 | P2 |
| `bg/unified_probe_scheduler.go` | 已废弃 | 删除 | P0 |
| `domains/streaming/executors/health_tracker.go` | 健康追踪 | `ursm.StateRecorder` | P0 |
| `domains/streaming/executors/route_node_recorder.go` | 路由节点记录 | `ursm.StateRecorder` | P0 |
| DB `credential_state_log` 表 | 状态日志 | `state_change_log` | P1 |
| DB `credential_state_nodes` 表 | 节点注册 | 不需要 | P1 |
| DB `passive_probe_state` 表 | 被动探测 | `state_change_log` | P2 |

### 4.2 迁移步骤

```
Phase A: 创建新组件（不与旧组件共存）
  1. 创建 ursm/recorder.go — StateRecorder (Lua + HMSET) + GetNodeState
  2. 创建 DB migration — state_change_log 表
  3. 在 executor.go 中注入 StateRecorder（仅创建，尚未接入）
  4. 在 router.go 中替换 FpSlots.GetNodeState → StateRecorder.GetNodeState

Phase B: 双写期（新旧并存，比较校验）
  5. 修改 executor.go: 成功/失败路径追加 StateRecorder.Record()（旧调用保留）
  6. 修改 bg/credential_probe_v2.go: writeHealth 追加 StateRecorder.Record()
  7. 修改 bg/model_probe.go: applyResult 追加 StateRecorder.Record()
  8. 修改 executor.go: writeCredentialStateOnError 追加 StateRecorder.Record()
  9. 修改 router.go: IsAvailable 切换为 StateRecorder 新路径
  10. 验证: 对比 RetVal 差异，不一致则告警

Phase C: 切流到新组件
  11. executor.go: 移除 HealthTracker.OnSuccess/OnError
  12. executor.go: 移除 StateObserver.UpdateOnSuccess/OnFailure
  13. executor.go: 移除 RouteNodeRecorder 调用
  14. executor.go: 移除 writeCredentialStateOnError / restoreCredentialState
  15. bg/: 移除主动/被动探测旧写入
  16. 移动废弃包到 _to-be-deprecated/

Phase D: 清理
  17. 移动 domains/credentialstate/ → _to-be-deprecated/
  18. 移动 credentialhealth/ → _to-be-deprecated/
  19. 移动 bg/active_probe_worker.go → _to-be-deprecated/
  20. 移动 bg/passive_probe_listener.go → _to-be-deprecated/
  21. 移动 bg/unified_probe_scheduler.go → _to-be-deprecated/
  22. 移动 credentialfpslot/node_state.go → _to-be-deprecated/
  23. 移动 executors/health_tracker.go → _to-be-deprecated/
  24. 移动 executors/route_node_recorder.go → _to-be-deprecated/
  25. DB 清理: DROP credential_state_log, credential_state_nodes, passive_probe_state
  26. 适应 bg/credential_recovery.go 读取 Redis 状态

Phase E: 7天保留实施
  27. 创建 pg_cron 或应用定时任务: 清理 state_change_log
  28. 为 model_probe_runs 添加 7天清理
  29. 为 candidate_failure_logs 添加 7天清理（已有列式存储，仍需清理）
```

### 4.3 目录结构变化

```
before:                              after:
  domains/credentialstate/             domains/ursm/
  credentialhealth/                      recorder.go    ← NEW (核心)
  credentialfpslot/                      state.go       ← 简化
    node_state.go                         cache.go       ← 简化/删除
  bg/                                    config.go
    active_probe_worker.go               keys.go
    passive_probe_listener.go            routing.go     ← 简化 IsAvailable
    unified_probe_scheduler.go           batch_writer.go ← 删除
  domains/streaming/executors/           tuner.go       ← 移入
    health_tracker.go                  api_update.go
    route_node_recorder.go             api_record.go   ← 简化
                                       api_probe.go
                                     _to-be-deprecated/
                                       credentialstate/
                                       credentialhealth/
                                       credentialfpslot/node_state.go
                                       bg/active_probe_worker.go
                                       bg/passive_probe_listener.go
                                       bg/unified_probe_scheduler.go
                                       executors/health_tracker.go
                                       executors/route_node_recorder.go
```

## 5. 可行性验证

### 5.1 路由路径验证

```
当前路由读路径 (executor.go → router.go):
  PlanCandidates()
    ├─ URSM.GetAvailableNodes() → 4 层缓存查询 → DB JOIN
    └─ filterAvailableWithStateManager() → credentialstate.IsAvailable()
         └─ memCache(10s) → Redis(5min) → DB(fallback)

优化后路由读路径:
  PlanCandidates()
    └─ ursm.StateRecorder.IsAvailable() → Redis HGETALL (单次)
         └─ miss → 默认可用 (fail-open)
```

**结论**: 路由读路径从 N+1 次查询（4层缓存+DB JOIN）简化为 1 次 Redis HGETALL。无数据丢失风险。

### 5.2 并发安全验证

当前系统问题:
- `credentialstate` 写 mem → redis → DB 不是原子的
- `credentialhealth.Recorder` 用 Redis Pipeline（LPUSH+LTRIM+EXPIRE）但读取用 LRange 有竞态

优化方案:
- 写入用 **Lua 脚本**：HGETALL → 判断 → HMSET + EXPIRE 一个脚本内原子完成
- 读取用 HGETALL：单次原子读取
- 不需要分布式锁：Redis 单线程执行 Lua，天然串行

**结论**: 并发安全优于当前方案。

### 5.3 状态转换准确性验证

| 场景 | 当前行为 | 优化后行为 | 一致性 |
|------|---------|-----------|--------|
| 1 次成功后续连 | credentialstate 设 Available=true, consecFail=0 | 同左，但原子 HMSET | ✅ |
| 2 次连续失败 | credentialstate 设 Available=false, cooling 5min | 同左，阈值可配 | ✅ |
| 1 次失败（非连续） | credentialstate 仅记录，不转换 | 同左 | ✅ |
| 冷却到期自动恢复 | credentialhealth.RecoverExpired 定时扫 DB | Lua 脚本在读取时判断 now ≥ disabled_until | ✅（更即时） |
| 用户取消请求 | credentialstate 过滤 KindCanceled | 同左 | ✅ |
| 客户端错误 | credentialstate 过滤 IsClientBug | 同左 | ✅ |
| 探测恢复 | UpdateFromProbe 直接设 Available=true | StateRecorder.Record(success=true) | ✅ |

**结论**: 状态转换逻辑完全一致，且由 Lua 保证原子性。

### 5.4 数据完整性验证

当前: 状态写入 N 个地方，部分丢失不影响路由（DB 有兜底）
优化后: 状态只在 Redis 存一份，DB 只记状态转换

风险: **Redis 数据如果丢失，路由会 fail-open（默认可用）**。

缓解:
1. Redis 7d TTL：即使长时间没有事件，状态也不会意外过期
2. Redis Sentinel/Cluster：高可用
3. Route miss 时 **fail-open**：返回 true 而不是拒绝请求
4. 探测系统 (model_probe_state) 仍在 DB 中保留共识状态，可作为恢复源

### 5.5 写入量对比

| 场景 | 当前写入次数 | 优化后写入次数 | 节省 |
|------|------------|--------------|------|
| 1 次请求成功 | HealthTracker(Redis)+StateObserver(3层)+URSM(3层)+RouteNodeRecorder(Redis Lua) = ~8 次写入 | StateRecorder(1 次 Lua HMSET) = **1 次** | 87.5% |
| 1 次请求失败 | 同成功 + credential.Writer(DB) + candidate_failure_logs(DB) = ~10 次写入 | StateRecorder(1 次 Lua) + FailureLogger(DB) = **2 次** | 80% |
| 探测完成 (每小时) | CredentialProbeV2(DB) + ModelProbeRunner(DB+Redis) = ~4 次 | StateRecorder(1 次) = **1 次** | 75% |
| 状态转换发生时 | 额外写入 credential_state_log + model_probe_runs | state_change_log (1 次 INSERT) = **1 次** | — |

## 6. 实施优先级

```
P0 (核心功能，不可缺失):
  □ ursm/recorder.go — StateRecorder 实现 (Lua 脚本 + Redis HASH)
  □ DB migration — state_change_log 表 + 索引
  □ executor.go — 替换 StateObserver/HealthTracker/URSM 调用为 StateRecorder
  □ router.go — 替换 IsAvailable 读取路径为 Redis HGETALL

P1 (功能完整，可滞后):
  □ bg/credential_probe_v2.go — writeHealth 切换到 StateRecorder
  □ bg/model_probe.go — applyResult 切换到 StateRecorder
  □ 废弃包移动到 _to-be-deprecated/
  □ DB 表清理 (credential_state_log, credential_state_nodes)

P2 (清理优化):
  □ bg/active_probe_worker.go — 简化/合并到 StateRecorder 触发
  □ bg/passive_probe_listener.go — 废弃
  □ credentialhealth.recorder/checker — 完全移除
  □ ursm.LayerCache 泛型 — 简化/删除
  □ credentialfpslot/node_state.go — 移除

P3 (数据保留):
  □ 7天保留定时任务 (state_change_log, model_probe_runs, candidate_failure_logs)
  □ 监控告警: Redis 节点状态数量、转换频率
```

## 7. 兼容性保障

```
Phase A（双写期）:
  StateRecorder.Record() 写入 Redis HASH
  + 旧 StateObserver.UpdateOnSuccess/OnFailure 继续写入 credentialstate
  + 校验: 对比两边的状态是否一致，不一致则告警

Phase B（只读新路径）:
  路由读 StateRecorder.IsAvailable()
  旧 credentialstate 只写不读
  bg 探测同时写两边

Phase C（下线旧路径）:
  确认无调用方后，移动旧代码到 _to-be-deprecated/
  一版本后删除
```
