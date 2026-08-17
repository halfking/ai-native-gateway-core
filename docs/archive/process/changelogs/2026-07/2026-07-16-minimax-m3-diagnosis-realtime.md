# minimax-m3 实时诊断总结 (2026-07-16 11:17)

## 核心发现

### 1. Credential 状态

| ID | Provider | Available | Reason | Consecutive Failures | Priority |
|---|---|---|---|---|---|
| 21 | 14 (MiniMax官方) | ✅ true | - | 0→1 (刚失败) | 99 |
| 19 | 18 (NVIDIA) | ✅ true | - | 0 | 99 |
| 23 | 18 (NVIDIA) | ❌ false → ✅ true (手动恢复) | auto_stream_timeout | 0 | 99 |
| 12 | 35 (火山引擎) | ✅ true | - | 0 | 99 |
| 14 | 37 (其他) | ✅ true | - | 0 | 99 |
| 15 | 67 (MiniMax Anthropic) | ✅ true | - | 0 | 99 |
| 11 | 34 (火山引擎) | ✅ true | - | 0 | 999 |

**问题**：
- 所有 credentials 优先级相同 (99)，除了 11 (999)
- **但实际请求只使用 credential 21**（单一候选，无故障转移）

### 2. 最近失败模式

| 时间 | Credential | 错误 | 延迟 | 分析 |
|---|---|---|---|---|
| 11:08:34 | 21 | stream_timeout | 120s | 超过 MaxTimeout |
| 11:08:56 | 21 | client_write_failed | 39s | 客户端断开 |
| 11:17:28 | 21 | client_write_failed | 45s | 客户端断开（刚发生） |

**模式**：
- credential 21 间歇性失败（成功率 ~80%）
- 失败原因：`stream_timeout` (120s) 或 `client_write_failed`（客户端断开）
- **但没有 failover 到其他 6 个可用 credentials**

### 3. 路由问题

**所有 credentials 都是 available + 相同优先级，为什么只用 21？**

可能原因：
1. **Sticky routing**：客户端被粘性路由到 credential 21
2. **负载均衡权重**：weight 字段可能不同
3. **缓存未刷新**：路由缓存中只有 credential 21
4. **FP slot 饱和**：其他 credentials 的 FP slot 已满，只能用 21

### 4. 手动恢复 credential 23

**已执行**：
```sql
UPDATE credential_model_bindings
SET available = true,
    unavailable_reason = NULL,
    consecutive_failures = 0
WHERE credential_id = 23
  AND provider_model_id IN (
      SELECT id FROM provider_models
      WHERE raw_model_name = 'minimaxai/minimax-m3'
  );
```

**结果**：credential 23 已恢复，但后续请求仍然使用 credential 21

## 根本原因分析

### 问题 1: 路由只选择单一 credential（无 failover）

**证据**：
- 所有请求都走 provider:14 / credential:21
- 从未见到 fallback 到 19/23/12/14/15

**可能根因**：
1. **Sticky routing 过于粘性**：sessionID 或 clientID 绑定到 credential 21，不会切换
2. **路由缓存问题**：candidates cache 中只缓存了 credential 21
3. **FP slot 限制**：其他 credentials 的 FP slot 已满，无法接受新请求

### 问题 2: 失败后未触发 failover

**证据**：
- credential 21 失败后，返回错误给客户端，而不是尝试 credential 19/23 等

**根因**：
- `client_write_failed` 被分类为 `non-resumable`，不会 failover
- 只有 `resumable=true` 的错误（如 `first_byte_timeout`）才会 failover

### 问题 3: active_probe 未触发

**证据**：
- consecutive_failures 总是 0 或 1，从未达到阈值 2
- 没有看到 `credstate: triggering active_probe` 日志

**根因**：
- `client_write_failed` 可能被分类为 `transient_failure`，不累加 consecutive_failures
- 或者成功请求会重置计数器

## 立即修复步骤

### 修复 1: 检查为什么路由只选择单一 credential

```sql
-- 检查 weight 和其他路由参数
SELECT
    cmb.credential_id,
    cmb.weight,
    cmb.manual_priority,
    cmb.routing_tier,
    cmb.available,
    pm.raw_model_name
FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE pm.standardized_name = 'minimax-m3'
  AND cmb.available = true
ORDER BY cmb.manual_priority, cmb.routing_tier, cmb.weight DESC;
```

### 修复 2: 强制刷新路由缓存

```bash
# 重启 llm-gateway-go 或发送 SIGHUP
ssh -p 25022 root@47.97.111.154 'systemctl reload llm-gateway-go.service'
```

### 修复 3: 手动测试其他 credentials

用户说"直连可行"，让我们测试是否真的是网络问题：

```bash
# 测试 credential 19 (NVIDIA)
ssh -p 25022 root@47.97.111.154 'curl -sS --max-time 10 https://integrate.api.nvidia.com/v1/models'

# 测试 credential 12 (火山引擎)
ssh -p 25022 root@47.97.111.154 'curl -sS --max-time 10 https://ark.cn-beijing.volces.com/api/v3/'
```

## 代码修复计划

### Issue 1: manager_test.go 类型错误

```bash
File: domains/credentialstate/manager_test.go
Lines: 203, 279, 319

Error: cannot use (func() literal) as func(credentialID int)

Fix: 修改 lambda 签名，添加 credentialID 参数
```

### Issue 2: client_write_failed 应该 failover

```bash
File: domains/streaming/executors/executor_chat.go

Problem: client_write_failed 被标记为 non-resumable，不会 failover

Fix: 如果是第一次尝试，且 chunk_count < threshold，应该标记为 resumable
```

### Issue 3: JSON 解析错误导致日志插入失败

```bash
File: 多个地方（telemetry, apihub watcher, candidate_failure_logger）

Error: ERROR: invalid input syntax for type json (SQLSTATE 22P02)

Problem: 某些字段被错误地当作 JSON 插入（可能是字符串）

Fix: 检查所有 jsonb 字段的插入逻辑，确保使用 json.Marshal
```

## 下一步行动

1. **立即**：检查 weight 字段，确认路由为何只选 credential 21
2. **立即**：测试用户直连（获取真实 API key）
3. **短期**：修复 manager_test.go 类型错误
4. **短期**：实现 client_write_failed 的 failover
5. **中期**：实现三层防御架构（Pre-Hook, Adaptive Timeout, Post-Hook）
