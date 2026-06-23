# 并发槽与身份池配对审计（2026-06-23）

## 🔍 审计发现

### 问题 1: identityPool（Layer 0）未集成到 executor

**状态**: 🔴 严重

**现象**: `identitypool` 包已实现，但 `routing/executor.go` 中**没有任何调用**。

**影响**: 全局身份池功能完全不生效，用户的核心需求"超过量的用户可以复用之前的用户的指纹"无法实现。

**修复**:
1. 在 `routing/executor.go` 中的 `Executor` 结构体添加 `IdentityPool` 字段
2. 在处理每个请求前调用 `IdentityPool.Acquire(ctx, identity)`
3. 无需显式 Release（LRU TTL 自动过期）

---

### 问题 2: 硬编码的超时和 TTL 常量

**状态**: 🟡 中等

**现象**: 多处硬编码的超时和 TTL：
- `credentialfpslot/slot.go:29`: `slotTTLSeconds = 86400` (24h)
- `credentialfpslot/slot.go:34`: `sessionPinTTLSeconds = 86400` (24h)
- `identitypool/pool.go:48`: `LRUWindow = 24 * time.Hour`
- `limiter/limiter.go`: 各种超时常量

**影响**: 无法动态调整，需要重新编译+重启才能修改。

**修复**: 所有常量改为从 settings 表读取，支持热更新。

---

### 问题 3: FpSlots.Acquire/Release 配对检查

**状态**: ✅ 正常

**配对情况**:
```go
// routing/executor.go:626
fpLease, ok := e.FpSlots.Acquire(...)

// Path 1: Circuit open → line 645
if !e.Circuit.Allow(...) {
    e.FpSlots.Release(params.R.Context(), fpLease)
    continue
}

// Path 2: Limiter saturated → line 664
release, acquireErr := e.Limiter.AcquireAll(...)
if acquireErr != nil {
    e.FpSlots.Release(params.R.Context(), fpLease)
    continue
}

// Path 3: Execute success/failure → line 689 (defer)
defer func() {
    release()
    if fpLease != nil {
        e.FpSlots.Release(params.R.Context(), fpLease)
    }
}()
```

**结论**: 所有路径都正确配对。

---

### 问题 4: Limiter.AcquireAll/Release 配对检查

**状态**: ✅ 正常

**配对情况**:
```go
// routing/executor.go:650
release, acquireErr := e.Limiter.AcquireAll(...)

// line 687: defer 包裹
defer func() {
    release()  // ← 函数闭包，无论如何都会执行
    ...
}()
```

**结论**: 通过 defer 确保释放，即使 execute 提前返回也会调用。

---

## 📋 待修复项

| # | 问题 | 优先级 | 状态 |
|---|------|--------|------|
| 1 | 集成 identityPool 到 executor | 🔴 P0 | 待修复 |
| 2 | 将所有 TTL/超时常量改为可配置 | 🟡 P1 | 待修复 |
| 3 | 实现热更新机制（settings 表监听） | 🟡 P1 | 待修复 |
| 4 | 添加配置 UI（/admin/settings） | 🟡 P1 | 待修复 |

---

## 🎯 需要配置化的参数

### credentialfpslot 包
- `slot_ttl_seconds` (default: 86400) — 指纹槽 TTL
- `session_pin_ttl_seconds` (default: 86400) — 会话 pin TTL
- `default_fp_slot_limit` (default: 5) — 默认指纹池大小

### identitypool 包
- `max_identities` (default: 10000) — 全局身份上限
- `lru_window_seconds` (default: 86400) — LRU 回收窗口

### limiter 包
- `default_concurrency_limit` (default: 10) — 默认并发限制
- `global_limit` — 全局限流
- `pool_limit` — 池级别限流

### 其他
- `circuit_breaker_threshold` — 熔断阈值
- `circuit_breaker_timeout` — 熔断超时

---

## 🔄 热更新机制设计

1. **Settings 表结构** (已存在):
   ```sql
   CREATE TABLE settings_kv (
       key TEXT PRIMARY KEY,
       value JSONB NOT NULL,
       updated_at TIMESTAMPTZ DEFAULT now()
   );
   ```

2. **配置读取流程**:
   - 启动时：从 settings_kv 加载到内存
   - 运行时：每 30 秒 poll 一次 settings_kv
   - 变更时：原子替换内存中的配置

3. **配置作用域**:
   - Platform-level: 所有租户共享
   - Tenant-level: 租户覆盖（通过 tenant_settings_kv）

---

## 📝 下一步行动

1. ✅ 审计 Acquire/Release 配对
2. 🔲 集成 identityPool 到 executor
3. 🔲 实现配置热加载器
4. 🔲 迁移所有硬编码常量到 settings
5. 🔲 前端 UI 支持
6. 🔲 编写测试
7. 🔲 提交推送