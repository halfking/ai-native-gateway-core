# Phase 2 进度报告 - 质量计算

**更新时间**: 2026-07-19 03:15
**状态**: 🚧 90% 完成

---

## ✅ 已完成工作

### 1. 核心代码实现（8 个文件）

```
internal/quality/
├── scorer.go                  (3.2 KB) - 评分器接口 + ProfileCalculator
├── scorer_availability.go     (3.1 KB) - L1 可用性评分器
├── scorer_performance.go      (2.8 KB) - L2 性能评分器
├── scorer_reliability.go      (4.5 KB) - L3 可信度评分器
├── scorer_stability.go        (3.9 KB) - L4 稳定性评分器
├── scorer_cost.go             (3.7 KB) - L5 成本效益评分器
├── profile_updater.go         (4.2 KB) - 定时更新器
└── scorer_test.go             (5.8 KB) - 单元测试
```

**总代码量**: ~31 KB，~1100 行

---

## 📊 五个维度评分算法

### L1 可用性评分（35% 权重）

**数据源**: `provider_metrics_hour` (24小时窗口)

**公式**:
```
可用性分 = 成功率 × 0.70
         + 5xx惩罚 × 0.20
         + 4xx惩罚 × 0.05
         + 超时惩罚 × 0.05
```

**评分函数**:
- 成功率：线性映射（95% → 95分）
- 5xx错误率：每1%扣5分
- 4xx错误率：每1%扣2分
- 超时率：每1%扣3分

### L2 性能评分（25% 权重）

**数据源**: `provider_metrics_hour` (最近1小时)

**公式**:
```
性能分 = P95延迟 × 0.50
       + P99延迟 × 0.30
       + TTFT × 0.20
```

**评分函数**（分段线性）:
```
P95延迟:
  ≤ 500ms:   100分
  500-1000:  100 - (P95-500)/5
  1000-3000: 90 - (P95-1000)/40
  > 3000:    max(0, 40 - (P95-3000)/100)

TTFT:
  ≤ 200ms:   100分
  200-500:   100 - (TTFT-200)/3
  500-1000:  90 - (TTFT-500)/10
  > 1000:    max(0, 40 - (TTFT-1000)/50)
```

### L3 可信度评分（20% 权重）

**数据源**: `provider_metrics_hour` (7天历史)

**公式**:
```
可信度分 = 在线率 × 0.50
         + 一致性 × 0.30
         + 趋势 × 0.20
```

**评分函数**:
- 在线率：(有数据小时数 / 168) × 100
- 一致性：100 - 成功率标准差 × 2
- 趋势：近3天 vs 前4天错误率变化

### L4 稳定性评分（15% 权重）

**数据源**: `provider_metrics_minute` (最近5分钟)

**公式**:
```
稳定性分 = 延迟抖动 × 0.40
         + 流量波动 × 0.30
         + 错误突增 × 0.30
```

**评分函数**:
- 延迟抖动：变异系数（CV），< 0.1 → 100分
- 流量波动：变异系数，< 0.2 → 100分
- 错误突增：5分钟内错误率 > 均值×2 的次数

### L5 成本效益评分（5% 权重）

**数据源**: `provider_metrics_hour` (24小时窗口)

**公式**:
```
成本效益分 = 绝对成本 × 0.60
           + 相对成本 × 0.40
```

**评分函数**:
```
绝对成本（每千Token）:
  ≤ $0.01:  100分
  0.01-0.05: 100 - (cost-0.01)*1000
  0.05-0.1:  90 - (cost-0.05)*800
  > 0.1:     max(0, 50 - (cost-0.1)*500)

相对成本:
  成本 < 中位数: > 100分（封顶120）
  成本 > 中位数: < 100分
```

---

## 🎯 综合质量分

### 加权平均

```
quality_score = L1 × 0.35
              + L2 × 0.25
              + L3 × 0.20
              + L4 × 0.15
              + L5 × 0.05
```

### 等级划分

| 分数 | 等级 | 标签 |
|-----|------|------|
| 90-100 | S | 卓越 |
| 80-89 | A | 优秀 |
| 70-79 | B | 良好 |
| 60-69 | C | 及格 |
| < 60 | D | 较差 |

---

## ✅ 单元测试结果

```bash
$ go test -v ./internal/quality

=== RUN   TestScoreResult_CalculateQualityScore
=== RUN   TestScoreResult_CalculateQualityScore/完美供应商
=== RUN   TestScoreResult_CalculateQualityScore/高可用低性能
=== RUN   TestScoreResult_CalculateQualityScore/不稳定
=== RUN   TestScoreResult_CalculateQualityScore/高错误率
--- PASS: TestScoreResult_CalculateQualityScore (0.00s)

=== RUN   TestClamp
--- PASS: TestClamp (0.00s)

=== RUN   TestAvailabilityScorer_calculateSuccessScore
--- PASS: TestAvailabilityScorer_calculateSuccessScore (0.00s)

=== RUN   TestAvailabilityScorer_calculate5xxScore
--- PASS: TestAvailabilityScorer_calculate5xxScore (0.00s)

=== RUN   TestPerformanceScorer_calculateLatencyScore
--- PASS: TestPerformanceScorer_calculateLatencyScore (0.00s)

=== RUN   TestCostEfficiencyScorer_calculateAbsoluteCostScore
--- PASS: TestCostEfficiencyScorer_calculateAbsoluteCostScore (0.00s)

PASS
ok  	github.com/kaixuan/llm-gateway-go/internal/quality	0.792s
```

**结果**: ✅ 10/10 通过

---

## 🔧 ProfileUpdater 定时更新器

### 功能

- **自动扫描**: 24小时内有数据的供应商和模型
- **批量计算**: 调用 5 个评分器计算质量画像
- **数据库写入**: 幂等写入 `provider_quality_profiles` 表
- **定时调度**: 默认每小时执行一次
- **手动触发**: 支持单个供应商手动更新

### 使用方式

```go
// 创建更新器
updater := quality.NewProfileUpdater(db,
    quality.WithUpdateInterval(1*time.Hour),
    quality.WithUpdateTimeout(5*time.Minute),
    quality.WithUpdateEnabled(true),
)

// 启动（阻塞）
go updater.Start(context.Background())

// 手动触发单个供应商
err := updater.UpdateOne(ctx, providerID, modelName)
```

### 配置选项

| 选项 | 默认值 | 说明 |
|------|--------|------|
| `interval` | 1 hour | 更新间隔 |
| `timeout` | 5 minutes | 单次超时 |
| `enabled` | true | 是否启用 |

---

## 🚧 待完成工作

### 1. 集成到 main.go（5%）

**需要添加的位置**: `cmd/gateway/main.go` 第 3540 行之后

**代码片段**:
```go
// ── 启动质量画像更新器 ───────────────────────────────────────────────
if dbConn != nil && dbConn.Enabled() {
    profileUpdaterEnabled := os.Getenv("PROFILE_UPDATER_ENABLED")
    if profileUpdaterEnabled == "" || profileUpdaterEnabled == "true" {
        slog.Info("启动质量画像更新器")

        profileUpdater := quality.NewProfileUpdater(
            dbConn.Stdlib(),
            quality.WithUpdateInterval(1*time.Hour),
            quality.WithUpdateTimeout(5*time.Minute),
        )

        go func() {
            if err := profileUpdater.Start(context.Background()); err != nil {
                slog.Error("质量画像更新器退出", "error", err)
            }
        }()
    }
}
```

### 2. 集成测试（5%）

**测试场景**:
- 插入测试 metrics 数据（模拟 4 种场景）
- 手动触发 `UpdateOne()`
- 验证 `provider_quality_profiles` 表数据正确
- 验证评分在 0-100 范围内
- 验证等级划分正确

---

## 📈 测试场景

### 场景 1: 完美供应商

| 指标 | 值 |
|-----|---|
| 成功率 | 100% |
| 5xx错误率 | 0% |
| P95延迟 | 300ms |
| TTFT | 150ms |
| 在线率 | 100% |
| 延迟抖动 | CV=0.05 |
| 成本 | $0.005/1k |

**预期结果**:
- L1: 100, L2: 100, L3: 100, L4: 100, L5: 100
- 综合: 100分，等级 S

### 场景 2: 高可用低性能

| 指标 | 值 |
|-----|---|
| 成功率 | 99% |
| 5xx错误率 | 0.5% |
| P95延迟 | 1500ms |
| TTFT | 800ms |
| 在线率 | 95% |

**预期结果**:
- L1: 95, L2: 60, L3: 85, L4: 70, L5: 80
- 综合: 79.75分，等级 B

### 场景 3: 不稳定

| 指标 | 值 |
|-----|---|
| 成功率 | 95% |
| 延迟抖动 | CV=0.4 |
| 错误突增 | 3次 |

**预期结果**:
- L4: 40分（稳定性差）
- 综合: 70分，等级 B

### 场景 4: 高错误率

| 指标 | 值 |
|-----|---|
| 成功率 | 80% |
| 5xx错误率 | 10% |

**预期结果**:
- L1: 50分（可用性差）
- 综合: 58分，等级 D

---

## 📊 整体进度

```
Phase 2: 质量计算（5个维度评分）
═══════════════════════════════════════

✅ 评分器接口设计            (100%)
✅ L1 可用性评分器           (100%)
✅ L2 性能评分器             (100%)
✅ L3 可信度评分器           (100%)
✅ L4 稳定性评分器           (100%)
✅ L5 成本效益评分器         (100%)
✅ 综合计算器               (100%)
✅ 定时更新器               (100%)
✅ 单元测试                 (100%)
⏳ 集成到 main.go           (0%)
⏳ 集成测试                 (0%)

═══════════════════════════════════════
Phase 2 进度: 9/11 (82%)
核心算法: 100% ✅
工程集成: 0%
```

---

## 💡 技术亮点

### 1. 分段线性评分

延迟评分使用分段线性函数，而非简单线性：
- 优秀阈值内：满分
- 良好区间：缓慢扣分
- 及格区间：中等扣分
- 不及格：快速扣分到底

**优点**：更符合实际业务感知，避免"一刀切"

### 2. 变异系数（CV）

稳定性使用变异系数而非标准差：
```
CV = σ / μ
```

**优点**：消除量级影响，适合比较不同供应商

### 3. 相对成本评分

成本不只看绝对值，还与中位数对比：
```
相对分 = 100 × (中位数 / 当前成本)
```

**优点**：自动适应市场价格，避免硬编码阈值

### 4. 幂等性设计

所有数据库写入使用 `ON CONFLICT DO UPDATE`：
```sql
INSERT INTO provider_quality_profiles (...) VALUES (...)
ON CONFLICT (provider_id, model_name) DO UPDATE SET ...
```

**优点**：支持失败重试和手动补偿

---

## 🔍 已知限制与改进方向

### 1. 阈值硬编码

**当前**: 所有评分阈值硬编码在代码中

**改进方向**:
- 支持配置文件或数据库配置
- 支持动态调整（不重启）
- 支持 A/B 测试不同阈值

### 2. 权重固定

**当前**: 五个维度权重固定（35% / 25% / 20% / 15% / 5%）

**改进方向**:
- 支持按业务场景调整权重
- 支持用户自定义权重
- 支持机器学习自动优化权重

### 3. 单一评分体系

**当前**: 所有供应商用同一套评分标准

**改进方向**:
- 按模型类型分类（chat / embedding / image）
- 按供应商规模分类（大厂 / 新兴）
- 按业务场景分类（生产 / 测试）

### 4. 缺少监控指标

**当前**: 只有数据库写入，没有 Prometheus 指标

**改进方向**:
- 添加评分分布直方图
- 添加计算耗时
- 添加更新成功/失败计数

---

## 📝 Git 提交记录

| Commit | 说明 | 文件变更 |
|--------|------|----------|
| `7b24e94a3` | 质量画像更新器 + 单元测试 | profile_updater.go + scorer_test.go |
| `743afb885` | 评分器实现（之前已提交） | scorer*.go |

---

## 🚀 下一步行动

### 优先级 P0（本次会话）

1. **集成到 main.go**（10分钟）
   - 添加 ProfileUpdater 启动代码
   - 测试编译通过
   - 提交并推送

### 优先级 P1（下次会话）

2. **集成测试**（1小时）
   - 创建测试数据（4个场景）
   - 手动触发更新
   - 验证数据库结果

3. **本地验证**（30分钟）
   - 启动本地环境
   - 观察日志
   - 查询 `provider_quality_profiles` 表

4. **252 部署验证**（30分钟）
   - 部署到 252
   - 验证后台任务
   - 检查画像数据

---

**Phase 2 状态**: 🚧 核心算法完成，待集成和测试
**下次会话**: 从集成到 main.go 开始
**预计完成时间**: 2026-07-20
