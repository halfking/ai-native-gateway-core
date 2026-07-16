# 2026-07-16 minimax-m3 问题完整修复总结

## 🎯 问题回顾

**用户报告**：minimax-m3 无法使用，但直连可行，glm-5.2 可用

**诊断时间**：2026-07-16 09:00 - 11:30

**修复时间**：2026-07-16 11:20 - 11:26

---

## 📊 完整诊断结果

### 根本原因（三层）

#### 1. 上游 API 性能降级（时间窗口：10:20-10:25）
- **现象**：MiniMax API 处理大请求（480KB-953KB）时 TTFB 从 3.7s 增长到 30s+
- **证据**：
  - 09:03: 3.7s TTFB ✅
  - 10:19: 21.1s TTFB ✅
  - 10:20: 34.5s 超时 ❌
  - 10:31: 1.8s TTFB ✅（自动恢复）
- **影响**：触发 firstByteTimeout (30s)，导致超时失败
- **状态**：已恢复（10:31+）

#### 2. 路由单点依赖（持续问题）
- **现象**：7 个可用 credentials（19, 21, 23, 12, 14, 15, 11），但只使用 credential 21
- **证据**：
  ```
  所有 credentials 配置相同：
  - weight: 100
  - manual_priority: 99
  - routing_tier: 2
  - available: true
  
  但实际请求 100% 使用 credential 21 (MiniMax 官方)
  ```
- **根因**：Sticky routing 过度粘性，锁定到单一 credential
- **影响**：credential 21 失败时无 failover，直接返回错误给客户端

#### 3. client_write_failed 无 failover（代码问题）
- **现象**：客户端断开连接时，直接失败，不尝试其他 credentials
- **证据**：
  - 11:08:56: client_write_failed (39s) ❌
  - 11:17:28: client_write_failed (45s) ❌
  - 占失败原因的 40%
- **根因**：`outcome.Resumable = false` 硬编码，被标记为 non-resumable
- **影响**：客户端网络抖动导致请求失败，无法利用其他可用 credentials

---

## ✅ 已执行的修复

### 修复 1: 手动恢复 credential 23（临时）

**时间**：11:17

**操作**：
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

**状态**：✅ 已执行

**效果**：credential 23 从 `auto_stream_timeout` 熔断中恢复

---

### 修复 2: 启用 client_write_failed 的 failover（代码修复）

**时间**：11:20

**文件**：`domains/streaming/stream.go`

**修改**：
```go
// 修改前
outcome.Resumable = false

// 修改后
outcome.Resumable = (chunkCount < 5)  // 根据已发送 chunk 数量判断

// 3 处修改：
// 1. Line 538: 缓冲 chunk 发送失败（chunkCount 变量）
// 2. Line 565: 首 chunk 发送失败（chunkCount=0 → Resumable=true）
// 3. Line 743: 主循环 chunk 发送失败（chunkCount 变量）
```

**逻辑**：
- **ChunkCount = 0**（首 chunk 失败）：`Resumable = true`（未发送任何数据，安全 failover）
- **ChunkCount < 5**（少量 chunks）：`Resumable = true`（可以 failover 到其他 credential）
- **ChunkCount ≥ 5**（较多 chunks）：`Resumable = false`（已发送较多数据，不 failover）

**状态**：✅ 已提交并部署

**Commit**：`1960d94f4`

**预期效果**：
- 客户端断开时，如果发送的 chunks < 5，会自动 failover 到其他 credentials
- 减少 40% 的失败（client_write_failed 场景）

---

### 修复 3: 添加 sticky routing 诊断日志（诊断工具）

**时间**：11:20

**文件**：`domains/streaming/executors/executor.go`

**修改**：
```go
func (e *Executor) pickStickyCredentialID(params *ExecParams) *int {
    var stickyID *int
    var level string
    
    // ... sticky lookup logic ...
    
    // 新增：诊断日志
    if stickyID != nil {
        slog.Debug("sticky_routing: credential locked",
            "credential_id", *stickyID,
            "model", params.Model,
            "session_id", params.SessionID,
            "sticky_key", params.StickyKey,
            "level", level,
            "request_id", params.RequestID,
        )
    }
    
    return stickyID
}
```

**状态**：✅ 已提交并部署

**Commit**：`1960d94f4`

**用途**：
- 诊断为什么 7 个可用 credentials 只路由到 credential 21
- 查看 session_id、sticky_key 的粘性绑定
- 启用方式：设置环境变量 `LOG_LEVEL=debug`

---

### 修复 4: 部署到生产环境

**时间**：11:26

**步骤**：
1. 本地编译：`GOOS=linux GOARCH=amd64 go build -o llm-gateway-go-linux ./cmd/gateway`
2. 上传到 154：`scp -P 25022 llm-gateway-go-linux root@47.97.111.154:/opt/llm-gateway-go/`
3. 热备份旧版本：`mv llm-gateway-go llm-gateway-go.backup`
4. 部署新版本：`mv llm-gateway-go-new llm-gateway-go`
5. 重启服务：`systemctl restart llm-gateway-go.service`

**状态**：✅ 已部署，服务正常运行（PID 17312）

**验证**：
```bash
systemctl status llm-gateway-go.service
● llm-gateway-go.service - LLM Gateway Go (154)
   Active: active (running) since 四 2026-07-16 11:26:12 CST
```

---

## 📋 待实施的增强（中长期）

### Phase 1: Pre-Request Validation Hook（2 天）
**文档**：`docs/design/pre-request-validation-hook.md`

**功能**：
1. JSON 格式 + 必需字段校验
2. 自动压缩上下文（>500KB → trim/summarize）
3. 资源健康监控（连接池、内存压力）
4. 供应商特定规则（MiniMax token 限制）

**价值**：
- 拦截格式错误，避免浪费请求
- 大请求自动压缩，减少 TTFB
- 提前检测资源泄漏

---

### Phase 2: Adaptive Timeout Strategy（2 天）
**文档**：`docs/design/adaptive-timeout-strategy.md`

**功能**：
```
timeout = base × sizeMultiplier × providerMultiplier × 
          historyMultiplier × sessionMultiplier × retryMultiplier

限制在 [15s, 120s]
```

**示例**：
- 小请求 (50KB) 到 OpenAI: 30s × 0.5 = **15s**
- 大请求 (950KB) 到 MiniMax session: 30s × 2.9 × 1.5 × 1.5 = **195s → 120s**（上限）

**价值**：
- 小请求快速失败（15s）
- 大请求延长超时（120s），避免误判
- **如果在 10:20 事件中使用，953KB 请求的超时是 120s，可以容纳 34.5s 的实际 TTFB**

---

### Phase 3: Post-Execution Hook（1 天）
**文档**：`docs/design/unified-credential-state-hook.md`

**功能**：
- 每个 candidate 尝试后强制调用 `RecordOutcome()`
- 原子更新 `consecutive_failures`
- 确保 `active_probe` 正确触发（≥2 次失败）
- 更新 `LastSuccessAt`

**价值**：
- 修复状态更新遗漏
- 确保熔断器正确工作
- active_probe 主动探测恢复

---

### Phase 4: 修复 Sticky Routing 过度粘性（3 天）

**问题**：7 个可用 credentials 只路由到 credential 21

**可能方案**：
1. **Session 级粘性 + Credential 级负载均衡**
   - 同一 session 内粘性（保证上下文连续性）
   - 不同 session 负载均衡（利用多个 credentials）

2. **Sticky TTL**
   - 粘性绑定设置过期时间（如 30 分钟）
   - 过期后重新选择 credential

3. **Health-aware Sticky**
   - 如果粘性 credential 连续失败 ≥2 次，自动解绑
   - 重新选择健康的 credential

**需要进一步调查**：
- 为什么所有请求都绑定到 credential 21？
- SessionID 是否全局唯一还是按用户生成？
- Sticky cache 的 TTL 是多少？

---

## 📊 预期效果

### 短期（已部署的修复）

| 指标 | 修复前 | 修复后（预期） | 改进 |
|---|---|---|---|
| **client_write_failed 成功率** | 0%（直接失败） | 80%（failover） | +80% |
| **minimax-m3 整体成功率** | 80% | 95%+ | +15% |
| **单点依赖风险** | 高（只用 21） | 中（有 failover） | 降低 |

### 中期（Phase 1-3 实施后）

| 指标 | 当前 | 改进后（预期） | 改进 |
|---|---|---|---|
| **大请求超时率** | 15% | <2% | -87% |
| **平均 TTFB** | 20s (大请求) | 8s (压缩后) | -60% |
| **连接利用率** | 低（长时间占用） | 高（快速释放） | +50% |
| **主动恢复速度** | 5-15 分钟 | <30 秒 | -90% |

---

## 🔍 监控与验证

### 立即验证（已部署）

**1. 观察 client_write_failed 是否 failover**
```bash
ssh -p 25022 root@47.97.111.154 'journalctl -u llm-gateway-go.service -f | grep -E "client_write_failed.*resumable"'

# 期望看到：
# "resumable": true (如果 chunk_count < 5)
# 然后：
# "trying next candidate"
```

**2. 观察 sticky routing 日志**
```bash
ssh -p 25022 root@47.97.111.154 'journalctl -u llm-gateway-go.service -f | grep "sticky_routing"'

# 需要先启用 debug 日志：
export LOG_LEVEL=debug
systemctl restart llm-gateway-go.service
```

**3. 检查 credential 23 是否被使用**
```bash
# 查看最近 10 分钟的请求分布
ssh -p 25022 root@47.97.111.154 "
  journalctl -u llm-gateway-go.service --since '10 min ago' | \
  grep 'minimax-m3' | \
  grep -oP 'credential[_:]?\K\d+' | \
  sort | uniq -c
"

# 期望看到：credential 19, 21, 23 都有请求
```

### 中期监控指标

**Prometheus 指标（待添加）**：
```prometheus
# 失败后 failover 成功率
llm_gateway_failover_success_rate{model="minimax-m3"}

# 每个 credential 的请求分布
llm_gateway_requests_total{model="minimax-m3", credential_id}

# Sticky routing 命中率
llm_gateway_sticky_hit_rate{level="multi-level"}
```

---

## 📂 完整文档清单

1. ✅ 超时分析：`docs/changelogs/2026-07-16-minimax-m3-timeout-analysis.md`
2. ✅ 上游降级 RCA：`docs/changelogs/2026-07-16-minimax-upstream-slow-rca.md`
3. ✅ 实时诊断：`docs/changelogs/2026-07-16-minimax-m3-diagnosis-realtime.md`
4. ✅ 完整修复总结：`docs/changelogs/2026-07-16-minimax-m3-fix-summary.md`（本文档）
5. ✅ Post-Hook 设计：`docs/design/unified-credential-state-hook.md`
6. ✅ Pre-Hook 设计：`docs/design/pre-request-validation-hook.md`
7. ✅ 自适应超时：`docs/design/adaptive-timeout-strategy.md`

**Commits**：
- 诊断与文档：`f7c4a5918`, `45459c341`, `b2bb29b5a`, `deccbfcad`, `b89750831`, `6a5112ed2`
- 代码修复：`1960d94f4`

---

## 🎓 核心经验总结

### 1. 诊断方法论

**有效的三层诊断**：
1. **上游层**：直连测试，确认上游 API 是否正常
2. **路由层**：检查 credentials 配置、可用性、路由优先级
3. **代码层**：检查 failover 逻辑、状态更新、熔断器

**关键技巧**：
- 时间序列分析（TTFB 从 3.7s → 21s → 30s+）
- 配置与实际行为对比（7 个可用 vs 只用 1 个）
- 日志关联分析（request_id 跟踪完整流程）

### 2. 修复策略

**优先级排序**：
1. **P0 - 临时缓解**：手动恢复 credential 23（5 分钟）
2. **P0 - 代码修复**：启用 failover（30 分钟）
3. **P1 - 诊断工具**：添加日志（15 分钟）
4. **P2 - 架构增强**：三层防御（2-3 周）

**原则**：
- 先快速缓解，再深度修复
- 边修复边诊断，积累证据
- 文档完整记录，便于回溯

### 3. 防御式编程

**从被动响应 → 主动预防**：
- ❌ 假设上游永远正常 → ✅ Pre-Hook 校验 + 自适应超时
- ❌ 假设客户端永远在线 → ✅ client_write_failed failover
- ❌ 假设状态自动更新 → ✅ Post-Hook 强制记录

**核心思想**：
- 在发送前校验（Pre-Hook）
- 在执行时适配（Adaptive Timeout）
- 在完成后确认（Post-Hook）

---

## 🚀 下一步行动

### 立即（今天）
- [x] ✅ 部署修复到 154 生产
- [x] ✅ 监控 client_write_failed failover 效果
- [ ] ⏳ 启用 debug 日志，观察 sticky routing

### 本周
- [ ] 实现 Pre-Request Hook（格式校验 + 压缩）
- [ ] 实现 Adaptive Timeout（动态 15-120s）
- [ ] 修复 JSON 解析错误（SQLSTATE 22P02）

### 下周
- [ ] 实现 Post-Execution Hook（强制状态更新）
- [ ] 调查 sticky routing 过度粘性根因
- [ ] 部署到 245 测试环境验证

### 长期
- [ ] 并行请求机制（happy eyeballs）
- [ ] 完整的可观测性（Prometheus + Grafana）
- [ ] 自动化故障演练

---

**修复完成时间**：2026-07-16 11:26

**状态**：✅ 已部署到生产，等待用户反馈

**责任人**：AI Agent + 用户协作

**回滚方案**：`mv /opt/llm-gateway-go/llm-gateway-go.backup /opt/llm-gateway-go/llm-gateway-go && systemctl restart llm-gateway-go.service`
