# 探测系统后续优化项

**日期**: 2026-07-13  
**背景**: 修复 NVIDIA NIM minimax-m3 主动探测失败时，发现的其他潜在问题

## 已修复（commit 19d7c4d4d）

✅ 主动探测模型名空值不回退  
✅ 主动探测 StateManager 未注入  
✅ 主动探测 backoff 索引 off-by-one  

---

## 待修复问题

### 高优先级

#### 1. CredentialProbeV2 的 `/models` URL 候选失败逻辑

**问题描述**:
- `ModelsURLCandidates` 返回多个候选 URL（如 `/models`、`/v1/models`）
- 但 `CredentialProbeV2` 在首个 URL 返回 404 时立即失败，不尝试后续候选
- 对 Minimax 等 base URL 为 `/anthropic`、实际只提供 `/anthropic/v1/models` 的场景会误判

**位置**:
- `bg/credential_probe_v2.go:319-374`
- `internal/upstreamurl/upstreamurl.go:192-225`

**建议修复**:
```go
// 在 CredentialProbeV2.probeWithRetry 中对 404 进行候选 fallback
for _, url := range candidates {
    result := doProbe(url)
    if result.StatusCode == 404 && hasMoreCandidates {
        continue // 尝试下一个候选
    }
    return result
}
```

**影响范围**: 中等（仅影响 credential 级探测，model 级探测不受影响）

---

#### 2. 新 binding 无法首次入队

**问题描述**:
- `ModelProbeRunner.cycle()` 查询条件要求 `mps.next_retry_at <= NOW()`
- 新创建的 `credential_model_bindings` 没有对应 `model_probe_state` 行
- `LEFT JOIN` 后 `next_retry_at` 为 NULL，不满足条件，永远不被选中
- 状态行只在首次探测成功后由 `applyResult` 创建，形成死锁

**位置**:
- `bg/model_probe.go:184-225`

**建议修复**:
```sql
WHERE (mps.next_retry_at IS NULL OR mps.next_retry_at <= NOW())
```

或在 binding 创建时同步创建初始 `model_probe_state` 行。

**影响范围**: 高（新模型可能永远不被探测）

---

#### 3. Redis availability cache 初始化时序问题

**问题描述**:
- 探测器注入 `modelAvailabilityCache` 时（main.go:1559, 1585, 1625），该变量尚未创建
- `modelAvailabilityCache` 实际在 `:1777-1780` 才创建
- 导致所有探测器实际拿到 `nil`，Redis 写回分支不执行

**位置**:
- `cmd/gateway/main.go:1559, 1585, 1625, 1777-1780`

**建议修复**:
1. 将 `modelAvailabilityCache` 创建移到探测器之前
2. 或使用延迟注入（在 cache 创建后调用 `SetCache()`）

**影响范围**: 高（Redis 状态不同步，路由依赖内存状态）

---

### 中优先级

#### 4. 恢复阈值不一致

**问题描述**:
- 主共识状态机要求连续 3 次成功 → `healthy_confirmed`
- 但 `reconcileHealthyConfirmedBindings` 只要求 `consecutive_successes >= 2`
- 可能在状态未达成共识时就恢复 binding

**位置**:
- `bg/model_probe.go:401-445` (主共识)
- `bg/model_probe_reconcile_healthy.go:27-35` (恢复逻辑)

**建议修复**:
```sql
WHERE mps.consecutive_successes >= 3  -- 与主共识一致
```

**影响范围**: 低（提前恢复通常无害，但逻辑不一致）

---

#### 5. 模型列表大小写敏感比较

**问题描述**:
- `/v1/models` 响应解析后用 `==` 比较模型 ID
- NVIDIA/MiniMax 可能返回 `MiniMax-M3`，数据库存 `minimax-m3`
- 导致误判为 `model_not_found`，进入 `broken_confirmed`

**位置**:
- `bg/probe_http.go:386-395`

**建议修复**:
```go
// 使用大小写不敏感比较
if strings.EqualFold(modelID, targetModel) {
    found = true
    break
}
```

**影响范围**: 中等（已知 Minimax 存在大小写变体）

**参考文档**:
- `docs/2026-06-23-minimax-m3-no-candidates-diagnostic.md:52-78`

---

#### 6. Anthropic 协议主动探测不完整

**问题描述**:
- `ActiveProbeExecutor` 对 Anthropic 使用 `/v1/messages` endpoint
- 但请求体始终使用 OpenAI 风格，只调整了 `max_tokens`
- 没有完整使用 `providercap.ApplyAuthHeaders` 的协议转换

**位置**:
- `bg/active_probe_executor.go:262-297`
- 对比 `bg/credential_probe_v2.go:542-579` 的完整实现

**建议修复**:
- 统一使用 `providercap.Descriptor` 的协议配置
- 或抽取 `CredentialProbeV2.miniAnthropic` 为公共函数

**影响范围**: 低（当前 Minimax Anthropic 路径实际可用，但语义不一致）

---

### 低优先级

#### 7. 测试覆盖缺口

**当前缺失**:
- `active_probe_integration_test.go` (代码注释声称存在但未找到)
- Worker backoff 映射的单元测试（`processOne` 的 attempt → backoff 计算）
- 空模型名场景的 executor 测试
- Redis cache 初始化时序的集成测试

**建议**:
- 增加 worker backoff 回归测试（注入可控 backoff 函数）
- 增加 executor 空模型名测试用例
- 增加 cache 注入顺序的启动集成测试

---

## 验证计划

### 测试环境验证（推荐优先级）

1. **确认 NVIDIA provider 和 credential 19**
   ```bash
   curl -H "Authorization: Bearer $JWT" \
     https://test-admin.domain/api/providers | jq '.[] | select(.display_name | contains("NVIDIA"))'
   ```

2. **触发真实探测**
   ```bash
   curl -X POST -H "Authorization: Bearer $JWT" \
     -H "Content-Type: application/json" \
     -d '{"credential_id":19,"raw_model_name":"minimaxai/minimax-m3"}' \
     https://test-admin.domain/api/providers/18/probe-history/trigger
   ```

3. **检查结果**
   ```bash
   curl -H "Authorization: Bearer $JWT" \
     'https://test-admin.domain/api/providers/18/probe-history?limit=20' \
     | jq '.[] | select(.credential_id == 19)'
   ```

   **期望**:
   - `http_status != 0` (真实发送)
   - `status: "ok"` 或具体错误分类
   - `triggered_by: "manual"`

4. **触发业务失败后的主动探测**
   - 对该模型连续发送 2 次必失败请求
   - 观察日志：`credstate: triggering active_probe`
   - 确认约 5 秒后执行（而非 30 秒）
   - 确认 `request_logs.task_type=probe_triggered`
   - 确认 `model_probe_state` 更新

### 生产灰度（需运维批准）

1. **优先部署到 252**（数据库主节点）
   - 观察 24 小时
   - 监控：主动探测成功率、状态回写延迟、backoff 分布

2. **确认无异常后部署到 154**
   - 同样监控 24 小时

3. **回滚准备**
   ```bash
   systemctl stop llm-gateway-go && \
   ln -sf llm-gateway-go.bak-20260713 llm-gateway-go && \
   systemctl start llm-gateway-go
   ```

---

## 相关文件

**修改文件（本次修复）**:
- `bg/active_probe_executor.go`
- `bg/active_probe_worker.go`
- `cmd/gateway/main.go`

**待修改文件（后续优化）**:
- `bg/credential_probe_v2.go` (问题 1)
- `bg/model_probe.go` (问题 2)
- `cmd/gateway/main.go` (问题 3)
- `bg/model_probe_reconcile_healthy.go` (问题 4)
- `bg/probe_http.go` (问题 5)
- `bg/active_probe_executor.go` (问题 6)

**测试文件**:
- `bg/active_probe_backoff_test.go`
- `bg/active_probe_worker_test.go`
- `bg/probe_http_test.go`

---

## 参考

- Commit: `19d7c4d4d` (本次修复)
- Commit: `2a5604651` (引入主动探测)
- 诊断文档: `docs/2026-06-23-minimax-m3-no-candidates-diagnostic.md`
- 设计文档: `docs/design-docs/04-error-triggered-probe-design.md`
