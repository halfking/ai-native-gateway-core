# LLM Gateway 全方位测试方案

**版本**: v1.0  
**日期**: 2026-07-22  
**目标**: 确保系统在供应商不稳定情况下的高可用性  
**核心诉求**: 快速检测、智能路由、自适应负载均衡  

---

## 执行摘要

本测试方案针对生产环境中的实际痛点：
1. ❌ **长延时导致5xx** — 供应商响应慢，用户请求超时
2. ❌ **资源紧张** — 供应商quota耗尽，频繁429/503
3. ❌ **静态路由** — 无法动态避开故障节点
4. ❌ **检测滞后** — 发现问题太晚，影响大量请求

**解决方案**:
- ✅ **分层健康检查** — TCP ping → HTTP ping → AI轻量推理 → 20K上下文压测
- ✅ **5xx即时检测** — 单次失败立即触发快速检测
- ✅ **动态权重路由** — 按每分钟错误率调整权重
- ✅ **自适应降级** — 延时过高自动降级，延时恢复自动升级

---

## 第一部分：分层健康检查系统

### 1.1 四层检查架构

```
L1: TCP连通性检查 (1s超时)
    ↓ 失败 → Unhealthy
    ↓ 成功
L2: HTTP健康端点 (3s超时)
    ↓ 失败 → Degraded
    ↓ 成功
L3: AI轻量推理 (10s超时, 简单prompt)
    ↓ 失败 → Degraded
    ↓ 成功 或 延时>5s → 触发L4
L4: 20K上下文压测 (30s超时)
    ↓ 失败 或 延时>20s → Unhealthy
    ↓ 延时10-20s → Degraded
    ↓ 延时<10s → Active
```

### 1.2 分层检查触发策略

| 层级 | 触发条件 | 频率 | 用途 |
|------|---------|------|------|
| **L1 TCP** | 定时 + 连续失败3次 | 每30秒 | 快速检测网络连通性 |
| **L2 HTTP** | L1成功后 | 每30秒 | 检测服务端可用性 |
| **L3 轻量AI** | 单次5xx + L2成功 | 按需触发 | 检测推理能力 |
| **L4 重负载** | L3延时>5s + 每小时 | 按需+定时 | 检测高负载性能 |

### 1.3 测试用例设计

#### L1: TCP连通性测试

**单元测试**:
```go
TestHealthCheck_TCPPing_Success
TestHealthCheck_TCPPing_Timeout
TestHealthCheck_TCPPing_Refused
TestHealthCheck_TCPPing_HostUnreachable
```

**Mock实现**:
- 使用`net.Dial("tcp", addr)`
- Mock延时：立即、500ms、1s、2s（超时）
- 验证超时阈值（1秒）

#### L2: HTTP健康检查

**单元测试**:
```go
TestHealthCheck_HTTPPing_200OK
TestHealthCheck_HTTPPing_503
TestHealthCheck_HTTPPing_Timeout
TestHealthCheck_HTTPPing_SSLError
```

**Mock实现**:
- httptest.Server模拟各种响应
- 延时：立即、1s、3s、5s（超时）
- 状态码：200、429、500、503、504

#### L3: AI轻量推理

**单元测试**:
```go
TestHealthCheck_LightInference_Success
TestHealthCheck_LightInference_SlowButOK     // 3-5秒
TestHealthCheck_LightInference_TooSlow       // >5秒，触发L4
TestHealthCheck_LightInference_InvalidResponse
```

**Mock实现**:
```json
// 轻量prompt（预期<1秒）
{
  "model": "gpt-4",
  "messages": [{"role": "user", "content": "1+1=?"}],
  "max_tokens": 10
}
```

**验证**:
- 延时 < 5s → Degraded → Active
- 延时 > 5s → 触发L4
- 返回正确 → 继续
- 返回错误 → Degraded

#### L4: 20K上下文压测

**单元测试**:
```go
TestHealthCheck_HeavyLoad_Success
TestHealthCheck_HeavyLoad_SlowButOK         // 10-20秒
TestHealthCheck_HeavyLoad_TooSlow           // >20秒 → Unhealthy
TestHealthCheck_HeavyLoad_ContextWindowExceeded
```

**Mock实现**:
```json
{
  "model": "gpt-4",
  "messages": [
    {"role": "user", "content": "<20K tokens context>"}
  ],
  "max_tokens": 100
}
```

**验证**:
- 延时 < 10s → Active
- 延时 10-20s → Degraded
- 延时 > 20s → Unhealthy
- 上下文拒绝 → Credential配置错误

---

## 第二部分：5xx即时检测与快速恢复

### 2.1 5xx检测逻辑

```
用户请求 → 选中Credential A → 调用供应商
    ↓
返回5xx（500/502/503/504）
    ↓
即时触发：
    1. CredentialA.ConsecutiveFails++
    2. 如果ConsecutiveFails >= 3:
        Status = Unhealthy
    3. 异步触发快速检测（L1→L2→L3）
    4. 记录到错误计数器（用于权重计算）
```

### 2.2 5xx分类处理

| 状态码 | 根因 | 检测策略 | 恢复策略 |
|--------|------|----------|----------|
| **500** | 供应商内部错误 | L3轻量推理 | 3次成功恢复 |
| **502** | 网关错误/超时 | L1 TCP + L2 HTTP | 连通性恢复 |
| **503** | 服务不可用/quota | L2 HTTP | 等待quota恢复（时间推断） |
| **504** | 超时 | L3+L4（检测延时） | 延时降低后恢复 |

### 2.3 测试用例

#### 单次5xx触发检测

**单元测试**:
```go
TestErrorHandling_Single500_TriggerL3Check
TestErrorHandling_Single503_TriggerQuotaRecovery
TestErrorHandling_Single504_TriggerLatencyCheck
TestErrorHandling_Consecutive5xx_MarkUnhealthy
```

**场景**:
```
初始状态：CredentialA = Active, fails=0
1. 请求返回500
2. 验证：fails=1, Status=Degraded
3. 验证：L3检测已加入队列
4. 再次500
5. 验证：fails=2, Status=Degraded
6. 第3次500
7. 验证：fails=3, Status=Unhealthy
8. 第4次请求应该路由到CredentialB
```

#### 快速恢复测试

**单元测试**:
```go
TestErrorHandling_FastRecovery_AfterL3Success
TestErrorHandling_SlowRecovery_AfterQuotaTimeout
TestErrorHandling_NoRecovery_PersistentFailure
```

**场景**:
```
Credential = Unhealthy, fails=5
1. 后台L3检测 → 成功
2. 验证：fails=4, Status=Degraded
3. 用户请求 → 成功
4. 验证：fails=3, Status=Degraded
5. 继续2次成功
6. 验证：fails=0, Status=Active
```

---

## 第三部分：动态权重路由

### 3.1 权重计算公式

```
权重 = BaseWeight × ErrorRatePenalty × LatencyPenalty

BaseWeight = 1.0 (默认)

ErrorRatePenalty = max(0.1, 1 - (ErrorsPerMinute / 10))
  - 0错误 → 1.0
  - 5错误/分钟 → 0.5
  - 10+错误/分钟 → 0.1（最低权重）

LatencyPenalty = max(0.5, 1 - (AvgLatency - 2s) / 10s)
  - <2s → 1.0
  - 5s → 0.7
  - 10s → 0.5
  - >12s → 0.5（最低）
```

### 3.2 滑动窗口计数

```go
type ErrorCounter struct {
    window []int   // 60个槽，每个代表1秒
    current int    // 当前槽
    lastUpdate time.Time
}

// 每秒更新
func (c *ErrorCounter) Tick() {
    c.current = (c.current + 1) % 60
    c.window[c.current] = 0
}

// 获取最近1分钟错误数
func (c *ErrorCounter) Last1Min() int {
    sum := 0
    for _, v := range c.window {
        sum += v
    }
    return sum
}
```

### 3.3 测试用例

#### 权重计算测试

**单元测试**:
```go
TestWeightedRouting_NoErrors_FullWeight
TestWeightedRouting_FewErrors_ModerateWeight
TestWeightedRouting_ManyErrors_LowWeight
TestWeightedRouting_HighLatency_Penalty
TestWeightedRouting_Combined_ErrorAndLatency
```

**场景**:
```
CredentialA: 0 errors, 1s latency → Weight=1.0
CredentialB: 5 errors/min, 1s latency → Weight=0.5
CredentialC: 0 errors, 8s latency → Weight=0.4

期望分布（100次请求）:
A: ~52次 (1.0 / 1.9 * 100)
B: ~26次 (0.5 / 1.9 * 100)
C: ~22次 (0.4 / 1.9 * 100)

允许误差：±10%
```

#### 动态调整测试

**集成测试**:
```go
TestWeightedRouting_DynamicAdjustment_ErrorSpike
TestWeightedRouting_DynamicAdjustment_LatencySpike
TestWeightedRouting_DynamicAdjustment_Recovery
```

**场景**:
```
T0: 3个Credential，权重均为1.0
T1: CredentialA产生10个错误
T2: 验证权重：A=0.1, B=1.0, C=1.0
T3: 100次请求，A应该只收到~5次
T4: A无新错误，1分钟后窗口滑动
T5: 验证权重：A恢复到1.0
T6: 100次请求，分布应该均匀
```

---

## 第四部分：自适应降级与升级

### 4.1 降级条件

| 当前状态 | 降级条件 | 目标状态 |
|---------|---------|---------|
| Active | 连续3次5xx | Degraded |
| Active | L3延时>5s 或 L4延时>20s | Degraded |
| Active | TCP连接失败 | Unhealthy |
| Degraded | 连续3次5xx | Unhealthy |
| Degraded | L1/L2失败 | Unhealthy |

### 4.2 升级条件

| 当前状态 | 升级条件 | 目标状态 |
|---------|---------|---------|
| Unhealthy | L1+L2成功 | Degraded |
| Degraded | 连续5次成功 + L3延时<3s | Active |
| Degraded | quota恢复时间到达 | Active |

### 4.3 测试用例

#### 降级测试

**单元测试**:
```go
TestAdaptive_Downgrade_Active_To_Degraded_After3Failures
TestAdaptive_Downgrade_Active_To_Unhealthy_OnTCPFailure
TestAdaptive_Downgrade_Degraded_To_Unhealthy_OnPersistentFailure
```

#### 升级测试

**单元测试**:
```go
TestAdaptive_Upgrade_Unhealthy_To_Degraded_OnL2Success
TestAdaptive_Upgrade_Degraded_To_Active_After5Success
TestAdaptive_Upgrade_QuotaRecovery_Immediate
```

---

## 第五部分：集成测试（Mock供应商）

### 5.1 Mock供应商行为

**实现**:
```go
type MockProvider struct {
    behavior string // "healthy" / "slow" / "error" / "quota_exhausted"
    latency  time.Duration
    errorRate float64 // 0.0 - 1.0
}

func (m *MockProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    switch m.behavior {
    case "healthy":
        time.Sleep(m.latency)
        json.NewEncoder(w).Encode(openai.ChatCompletionResponse{...})
    case "slow":
        time.Sleep(20 * time.Second)
        json.NewEncoder(w).Encode(openai.ChatCompletionResponse{...})
    case "error":
        w.WriteHeader(500)
    case "quota_exhausted":
        w.Header().Set("Retry-After", "60")
        w.WriteHeader(429)
    }
}
```

### 5.2 场景测试

#### 场景1：供应商逐渐变慢

**测试**:
```go
TestIntegration_ProviderSlowdown_AdaptiveResponse
```

**步骤**:
```
1. 初始：3个Provider，延时均为500ms
2. 时间T1：ProviderA延时提升到5s
3. 验证：L3检测触发
4. 验证：ProviderA权重降低
5. 时间T2：100次请求，ProviderA收到<20次
6. 时间T3：ProviderA延时恢复到500ms
7. 验证：权重恢复
8. 时间T4：100次请求，分布均匀
```

#### 场景2：供应商突发5xx

**测试**:
```go
TestIntegration_Provider5xxSpike_FastFailover
```

**步骤**:
```
1. 初始：2个Provider正常
2. ProviderA开始返回500（100%错误率）
3. 验证：3次失败后ProviderA=Unhealthy
4. 验证：后续请求全部路由到ProviderB
5. ProviderA恢复正常
6. 验证：L3检测成功，ProviderA→Degraded
7. 验证：5次成功后ProviderA→Active
8. 验证：流量重新分布到两个Provider
```

#### 场景3：供应商Quota耗尽

**测试**:
```go
TestIntegration_ProviderQuotaExhausted_TimedRecovery
```

**步骤**:
```
1. 初始：ProviderA正常
2. ProviderA返回429，Retry-After: 60
3. 验证：ProviderA=Unhealthy
4. 验证：QuotaRecoverAt设置为now+60s
5. 时间+30s：ProviderA仍然Unhealthy
6. 时间+65s：自动触发L2检测
7. L2成功：ProviderA→Active
8. 验证：流量恢复到ProviderA
```

#### 场景4：所有Provider故障（灾难场景）

**测试**:
```go
TestIntegration_AllProvidersDown_GracefulDegradation
```

**步骤**:
```
1. 3个Provider全部返回503
2. 验证：全部标记为Unhealthy
3. 用户请求：返回503（无可用Provider）
4. 验证：错误消息明确说明原因
5. 1个Provider恢复
6. 验证：流量立即切换到恢复的Provider
7. 其他Provider逐步恢复
8. 验证：流量逐步分布到所有Provider
```

---

## 第六部分：真实供应商测试（E2E）

### 6.1 测试环境

**供应商列表**:
- OpenAI (gpt-4, gpt-3.5-turbo)
- Anthropic (claude-3-5-sonnet)
- Doubao (doubao-1.5)
- DeepSeek (deepseek-chat)

**测试配置**:
```yaml
providers:
  - id: openai-prod
    base_url: https://api.openai.com/v1
    credentials:
      - id: cred-openai-1
        api_key: ${OPENAI_API_KEY_1}
      - id: cred-openai-2
        api_key: ${OPENAI_API_KEY_2}
  
  - id: anthropic-prod
    base_url: https://api.anthropic.com
    credentials:
      - id: cred-anthropic-1
        api_key: ${ANTHROPIC_API_KEY_1}
```

### 6.2 E2E测试用例

#### E2E1: 真实延时检测

**测试**:
```bash
# 脚本：scripts/test-real-latency.sh
./test-real-latency.sh --provider=openai --model=gpt-4 --iterations=10
```

**验证**:
- L3轻量推理延时 < 5s
- L4重负载延时 < 20s
- 记录实际延时分布（P50/P90/P99）

#### E2E2: 真实Quota处理

**测试**:
```bash
# 用小额度credential测试quota耗尽
./test-quota-exhaustion.sh --credential=test-low-quota
```

**验证**:
- 收到429后正确解析Retry-After
- QuotaRecoverAt设置正确
- 恢复时间到达后自动重新激活

#### E2E3: 多供应商故障切换

**测试**:
```bash
# 模拟OpenAI不可用，验证切换到Anthropic
./test-failover.sh --kill=openai --model=gpt-4
```

**验证**:
- OpenAI失败后3次内切换到Anthropic
- 用户请求延迟 < 5s（含重试）
- 切换过程用户无感知（返回正常）

---

## 第七部分：性能与压力测试

### 7.1 健康检查开销测试

**目标**: 健康检查不应影响主请求性能

**测试**:
```go
TestPerformance_HealthCheckOverhead_Minimal
```

**验证**:
- 后台L1/L2检查CPU < 5%
- L3检查QPS < 1（每30秒1次）
- L4检查QPS < 0.02（每小时1次）
- 主请求路径延迟增加 < 1ms

### 7.2 权重计算开销测试

**测试**:
```go
BenchmarkWeightedRouting_100Credentials
```

**验证**:
- 100个Credential权重计算 < 100µs
- 路由选择延迟 < 50ns（仍然O(1)）

### 7.3 错误计数器内存测试

**测试**:
```go
TestPerformance_ErrorCounter_MemoryBound
```

**验证**:
- 100个Credential × 60秒窗口 × 4字节 = 24KB
- 总内存增长 < 1MB

---

## 第八部分：测试执行计划

### 8.1 Phase 1: 单元测试（本周）

**任务**:
- [ ] 实现4层健康检查（L1-L4）
- [ ] 实现5xx即时检测
- [ ] 实现动态权重计算
- [ ] 实现自适应降级/升级
- [ ] 60个单元测试（覆盖率>80%）

**验收**:
```bash
go test ./domains/health/... -v -count=1
go test ./domains/routing/... -run="Weighted" -v
```

### 8.2 Phase 2: 集成测试（下周）

**任务**:
- [ ] 实现MockProvider服务器
- [ ] 4个场景测试（变慢/5xx/quota/全故障）
- [ ] 端到端流程验证

**验收**:
```bash
go test ./integration/... -v -count=1
```

### 8.3 Phase 3: 真实供应商测试（2周后）

**任务**:
- [ ] 配置真实供应商credentials
- [ ] E2E延时测试（OpenAI+Anthropic）
- [ ] E2E quota测试
- [ ] E2E故障切换测试

**验收**:
```bash
./scripts/test-real-providers.sh --all
```

### 8.4 Phase 4: 生产验证（3周后）

**任务**:
- [ ] 灰度1台服务器
- [ ] 监控健康检查准确性
- [ ] 监控路由权重变化
- [ ] 监控故障恢复时间

**验收**:
- 健康检查准确率 > 99%
- 故障检测延迟 < 5s
- 权重调整生效时间 < 1分钟
- 无误判（健康节点被标记为Unhealthy）

---

## 第九部分：监控与告警

### 9.1 关键指标

| 指标 | 阈值 | 告警级别 |
|------|------|---------|
| 健康检查失败率 | > 5% | P2 |
| L3检测延迟 | > 10s | P3 |
| L4检测延迟 | > 40s | P3 |
| 权重计算延迟 | > 1ms | P2 |
| 误判率（健康→Unhealthy） | > 1% | P1 |
| 恢复延迟（Unhealthy→Active） | > 60s | P2 |

### 9.2 监控仪表板

**指标**:
- 实时健康状态分布（Active/Degraded/Unhealthy）
- 每分钟错误率趋势
- 权重变化曲线
- 检测延迟分布（P50/P90/P99）

---

## 第十部分：文档与交付

### 10.1 代码交付

**新增模块**:
```
domains/health/
  ├── tcp_checker.go          // L1 TCP检查
  ├── http_checker.go         // L2 HTTP检查
  ├── inference_checker.go    // L3+L4 AI检查
  ├── adaptive_manager.go     // 降级/升级逻辑
  └── *_test.go              // 单元测试

domains/routing/
  ├── weighted_router.go      // 动态权重路由
  ├── error_counter.go        // 滑动窗口计数
  └── *_test.go

integration/
  ├── mock_provider.go        // Mock供应商
  ├── scenarios_test.go       // 4个场景测试
  └── e2e_test.go            // E2E测试
```

### 10.2 文档交付

- [ ] 本文档（comprehensive-test-plan.md）
- [ ] 健康检查设计文档（health-check-design.md）
- [ ] 动态路由设计文档（weighted-routing-design.md）
- [ ] 测试报告（test-report-YYYY-MM-DD.md）

---

**文档版本**: v1.0  
**最后更新**: 2026-07-22 08:00 UTC+8  
**下次审查**: 实现Phase 1后更新
