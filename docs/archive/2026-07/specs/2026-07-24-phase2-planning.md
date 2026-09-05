---
archived_from: docs/2026-07-24-phase2-planning.md
archived_at: 2026-08-17
archived_by: docs-archive v1.0
backup_ts: 20260817-190606
status: archived
---

> 本文档已归档，原文保持不变。

# Phase 2: 压力信号反馈机制 - 规划文档

**日期**: 2026-07-24  
**前置条件**: Phase 1 已完成并部署到 245  
**预估时间**: 2-4 周  
**优先级**: Medium（可选优化）

---

## 一、Phase 2 目标

### 1.1 核心目标

将**防封锁层的压力信号**反馈给 **URSM v2 评分系统**，使路由决策能够感知资源压力，避免过度使用接近上限的节点。

### 1.2 业务价值

#### 当前问题
- URSM v2 基于健康状态评分（成功率、延迟、错误码）
- 但不感知**资源压力**（FpSlots 占用率、Limiter 并发饱和度）
- 可能导致：
  - 高分节点被过度使用，直到触发限流
  - FpSlots 耗尽后才降级到其他节点
  - Limiter 饱和后才发现并发过高

#### 预期改善
- 提前感知资源压力，在**接近上限前**降低评分
- 更均衡的负载分布，避免"热点节点"
- 减少因资源耗尽导致的请求失败

---

## 二、技术方案

### 2.1 压力信号来源

#### 信号 1: FpSlots 占用率
**定义**: 虚拟指纹槽位的占用比例

```
FpSlots 压力 = 当前占用槽位数 / 槽位上限 (fp_slot_limit)
```

**示例**:
- `fp_slot_limit = 100`, 当前占用 80 → 压力 = 0.8
- `fp_slot_limit = 0` (无限制) → 压力 = 0

**数据源**: `credentialfpslot.Manager`

#### 信号 2: Limiter 并发饱和度
**定义**: 4 层 Limiter 的并发占用比例

```
Limiter 压力 = max(
  Global层占用率,
  Pool层占用率,
  Credential层占用率,
  Identity层占用率
)
```

**示例**:
- Credential 层：`concurrency_limit = 50`, 当前 in-flight = 45 → 压力 = 0.9
- Global 层：无限制 → 压力 = 0

**数据源**: `limiter.Manager`

#### 信号 3: RPM 使用率（可选）
**定义**: 每分钟请求数的使用比例

```
RPM 压力 = 当前1分钟请求数 / RPM 上限
```

**优先级**: Low（暂不实现，RPM 通常不是瓶颈）

---

### 2.2 压力信号注入点

#### 方案 A: 注入到 URSM v2 评分系统（推荐）

**位置**: `domains/ursm/v2/scorer.go`

**实现方式**:
```go
// 在 URSM v2 评分时，根据压力信号调整分数
func (s *Scorer) Score(candidate Candidate) float64 {
    baseScore := s.calculateBaseScore(candidate) // 健康状态评分
    
    // 获取压力信号
    fpPressure := s.getFpSlotsPressure(candidate.CredentialID)
    limiterPressure := s.getLimiterPressure(candidate.CredentialID)
    
    // 压力惩罚（压力越高，惩罚越重）
    pressurePenalty := calculatePressurePenalty(fpPressure, limiterPressure)
    
    return baseScore * (1 - pressurePenalty)
}
```

**优点**:
- ✅ 集中管理，逻辑清晰
- ✅ 压力信号只影响评分，不影响可用性判断
- ✅ 易于 A/B 测试（通过 feature flag 开关）

**缺点**:
- ⚠️ URSM v2 需要依赖 FpSlots/Limiter 接口

#### 方案 B: 在 Router 层叠加压力惩罚

**位置**: `domains/streaming/executors/router.go`

**实现方式**:
```go
// 在排序前，根据压力信号调整候选节点的权重
func (r *Router) adjustWeightsByPressure(candidates []provider.Candidate) {
    for i := range candidates {
        fpPressure := r.fpSlotsManager.GetPressure(candidates[i].CredentialID)
        limiterPressure := r.limiterManager.GetPressure(candidates[i].CredentialID)
        
        pressurePenalty := calculatePressurePenalty(fpPressure, limiterPressure)
        candidates[i].Weight = int(float64(candidates[i].Weight) * (1 - pressurePenalty))
    }
}
```

**优点**:
- ✅ URSM v2 保持独立，不增加依赖
- ✅ 实现简单

**缺点**:
- ⚠️ Router 层逻辑变复杂
- ⚠️ 压力惩罚与 URSM v2 评分分离，不统一

#### 推荐方案: **方案 A**

理由：URSM v2 本身就是"评分系统"，压力信号是评分的一部分，集中管理更合理。

---

### 2.3 压力惩罚函数设计

#### 目标
- 压力低时（< 0.5）：几乎无惩罚
- 压力中等时（0.5-0.8）：轻度惩罚
- 压力高时（> 0.8）：重度惩罚

#### 函数设计

```go
// calculatePressurePenalty 计算压力惩罚系数（0-1 之间）
// 压力越高，惩罚越重，返回值越大
func calculatePressurePenalty(fpPressure, limiterPressure float64) float64 {
    // 取两者最大值（最严重的压力）
    maxPressure := math.Max(fpPressure, limiterPressure)
    
    if maxPressure < 0.5 {
        // 低压力：几乎无惩罚
        return 0
    } else if maxPressure < 0.8 {
        // 中压力：线性惩罚 0-30%
        return (maxPressure - 0.5) / 0.3 * 0.3
    } else {
        // 高压力：指数惩罚 30-70%
        excess := maxPressure - 0.8
        return 0.3 + excess / 0.2 * 0.4
    }
}
```

**示例**:
| 压力 | 惩罚系数 | 实际得分 (假设基础 100) |
|------|----------|------------------------|
| 0.3  | 0%       | 100                    |
| 0.5  | 0%       | 100                    |
| 0.65 | 15%      | 85                     |
| 0.8  | 30%      | 70                     |
| 0.9  | 50%      | 50                     |
| 1.0  | 70%      | 30                     |

---

### 2.4 数据采集接口设计

#### FpSlots 压力查询

```go
// credentialfpslot/manager.go

// GetPressure 返回指定 credential 的 FpSlots 压力（0-1）
func (m *Manager) GetPressure(credentialID int) float64 {
    // 查询当前占用的槽位数
    used := m.countUsedSlots(credentialID)
    
    // 查询槽位上限（从配置或 DB 读取）
    limit := m.getSlotLimit(credentialID)
    
    if limit == 0 {
        return 0 // 无限制
    }
    
    return float64(used) / float64(limit)
}
```

#### Limiter 压力查询

```go
// limiter/manager.go

// GetPressure 返回指定 credential 的 Limiter 压力（0-1）
func (m *Manager) GetPressure(credentialID int) float64 {
    // 查询 4 层 Limiter 的占用率
    globalPressure := m.getGlobalPressure()
    poolPressure := m.getPoolPressure(poolID)
    credentialPressure := m.getCredentialPressure(credentialID)
    identityPressure := m.getIdentityPressure(identityID)
    
    // 返回最大值（最严重的压力）
    return math.Max(
        math.Max(globalPressure, poolPressure),
        math.Max(credentialPressure, identityPressure),
    )
}
```

---

## 三、实施计划

### 3.1 Phase 2.1: 数据采集接口（1 周）

**任务**:
1. [ ] 在 `credentialfpslot.Manager` 添加 `GetPressure()` 方法
2. [ ] 在 `limiter.Manager` 添加 `GetPressure()` 方法
3. [ ] 编写单元测试验证压力计算逻辑
4. [ ] 添加监控指标（Prometheus metrics）

**验收标准**:
- ✅ 压力查询接口可用
- ✅ 单元测试覆盖率 > 80%
- ✅ Grafana 可视化压力指标

### 3.2 Phase 2.2: 压力信号注入（1 周）

**任务**:
1. [ ] 在 URSM v2 Scorer 添加压力感知逻辑
2. [ ] 实现 `calculatePressurePenalty()` 函数
3. [ ] 添加 feature flag: `URSM_V2_PRESSURE_AWARE`（默认 off）
4. [ ] 编写单元测试验证惩罚计算

**验收标准**:
- ✅ 压力惩罚逻辑可用
- ✅ Feature flag 可动态开关
- ✅ 单元测试通过

### 3.3 Phase 2.3: A/B 测试（1-2 周）

**任务**:
1. [ ] 部署到 245，开启 `URSM_V2_PRESSURE_AWARE=true`
2. [ ] 对比开启前后的节点选择分布
3. [ ] 监控 P95 延迟变化
4. [ ] 监控请求失败率变化
5. [ ] 调整惩罚函数参数（如需要）

**验收标准**:
- ✅ 负载分布更均衡
- ✅ P95 延迟无明显上升
- ✅ 请求失败率无明显上升

---

## 四、风险评估

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|----------|
| 压力查询性能开销 | 中 | 中 | 缓存压力数据（TTL 1-5秒） |
| 惩罚过重导致节点闲置 | 中 | 中 | A/B 测试调整参数，feature flag 关闭 |
| FpSlots/Limiter 依赖增加 | 低 | 低 | 接口简单，仅读取压力数据 |
| 压力信号不准确 | 中 | 中 | 监控压力指标，对比实际使用率 |

---

## 五、监控指标

### 5.1 压力信号指标

```
# FpSlots 压力
credential_fpslots_pressure{credential_id="123"} 0.75

# Limiter 压力
credential_limiter_pressure{credential_id="123", layer="credential"} 0.85

# 压力惩罚系数
ursm_v2_pressure_penalty{credential_id="123"} 0.40
```

### 5.2 效果指标

```
# 节点选择分布（直方图）
ursm_v2_candidate_selection_count{credential_id="123"} 120

# 压力感知生效次数
ursm_v2_pressure_aware_applied_total 450
```

---

## 六、回退方案

### 6.1 Feature Flag 关闭

```bash
# 方案 1: 环境变量关闭
export URSM_V2_PRESSURE_AWARE=false
systemctl restart llm-gateway

# 方案 2: 配置文件关闭
# config.yaml
ursm_v2:
  pressure_aware: false
```

### 6.2 代码回滚

```bash
# 回滚到 Phase 1 版本
bash scripts/deploy-seamless.sh rollback 245
```

---

## 七、关键决策

### 7.1 为什么选择方案 A（注入到 URSM v2）？

**原因**:
- ✅ URSM v2 是评分系统，压力信号是评分的一部分
- ✅ 集中管理，逻辑清晰
- ✅ 易于 A/B 测试和回退

### 7.2 为什么不直接过滤掉高压力节点？

**原因**:
- ⚠️ 过滤会导致可用节点减少，可能无节点可用
- ✅ 惩罚评分更平滑，保留"最后选择"的余地
- ✅ 高压力节点仍可用于兜底

### 7.3 压力信号要不要持久化？

**选择**: 否（仅内存缓存）

**原因**:
- ✅ 压力信号是实时数据，持久化无意义
- ✅ 避免 Redis 写入开销
- ✅ 服务重启后重新计算即可

---

## 八、下一步

### 8.1 立即行动（Phase 2.1）

1. 分析 `credentialfpslot.Manager` 和 `limiter.Manager` 的现有接口
2. 设计 `GetPressure()` 方法的签名和实现
3. 编写单元测试
4. 部署到 245 验证

### 8.2 后续规划（Phase 2.2-2.3）

- 实现压力信号注入
- A/B 测试验证效果
- 根据数据调整参数

---

**文档状态**: 规划中  
**下一步**: 分析现有代码，设计 GetPressure() 接口
