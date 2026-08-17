# BUG修复：stale periodic cleanup 无视 quota_recover_at 造成 429→suspend→clear 死循环 (2026-08-08)

## 问题描述

08-07 已修复「周期性配额用尽未暂停服务」（writer 写 `periodic_exhausted`+`suspended`）。
但 08-08 线上复查发现：凭据命中 `quota_periodic` 被挂起后，**几秒内又被自动清回 `ok/ready`**，
然后路由重新选中该凭据 → 再次 429 → 再次 suspend → 再次清回，形成死循环，
导致 zhima-max (cred 35, provider 9271) 在 14:05-14:35 反复抖动，上游配额用尽但服务持续被打。

## 根本原因分析

### 死循环链路

```
1. writer (domains/credential/writer.go:160-175) 命中 KindQuotaPeriodic
   → 写 periodic_exhausted + suspended + quota_recover_at (=inferQuotaRecoverAt, 5h 窗口)

2. periodic_quota_probe (bg/periodic_quota_probe.go, 每 5min)
   → 用 default_probe_model (claude-fable-5) 探测该凭据
   → 探测成功 → 写 health_status='healthy', health_checked_at=now()

3. 60s ticker 的 stalePeriodicExhaustedCleanupSQL (bg/credential_recovery.go 原 329-354 行)
   WHERE quota_state='periodic_exhausted' AND health_status='healthy'
         AND health_checked_at > now()-2h
   → **无视 quota_recover_at** → 无条件清回 ok + ready
   → 路由重新选中 → 再 429 → 回到第 1 步
```

### 为什么 probe 成功是误导信号

`default_probe_model`（claude-fable-5）是低消耗探测模型，配额充足。
它的探测成功**只证明探测模型可用，不代表 gpt-5.6-sol 等业务模型已恢复 5h 配额窗口**。
因此 `health_status='healthy'` 不能作为 `periodic_exhausted` 应被清除的依据。

### 修复前 SQL（错误）

```sql
UPDATE credentials
SET quota_state = 'ok', quota_recover_at = NULL, state_reason_code = NULL,
    availability_state = 'ready', availability_recover_at = NULL,
    state_updated_at = now()
WHERE quota_state = 'periodic_exhausted'
  AND health_status = 'healthy'
  AND health_checked_at > now() - interval '2 hours'
  AND lifecycle_status = 'active';
```

## 修复方案

**文件**：`bg/credential_recovery.go` — `stalePeriodicExhaustedCleanupSQL()`

在 WHERE 增加 `quota_recover_at` 守卫，只有配额恢复时间已到时才允许清除：

```sql
AND (quota_recover_at IS NULL OR quota_recover_at <= now())
```

**语义变化**：
- writer 路径（KindQuotaPeriodic 写 `quota_recover_at`=5h 后）：5h 内保持 suspended，
  由标准 quota 恢复 SQL（`quota_recover_at <= now()`）到期翻回 `ok`。stale cleanup 不再提前清回。
- probe-v2 402 路径（无 `quota_recover_at`）：`IS NULL` 分支保留原兜底清理语义，不受影响。

## 验证结果

### 1. 单元测试（本地，全 PASS）

- `go build ./...` ✅
- `go test ./bg/ -run "TestStalePeriodic|TestRecoverOrdering|TestSuspendedRecovery"` ✅
- `go test ./bg/` 全绿 ✅
- 新增 `TestStalePeriodicExhaustedCleanupSQLGuards` 锁定新守卫字符串
  `"quota_recover_at IS NULL OR quota_recover_at <= now()"`

### 2. SQL 级验证（154 共享 DB，psql 事务内模拟，全部 ROLLBACK）

| 场景 | 结果 |
|------|------|
| 新守卫 + recover_at 未到期（5h 后） | UPDATE 0 行，保持 `periodic_exhausted + suspended` ✅ |
| 新守卫 + recover_at 已到期 | 清回 `ok + ready`（与标准 quota 恢复 SQL 协同）✅ |
| 旧行为（无守卫，对照组） | 无条件清回即使 recover_at 未到期 ✗（死循环根因）|

### 3. 端到端验证（154 生产，新版本 6cc4e235）

- 手动将 cred 35 置为 `periodic_exhausted + suspended`（recover_at=2h 后）
- **3 分钟内**（60s ticker 运行多次）cred 35 **保持 suspended，未被清回** ✅
- 新版本日志 stale cleanup 次数 = **0**（旧版本同窗口每 60s 清一次）✅
- 14:49:44 真实 `periodic_quota_probe` 提交探测（count=1 = cred 35），探针后仍保持 suspended ✅

## 部署记录

| 环境 | 版本 | 启动时间 | 状态 |
|------|------|---------|------|
| 245（预生产） | releases/1473-6cc4e235 | 08-08 14:39 | ✅ |
| 154（生产） | releases/1474-6cc4e235 | 08-08 14:40 | ✅ |

## 涉及文件

### 修改
1. `bg/credential_recovery.go` — `stalePeriodicExhaustedCleanupSQL()` 增加 quota_recover_at 守卫 + P0 注释
2. `bg/credential_recovery_test.go` — 新增守卫锁定测试

### 相关但未修改
1. `domains/credential/writer.go` — KindQuotaPeriodic 写 periodic_exhausted+suspended+quota_recover_at（08-07 已修）
2. `bg/periodic_quota_probe.go` — 5min 探针（用 default_probe_model，探针成功≠业务模型配额恢复）
3. `bg/balance_quota_probe.go` — 2min 余额探针（仅 balance/permanent，不受影响）

## 遗留与风险

- ⚠️ cred 35 目前 apigpt (cred 2) 是 gpt-5.6-sol 主力，zhima-max 为 fallback，
  仅当 apigpt 故障时才被选中；本次验证通过手动注入确认修复生效，但尚未等到
  「自然业务流量命中 cred 35 → 保持 suspended 5h」的完整闭环观测。
- 建议后续观察 1-2 天，确认无再次抖动；若有需要可临时对 zhima-max 设置路由降权。

---

**修复时间**：2026-08-08
**影响版本**：所有历史版本
**优先级**：P0（线上反复 429 抖动）
**类型**：逻辑缺陷修复（恢复条件守卫）
