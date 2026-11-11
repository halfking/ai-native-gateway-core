# 将 claude-fable-5-2 纳入主动探测系统 - 操作指南

**文档版本**: v1.0  
**创建时间**: 2026-11-11 17:10 +08  
**目标**: 将 claude-fable-5-2 纳入可信度评估，获取正式评分

---

## 执行摘要

**当前状态**: claude-fable-5-2 仅有被动指纹数据（推断可信度88/100），未纳入主动探测  
**目标状态**: 启用主动探测，获取官方可信度评分（identity/capability/consistency）  
**预期收益**: 完整的可信度评估、性能基线、异常检测能力

---

## 一、背景与现状

### 1.1 当前状态

**claude-fable-5-2 数据现状**:
- ✅ 被动指纹: 已采集（3267样本，14.9小时）
- ✅ 推断可信度: 88/100
- ✅ 性能数据: ITT P50=5ms, 吞吐104tok/s
- ⚠️ TTFT异常: 14.9秒（已确认为模型特性）
- ❌ 主动探测: **未启用**
- ❌ 官方评分: **无**

**对比 claude-fable-5-1**:
```
fable-5-1:
  ✅ 主动探测: 已启用
  ✅ 可信度评分: 100分
  ✅ Identity Score: 100
  ✅ Capability Score: 100
  ✅ Verdict: verified

fable-5-2:
  ❌ 主动探测: 未启用
  ❌ 可信度评分: 无
  ⚠️ 推断评分: 88（基于被动指纹）
```

### 1.2 为什么需要纳入主动探测

**1. 获取正式评分**
- 当前只有基于被动指纹的推断评分（88/100）
- 缺少identity/capability维度的官方评估
- 无法确认模型是否存在掺水风险

**2. 建立性能基线**
- 被动指纹依赖真实流量，采样不均匀
- 主动探测可以在固定条件下测量
- 建立稳定的性能基准

**3. 异常检测能力**
- 主动探测可以定期检测模型漂移
- 及时发现上游切换或降级
- 配合被动指纹形成双重验证

**4. 业务价值**
- fable-5-2是凭据31的主力模型（96.7%流量）
- 样本量大（4086次），重要性高
- 需要完整的质量保障

---

## 二、系统架构理解

### 2.1 可信度评估系统组件

```
┌─────────────────────────────────────────────────────────────┐
│                  可信度评估系统架构                          │
└─────────────────────────────────────────────────────────────┘

1. 扫描范围控制:
   routing_policy.featured_models (数组)
   └─ 当前状态: 空数组 {}
   └─ scope设置: "featured" | "all"

2. ModelCredibilityWorker:
   ├─ 定期扫描（默认6小时）
   ├─ 每节点发一次标定请求
   └─ 写入 provider_model_credibility

3. 触发方式:
   A. 定期扫描（cycle_hours）
   B. 探测成功后触发（probe success）
   C. 完整性异常触发（integrity anomaly）

4. 数据存储:
   ├─ provider_model_credibility: 最新评分
   └─ provider_model_credibility_runs: 历史记录
```

### 2.2 当前配置分析

**featured_models 列表**:
```sql
SELECT featured_models FROM routing_policy WHERE tenant_id = 'default';
-- 结果: {}  (空数组)
```

**含义**:
- 当前没有任何模型在featured列表中
- 如果`model_credibility.scope = "featured"`，则不会扫描任何模型
- 如果`model_credibility.scope = "all"`，则扫描所有可路由模型

**可信度配置**（使用默认值）:
```
model_credibility.enabled: false (默认)
model_credibility.scope: "featured" (默认)
model_credibility.cycle_hours: 6 (默认)
```

---

## 三、操作方案

### 方案A: 添加到 featured_models（推荐）

**优点**:
- ✅ 精确控制扫描范围
- ✅ 成本可控（只扫描指定模型）
- ✅ 符合系统设计意图

**缺点**:
- ⚠️ 需要SQL操作
- ⚠️ 需要维护featured列表

**操作步骤**:

#### 步骤1: 查看当前featured列表
```sql
SELECT tenant_id, featured_models, updated_at 
FROM routing_policy 
WHERE tenant_id = 'default';
```

#### 步骤2: 添加 claude-fable-5-2 到列表
```sql
UPDATE routing_policy 
SET featured_models = ARRAY[
    'claude-fable-5-2',
    'claude-fable-5-1',  -- 已有的5-1
    'claude-opus-5',
    'claude-sonnet-5'
    -- 可以添加其他重要模型
],
updated_at = NOW()
WHERE tenant_id = 'default';
```

#### 步骤3: 确认可信度系统启用
```sql
-- 查询当前设置
SELECT key, value FROM system_settings 
WHERE key LIKE 'model_credibility%';

-- 如果需要，启用可信度系统
-- (注意：这可能影响成本，需要运维团队确认)
INSERT INTO system_settings (key, value, updated_at)
VALUES ('model_credibility.enabled', 'true', NOW())
ON CONFLICT (key) DO UPDATE 
SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at;
```

#### 步骤4: 验证配置
```sql
SELECT tenant_id, featured_models FROM routing_policy 
WHERE tenant_id = 'default';
```

**预期结果**:
- featured_models包含claude-fable-5-2
- 6小时后自动扫描一次
- 结果写入provider_model_credibility表

---

### 方案B: 使用 scope="all"（替代方案）

**优点**:
- ✅ 无需修改数据库
- ✅ 扫描所有模型，覆盖全面

**缺点**:
- ⚠️ 成本较高（扫描所有可路由模型）
- ⚠️ 扫描时间更长

**操作步骤**:

#### 步骤1: 修改scope设置
```sql
INSERT INTO system_settings (key, value, updated_at)
VALUES ('model_credibility.scope', 'all', NOW())
ON CONFLICT (key) DO UPDATE 
SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at;
```

#### 步骤2: 确认启用
```sql
INSERT INTO system_settings (key, value, updated_at)
VALUES ('model_credibility.enabled', 'true', NOW())
ON CONFLICT (key) DO UPDATE 
SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at;
```

**注意事项**:
- ⚠️ 会扫描所有模型，成本较高
- ⚠️ 建议先评估模型数量和成本

---

### 方案C: 手动触发单次扫描（快速验证）

**优点**:
- ✅ 立即获取结果
- ✅ 无需修改配置
- ✅ 适合测试验证

**缺点**:
- ⚠️ 仅一次性，不自动重复
- ⚠️ 需要API或admin工具支持

**操作方式**:
```bash
# 如果有admin API
curl -X POST http://localhost:8781/admin/credibility/scan \
  -H "Content-Type: application/json" \
  -d '{
    "tenant_id": "default",
    "credential_id": 31,
    "model_name": "claude-fable-5-2"
  }'
```

**注意**: 需要确认是否有对应的admin接口

---

## 四、验证与监控

### 4.1 验证扫描是否执行

**查询最近的扫描记录**:
```sql
SELECT 
  raw_model_name,
  credibility_score,
  identity_score,
  capability_score,
  verdict,
  measured_at,
  updated_at
FROM provider_model_credibility
WHERE raw_model_name = 'claude-fable-5-2'
ORDER BY updated_at DESC
LIMIT 5;
```

**查询扫描历史**:
```sql
SELECT 
  raw_model_name,
  credibility_score,
  verdict,
  measured_at
FROM provider_model_credibility_runs
WHERE raw_model_name = 'claude-fable-5-2'
ORDER BY measured_at DESC
LIMIT 10;
```

### 4.2 预期结果

**首次扫描后（约6小时内）**:
```sql
raw_model_name: claude-fable-5-2
credibility_score: 85-100 (预期)
identity_score: 90-100 (预期)
capability_score: 85-100 (预期)
consistency_score: 80-95 (预期，因TTFT波动)
verdict: "verified" | "unknown" | "suspicious"
```

**与被动指纹对比**:
| 维度 | 被动指纹（推断） | 主动探测（预期） |
|------|------------------|------------------|
| 样本充足性 | 100/100 | N/A |
| 数据一致性 | 85/100 | 90-100 |
| 性能稳定性 | 80/100 | 80-95 |
| Identity | N/A | 90-100 |
| Capability | N/A | 85-100 |
| **综合** | **88/100** | **85-100** |

### 4.3 监控指标

**关键指标**:
1. `credibility_score`: 综合可信度评分
2. `identity_score`: 身份匹配度
3. `capability_score`: 能力匹配度
4. `verdict`: verified/unknown/suspicious
5. `measured_at`: 最后扫描时间

**告警条件**:
- credibility_score < 70
- identity_score < 80
- verdict = "suspicious"
- measured_at > 24小时前（扫描未执行）

---

## 五、成本评估

### 5.1 扫描成本

**单次扫描成本**（每节点）:
- 请求数: 1次
- 提示词长度: ~50 tokens
- 输出长度: ~20 tokens
- 总token: ~70 tokens
- 成本: 约 0.001-0.01 USD（取决于模型定价）

**claude-fable-5-2 @ 凭据31**:
- 节点数: 1
- 扫描频率: 6小时/次 = 4次/天
- 日成本: 0.004-0.04 USD
- 月成本: 0.12-1.2 USD

**如果使用 scope="all"**:
- 假设总节点数: 100
- 日成本: 0.4-4 USD
- 月成本: 12-120 USD

### 5.2 成本优化建议

**方案1: featured 模式（推荐）**
- 只扫描重要模型（5-10个）
- 月成本: <5 USD
- 性价比最高

**方案2: 调整频率**
```sql
INSERT INTO system_settings (key, value, updated_at)
VALUES ('model_credibility.cycle_hours', '12', NOW())
ON CONFLICT (key) DO UPDATE 
SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at;
```
- 12小时扫描一次（降低50%成本）
- 仍能及时发现问题

---

## 六、推荐操作流程

### 阶段1: 准备与验证（5分钟）

**1. 确认当前状态**
```sql
-- 查看featured列表
SELECT featured_models FROM routing_policy WHERE tenant_id = 'default';

-- 查看可信度设置
SELECT key, value FROM system_settings 
WHERE key LIKE 'model_credibility%';
```

**2. 评估成本**
- 确认要添加的模型数量
- 计算预期月成本
- 获得团队批准

### 阶段2: 配置实施（10分钟）

**推荐配置**（方案A - featured模式）:

```sql
-- 1. 添加到featured列表
UPDATE routing_policy 
SET featured_models = ARRAY[
    'claude-fable-5-2',
    'claude-fable-5-1',
    'claude-opus-5',
    'claude-sonnet-5'
],
updated_at = NOW()
WHERE tenant_id = 'default';

-- 2. 启用可信度系统（如果尚未启用）
INSERT INTO system_settings (key, value, updated_at)
VALUES 
    ('model_credibility.enabled', 'true', NOW()),
    ('model_credibility.scope', 'featured', NOW()),
    ('model_credibility.cycle_hours', '6', NOW())
ON CONFLICT (key) DO UPDATE 
SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at;

-- 3. 验证配置
SELECT tenant_id, featured_models FROM routing_policy WHERE tenant_id = 'default';
SELECT key, value FROM system_settings WHERE key LIKE 'model_credibility%';
```

### 阶段3: 验证与监控（首次扫描后）

**等待首次扫描**（6小时内）:
```sql
-- 查询是否有扫描记录
SELECT 
  raw_model_name,
  credibility_score,
  identity_score,
  verdict,
  measured_at
FROM provider_model_credibility
WHERE raw_model_name = 'claude-fable-5-2';
```

**如果6小时后仍无数据**:
1. 检查worker日志
```bash
ssh 211 "docker logs --since 6h llm-gateway-211-b 2>&1" | grep -i credibility
```

2. 确认worker是否启动
3. 检查配置是否生效

### 阶段4: 持续监控（长期）

**每日检查**:
- credibility_score >= 70
- verdict != "suspicious"
- measured_at < 24小时前

**每周回顾**:
- 对比被动指纹与主动探测数据
- 评估TTFT趋势
- 检查异常告警

---

## 七、注意事项与风险

### 7.1 注意事项

⚠️ **TTFT特性**:
- claude-fable-5-2 的TTFT异常高（15秒）
- 这是模型特性，非系统问题
- 主动探测结果应注明此特性

⚠️ **成本控制**:
- 使用featured模式控制成本
- 避免不必要的高频扫描
- 定期审查featured列表

⚠️ **数据对比**:
- 主动探测结果可能与被动指纹有差异
- 被动指纹反映真实流量
- 主动探测使用标准化测试
- 两者互补，不应矛盾

### 7.2 风险与缓解

**风险1: 扫描失败**
- 原因: 凭据失效、上游API故障
- 缓解: 监控扫描成功率，设置告警

**风险2: 成本超支**
- 原因: scope="all"扫描过多模型
- 缓解: 使用featured模式，定期审查

**风险3: 评分异常**
- 原因: TTFT高导致consistency_score低
- 缓解: 在评估中注明TTFT特性

---

## 八、总结与建议

### 8.1 推荐方案

**✅ 推荐：方案A - featured模式**

**理由**:
1. 成本可控（<5 USD/月）
2. 精确控制扫描范围
3. 符合系统设计
4. 易于维护

**操作**:
```sql
UPDATE routing_policy 
SET featured_models = ARRAY['claude-fable-5-2', 'claude-fable-5-1', 'claude-opus-5', 'claude-sonnet-5']
WHERE tenant_id = 'default';

-- 确保启用
INSERT INTO system_settings (key, value, updated_at)
VALUES ('model_credibility.enabled', 'true', NOW())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();
```

### 8.2 预期效果

**6小时后**:
- ✅ 获得首次可信度评分
- ✅ identity/capability维度完整数据
- ✅ 与被动指纹形成对比

**1周后**:
- ✅ 建立稳定的评分基线
- ✅ 验证被动指纹推断准确性
- ✅ 形成完整的质量画像

**长期**:
- ✅ 持续监控模型质量
- ✅ 及时发现异常
- ✅ 辅助路由决策

### 8.3 后续行动

**立即执行** (P0):
1. [ ] 与团队确认方案和成本
2. [ ] 执行SQL添加到featured列表
3. [ ] 验证配置生效

**6小时后** (P1):
4. [ ] 检查首次扫描结果
5. [ ] 对比被动指纹数据
6. [ ] 更新文档

**1周后** (P2):
7. [ ] 回顾评分稳定性
8. [ ] 评估TTFT影响
9. [ ] 完善监控告警

---

**文档状态**: ✅ 完成  
**批准状态**: ⏳ 待运维团队确认  
**执行状态**: ⏳ 待执行  

**联系人**: 运维团队  
**下一步**: 团队会议讨论方案
