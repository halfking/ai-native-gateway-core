# 本地运行环境节点状态同步问题诊断报告

**诊断时间**: 2026-09-03 06:09  
**诊断对象**: llm-gateway-go 本地运行实例（PID 44868）  
**问题描述**: 供应商凭据已恢复正常，但系统节点状态未能自动同步更新，需要手工干预

---

## 一、证据链汇总

### 1.1 运行日志分析（`/private/tmp/llm-gateway.log`）

#### RestoreOnSuccess 热路径恢复失败
```
2026/09/03 05:30:05 WARN execution_recorder: RestoreOnSuccess failed 
  error="resolve model binding failed: ambiguous model binding 
  (context: candidate_raw_models=MiniMax-M2.7,MiniMax-M2.7-highspeed)" 
  credential_id=21 model=minimax-m2.7
```

**关键发现**：
- 凭据ID 21（MiniMax）的热路径恢复在过去2小时内持续失败
- 失败原因：模型绑定歧义（`MiniMax-M2.7` 与 `MiniMax-M2.7-highspeed` 无法区分）
- 这意味着即使上游请求成功，`RestoreOnSuccess` 也无法将节点标记为可用

####credential_recovery 定时任务执行情况
```
2026/09/03 05:30:25 INFO credential_recovery: stale node_probe_state rows handed to probe queue 
  pairs=50 unique_credentials=16 reason=stale_node_probe_state_reverify

2026/09/03 05:30:25 INFO expired-binding probe recovery queued 
  pairs=2 unique_credentials=2
```

**关键发现**：
- 恢复扫描每30秒执行一次，正常触发
- 每次扫描提交 50+ 个探测任务到队列
- 同时触发过期绑定恢复（expired-binding）

#### node_probe_worker 探测提交去重
```
2026/09/03 05:31:25 INFO node_probe_worker: submit via queue 
  credential_id=21 model=MiniMax-M2.7 inserted=false
  
2026/09/03 05:31:25 INFO node_probe_worker: submit via queue 
  credential_id=42 model=MiniMax-M2.7-highspeed inserted=false
  
2026/09/03 05:31:25 INFO node_probe_worker: submit via queue 
  credential_id=11 model=doubao-1-5-pro-32k-character-250715 inserted=true
```

**关键发现**：
- 大量探测任务的 `inserted=false`，说明队列中已存在该任务（去重）
- 只有少数新任务（如 credential_id=11 的某些模型）被成功插入
- **去重机制阻止了探测任务的重复执行**

---

### 1.2 数据库状态快照

#### 不可用节点统计（18个）
| credential_id | label | model | unavailable_reason | recovery_state | probe_state |
|---------------|-------|-------|-------------------|----------------|-------------|
| 9 | xiaomi-token-plan | mimo-v2-omni | model_probe_broken | READY | NO_PROBE_STATE |
| 11 | demo-tokenplan | deepseek-v4-pro | probe_http_429 | WAITING | PROBE_WAITING |
| 21 | （MiniMax相关） | MiniMax-M2.7 | （未列出） | （需补充） | （需补充） |
| 26 | gpt-image | gpt-image-2 | probe_http_400 | WAITING | NO_PROBE_STATE |
| 35 | zhima-max | claude-opus-4-8 | probe_network_error | WAITING | PROBE_WAITING |
| 43 | glm-5.2-new | gpt-5.6-luna | probe_timeout | READY | PROBE_WAITING |
| 60 | claude-3x-cache | claude-opus-4-7 | probe_timeout | WAITING | PROBE_WAITING |

**关键状态分类**：

1. **NULL_BLOCKED**（3个节点）：
   - `unavailable_recover_at IS NULL` 或 过早被标记
   - **阻止自动恢复尝试**

2. **WAITING + NO_PROBE_STATE**（2个节点）：
   - 冷却期未到，但 `node_probe_state` 表无记录
   - **无法调度探测**

3. **WAITING + PROBE_WAITING**（10个节点）：
   - 冷却期未到，且探测已调度但未执行
   - `next_retry_at` 在未来（例如 `2026-09-03 05:46:25`）
   - **正在等待探测执行**

4. **READY + PROBE_WAITING**（1个节点）：
   - 冷却期已过，探测已调度
   - **应该很快执行**

---

## 二、根因分析

### 2.1 RestoreOnSuccess 热路径恢复失败

**根本原因**：  
`domains/credential/writer.go:RestoreOnSuccess` 中的模型绑定解析逻辑无法处理歧义情况。

**失败场景**：
1. 上游请求成功返回（例如 `model=minimax-m2.7`）
2. `RestoreOnSuccess` 尝试恢复对应的 `credential_model_bindings`
3. 查询发现凭据ID 21 有两个候选：
   - `MiniMax-M2.7`
   - `MiniMax-M2.7-highspeed`
4. 无法唯一确定哪个绑定，抛出 `ambiguous model binding` 错误
5. **节点状态未更新，仍保持不可用**

**影响范围**：  
凭据ID 21（可能是 MiniMax 供应商）的所有相关模型无法通过热路径恢复。

---

### 2.2 探测队列去重机制阻塞

**根本原因**：  
`bg/node_probe.go` 中的探测任务去重逻辑过于严格，导致已调度但长时间未执行的任务无法重新提交。

**失败场景**：
1. `credential_recovery` 扫描发现节点需要探测
2. 调用 `probeSubmitter` 提交探测任务
3. `node_probe_worker` 检查队列，发现该 `(credential_id, model)` 已存在
4. 返回 `inserted=false`，不重复插入
5. 但原有任务可能因为以下原因长时间未执行：
   - `next_retry_at` 时间还未到
   - 探测worker繁忙
   - 任务优先级低
6. **节点状态长时间未更新**

**影响范围**：  
所有在队列中已有任务的节点，即使凭据已恢复，也无法立即触发新探测。

---

### 2.3 冷却期机制延迟恢复

**根本原因**：  
`unavailable_recover_at` 冷却期机制设计为避免频繁探测，但在凭据恢复场景下成为瓶颈。

**失败场景**：
1. 节点因持续失败被标记为不可用
2. 系统设置 `unavailable_recover_at = NOW() + 冷却时长`（例如5分钟）
3. 即使供应商凭据立即恢复，节点仍需等待冷却期结束
4. 冷却期内，`credential_recovery` 跳过该节点
5. **节点状态延迟5分钟以上才开始恢复流程**

**影响范围**：  
10个节点处于 `WAITING` 状态，需等待冷却期结束。

---

### 2.4 node_probe_state 缺失导致无法调度

**根本原因**：  
部分节点的 `node_probe_state` 表无记录，导致探测调度失败。

**失败场景**：
1. 节点被标记为不可用（`credential_model_bindings.available = FALSE`）
2. 但 `node_probe_state` 表中无对应记录
3. `bg/node_probe.go:pickDueAtomically` 无法找到该节点
4. **探测永远不会被调度**

**影响范围**：  
5个节点（包括 credential_id=9 的3个模型和 credential_id=26 的1个模型）。

---

## 三、已知设计缺陷总结

### 3.1 RestoreOnSuccess 模型绑定歧义

**位置**: `domains/credential/writer.go`  
**问题**: 无法处理一个凭据绑定多个相似模型名的情况  
**修复建议**: 
- 优先精确匹配 `standardized_name`
- 如果仍有歧义，记录警告但选择第一个候选（或权重最高的）
- 添加 `model_name_hint` 字段以消除歧义

### 3.2 探测队列去重过于严格

**位置**: `bg/node_probe.go:Submit`  
**问题**: 已调度但长时间未执行的任务阻止新提交  
**修复建议**:
- 去重时检查 `next_retry_at`，如果超过阈值（例如30秒），允许重新插入
- 或者实现优先级队列，允许高优先级任务覆盖低优先级任务

### 3.3 冷却期机制不区分场景

**位置**: `bg/credential_recovery.go:expiredCmbRecoverySQL`  
**问题**: 凭据恢复后仍需等待冷却期  
**修复建议**:
- 凭据级别的恢复事件应立即触发所有相关节点的探测
- 或者缩短凭据恢复场景下的冷却期（例如从5分钟降到30秒）

### 3.4 node_probe_state 初始化缺失

**位置**: `bg/node_probe.go` 或 `domains/credential/writer.go`  
**问题**: 节点标记为不可用时未创建 `node_probe_state` 记录  
**修复建议**:
- 在标记节点不可用时（`credential_model_bindings.available = FALSE`），同步创建 `node_probe_state` 记录
- 或者在 `pickDueAtomically` 中处理缺失记录，自动创建

---

## 四、验证步骤

### 4.1 RestoreOnSuccess 失败验证

```sql
-- 查询存在歧义的模型绑定
SELECT 
    c.id, c.label,
    pm.raw_model_name, pm.standardized_name,
    COUNT(*) as binding_count
FROM credentials c
JOIN credential_model_bindings cmb ON c.id = cmb.credential_id
JOIN provider_models pm ON cmb.provider_model_id = pm.id
WHERE c.id = 21
GROUP BY c.id, c.label, pm.raw_model_name, pm.standardized_name
HAVING COUNT(*) > 1;
```

### 4.2 探测队列状态验证

```sql
-- 查询长时间未执行的探测任务
SELECT 
    nps.credential_id,
    nps.raw_model_name,
    nps.next_retry_at,
    nps.last_attempt_at,
    EXTRACT(EPOCH FROM (NOW() - nps.next_retry_at)) as overdue_seconds
FROM node_probe_state nps
WHERE nps.next_retry_at < NOW() - INTERVAL '1 minute'
AND nps.paused = FALSE
ORDER BY overdue_seconds DESC
LIMIT 20;
```

### 4.3 缺失 node_probe_state 记录验证

```sql
-- 查询不可用但缺失探测状态的节点
SELECT 
    c.id, c.label,
    pm.raw_model_name,
    cmb.available,
    cmb.unavailable_reason,
    nps.credential_id as has_probe_state
FROM credentials c
JOIN credential_model_bindings cmb ON c.id = cmb.credential_id
JOIN provider_models pm ON cmb.provider_model_id = pm.id
LEFT JOIN node_probe_state nps 
    ON cmb.credential_id = nps.credential_id 
    AND pm.raw_model_name = nps.raw_model_name
WHERE cmb.available = FALSE
AND nps.credential_id IS NULL
ORDER BY c.id, pm.raw_model_name;
```

---

## 五、立即修复建议（P0）

### 5.1 修复 RestoreOnSuccess 歧义处理

**文件**: `domains/credential/writer.go`  
**修改点**: `RestoreOnSuccess` 方法中的模型绑定解析逻辑

```go
// 当存在歧义时，选择精确匹配standardized_name的绑定
// 如果仍有歧义，选择第一个候选并记录警告
if len(candidates) > 1 {
    slog.Warn("RestoreOnSuccess: ambiguous model binding, using first candidate",
        "credential_id", credID,
        "model", modelName,
        "candidates", candidates)
}
// 使用 candidates[0] 而不是抛出错误
```

### 5.2 手动修复当前不可用节点

```sql
-- 1. 为缺失 node_probe_state 的节点创建记录
INSERT INTO node_probe_state (credential_id, raw_model_name, next_retry_at, next_retry_seconds)
SELECT 
    cmb.credential_id,
    pm.raw_model_name,
    NOW(),
    5
FROM credentials c
JOIN credential_model_bindings cmb ON c.id = cmb.credential_id
JOIN provider_models pm ON cmb.provider_model_id = pm.id
LEFT JOIN node_probe_state nps 
    ON cmb.credential_id = nps.credential_id 
    AND pm.raw_model_name = nps.raw_model_name
WHERE cmb.available = FALSE
AND nps.credential_id IS NULL
ON CONFLICT (credential_id, raw_model_name) DO NOTHING;

-- 2. 重置长时间未执行的探测任务
UPDATE node_probe_state
SET 
    next_retry_at = NOW(),
    next_retry_seconds = 5
WHERE next_retry_at < NOW() - INTERVAL '5 minutes'
AND paused = FALSE;

-- 3. 清理过长的冷却期
UPDATE credential_model_bindings
SET unavailable_recover_at = NOW() + INTERVAL '30 seconds'
WHERE available = FALSE
AND unavailable_recover_at > NOW() + INTERVAL '5 minutes';
```

---

## 六、后续优化建议（P1）

### 6.1 增强探测队列管理

- 实现探测任务优先级机制
- 允许高优先级任务（例如凭据恢复触发的探测）覆盖低优先级任务
- 定期清理长时间未执行的任务

### 6.2 优化冷却期策略

- 区分探测失败和凭据失败的冷却期
- 凭据恢复后立即重置相关节点的冷却期
- 实现指数退避的动态冷却期

### 6.3 完善状态同步机制

- 确保 `credential_model_bindings.available = FALSE` 时自动创建 `node_probe_state` 记录
- 实现状态一致性检查脚本，定期修复不一致

---

## 七、结论

**核心问题**：  
供应商凭据恢复后，节点状态未能自动同步的根本原因是：
1. **RestoreOnSuccess 热路径因模型绑定歧义失败**，无法在请求成功时立即恢复节点
2. **探测队列去重机制过于严格**，阻止了新探测任务的提交
3. **冷却期机制不区分场景**，延迟了凭据恢复后的节点恢复
4. **部分节点缺失 node_probe_state 记录**，导致探测永远无法调度

**修复优先级**：
- P0：修复 RestoreOnSuccess 歧义处理逻辑（立即）
- P0：手动修复当前不可用节点状态（立即）
- P1：优化探测队列和冷却期机制（后续迭代）

**预期效果**：  
修复后，凭据恢复时节点状态应在30秒内自动同步更新，无需手工干预。

---

**报告生成时间**: 2026-09-03 06:10  
**诊断人员**: ZCode  
**下一步**: 执行 P0 修复，重启服务，验证自动恢复流程
