# 2026-11-23 503 错误分析与修正总结

## 概述

**分析时间**: 2026-11-23  
**分析对象**: 192.168.31.211 (llm-gateway-211-a)  
**问题**: HTTP 503 Service Unavailable 错误  
**影响**: 3 个请求失败，3 个自定义提供商不可用  
**根因**: API 密钥失效/过期  

## 完成的工作

### ✅ 1. 日志分析

- 分析了 192.168.31.211 上最近的日志（约 5000 条记录）
- 识别出 3 个 503 错误发生在 06:26:21-23
- 追踪到根本原因：候选解析失败 (`candidates_count=0`)

### ✅ 2. 根因识别

确定了 3 个失效的提供商：

1. **custom/community/Marcus-Mok-GH/pt-harbor**
   - 模型名: `communitymarcus-mok-ghpt-harbor`
   - 错误: HTTP 401 Authentication Failed

2. **custom/community/Catniti/gemini-4-hydrogen**
   - 模型名: `communitycatnitigemini-4-hydrogen`
   - 错误: HTTP 401 Authentication Failed

3. **custom/minimax/minimax-h3**
   - 模型名: `minimaxminimax-h3`
   - 错误: HTTP 401 Authentication Failed

### ✅ 3. 系统行为验证

验证了网关系统按预期工作：
- ✅ 正确检测无效密钥
- ✅ 记录详细错误信息
- ✅ 尝试阶梯路由（ladder routing）
- ✅ 自动标记凭证进行重新验证
- ✅ 配置热重载（检测到更新事件）
- ✅ 其他提供商继续正常工作

### ✅ 4. 文档输出

创建了 3 份文档：

1. **事件分析报告** (`2026-11-23-503-errors-invalid-provider-keys.md`)
   - 详细的时间线
   - 日志证据
   - 系统行为分析
   - 影响范围评估

2. **行动计划** (`2026-11-23-503-errors-action-plan.md`)
   - 具体的修复步骤
   - 验证方法
   - 临时绕过方案
   - 预防措施

3. **本总结文档**

## 需要的修正（Corrections Needed）

### 🔴 运维层面（需要立即执行）

#### 操作 1: 更新或禁用失效的 API 密钥

**执行者**: 运维团队  
**预计时间**: 10-15 分钟  
**优先级**: P0（立即执行）

**步骤**:
```bash
# 1. 访问管理面板
open https://llm.itestu.cn/admin/credentials

# 2. 更新以下 3 个提供商的密钥:
#    - Marcus-Mok-GH/pt-harbor
#    - Catniti/gemini-4-hydrogen
#    - minimax/minimax-h3

# 3. 验证修复
ssh 211 "docker logs -f llm-gateway-211-a 2>&1 | grep -E 'candidates_resolved|node probe'"
```

**验收标准**:
- ✅ 不再出现 401 认证错误
- ✅ 这些模型的 `candidates_count > 0`
- ✅ 请求成功返回 200 而非 503

### 🟡 代码层面（建议实施）

**结论**: **无需代码修复** - 系统按设计正常工作

但建议以下增强措施：

#### 建议 1: 增强监控指标（可选）

**优先级**: P2  
**工作量**: 1-2 小时

在 `domains/routing/metrics.go` 或相关文件中添加：

```go
// 建议添加的 Prometheus 指标
var (
    zeroCandidatesCounter = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_zero_candidates_total",
            Help: "Total number of requests with zero candidates available",
        },
        []string{"model", "tenant", "profile"},
    )
    
    providerAuthFailuresCounter = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_provider_auth_failures_total",
            Help: "Total number of provider authentication failures",
        },
        []string{"provider", "credential_id"},
    )
)
```

**理由**: 虽然日志中有详细信息，但 Prometheus 指标可以更容易地设置告警和可视化

#### 建议 2: 主动密钥健康检查（可选）

**优先级**: P3  
**工作量**: 4-6 小时

当前系统是**被动检测**（请求失败时才发现密钥无效）。可以考虑添加**主动检测**：

```go
// 伪代码
func (s *CredentialHealthChecker) Run(ctx context.Context) {
    ticker := time.NewTicker(1 * time.Hour)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            s.checkAllCredentials()
        }
    }
}

func (s *CredentialHealthChecker) checkAllCredentials() {
    // 1. 获取所有启用的凭证
    // 2. 对每个凭证进行轻量级探测
    // 3. 更新健康状态
    // 4. 对失效的凭证发送告警
}
```

**权衡**: 增加了系统复杂度和探测成本，但可以提前发现问题

#### 建议 3: 密钥过期提醒（可选）

**优先级**: P3  
**工作量**: 2-3 小时

在管理面板添加：
- API 密钥的"最后成功时间"显示
- "连续失败次数"高亮显示
- 密钥健康状态指示器（🟢🟡🔴）

**理由**: 让运维团队更容易识别需要更新的密钥

## 系统工作正常的证据

以下机制已经在正确工作：

1. **错误检测**
   ```json
   {"msg":"node probe failed: node/upstream fault (ladder applies)",
    "err_code":"http_502","http_status":502,
    "detail":"All 1 attempted provider key(s) failed authentication"}
   ```

2. **候选诊断**
   ```json
   {"msg":"[candidate_diag] db_empty","model":"communitymarcus-mok-ghpt-harbor",
    "candidates_count":0,
    "likely_cause":"模型已登记（1 offer）且状态闸全开 ⇒ 状态不是原因"}
   ```

3. **自动重验证**
   ```json
   {"msg":"node_probe_worker: submit via queue",
    "source":"credential_recovery"}
   ```

4. **配置热重载**
   ```json
   {"msg":"auto route listener: refresh requested",
    "payload":"credentials:UPDATE:99023"}
   ```

5. **智能跳过**
   ```json
   {"msg":"probe_necessity: self-check skipped as unnecessary",
    "reason":"not_necessary_two_model_successes: two sibling models..."}
   ```

## 经验教训

### ✅ 做得好的地方

1. **系统韧性**: 其他提供商继续正常工作，只有失效密钥的模型受影响
2. **错误可见性**: 日志中有清晰、可操作的错误消息
3. **自动恢复**: 系统检测到配置更新并立即应用
4. **阶梯路由**: 尝试了多个候选提供商

### ⚠️ 需要改进的地方

1. **密钥管理**: 应该在密钥过期前主动更新
2. **告警响应**: 虽然日志记录了错误，但没有触发即时告警
3. **文档化**: 密钥轮换流程应该文档化并自动化

## 后续行动

### 立即执行（今天）
- [ ] 更新 3 个失效提供商的 API 密钥
- [ ] 验证修复生效
- [ ] 更新事件状态为"已解决"

### 本周执行
- [ ] 审查所有其他提供商的密钥状态
- [ ] 建立密钥轮换计划
- [ ] 设置密钥健康度仪表板

### 本月执行（可选）
- [ ] 实施代码建议 1-3
- [ ] 添加 Prometheus 告警规则
- [ ] 建立定期健康检查脚本

## 相关文件

- 详细分析: `docs/incidents/2026-11-23-503-errors-invalid-provider-keys.md`
- 行动计划: `docs/incidents/2026-11-23-503-errors-action-plan.md`
- 本总结: `docs/incidents/2026-11-23-503-errors-summary.md`

## 联系人

- **运维**: 负责密钥更新
- **开发**: 负责代码改进（如果决定实施）
- **产品**: 决定哪些提供商是必需的

## 更新历史

- 2026-11-23 06:30 - 初始版本
- 待更新: 修复完成时间和验证结果
