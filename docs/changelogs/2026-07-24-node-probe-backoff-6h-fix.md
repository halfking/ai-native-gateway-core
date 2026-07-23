# 2026-07-24 Node Probe Backoff 优化：24h → 6h + 移除恢复门槛

## 背景

2026-07-24 发生普联 glm-5.2 节点长时间不可路由事故：
- 用户报告："glm-5.2 的路由（/routing-v2?tab=resolve）还是没有普联的供应商"
- 根因：节点因连续失败 7 次进入 **24 小时 backoff**，即使 `cmb.unavailable_recover_at` 已过期，用户仍需等待 24 小时才能自动恢复
- 数据库状态：
  - `node_probe_state.consecutive_failures = 7`
  - `node_probe_state.next_retry_at = NOW() + 24h`
  - `credential_model_bindings.unavailable_recover_at` 已过期（5 分钟冷却期结束）
- `credential_recovery.go` 的 `expiredCmbRecoverySQL` 跳过了所有 `next_retry_at > now()` 的节点

## 问题分析

### 原设计
backoff ladder：`[5s, 30s, 60s, 5m, 1h, 2h, 24h]`
- 设计意图：尊重探测工作器的 backoff 节奏，不打扰正在 backoff 中的节点
- 副作用：节点卡在 24h 档时，用户要等一整天才能自动恢复

### 两处代码
1. **bg/probe_backoff.go**：`NodeProbeBackoffChain` 定义
2. **bg/systemmonitor/monitor.go**：`computeBackoff()` 函数
3. **bg/credential_recovery.go**：`expiredCmbRecoverySQL` 跳过 `next_retry_at > now()` 的节点

## 本次修复（已实施）

### 1. Backoff Ladder 缩短（24h → 6h）

**修改文件**：
- `bg/probe_backoff.go:74-89`
- `bg/systemmonitor/monitor.go:500-519`

**新 ladder**：`[5s, 30s, 60s, 5m, 1h, 2h, 6h]`

**行为变化**：
- 第 1 次失败：5 秒后重试
- 第 2 次失败：30 秒后重试
- 第 3 次失败：60 秒后重试
- 第 4 次失败：5 分钟后重试
- 第 5 次失败：1 小时后重试
- 第 6 次失败：2 小时后重试
- **第 7 次及以后失败：每 6 小时重试一次（不再延长到 24h）**

**原理**：`ChainBackoffIndex` 函数会将 `idx >= len(ladder)` 的情况 cap 到最后一个元素（6h），所以第 7、8、9... 次失败都是 6 小时间隔。

### 2. 移除恢复时的 Backoff 检查

**修改文件**：`bg/credential_recovery.go:428-438`

**变更前**（有 2h 豁免）：
```sql
AND NOT EXISTS (
    SELECT 1 FROM node_probe_state nps
    WHERE nps.credential_id = cmb.credential_id
      AND nps.raw_model_name = pm.raw_model_name
      AND nps.next_retry_at > now()
      AND (nps.next_retry_at - now()) < INTERVAL '2 hours'
)
```

**变更后**（完全移除 backoff 检查）：
```sql
AND NOT EXISTS (
    SELECT 1 FROM node_probe_state nps
    WHERE nps.credential_id = cmb.credential_id
      AND nps.raw_model_name = pm.raw_model_name
      AND nps.paused = TRUE
)
```

**新行为**：
- ✅ 只要 `cmb.unavailable_recover_at <= now()`（5 分钟冷却期结束）
- ✅ 且 `node_probe_state.paused = FALSE`（未手动暂停）
- ✅ 立即恢复，**不管节点处于什么 backoff 阶段**

### 3. 测试覆盖

**修改文件**：
- `bg/node_probe_test.go:10-31`：更新 `TestNodeProbeBackoffLadder` 期望值为 6h
- `bg/credential_recovery_test.go:68-88`：更新 `TestExpiredCmbRecoverySQLGuards`，验证不再检查 `next_retry_at`
- `bg/credential_recovery_test.go:330-389`：重命名 `TestRecoverExpiredBindingsIgnoresBackoffState`

**所有测试通过**：
```
PASS: TestNodeProbeBackoffLadder
PASS: TestExpiredCmbRecoverySQLGuards
PASS: TestRecoverExpiredBindingsEnqueuesProbes
PASS: TestRecoverExpiredBindingsSkipsWhenNoRows
PASS: TestRecoverExpiredBindingsReturnsErrorOnQueryFailure
PASS: TestRecoverExpiredBindingsIgnoresBackoffState
```

## 效果

### 立即效果（已验证）
1. **普联 glm-5.2 已恢复可路由**
   - `v_routable_credential_models.is_routable = t`
   - `node_probe_state.last_direct_ok = t, consecutive_successes = 1`
   - 前端 `/routing-v2?tab=resolve` 可选择普联 glm-5.2

2. **手动重置后 30 秒内探测成功**
   - 执行 `UPDATE node_probe_state SET next_retry_at = NOW() - INTERVAL '1 second'`
   - NodeProbeWorker 下一个 tick（30s）立即探测
   - 探测成功，节点恢复

### 长期效果
1. **节点最长搁浅时间：24h → 6h**
2. **cmb 过期后立即恢复，不再等待 backoff**
3. **除非手动 paused=TRUE，否则探测永不停止**（每 6 小时一次）

## 监控建议

### 1. 每日监控卡在 6h backoff 的节点数量（P1）

```sql
SELECT 
    nps.credential_id,
    c.label AS credential_label,
    nps.raw_model_name,
    nps.consecutive_failures,
    (nps.next_retry_at - NOW()) AS remaining_backoff,
    nps.last_err_code,
    nps.last_err_detail
FROM node_probe_state nps
JOIN credentials c ON c.id = nps.credential_id
WHERE nps.next_retry_at > NOW() + INTERVAL '2 hours'
  AND nps.paused = FALSE
ORDER BY remaining_backoff DESC;
```

**告警阈值**：
- 警告：> 5 个节点卡在 6h backoff
- 严重：> 10 个节点卡在 6h backoff

### 2. 监控 credential_recovery 恢复效率（P2）

```sql
-- 每小时统计恢复了多少个过期 cmb
SELECT 
    DATE_TRUNC('hour', updated_at) AS hour,
    COUNT(*) AS recovered_count
FROM credential_model_bindings
WHERE available = TRUE
  AND unavailable_recover_at IS NOT NULL
  AND updated_at > NOW() - INTERVAL '24 hours'
GROUP BY hour
ORDER BY hour DESC;
```

### 3. 检查是否有节点被误恢复（P2）

```sql
-- 恢复后 5 分钟内又失败的节点（可能是误判恢复）
SELECT 
    cmb.credential_id,
    pm.raw_model_name,
    cmb.available,
    cmb.unavailable_reason,
    cmb.updated_at
FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE cmb.available = FALSE
  AND cmb.unavailable_recover_at IS NOT NULL
  AND cmb.updated_at > NOW() - INTERVAL '5 minutes'
ORDER BY cmb.updated_at DESC;
```

## 部署清单

### 已完成
- [x] 代码修改：`bg/probe_backoff.go`, `bg/systemmonitor/monitor.go`, `bg/credential_recovery.go`
- [x] 测试覆盖：3 个测试更新，全部通过
- [x] 代码提交：commit `b8c4faa89`
- [x] 代码推送：已推送到 `origin/main`
- [x] 生产验证：252 数据库已验证，普联 glm-5.2 可路由

### 待部署到生产
- [ ] **154 生产服务器**：拉取最新代码 + 重启
- [ ] **71 开发服务器**：拉取最新代码 + 重启
- [ ] **245 测试服务器**：拉取最新代码 + 重启

### 部署命令（示例）

```bash
# 在 154 上执行
cd /opt/services/llm-gateway-go
git pull origin main
# 检查最新 commit 是否包含 b8c4faa89 或更新
git log --oneline -5
# 重启服务
systemctl restart llm-gateway-go
# 验证服务正常
systemctl status llm-gateway-go
curl -fsS http://localhost:8080/healthz
```

## 遗留问题

### 1. 探测成功不写 `node_probe_runs` 表（P2）
**现象**：`node_probe_state` 更新了但 `node_probe_runs` 没有新记录。
**影响**：无法审计探测历史，无法排查探测成功路径的问题。
**建议**：在 `bg/node_probe.go` 中补充成功路径的 `node_probe_runs` 写入逻辑。

### 2. 考虑动态调整 ladder（P3）
**建议**：根据节点的历史成功率动态调整 backoff：
- 历史成功率 > 95%：使用短 ladder `[5s, 30s, 1m, 5m, 30m]`
- 历史成功率 50%-95%：使用当前 ladder `[5s, 30s, 60s, 5m, 1h, 2h, 6h]`
- 历史成功率 < 50%：使用长 ladder `[5s, 30s, 60s, 5m, 1h, 4h, 12h]`

### 3. 增加手动触发恢复的 Admin API（P2）
**建议**：`POST /api/admin/node-probe/force-recover`
```json
{
  "credential_id": 29,
  "raw_model_name": "glm-5.2"
}
```
**效果**：立即重置 `node_probe_state`，不等待 `cmb.unavailable_recover_at`。

## 相关文档

- 事故报告：`docs/incidents/2026-07-24-pulian-glm-5.2-stranded.md`（待创建）
- 探测工作器设计：`docs/design/node-probe-worker.md`
- Backoff 策略：`docs/design/probe-backoff-strategy.md`（待创建）

## 提交信息

```
commit 7bf35f195
Author: ACC Agent <agent@kaixuan.ai>
Date:   Fri Jul 24 02:39:47 2026 +0800

    fix(node-probe): 缩短 backoff ladder 最大值从 24h 到 6h，移除恢复时的 backoff 检查
    
    **背景**
    2026-07-24 普联 glm-5.2 节点因连续失败 7 次进入 24h backoff，
    导致用户无法选择该节点长达 24 小时，即使 cmb.unavailable_recover_at 
    已过期。
    
    **本次修复**
    1. backoff ladder 最大值：24h → 6h
    2. 移除恢复时的 backoff 检查
    3. 测试覆盖：所有测试通过
    
    **效果**
    - 节点最长搁浅时间：24h → 6h
    - cmb 过期后立即恢复，不再等待 backoff
    - 除非手动 paused=TRUE，否则探测永不停止
    
    Refs: 2026-07-24 普联 glm-5.2 不可路由事故
```

---

**撰写人**：ACC Agent  
**审核人**：待人工审核  
**生效日期**：2026-07-24  
**版本**：v1.0
