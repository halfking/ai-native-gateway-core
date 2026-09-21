# 供应商错误处理审计摘要（来自 agent_318410c8）

## 总体评估

✅ **错误处理和可观测性基础设施完整且健壮**

## 完整闭环流程

```
客户端请求 → 路由层前置过滤 → 执行层错误分类 → Survival Coordinator failover
  → candidate_failure_logs_hot(记录) → Prometheus指标 → 客户端响应
  → success_response_body_missing监控
```

## ✅ 已完整实现

### 1. 错误分类体系（errorsx包）
- KindNetwork, KindTransient, KindTimeout, KindRateLimit
- KindQuotaExhausted, KindAuth, KindModelNotFound
- KindInvalidRequest, KindContentFilter, KindContextLength

### 2. candidate_failure_logs_hot 记录完整
- ✅ credential_id, provider_id, raw_model_name
- ✅ error_kind, upstream_status_code
- ✅ upstream_response_body（截断1KB）/ preview（320字符）
- ✅ tenant_id, request_id, session_id, attempt_index
- ✅ 3秒超时异步写入，不阻塞热路径

### 3. Failover机制（3层）
- Phase 1: 凭据级（同模型不同凭据，按优先级/成功率排序）
- Phase 2: 模型级（跨模型 fallback chain）
- Phase 3: Survival Coordinator循环（最多100次，2h deadline）

### 4. 用户通知（Thinking模式）
- ✅ SSE keepalive comment，不中断请求流程
- ✅ OpenAI: `: gw-survival-keepalive`
- ✅ Anthropic: `event: ping`

### 5. Prometheus指标齐全
- gateway_survival_attempt_total, gateway_survival_request_terminal
- llm_gateway_circuit_requests_total, circuit_state_transitions_total
- llm_gateway_success_response_body_missing_total

### 6. success_response_body_missing ADR落地验证
- ✅ Prometheus counter + WARN日志
- ✅ 包含完整追踪字段

## ⚠️ P1 问题

### Admin API未展示错误统计
- **位置**: `admin/provider_credential.go`
- **问题**: 凭据详情仅返回 `consecutive_failures`，未聚合 `candidate_failure_logs_hot`
- **影响**: 运维人员无法在UI直接查看错误明细
- **修复**:
```sql
SELECT error_kind, COUNT(*) as error_count, MAX(ts) as last_error_at,
       AVG(per_attempt_latency_ms) as avg_latency_ms
FROM candidate_failure_logs
WHERE credential_id = $1 AND ts > NOW() - INTERVAL '1 hour'
GROUP BY error_kind ORDER BY error_count DESC
```

## ⚠️ P2 问题

### 1. Key Rotation耗尽未记录到失败日志
- **位置**: `provider/client.go:enrichWithAPIKeys()`
- **问题**: 密钥解密失败标记 `Routable=false`，不进入executor，不记录到candidate_failure_logs_hot
- **影响**: 仅有Prometheus指标，缺少request级追踪
- **修复**: 在filterAvailable排除后记录到统一失败日志

### 2. Circuit-Open前置过滤无错误记录
- **位置**: `provider/client.go:GetCandidates()`
- **问题**: 断路器打开的凭据在前置过滤阶段被排除，未追踪到请求级失败日志
- **修复**: 考虑在GetCandidates返回空时记录诊断事件

## 推荐改进措施

### 短期（1-2周）
1. Admin API添加错误统计聚合端点
2. Key rotation失败记录到candidate_failure_logs_hot

### 中期（1个月）
3. Grafana dashboard：凭据错误率趋势、Survival恢复时长分布
4. Prometheus告警规则（HighCandidateFailureRate, SurvivalRecoverySlowdown）

### 长期（3个月）
5. provider_error_details聚合表（按小时/天）
6. 自动化熔断和恢复策略优化

---

详细报告：`/Users/xutaohuang/.zcode/cli/agents/sess_15d75c70-8085-4139-9ad4-d71fff7995b2/agent_318410c8-27a6-4224-a168-feec57ebb2c5/output.txt`
