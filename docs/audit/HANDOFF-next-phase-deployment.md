# 下一轮工作提示词 - Auto Model Routing V3 集成验证与部署

## 背景

已完成P0修复：
- ✅ 桥接V2/V3词汇表差异（添加11个V2任务类型到tier映射）
- ✅ 集成TierSelector到Decider（添加tierSelector字段和相关方法）
- ✅ 添加EnableTierSelection特性开关（默认关闭，支持灰度）
- ✅ 创建25+集成测试，全部通过
- ✅ 代码已提交推送 (commit 0277fbff6)
- ✅ **实现X-Gw-Agent-Depth header提取（P1优先级，2026-11-16）**
  - ✅ 在autoroute/affinity.go中添加WithAgentDepth和agentDepthFromContext helpers
  - ✅ 在domains/streaming/auto_route.go中添加header提取和context传递
  - ✅ 在domains/streaming/auto_route_nonchat.go中为/v1/messages和/v1/responses添加支持
  - ✅ 在autoroute/decision.go中调用tierSelector.SelectTier并填充Decision的Tier字段
  - ✅ 在autoroute/decision_v2.go中添加相同的tier selection逻辑
  - ✅ 添加E2E-4集成测试用例验证X-Gw-Agent-Depth header处理

**当前状态**：单元测试完整，X-Gw-Agent-Depth header提取已实现，缺少端到端验证。系统可安全启用，但未在真实环境中验证功能。

---

## 下一步任务清单

### 阶段1: 开发环境集成测试（优先级：P0，预计1-2天）

```bash
# 1. 部署到开发环境
cd /c/workspace/ai-native-tools/llm-gateway/llm-gateway-go
git pull origin main
go build -o llm-gateway cmd/gateway/*.go

# 2. 启用tier selection
export AUTO_ENABLE_TIER_SELECTION=true

# 3. 启动网关
./llm-gateway --config config.dev.yaml
```

**验证任务**：
1. **基础功能验证**：
   - 发送 `model="auto"` 请求，检查响应的 `X-Gw-Auto-Decision` header
   - 验证 `Decision.Tier` 字段是否正确填充（tier-a/b/c）
   - 确认 `Decision.TierSource` 和 `TierReason` 有意义的值

2. **任务类型映射验证**：
   ```bash
   # 测试reasoning任务 → tier-a
   curl -X POST http://localhost:8080/v1/chat/completions \
     -H "Content-Type: application/json" \
     -H "Authorization: Bearer ${API_KEY}" \
     -d '{
       "model": "auto",
       "messages": [{"role": "user", "content": "证明费马大定理"}]
     }' | jq '.headers."X-Gw-Auto-Decision"'
   
   # 预期: tier="tier-a"
   
   # 测试code任务 → tier-b
   curl -X POST http://localhost:8080/v1/chat/completions \
     -H "Content-Type: application/json" \
     -H "Authorization: Bearer ${API_KEY}" \
     -d '{
       "model": "auto",
       "messages": [{"role": "user", "content": "实现快速排序算法"}]
     }' | jq '.headers."X-Gw-Auto-Decision"'
   
   # 预期: tier="tier-b"
   
   # 测试creative任务 → tier-c
   curl -X POST http://localhost:8080/v1/chat/completions \
     -H "Content-Type: application/json" \
     -H "Authorization: Bearer ${API_KEY}" \
     -d '{
       "model": "auto",
       "messages": [{"role": "user", "content": "写一篇关于AI的文章"}]
     }' | jq '.headers."X-Gw-Auto-Decision"'
   
   # 预期: tier="tier-c"
   ```

3. **Header override验证**：
   ```bash
   curl -X POST http://localhost:8080/v1/chat/completions \
     -H "Content-Type: application/json" \
     -H "Authorization: Bearer ${API_KEY}" \
     -H "X-Gw-Model-Tier: tier-a" \
     -d '{
       "model": "auto",
       "messages": [{"role": "user", "content": "写README文档"}]
     }' | jq '.headers."X-Gw-Auto-Decision"'
   
   # 预期: tier="tier-a", source="header" (覆盖documentation的默认tier-c)
   ```

4. **数据库tier配置验证**：
   ```sql
   -- 连接到开发数据库
   psql -h localhost -U postgres -d llm_gateway_dev
   
   -- 查看tier配置
   SELECT * FROM task_type_tier_config ORDER BY task_type;
   
   -- 应该看到10个V3 task type的配置行
   -- 验证preferred_tier和fallback_tiers字段
   ```

5. **日志验证**：
   ```bash
   # 检查网关日志，确认tier selection被调用
   tail -f logs/gateway.log | grep -i "tier"
   
   # 应该看到类似的日志：
   # INFO tier_selector selected tier tier=tier-b source=default reason="task_type=code → tier-b"
   ```

---

### 阶段2: 监控指标配置（优先级：P1，预计1天）

**创建Grafana仪表板**：

```json
{
  "dashboard": {
    "title": "Auto Model Routing - Tier Distribution",
    "panels": [
      {
        "title": "Tier Distribution (Last 1h)",
        "targets": [{
          "rawSql": "SELECT (auto_decision->>'tier') as tier, COUNT(*) FROM request_logs WHERE created_at >= NOW() - INTERVAL '1 hour' AND model = 'auto' GROUP BY tier"
        }]
      },
      {
        "title": "Cost Estimation by Tier",
        "targets": [{
          "rawSql": "SELECT (auto_decision->>'tier') as tier, SUM(total_tokens) * CASE WHEN auto_decision->>'tier' = 'tier-a' THEN 30 WHEN auto_decision->>'tier' = 'tier-b' THEN 10 ELSE 2 END / 1000000 as cost_usd FROM request_logs WHERE created_at >= NOW() - INTERVAL '24 hours' GROUP BY tier"
        }]
      },
      {
        "title": "Tier Source Distribution",
        "targets": [{
          "rawSql": "SELECT (auto_decision->>'tier_source') as source, COUNT(*) FROM request_logs WHERE created_at >= NOW() - INTERVAL '1 hour' AND auto_decision->>'tier' IS NOT NULL GROUP BY source"
        }]
      }
    ]
  }
}
```

**设置告警**：
```yaml
# alerts.yaml
alerts:
  - name: TierDistributionAbnormal
    condition: |
      (tier_a_percentage < 15 OR tier_a_percentage > 25) OR
      (tier_c_percentage < 30)
    message: "Tier distribution outside expected range: tier-a should be 20% ±5%, tier-c should be >30%"
    
  - name: TierSelectionFailureRate
    condition: |
      tier_selection_errors / total_requests > 0.01
    message: "Tier selection failure rate > 1%"
```

---

### 阶段3: 灰度发布（优先级：P1，预计1周）

**10% 灰度**：
```bash
# 在生产环境的一个实例上启用
# instance-1 (10% traffic)
export AUTO_ENABLE_TIER_SELECTION=true

# 其他9个实例保持关闭
export AUTO_ENABLE_TIER_SELECTION=false
```

**监控清单**（24小时）：
- [ ] Tier分布是否符合预期？（20%/40%/40% ±5%）
- [ ] 错误率有无增加？（目标：<0.1%增量）
- [ ] P95延迟有无增加？（目标：<10ms增量）
- [ ] 用户投诉有无增加？
- [ ] 成本有无下降？（初步估算）

**决策点**：
- ✅ 指标正常 → 扩大到50%
- ❌ 指标异常 → 回滚（设置flag=false），分析问题

**50% 灰度**（48小时后）：
- 如果10%灰度成功，扩大到50%
- 继续监控相同指标
- 收集更多成本数据

**100% 全量**（1周后）：
- 如果50%灰度成功，全量发布
- 更新默认配置：`AUTO_ENABLE_TIER_SELECTION=true`

---

### 阶段4: P1遗留工作（优先级：P1，预计3-5天）

#### 任务4.1: 提取X-Gw-Agent-Depth header

**文件修改**：`cmd/gateway/main.go` 或相应的HTTP handler

```go
// 在请求处理开始时提取header
func extractContextHeaders(r *http.Request) map[string]string {
    headers := make(map[string]string)
    
    // 现有headers...
    
    // 新增: Agent Depth
    if depth := r.Header.Get("X-Gw-Agent-Depth"); depth != "" {
        headers["agent_depth"] = depth
    }
    
    return headers
}

// 在调用Decider.Decide()时传递
func (h *Handler) handleAutoRoute(ctx context.Context, r *http.Request) {
    headers := extractContextHeaders(r)
    
    // 转换depth为int
    agentDepth := 0
    if depthStr, ok := headers["agent_depth"]; ok {
        if d, err := strconv.Atoi(depthStr); err == nil {
            agentDepth = d
        }
    }
    
    // 传递给tier selector
    tierInput := autoroute.TierSelectionInput{
        TaskType:   classification.Primary,
        Confidence: classification.Confidence,
        AgentDepth: agentDepth,  // 新增
        TenantID:   getTenantID(ctx),
        HeaderTier: r.Header.Get("X-Gw-Model-Tier"),
    }
    
    tierResult, _ := h.decider.GetTierSelector().SelectTier(ctx, tierInput)
}
```

**测试**：
```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "X-Gw-Agent-Depth: 2" \
  -d '{
    "model": "auto",
    "messages": [{"role": "user", "content": "设计系统架构"}]
  }' | jq '.headers."X-Gw-Auto-Decision"'

# 预期: tier="tier-c", source="depth", reason包含"depth-based degradation"
```

#### 任务4.2: V3 Classifier集成（可选，未来迭代）

```go
// cmd/gateway/main.go

// 添加feature flag
enableV3Classifier := getEnvBool("AUTO_ENABLE_V3_CLASSIFIER", false)

var classifier autoroute.Classifier
if enableV3Classifier {
    // 使用V3 10-category classifier
    keywords := autoroute.DefaultV3Keywords()
    thresholds := autoroute.DefaultHeuristicThresholds()
    classifier = autoroute.NewV3Classifier(keywords, thresholds, true)
    log.Info("using V3 classifier (10 categories)")
} else {
    // 使用现有V2 classifier
    classifier = autoroute.NewHeuristicClassifierWithTuning(tuningStore)
    log.Info("using V2 classifier (11 categories)")
}

decider := autoroute.NewDecider(classifier, fallback, index, profileStore)
```

---

### 阶段5: 成果验收（优先级：P0）

**验收标准**：
1. ✅ 开发环境E2E测试通过（手工验证6个场景）
2. ✅ 监控仪表板配置完成，数据正常展示
3. ✅ 10%灰度24小时无异常
4. ✅ 50%灰度48小时无异常
5. ✅ Tier分布符合预期 (20%±5% / 40%±5% / 40%±5%)
6. ✅ 成本节约 >30% (实际数据验证)
7. ✅ P95延迟增量 <10ms
8. ✅ 错误率增量 <0.1%

**交付物**：
1. ✅ 端到端测试报告 (docs/audit/2026-11-16-maestro-e2e-test-plan.md)
2. ⏳ 灰度发布报告 (包含监控截图和数据分析)
3. ⏳ 成本节约验证报告 (实际vs预期对比)
4. ⏳ 运维手册更新 (feature flag配置说明)
5. ⏳ 用户文档更新 (X-Gw-Model-Tier header使用说明)

---

## 快速启动命令

### 本地验证
```bash
# 1. 确保代码最新
cd /c/workspace/ai-native-tools/llm-gateway/llm-gateway-go
git pull origin main

# 2. 运行单元测试（快速验证）
go test ./autoroute/ -run "TestTierSelector" -v

# 3. 构建
go build -o llm-gateway cmd/gateway/*.go

# 4. 启用tier selection并启动
export AUTO_ENABLE_TIER_SELECTION=true
./llm-gateway --config config.dev.yaml

# 5. 在另一个终端测试
./scripts/test-tier-selection.sh
```

### 生产灰度
```bash
# SSH到生产实例
ssh deploy@prod-gateway-01

# 设置feature flag
echo "export AUTO_ENABLE_TIER_SELECTION=true" >> /etc/llm-gateway/env

# 重启服务
systemctl restart llm-gateway

# 监控日志
tail -f /var/log/llm-gateway/gateway.log | grep -i tier

# 检查监控
# 访问 https://grafana.internal/d/auto-routing-tiers
```

---

## 关键决策点

1. **何时全量发布？**
   - 条件：10%灰度24h + 50%灰度48h 均正常
   - 指标：tier分布、错误率、延迟、成本均在目标范围内
   - 决策人：需要技术负责人审批

2. **如何回滚？**
   ```bash
   # 方法1: 关闭feature flag（推荐）
   export AUTO_ENABLE_TIER_SELECTION=false
   systemctl restart llm-gateway
   
   # 方法2: 回滚代码（如果发现严重bug）
   git revert 0277fbff6
   git push origin main
   # 重新部署
   ```

3. **何时集成V3 Classifier？**
   - 前置条件：Tier selection在生产稳定运行1周+
   - 时机：下一个Sprint
   - 策略：shadow mode → 灰度 → 全量

---

## 风险缓解

| 风险 | 缓解措施 | 回滚计划 |
|------|---------|---------|
| Tier分布异常 | 调整TaskTypeTierMapping | 暂停灰度，分析数据 |
| 延迟显著增加 | 优化TierSelector | 关闭flag回滚 |
| 成本不降反升 | 检查tier-a使用率 | 调整tier映射 |
| 生产故障 | Feature flag快速关闭 | 1分钟内回滚 |

---

## 联系人

- **技术负责人**: [待填写]
- **运维负责人**: [待填写]
- **监控负责人**: [待填写]

---

**文档版本**: 1.0  
**创建日期**: 2026-11-16  
**最后更新**: 2026-11-16  
**状态**: 待执行
