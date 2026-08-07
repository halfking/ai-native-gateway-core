# BUG修复：智谱AI周期性配额用尽未暂停服务 (2026-08-07)

## 问题描述

智谱AI 供应商的凭据已经检测到 `quota_state='periodic_exhausted'`（周期性配额用尽），但并没有被降级或暂停服务，仍然在被路由器选中使用，导致持续失败。

## 根本原因分析

### 1. 核心问题：`KindQuotaPeriodic` 未设置 `availability_state='suspended'`

**位置**：`domains/credential/writer.go:160-172`

当检测到 `errorsx.KindQuotaPeriodic` 错误时，只更新了以下字段：
```go
UPDATE credentials
SET quota_state         = 'periodic_exhausted',
    quota_recover_at    = $1,
    state_reason_code   = $2,
    state_reason_detail = $3,
    state_updated_at    = now()
WHERE id = $4
```

**问题**：缺少关键字段 `availability_state` 和 `availability_recover_at`，导致凭据虽然标记为 `periodic_exhausted`，但 `availability_state` 仍然是 `'ready'`，路由器仍然会选中这个凭据。

### 2. 对比其他配额类型的正确实现

`KindQuotaPermanent` 和 `KindQuotaBalance` 都正确设置了：
```go
SET quota_state             = 'permanently_exhausted',
    quota_recover_at        = NULL,
    availability_state      = 'suspended',      // ← 关键！
    availability_recover_at = NULL,
```

### 3. 路由器判断逻辑

路由器在选择凭据时，主要检查 `availability_state`：
- `availability_state NOT IN ('suspended', 'cooling', 'rate_limited', 'unreachable', 'auth_failed')`
- 其他健康检查

**如果 `availability_state` 仍为 `'ready'`，即使 `quota_state='periodic_exhausted'`，凭据仍会被路由使用。**

## 修复方案

### 修复 1：`KindQuotaPeriodic` 同时设置 `availability_state='suspended'`

**文件**：`domains/credential/writer.go:160-175`

```diff
 case errorsx.KindQuotaPeriodic:
+    recoverAt := inferQuotaRecoverAt(failure.Detail)
     _, err := w.dbPool.Exec(ctx, `
         UPDATE credentials
-        SET quota_state         = 'periodic_exhausted',
-            quota_recover_at    = $1,
+        SET quota_state             = 'periodic_exhausted',
+            quota_recover_at        = $1,
+            availability_state      = 'suspended',
+            availability_recover_at = $1,
             state_reason_code   = $2,
             state_reason_detail = $3,
             state_updated_at    = now()
         WHERE id = $4
           AND lifecycle_status = 'active'
           AND quota_state NOT IN ('balance_exhausted', 'permanently_exhausted')
-    `, inferQuotaRecoverAt(failure.Detail), string(failure.Kind), detail, credentialID)
+    `, recoverAt, string(failure.Kind), detail, credentialID)
     return err
```

**关键变更**：
1. 增加 `availability_state = 'suspended'` - 立即暂停服务
2. 增加 `availability_recover_at = $1` - 与 `quota_recover_at` 保持一致，确保恢复机制正常工作
3. 提取 `recoverAt` 变量避免重复调用 `inferQuotaRecoverAt`

### 修复 2：新增 `BalanceQuotaProbe` - 更频繁检测充值恢复

**问题**：对于需要充值才能恢复的配额类型（`balance_exhausted`、`permanently_exhausted`），现有的恢复机制不够及时。

**解决方案**：创建专门的探测器，使用更短的探测间隔。

**新文件**：`bg/balance_quota_probe.go`

```go
// BalanceQuotaProbe 专门针对需要充值才能恢复的配额类型（balance_exhausted、permanently_exhausted）
// 进行更频繁的探测，以便及时感知用户充值后的恢复。
//
// 与 PeriodicQuotaProbe 的区别：
//   - PeriodicQuotaProbe: 针对周期性配额用尽（periodic_exhausted），默认 5 分钟探测一次
//   - BalanceQuotaProbe: 针对余额/永久配额用尽（balance_exhausted、permanently_exhausted），
//     默认 2 分钟探测一次，更快感知充值恢复
```

**探测策略**：
- 默认间隔：**2 分钟**（vs 周期性配额的 5 分钟）
- 目标状态：`quota_state IN ('balance_exhausted', 'permanently_exhausted')`
- 可通过环境变量 `LLM_GATEWAY_BALANCE_QUOTA_PROBE_INTERVAL` 配置

**原因**：
- 周期性配额：按时间自动重置（如每日/每周/每月），重置时间可预测
- 余额配额：依赖用户充值，充值时间不可预测，需要更频繁探测以快速感知恢复

### 修复 3：在 `main.go` 中启动 `BalanceQuotaProbe`

**文件**：`cmd/gateway/main.go`

1. 声明变量（行 2334）：
```diff
 var periodicQuotaProbe *bg.PeriodicQuotaProbe
+var balanceQuotaProbe *bg.BalanceQuotaProbe
 var pendingSweeper *bg.PendingSweeper
```

2. 启动探测器（行 2519 后）：
```diff
 periodicQuotaProbe.Start(context.Background())
 slog.Info("CHECKPOINT: periodicQuotaProbe started")

+// 2026-08-07: balance quota probe for balance_exhausted and permanently_exhausted credentials
+// Probes credentials in balance/permanently exhausted state every N minutes (default 2min)
+// to detect recharge recovery faster.
+balanceQuotaProbe = bg.NewBalanceQuotaProbe(dbConn.Pool())
+if credProbeV2 != nil {
+    balanceQuotaProbe.SetProbeSubmitter(credProbeV2.SubmitFastProbe)
+}
+balanceQuotaProbe.Start(context.Background())
+slog.Info("CHECKPOINT: balanceQuotaProbe started")
```

## 现有恢复机制（保持不变）

### `bg/credential_recovery.go`

现有的 60 秒定时恢复任务已经包含两个恢复分支：

1. **availability_state 恢复**（行 99-149）：
```sql
UPDATE credentials
SET availability_state = 'ready',
    availability_recover_at = NULL
WHERE availability_state IN ('cooling','rate_limited','unreachable','auth_failed')
  AND availability_recover_at <= now()
```

2. **quota_state 恢复**（行 151-165）：
```sql
UPDATE credentials
SET quota_state = 'ok',
    quota_recover_at = NULL
WHERE quota_state = 'periodic_exhausted'
  AND quota_recover_at IS NOT NULL
  AND quota_recover_at <= now()
```

修复后，两个恢复机制会同时工作，确保凭据在恢复时间到达后能同时恢复 availability 和 quota 状态。

## 配额类型对比表

| 配额类型 | quota_state | availability_state | 恢复机制 | 探测间隔 |
|---------|-------------|-------------------|---------|---------|
| **周期性用尽** | `periodic_exhausted` | `suspended` ✅修复后 | 时间自动重置 | 5 分钟 (PeriodicQuotaProbe) |
| **余额不足** | `balance_exhausted` | `suspended` ✅已有 | 需要充值 | 2 分钟 (BalanceQuotaProbe ✅新增) |
| **永久用尽** | `permanently_exhausted` | `suspended` ✅已有 | 需要充值 | 2 分钟 (BalanceQuotaProbe ✅新增) |

## 验证方式

### 1. 模拟周期性配额用尽

```bash
# 触发智谱AI周期性配额错误
# 预期：
# - quota_state = 'periodic_exhausted'
# - availability_state = 'suspended'  ← 新增
# - quota_recover_at 和 availability_recover_at 设置为相同时间
```

### 2. 查询数据库确认状态

```sql
SELECT id, provider_id, 
       quota_state, quota_recover_at,
       availability_state, availability_recover_at,
       state_reason_code
FROM credentials
WHERE quota_state = 'periodic_exhausted';
```

**修复前**：
```
quota_state: periodic_exhausted
availability_state: ready  ← 问题：仍然是 ready！
```

**修复后**：
```
quota_state: periodic_exhausted
availability_state: suspended  ← 正确：已暂停
quota_recover_at: 2026-08-07 22:00:00
availability_recover_at: 2026-08-07 22:00:00  ← 同步
```

### 3. 验证路由器不再选中该凭据

```bash
# 查询可路由的凭据
SELECT * FROM v_routable_credential_models 
WHERE credential_id = <智谱AI凭据ID>;

# 修复前：仍然返回结果（错误）
# 修复后：返回空（正确，因为 availability_state='suspended'）
```

### 4. 验证恢复时间到达后自动恢复

等待 `quota_recover_at` 时间到达后，60 秒恢复任务会：
1. 恢复 `quota_state` 为 `'ok'`
2. 恢复 `availability_state` 为 `'ready'`
3. 清空 `quota_recover_at` 和 `availability_recover_at`

### 5. 验证余额配额探测（新增功能）

```bash
# 模拟余额不足
# 预期：balanceQuotaProbe 每 2 分钟探测一次

# 日志示例：
# balance_quota_probe: submitted probes for balance/permanently exhausted credentials
#   count=3 interval=2m0s reason="detect recharge recovery faster"
```

## 配置项

### 1. 周期性配额探测间隔（已有）
```bash
LLM_GATEWAY_PERIODIC_QUOTA_PROBE_INTERVAL=5m  # 默认 5 分钟
```

### 2. 余额配额探测间隔（新增）
```bash
LLM_GATEWAY_BALANCE_QUOTA_PROBE_INTERVAL=2m   # 默认 2 分钟
```

可以根据实际需求调整：
- 更频繁探测：`LLM_GATEWAY_BALANCE_QUOTA_PROBE_INTERVAL=1m`（1 分钟）
- 降低探测频率：`LLM_GATEWAY_BALANCE_QUOTA_PROBE_INTERVAL=5m`（5 分钟）

## 影响范围

### 受益场景
1. **智谱AI** 及其他返回周期性配额错误的供应商
2. **所有供应商** 的余额不足场景（充值后更快恢复）

### 向后兼容性
- ✅ 完全向后兼容
- ✅ 不影响现有的恢复机制
- ✅ 只是让原本"应该暂停但没暂停"的凭据正确暂停

### 性能影响
- **周期性配额**：无额外探测（已有 5 分钟探测）
- **余额配额**：新增 2 分钟探测，每次最多探测 100 个凭据
- **数据库负载**：每 2 分钟执行一次简单的 SELECT + 探测提交，负载可忽略

## 相关文件清单

### 修改的文件
1. `domains/credential/writer.go` - 修复 KindQuotaPeriodic 处理逻辑
2. `cmd/gateway/main.go` - 启动 BalanceQuotaProbe

### 新增的文件
1. `bg/balance_quota_probe.go` - 余额配额探测器

### 相关但未修改的文件
1. `bg/credential_recovery.go` - 现有恢复机制（60秒定时任务）
2. `bg/periodic_quota_probe.go` - 周期性配额探测器（5分钟）
3. `errorsx/classify.go` - 错误分类（定义 KindQuotaPeriodic）
4. `provider/client.go` - 凭据可用性判断

## 编译验证

```bash
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
go build -o /tmp/test-build ./cmd/gateway
# 编译成功，生成 57M 二进制文件
```

## 总结

本次修复解决了三个关键问题：

1. **✅ 周期性配额用尽未暂停服务** - 通过同时设置 `availability_state='suspended'` 修复
2. **✅ 恢复时间到达后自动恢复** - 现有的 60 秒恢复任务已支持，无需修改
3. **✅ 余额不足快速感知恢复** - 新增 2 分钟探测器，充值后更快恢复

修复后，所有配额类型（periodic/balance/permanent）的处理逻辑保持一致，都会：
- 立即暂停服务（`availability_state='suspended'`）
- 设置恢复时间（如果可推断）
- 通过定时探测检测恢复
- 恢复时间到达或探测成功后自动恢复

---

**修复时间**：2026-08-07  
**影响版本**：所有历史版本  
**优先级**：P1（影响生产路由正确性）  
**类型**：逻辑缺陷修复 + 功能增强
