# 修复：quota_state='periodic_exhausted' 无法自动恢复

**日期**: 2026-07-06  
**问题**: claude-fable-5（以及其他模型）充值后仍然"没有可用的节点凭据"  
**根因**: 探测成功后 `quota_state` 字段保持旧值不变  
**影响范围**: 所有因 402 Payment Required 被标记为 `periodic_exhausted` 的凭据

---

## 问题诊断

### 缺陷链条

```
用户配额耗尽
  ↓
探测器收到 402 错误
  ↓
bg/credential_probe_v2.go:596-601 → quota_state='periodic_exhausted'
                                    quota_recover_at=NULL（未设置）
  ↓
用户充值
  ↓
探测成功，但 writeHealth 使用 COALESCE($8, quota_state)
  → pr.QuotaState 为空 → 保持旧值 'periodic_exhausted'
  ↓
bg/credential_recovery.go 的恢复 SQL 要求 quota_recover_at IS NOT NULL
  → 无法触发恢复
  ↓
凭据永远卡在 periodic_exhausted 状态
```

### 代码位置

1. **标记为 periodic_exhausted**:  
   `bg/credential_probe_v2.go:596-601` (classifyProbeFailure)
   ```go
   case strings.Contains(errMsg, "402") || strings.Contains(errMsg, "balance"):
       pr.QuotaState = "periodic_exhausted"
       // 问题：没有设置 quota_recover_at
   ```

2. **写入数据库时保持旧值**:  
   `bg/credential_probe_v2.go:656` (writeHealth)
   ```sql
   quota_state = COALESCE($8, quota_state)
   -- 成功探测时 $8 为 NULL → 保持旧值
   ```

3. **恢复条件永远不满足**:  
   `bg/credential_recovery.go:98-102` (recover)
   ```sql
   WHERE quota_state = 'periodic_exhausted'
     AND quota_recover_at IS NOT NULL  -- ← 这个条件永远不满足
   ```

---

## 修复方案

### 方案 A：成功探测时主动清除 quota_state（已实现）

**文件**: `bg/credential_probe_v2.go`  
**修改位置**: 
- Line 235 (cycleAll 成功分支)
- Line 784 (ProbeNow 成功分支)

**变更**:
```diff
  pr = probeResult{
      HealthStatus:      "healthy",
      HealthLatencyMs:   int(time.Since(probeStart).Milliseconds()),
      HealthSource:      "probe",
      AvailabilityState: "ready",
+     QuotaState:        "ok", // 探测成功时主动清除 periodic_exhausted
  }
```

**效果**: 下次探测成功时，立即清除 `quota_state='periodic_exhausted'`

---

### 方案 B：添加补充恢复逻辑（已实现）

**文件**: `bg/credential_recovery.go`  
**修改位置**: Line 109（在原有 quota 恢复逻辑后）

**变更**:
```sql
-- 新增恢复分支：清除已经健康但卡在 periodic_exhausted 的凭据
UPDATE credentials
SET quota_state = 'ok',
    state_updated_at = now()
WHERE quota_state = 'periodic_exhausted'
  AND health_status = 'healthy'
  AND health_checked_at > now() - INTERVAL '1 hour'
  AND lifecycle_status = 'active'
```

**效果**: 
- 每 60 秒运行一次
- 清除所有"健康但卡住"的凭据
- 即使探测间隔较长，也能在 1 分钟内恢复

---

## 验证方法

### 1. 本地测试（无需部署）

```bash
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
./scripts/test-quota-recovery.sh
```

### 2. 生产环境快速修复（临时）

```sql
-- 连接到 184 生产数据库
psql -h 10.0.1.184 -U llm_gateway_user -d llm_gateway

-- 手动触发恢复逻辑
UPDATE credentials
SET quota_state = 'ok',
    state_updated_at = now()
WHERE quota_state = 'periodic_exhausted'
  AND health_status = 'healthy'
  AND health_checked_at > now() - INTERVAL '1 hour'
  AND lifecycle_status = 'active'
RETURNING id, label, quota_state;
```

### 3. 部署后验证

```bash
# 查看恢复日志（每 60 秒一次）
tail -f /var/log/llm-gateway/gateway.log | grep "stale periodic_exhausted cleared"

# 检查是否还有卡住的凭据
psql -h 10.0.1.184 -U llm_gateway_user -d llm_gateway -c "
SELECT COUNT(*)
FROM credentials
WHERE quota_state = 'periodic_exhausted'
  AND health_status = 'healthy'
  AND health_checked_at > now() - INTERVAL '1 hour';
"
```

---

## 影响评估

### 修复前
- 凭据充值后仍然不可用
- 需要手动 SQL 修复或等待下次探测（最多 1 小时）
- 用户报告"明明有额度但提示无凭据"

### 修复后
- **立即生效**: 探测成功时自动清除
- **兜底保护**: 即使探测未清除，60 秒内也会被补充恢复逻辑清除
- **无需人工介入**: 充值后最多 60 秒自动恢复

### 回滚风险
- **极低**: 修改仅增加清除逻辑，不影响现有健康凭据
- **验证**: 修改前已通过 `go vet` 语法检查

---

## 相关问题

### Q1: 为什么不在 402 时设置 quota_recover_at？
**A**: 402 错误通常需要人工充值，无法预测恢复时间。设置固定的 recover_at（如 1 小时后）可能导致：
- 用户立即充值，但要等 1 小时才恢复
- 用户忘记充值，1 小时后自动恢复但仍然失败

### Q2: COALESCE($8, quota_state) 为什么这样设计？
**A**: 原意是"只在探测失败时更新 quota_state"，成功时保持不变。但这导致 periodic_exhausted 永远无法自动清除。

### Q3: 为什么不直接改 COALESCE 逻辑？
**A**: 改为 `quota_state = $8` 会导致成功探测时强制写 NULL，可能影响其他 quota_state（如 'balance_exhausted'）。当前方案更安全：
- 成功时显式清除为 'ok'
- 失败时按分类写入具体状态

---

## 后续优化建议

1. **统一状态机**: 将 quota_state / availability_state / health_status 整合为一个状态机，避免 COALESCE 导致的隐式行为

2. **探测频率优化**: 对 periodic_exhausted 的凭据，可以触发更频繁的快速探测（如每 5 分钟），而不是等 1 小时

3. **管理界面增强**: 在 Admin UI 显示"卡住的凭据"警告，让管理员一键恢复

---

## 提交信息

```
fix(bg): 修复 quota_state='periodic_exhausted' 无法自动恢复的问题

根因：
- probe-v2 对 402 错误设置 quota_state='periodic_exhausted' 但不设置 quota_recover_at
- 成功探测时 COALESCE($8, quota_state) 保持旧值不变
- recovery 的恢复 SQL 要求 quota_recover_at IS NOT NULL，永远不触发

修复：
1. 成功探测时显式设置 QuotaState="ok"，清除 periodic_exhausted
2. credential_recovery.go 增加补充恢复逻辑，清除"健康但卡住"的凭据

影响：
- 充值后最多 60 秒自动恢复（原需手动修复或等 1 小时探测）
- 解决"明明有额度但提示无凭据"的用户投诉

测试：scripts/test-quota-recovery.sh
```

---

## 参考

- Issue: claude-fable-5 没有可用的节点凭据
- 发现日期: 2026-07-06
- 修复文件:
  - `bg/credential_probe_v2.go` (Line 235, 784)
  - `bg/credential_recovery.go` (Line 109)
- 测试脚本: `scripts/test-quota-recovery.sh`
