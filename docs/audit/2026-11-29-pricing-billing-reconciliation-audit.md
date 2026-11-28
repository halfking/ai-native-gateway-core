# 价格、计费、对账系统审计报告

**审计日期**: 2026-11-29  
**审计范围**: 价格配置、计费计量、对账系统  
**审计员**: ZCode Agent  

## 执行摘要

完成对价格设置、计费和对账逻辑的全面审计。核心功能实现正确，但发现**关键计费公式缺少单元测试**（P1缺陷），已修复。搜索代理报告与实际代码存在部分差异，已对照确认。

## 审计发现

### 1. 搜索报告与实际代码的对照

#### 1.1 价格配置架构

**搜索报告声称**：
- `sql/migrations/startup/maas_pricing_schema.sql` 文件包含三层价格配置
- `maas_price_versions`, `maas_price_rules`, `maas_price_modifiers` 三张表

**实际情况**：
- ✅ `maas_pricing_schema.sql` 文件**不存在**
- ✅ 实际价格版本化在 `952_model_price_versions.sql`（2026-11-09）
- ✅ `model_credit_rates` 表实际存储信用点费率（见 `maas/model_rates.go`）
- ✅ 销售报价系统在 `sales/quote.go`（五价轴快照）
- ⚠️ 未发现独立的 `maas_price_rules` 和 `maas_price_modifiers` 表

#### 1.2 计费计量流程

**搜索报告声称**：
- `maas/consumption_detail.go` 实现计费公式（行59-104）
- `maas/bills.go` 实现账单生成（行59-64）
- `maas_credit_consumption_buckets` 表存储时间桶聚合

**实际情况**：
- ✅ `consumption_detail.go` 存在，计费公式在**行128-146**（搜索报告行号有偏移）
- ✅ `bills.go` 存在，金额转换在**行59-64**（行号准确）
- ✅ `maas_credit_consumption_buckets` 表存在（迁移389）
- ✅ 计费公式实现正确，包含AF-1货币混合bug修复

#### 1.3 对账系统

**搜索报告声称**：
- `domains/stats/reconciliation.go` 实现6小时对账（行157-200核心逻辑）
- `domains/stats/daily_monthly_rollup.go` 实现日度月度汇总（行136-150月度关闭）
- 自动修复阈值 < 2% 且 < 1000

**实际情况**：
- ✅ `reconciliation.go` 存在，核心逻辑确实在**行157-229**范围
- ✅ `daily_monthly_rollup.go` 存在，月度关闭逻辑确实在目标范围
- ✅ 自动修复阈值常量确认：`autoRepairThreshold = 0.02`, `autoRepairMaxValue = 1000`（行22-23）
- ✅ 对账工作器已在 `cmd/gateway/main.go:6197` 接线

### 2. 发现的缺陷

#### P1：关键计费公式缺少单元测试

**位置**: `maas/consumption_detail.go:128-146`  
**严重性**: P1 - 关键金钱计算路径无测试覆盖  

**问题描述**：
计费公式代码（credits → revenue → margin）是核心金钱计算路径，包含：
- 信用点到金额的转换
- 多币种汇率处理
- 毛利率计算
- AF-1货币混合bug修复

该段代码**没有单元测试文件**（`consumption_detail_test.go` 不存在）。

**修复措施**：
创建 `maas/consumption_detail_test.go`，包含：
- `TestConsumptionDetailRow_RevenueCalculation`: 核心计费公式测试（6个场景）
- `TestConsumptionDetailRow_CurrencyMixingBug`: AF-1修复的回归测试

**测试结果**：
```
=== RUN   TestConsumptionDetailRow_RevenueCalculation
--- PASS: TestConsumptionDetailRow_RevenueCalculation (0.00s)
=== RUN   TestConsumptionDetailRow_CurrencyMixingBug
--- PASS: TestConsumptionDetailRow_CurrencyMixingBug (0.00s)
PASS
ok      github.com/kaixuan/llm-gateway-go/maas  0.818s
```

### 3. 实际代码结构确认

#### 3.1 价格配置文件

| 组件 | 文件路径 | 大小 | 最后修改 | 状态 |
|------|---------|------|---------|------|
| 价格版本表 | `sql/migrations/startup/952_model_price_versions.sql` | 5.4K | 2026-11-09 | ✅ 存在 |
| 模型费率 | `maas/model_rates.go` | 20K | 2026-11-09 | ✅ 存在 |
| 销售报价 | `sales/quote.go` | 70K | 2026-11-19 | ✅ 存在 |

#### 3.2 计费计量文件

| 组件 | 文件路径 | 大小 | 最后修改 | 状态 |
|------|---------|------|---------|------|
| 消费明细 | `maas/consumption_detail.go` | 6.8K | 2026-11-26 | ✅ 存在 |
| 账单生成 | `maas/bills.go` | 9.5K | 2026-11-04 | ✅ 存在 |
| 使用统计 | `maas/usage.go` | 8.3K | 2026-11-24 | ✅ 存在 |
| 消费桶表 | `sql/migrations/startup/389_maas_credit_consumption_buckets.sql` | 862B | 2026-07-15 | ✅ 存在 |

#### 3.3 对账系统文件

| 组件 | 文件路径 | 大小 | 最后修改 | 状态 |
|------|---------|------|---------|------|
| 对账工作器 | `domains/stats/reconciliation.go` | 25K | 2026-10-28 | ✅ 存在 |
| 日度月度汇总 | `domains/stats/daily_monthly_rollup.go` | 17K | 2026-10-27 | ✅ 存在 |
| 使用事实表 | `sql/migrations/startup/537_usage_facts.sql` | 2.8K | 2026-08-19 | ✅ 存在 |
| 对账基础表 | `sql/migrations/startup/536_stats_analytics_foundation.sql` | - | - | ✅ 存在 |

### 4. 核心功能验证

#### 4.1 计费公式实现

**代码位置**: `maas/consumption_detail.go:128-146`

```go
// AF-1修复：多币种汇率正确转换
revenueBillMajor := float64(row.CreditsCharged) * centsPerCredit / 100
row.TenantRevenueUSD = revenueBillMajor

if billCurrency != "" && !strings.EqualFold(billCurrency, "USD") {
    usdRevenue, err := settings.ConvertToUSD(revenueBillMajor, billCurrency)
    if err == nil {
        row.TenantRevenueUSD = usdRevenue
    } else if strings.EqualFold(billCurrency, "CNY") {
        row.TenantRevenueUSD = revenueBillMajor / settings.CNYToUSDFX
    }
}

row.GrossMarginUSD = row.TenantRevenueUSD - row.UpstreamCostUSD
if row.TenantRevenueUSD != 0 {
    row.GrossMarginRate = row.GrossMarginUSD / row.TenantRevenueUSD
}
```

**验证结果**: ✅ 实现正确，包含AF-1货币混合bug修复

#### 4.2 汇率常量

**代码位置**: `settings/cny_fx.go:14`

```go
const CNYToUSDFX = 7.2
```

**多币种支持**: `settings/exchange_rates.go` 实现 `ConvertToUSD()` 函数，支持8种币种。

#### 4.3 对账自动修复阈值

**代码位置**: `domains/stats/reconciliation.go:22-23`

```go
const (
    autoRepairThreshold    = 0.02   // 2% difference threshold
    autoRepairMaxValue     = 1000   // max absolute value
)
```

**验证结果**: ✅ 阈值与报告描述一致

### 5. 编译与测试验证

#### 5.1 编译检查

```bash
$ go build ./maas/...        # ✅ 编译通过
$ go build ./sales/...       # ✅ 编译通过  
$ go build ./domains/stats/... # ✅ 编译通过
```

#### 5.2 测试执行

```bash
$ go test ./maas/... -run "TestConsumption|TestBills" -short
ok      github.com/kaixuan/llm-gateway-go/maas  0.328s
```

**新增测试覆盖**：
- `TestConsumptionDetailRow_RevenueCalculation`: 6个计费场景
- `TestConsumptionDetailRow_CurrencyMixingBug`: AF-1回归测试

## 结论

### 核心功能状态

| 功能模块 | 实现状态 | 测试覆盖 | 备注 |
|---------|---------|---------|------|
| 价格配置 | ✅ 完整 | ⚠️ 部分 | 实际结构与报告有差异 |
| 计费计量 | ✅ 完整 | ✅ 已补齐 | P1缺陷已修复 |
| 对账系统 | ✅ 完整 | ✅ 完整 | 包含集成测试 |

### 修正措施

1. ✅ **已完成**: 创建 `maas/consumption_detail_test.go`，补齐关键计费公式测试
2. ✅ **已完成**: 编译验证所有相关包
3. ✅ **已完成**: 运行测试确保正确性

### 遗留问题

1. **搜索报告准确性**: 报告提到的 `maas_pricing_schema.sql` 和三层价格表结构与实际代码不符，可能是搜索代理基于旧版本或设计文档生成的报告
2. **文档同步**: 实际代码结构需要更新到设计文档中

### 建议

1. **立即行动**: 将修复提交到代码库
2. **后续任务**: 核对设计文档与实际实现的一致性
3. **测试增强**: 考虑为 `sales/quote.go` 的五价轴逻辑补充测试

## 附录：关键代码路径

### 计费流程
```
Request → request_logs_with_current_month (view)
       → maas_credit_consumption_buckets (aggregation)
       → maas/bills.go (monthly bill generation)
       → maas_monthly_bills (table)
```

### 对账流程
```
Request → stats_event_inbox
       → usage_facts (source of truth)
       → stats_usage_daily/monthly (projections)
       → ReconciliationWorker.ReconcilePeriod()
       → stats_reconciliation_diffs (discrepancies)
       → auto-repair (<2% & <1000) or manual approval
```

---

**审计人员**: ZCode Agent  
**复核状态**: 待owner复审  
**行动项**: 1个P1缺陷已修复，待提交推送
