# 路由质量门槛 (Quality Gate) 运维手册

## 概述

LLM Gateway 的路由系统现在包含一个**多轮降级策略**，用于处理候选凭据质量不足的情况。

### 工作原理

1. **第一轮** (严格模式): 只选择最近成功率 ≥ 30% 的凭据
2. **第二轮** (宽松模式): 如果第一轮无候选，降低到成功率 > 0% 的凭据
3. **失败**: 如果两轮都没有候选，返回 "no available provider"

这确保了即使只有低质量凭据可用，系统也会尝试路由，而不是直接失败。

## 监控指标

### Prometheus Metrics

#### 1. `llm_gateway_routing_quality_gate_fallback_total`
降级策略触发次数

```promql
# 每秒降级率
rate(llm_gateway_routing_quality_gate_fallback_total[5m])

# 按模型分组的降级总数
sum by (model) (llm_gateway_routing_quality_gate_fallback_total)
```

**标签**:
- `model`: 模型名称 (如 `claude-opus-4-8`)
- `threshold`: 使用的阈值 (`0.3_strict` 或 `0.0_relaxed`)

#### 2. `llm_gateway_routing_candidates_count`
候选数量分布直方图

```promql
# P95 候选数量
histogram_quantile(0.95, sum(rate(llm_gateway_routing_candidates_count_bucket[5m])) by (le, model))

# 零候选的请求数
sum(llm_gateway_routing_candidates_count_bucket{le="0"})
```

### 日志信号

#### 降级警告
```json
{
  "level": "WARN",
  "msg": "routing fallback: using relaxed quality gate",
  "model": "claude-opus-4-8",
  "threshold": 0,
  "candidates_count": 1
}
```

**含义**: 严格阈值没有找到候选，降级到宽松阈值

#### 调试信息
```json
{
  "level": "DEBUG",
  "msg": "routing quality gate: no candidates passed strict threshold",
  "model": "claude-opus-4-8",
  "threshold": 0.3
}
```

**含义**: 第一轮过滤后没有候选

## 诊断工具

### 1. Resolve API

查看模型的路由决策详情:

```bash
curl "http://gateway/api/routing/resolve?model=claude-opus-4-8" \
  -H "Authorization: Bearer YOUR_KEY" | jq
```

**响应示例**:
```json
{
  "client_model": "claude-opus-4-8",
  "quality_gate": {
    "threshold_used": 0,              // 使用的阈值
    "routable_count": 1,              // 通过的候选数
    "filtered_count": 0,              // 被过滤的候选数
    "total_count": 1,                 // 总候选数
    "fallback_applied": true,         // 是否触发降级
    "filter_reasons": {               // 过滤原因统计
      "recent_success_rate_low:24.00%": 1
    }
  },
  "candidates": [
    {
      "credential_id": 17,
      "routable": true,
      "block_reason": null,
      "recent_success_rate": 0.24,
      "recent_samples": 50
    }
  ]
}
```

### 2. Grafana Dashboard

导入 `docs/grafana-quality-gate-dashboard.json` 到 Grafana:

1. 登录 Grafana
2. 点击 "+" → "Import"
3. 上传 JSON 文件
4. 选择 Prometheus 数据源

**面板说明**:
- **Quality Gate Fallback Rate**: 降级触发频率
- **Candidate Count Distribution**: P50/P95/P99 候选数量
- **Models Using Fallback**: 有多少模型在使用降级
- **Zero Candidate Routing Failures**: 完全没有候选的失败次数

## 告警规则

### Prometheus Alert Rules

```yaml
groups:
  - name: routing_quality_gate
    interval: 1m
    rules:
      - alert: HighQualityGateFallbackRate
        expr: rate(llm_gateway_routing_quality_gate_fallback_total[5m]) > 0.1
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "High quality gate fallback rate for {{ $labels.model }}"
          description: "Model {{ $labels.model }} is using relaxed quality gate at {{ $value }} fallbacks/sec"

      - alert: FrequentZeroCandidateFailures
        expr: increase(llm_gateway_routing_candidates_count_bucket{le="0"}[10m]) > 10
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "Frequent zero-candidate routing failures for {{ $labels.model }}"
          description: "Model {{ $labels.model }} has {{ $value }} zero-candidate failures in last 10 minutes"

      - alert: SingleLowQualityCredential
        expr: |
          sum by (model) (llm_gateway_routing_quality_gate_fallback_total) > 0
          and
          sum by (model) (llm_gateway_routing_candidates_count_sum) / sum by (model) (llm_gateway_routing_candidates_count_count) < 1.5
        for: 15m
        labels:
          severity: warning
        annotations:
          summary: "Model {{ $labels.model }} relies on single low-quality credential"
          description: "Consider adding more credentials or investigating why current credential has low success rate"
```

## 运维响应流程

### 场景 1: 降级告警触发

**现象**: `HighQualityGateFallbackRate` 告警触发

**排查步骤**:
1. 查看 resolve API 找出低质量凭据:
   ```bash
   curl "http://gateway/api/routing/resolve?model=MODEL_NAME" | jq '.candidates[] | select(.recent_success_rate < 0.3)'
   ```

2. 检查凭据最近的失败日志:
   ```sql
   SELECT 
     ts, success, http_status, error_message
   FROM request_logs
   WHERE credential_id = CREDENTIAL_ID
     AND outbound_model = 'MODEL_NAME'
   ORDER BY ts DESC
   LIMIT 50;
   ```

3. 确定根因:
   - **API 配额用尽**: 检查 `quota_state`
   - **上游服务问题**: 查看 `http_status` 和 `error_message`
   - **配置错误**: 验证 API key、base_url

4. 采取行动:
   - **短期**: 添加其他可用凭据
   - **中期**: 修复根因问题
   - **长期**: 设置监控防止复发

### 场景 2: 零候选失败

**现象**: `FrequentZeroCandidateFailures` 告警触发

**排查步骤**:
1. 确认模型配置存在:
   ```sql
   SELECT COUNT(*) 
   FROM model_offers 
   WHERE raw_model_name = 'MODEL_NAME' 
     AND available = TRUE;
   ```

2. 检查所有候选的阻塞原因:
   ```bash
   curl "http://gateway/api/routing/resolve?model=MODEL_NAME" | \
     jq '.candidates[] | {id: .credential_id, block_reason}'
   ```

3. 常见原因:
   - **所有凭据都在冷却**: `circuit_state = 'open'`
   - **所有凭据都被手动禁用**: `manual_disabled = true`
   - **所有凭据 probe 失败**: `probe_state = 'broken_confirmed'`
   - **成功率全部为 0**: 即使降级也无法使用

4. 采取行动:
   - 立即添加健康凭据
   - 或等待冷却期结束
   - 或修复被 probe 标记为损坏的凭据

### 场景 3: 用户报告低成功率

**现象**: 用户反馈某模型经常失败

**排查步骤**:
1. 检查是否在使用降级策略:
   ```bash
   curl "http://gateway/metrics" | grep "quality_gate_fallback.*MODEL_NAME"
   ```

2. 查看候选质量:
   ```bash
   curl "http://gateway/api/routing/resolve?model=MODEL_NAME" | \
     jq '.candidates[] | {id, label, success_rate: .recent_success_rate}'
   ```

3. 如果确认是低质量凭据:
   - 与用户沟通预期成功率
   - 优先添加高质量凭据
   - 考虑临时禁用该模型直到有高质量凭据

## 配置调优

### 调整阈值

当前硬编码阈值:
- 严格模式: 30%
- 宽松模式: 0%

如果需要调整，修改 `provider/client.go`:

```go
thresholds := []float64{0.5, 0.3, 0.0}  // 三轮降级
```

### 调整样本窗口

当前使用最近 50 次请求计算成功率，最少需要 20 个样本。

修改 SQL 查询中的窗口大小:
```sql
ORDER BY ts DESC LIMIT 50  -- 改为其他值
```

修改最小样本阈值:
```sql
rsr.samples >= 20  -- 改为其他值
```

## 最佳实践

1. **为每个模型配置至少 2 个凭据**，避免单点依赖
2. **设置 Grafana 告警**，及时发现质量问题
3. **定期审查低质量凭据**，检查根因并修复
4. **监控 fallback_applied=true 的比例**，目标 < 5%
5. **使用 resolve API** 作为日常诊断工具

## 常见问题

### Q: 为什么不直接禁用低质量凭据？

A: 降级策略允许系统在没有更好选择时仍能提供服务。24% 成功率虽然低，但总比 0% 可用性好。系统会优先使用高质量凭据，只在必要时降级。

### Q: 如何知道我的请求是否使用了降级凭据？

A: 查看日志中的 WARN 级别消息，或通过 Prometheus metrics 监控 `llm_gateway_routing_quality_gate_fallback_total` 指标。

### Q: 降级策略会影响性能吗？

A: 影响极小。每轮查询是独立的数据库操作，额外开销约 5-10ms。只有在第一轮无结果时才会执行第二轮。

### Q: 可以禁用降级策略吗？

A: 不建议。如果确实需要，可以修改代码只保留单一阈值。但这会导致单凭据场景下的可用性问题。

---

**文档版本**: 1.0  
**最后更新**: 2026-06-26  
**维护者**: LLM Gateway Team
