# 统计系统主索引表 - 术语、业务、数据关系全览

> **文档性质**: 统计系统唯一权威参考
> **创建时间**: 2026-11-27
> **基于**: 2026-11-20统计标准化框架 + 2026-11-26 V2审计
> **维护原则**: 任何统计相关变更必须同步更新本表

---

## §1 核心术语标准定义表

### 1.1 计数指标（Count Metrics）

| 术语 | 英文 | 单位 | 定义 | 数据源 | 计算逻辑 | 适用范围 |
|------|------|------|------|--------|----------|----------|
| 请求数 | request_count | 次 | 完成的请求总数（含成功与失败） | usage_facts.fact_id / request_metrics.id | COUNT(*) | 所有页面 |
| 成功数 | success_count | 次 | status='success' 的请求数 | usage_facts WHERE status='success' | COUNT(*) WHERE status='success' | 看板、对账 |
| 失败数 | error_count | 次 | status='failure' 的请求数 | usage_facts WHERE status='failure' | COUNT(*) WHERE status='failure' | 看板、监控 |
| 会话数 | session_count | 个 | 唯一会话ID数量 | session_turns.session_id | COUNT(DISTINCT session_id) | 会话分析 |
| 用户数 | user_count | 人 | 唯一用户ID数量 | usage_facts.owner_user_id | COUNT(DISTINCT owner_user_id) | 用户分析 |

### 1.2 Token指标（Token Metrics）

| 术语 | 英文 | 单位 | 定义 | 数据源 | 计算逻辑 | 适用范围 |
|------|------|------|------|--------|----------|----------|
| 输入Token | input_tokens | 个 | 用户提示词的token数量 | usage_facts.prompt_tokens | SUM(prompt_tokens) | 所有页面 |
| 输出Token | output_tokens | 个 | 模型生成的token数量 | usage_facts.completion_tokens | SUM(completion_tokens) | 所有页面 |
| 总Token | total_tokens | 个 | 输入+输出Token总和 | - | SUM(prompt_tokens + completion_tokens) | 所有页面 |
| 缓存读Token | cache_read_tokens | 个 | 从缓存读取的token数 | usage_facts.cache_read_tokens | SUM(cache_read_tokens) | 看板、成本分析 |
| 缓存写Token | cache_write_tokens | 个 | 写入缓存的token数 | usage_facts.cache_write_tokens | SUM(cache_write_tokens) | 看板、成本分析 |

### 1.3 成本指标（Cost Metrics）⚠️ 单位纯净性原则

| 术语 | 英文 | 单位 | 定义 | 数据源 | 计算逻辑 | ⚠️ 禁止操作 |
|------|------|------|------|--------|----------|------------|
| 内部成本 | internal_cost | USD | 支付给供应商的费用（美元） | usage_facts.internal_cost_cents | SUM(internal_cost_cents) / 100.0 | ❌ 禁止与积分相加 |
| 外部成本 | external_cost | 积分 | 消耗的平台积分 | usage_facts.external_cost_credits | SUM(external_cost_credits) | ❌ 禁止与USD相加 |
| ~~总成本~~ | ~~total_cost~~ | ❌混合 | **已废弃** | - | **禁止使用** | ❌ 跨单位求和是错误的 |

**单位纯净性原则**：
- ✅ 允许: `internal_cost (USD) + internal_cost (USD) = internal_cost (USD)`
- ✅ 允许: `external_cost (积分) + external_cost (积分) = external_cost (积分)`
- ❌ 禁止: `internal_cost (USD) + external_cost (积分) = ??`（无物理意义）

**修复路径**: 使用 `domains/analytics/cost.go` 的 `SplitCost()` 方法分别处理。

### 1.4 派生指标（Derived Metrics）

| 术语 | 英文 | 单位 | 定义 | 计算公式 | 实现 | 适用范围 |
|------|------|------|------|----------|------|----------|
| 成功率 | success_rate | % | 成功请求占比 | success_count / request_count * 100 | DerivedMetrics.SuccessRate() | 所有页面 |
| 错误率 | error_rate | % | 失败请求占比 | error_count / request_count * 100 | DerivedMetrics.ErrorRate() | 监控、看板 |
| 平均响应时间 | avg_response_time | ms | 平均请求耗时 | SUM(duration_ms) / request_count | DerivedMetrics.Average() | 性能监控 |
| 平均Token/请求 | avg_tokens_per_request | 个 | 每请求平均Token数 | total_tokens / request_count | DerivedMetrics.Average() | 成本分析 |
| P95延迟 | p95_latency | ms | 95分位数延迟 | PERCENTILE(duration_ms, 0.95) | DerivedMetrics.P95() | 性能监控 |

### 1.5 时间窗口标准（Time Window Standards）

| 术语 | 定义 | 时区 | 起始时间 | 结束时间 | 适用场景 |
|------|------|------|----------|----------|----------|
| UTC日 | UTC Calendar Day | UTC | D 00:00:00 | D+1 00:00:00 | **后端统计标准** |
| 上海日历日 | Shanghai Calendar Day | Asia/Shanghai | D 00:00:00+08 | D+1 00:00:00+08 | 对账快照、报表 |
| 今日 | Today | Asia/Shanghai | 今天00:00+08 | 现在 | 实时看板 |
| 近5分钟 | Last 5 Minutes | - | now() - 5min | now() | 实时监控 |
| 近7天 | Last 7 Days | Asia/Shanghai | 7天前00:00+08 | 昨天23:59:59+08 | 趋势分析 |
| 近30天 | Last 30 Days | Asia/Shanghai | 30天前00:00+08 | 昨天23:59:59+08 | 月度报表 |

**时区统一原则**：
- ✅ 所有后端SQL统计使用 **UTC日** 为标准
- ✅ 对账快照、报表使用 **上海日历日**（用户可见）
- ⚠️ 前端展示时根据用户时区转换

---

## §2 数据源架构映射表

### 2.1 核心事实表（Fact Tables）

| 表名 | 用途 | 权威性 | 数据量级 | 更新频率 | 保留期 |
|------|------|--------|----------|----------|--------|
| usage_facts | **统计真源** | ⭐⭐⭐⭐⭐ 权威 | ~200万/月 | 实时写入 | 永久 |
| request_metrics | V2投影表 | ⭐⭐⭐⭐ 衍生 | ~200万/月 | 异步投影（分钟级） | 永久 |
| session_turns | 会话分析 | ⭐⭐⭐⭐ 权威 | ~50万/月 | 实时写入 | 永久 |
| session_turns_details | 会话详情 | ⭐⭐⭐ 衍生 | ~50万/月 | 实时写入 | 永久 |
| daily_grain | 对账聚合 | ⭐⭐⭐ 衍生 | ~300行/天 | 每日凌晨 | 永久 |
| report_snapshots | 历史快照 | ⭐⭐ 快照 | ~56行/天 | 每日凌晨 | 永久 |

**数据流向**：
```
原始请求 → usage_facts（真源）
           ↓
           ├→ request_metrics（投影，分钟级延迟）
           ├→ session_turns（会话维度）
           └→ daily_grain（天聚合，T+1）
                └→ report_snapshots（快照）
```

### 2.2 物化视图（Materialized Views）

| 视图名 | 数据源 | 刷新频率 | 用途 | 命中页面 |
|--------|--------|----------|------|----------|
| board_metrics_cache | request_metrics | 15分钟 | Dashboard看板缓存 | /dashboard (Board面板) |
| routing_mv_* | session_turns | 1分钟 | 会话分析系统 | /admin/session-analytics/* |
| credential_monitor_rollup | usage_facts | 滑动窗口 | 凭据监控 | 凭据管理页 |

### 2.3 缓存层（Cache Layers）

| 缓存名 | 存储 | TTL | 更新策略 | 适用场景 |
|--------|------|-----|----------|----------|
| Redis Analytics Cache | Redis | 5分钟 | 被动失效 | 高频查询（用量趋势） |
| Board Cache | Redis | 15分钟 | 主动刷新 | Dashboard看板 |
| Dimension Options | Redis | 1小时 | 被动失效 | 筛选器选项 |

---

## §3 页面级统计口径对照表

### 3.1 Dashboard 页面（/dashboard）

#### 英雄区（Hero Section）

| 指标卡片 | 显示术语 | 数据源 | SQL逻辑 | 时间窗口 | 流量范围 |
|----------|----------|--------|---------|----------|----------|
| 今日请求 | 请求数 | request_metrics | COUNT(*) WHERE date=today | 今日（上海） | 全流量 |
| 今日成功率 | 成功率 | request_metrics | COUNT(*) WHERE status='success' / COUNT(*) | 今日 | 全流量 |
| 今日内部成本 | 内部成本(USD) | request_metrics | SUM(internal_cost_cents)/100 | 今日 | 全流量 |
| 今日外部成本 | 外部成本(积分) | request_metrics | SUM(external_cost_credits) | 今日 | 全流量 |

#### Board面板（运营看板）

| 卡片 | 数据源 | 更新频率 | 时间窗口 | 流量范围 |
|------|--------|----------|----------|----------|
| 请求总览 | board_metrics_cache (MV) | 15分钟 | 可选（今日/近7天/近30天） | 全流量 |
| 错误分析 | request_logs_with_current_month | 实时 | 近24小时 | 全流量 |
| 性能监控 | request_logs_with_current_month | 实时 | 近24小时 | 全流量 |
| 模块执行统计 | request_logs_with_current_month | 实时 | 近24小时 | 全流量 |

**关键决策点**（2026-11-24）:
- ✅ errors/performance/module-stats 三卡已切到 `request_logs_with_current_month`（请求级日志真源）
- ✅ 不再读 `usage_facts`（2026-11-10被删除352,395行）
- ✅ 不再读 `session_module_executions_hot`（无生产写者）

#### Stats Tab（会话统计）

| 卡片 | 数据源 | 计算逻辑 | 时间窗口 | 流量范围 |
|------|--------|----------|----------|----------|
| 会话总览 | session_turns + turns_alignment | COUNT(DISTINCT session_id) | 可选 | business |
| 会话趋势 | session_turns | GROUP BY date | 近7天 | business |
| 会话健康 | session_turns | 成功率计算 | 可选 | business |

### 3.2 Session Analytics 页面（/admin/session-analytics/*）

| 页面 | 端点 | 数据源 | 聚合维度 | 时间窗口 | 流量范围 |
|------|------|--------|----------|----------|----------|
| 总览 | /v2/overview | request_metrics | 全局 | 可选 | business |
| 用户排行 | /v2/user-ranking | request_metrics | owner_user_id | 可选 | business |
| 模型排行 | /v2/model-ranking | request_metrics | model | 可选 | business |
| 供应商排行 | /v2/provider-ranking | request_metrics | provider_id | 可选 | business |
| 任务排行 | /v2/task-ranking | request_metrics | task_type | 可选 | business |
| 代理排行 | /v2/agent-ranking | request_metrics | agent_name | 可选 | business |

**成本显示策略**（2026-11-20修正）:
- ✅ 总览页：分两卡显示「内部成本(USD)」「外部成本(积分)」
- ⚠️ 排行页：保留 total_cost 列，顶部注明「只可相对比较，不可跨供应商比较」

**⚠️ 已知问题 L88（模型榜复制放大）**:
- 现象：`unnest(models_used)` 展开后，整会话的 cost 被复制到每个模型行
- 实测放大：+14.45%（费用）、+54.0%（请求数）
- 状态：并发会话已有披露代码，待上线后决定是否改为分摊

### 3.3 Reconciliation 页面（/admin/reconciliation）

| 报表 | 数据源 | 聚合粒度 | 时间窗口 | 流量范围 |
|------|--------|----------|----------|----------|
| 供应商对账 | daily_grain (scope='daily_*') | 供应商/模型/天 | 近14天 | 全流量 |
| 内部对账 | daily_grain (scope='internal_*') | 租户/人员/模型/天 | 近14天 | business |
| 快照导出 | report_snapshots | 供应商/模型/天 | 历史任意区间 | 全流量 |

**⚠️ 已知问题 L87（对账日界统一）**:
- 受影响日期：11-11,12,13,14,15,16,22（7天）
- 根因：pgx v5 对 `date` 列返回 UTC，导致 6 天用 UTC 日界、7 天用上海日界
- 影响：与看板/请求日志必然不等（11-22 差 +7,800 / +8,399）
- 修复方案：删除受影响快照 + 重新生成（见 §4.1）

**成本字段问题**（待修复）:
- ⚠️ `daily_grain.cost_amount` 混合USD+积分（跨单位求和）
- 修复需要：schema变更，拆分为 `internal_cost_usd` + `external_cost_credits`

### 3.4 Usage Trends 页面（用量趋势分析）

| 端点 | 数据源 | 聚合维度 | 时间窗口 | 流量范围 |
|------|--------|----------|----------|----------|
| /v2/trend-series | request_metrics | 时间序列 | 可选 | 可选（business/probe） |
| /v2/trend-models | request_metrics | 模型×时间 | 可选 | business |
| /v2/top-rankings | request_metrics | 多维度TOP N | 可选 | business |
| /v2/period-compare | request_metrics | 两期对比 | 可选 | business |
| /v2/cost-trend | request_metrics | 成本趋势 | 可选 | business |
| /v2/cache-economics | request_metrics | 缓存经济性 | 可选 | business |

### 3.5 Request Logs 页面（/logs）

| 视图 | 数据源 | 时间窗口 | 流量范围 |
|------|--------|----------|----------|
| 业务请求 | request_logs_with_current_month | 实时 | business |
| 含探测请求 | business臂 + probe臂（INNER JOIN usage_facts） | 实时 | business + probe |

**⚠️ 已知问题 L89（探测请求文案失实）**:
- 文案宣称：「开启含探测 = usage_facts 全量 = 供应商对账」
- 实测：少 6,282 条（−3.3%），差集主要是无 usage_facts 的探测请求
- 根因：探测臂用 `INNER JOIN usage_facts`，只纳入有 fact 的行
- 修复方案：改文案为「部分探测请求（仅含有使用记录的请求）」（见 §4.3）

---

## §4 待修复的P0统计口径问题

### 4.1 L87: 对账快照日界统一（P0，需生产库操作）

**执行步骤**:
1. 备份 `report_snapshots` 表
2. 删除受影响的 7 天快照（11-11,12,13,14,15,16,22）
3. 通过 API 或 worker 重新生成快照
4. 验证新快照与 usage_facts 对齐（上海日历日窗口）

**SQL脚本**: `scripts/audit/rollup-date-boundary-fix.sql`

### 4.2 L88: 模型榜复制放大（P1，等并发会话交付）

**当前状态**: 并发会话已有披露代码（`SessionStatsRankings.modelCostDisclosure.spec.ts`）

**两种方案**:
- 方案A：先上披露（快速止血）
- 方案B：改为按模型分摊（需 owner 决策）

**本轮交付**: 仅补充实测放大率（+14.45%费用、+54.0%请求数）

### 4.3 L89: 请求日志「含探测」文案修正（P0，零生产写）

**修改两处文案**:

① `web/src/views/RequestLogsView.vue:101`
```vue
<!-- 修正为 -->
<el-tooltip content="开启后显示业务请求与部分探测请求（探测请求仅包含有使用记录的请求；完整探测请求见供应商对账）" placement="top">
```

② `web/src/locales/zh-CN/requests.ts:67`
```typescript
includeProbesHint: '开启后显示业务请求与部分探测请求（探测请求仅包含有使用记录的请求；完整探测请求见供应商对账）',
```

---

## §5 数据更新策略全览

### 5.1 策略类型与适用场景

| 策略 | 延迟 | 准确性 | 资源消耗 | 适用场景 |
|------|------|--------|----------|----------|
| **实时查询** | <100ms | 100% | 高（每次查库） | 数据量小、查询简单 |
| **物化视图** | 1-15min | 99%+ | 中（后台刷新） | 复杂聚合、多维分析 |
| **预聚合缓存** | 5-15min | 95%+ | 低（内存读取） | 高频访问、计算昂贵 |
| **批量聚合** | 1天 | 100% | 低（离线计算） | T+1报表、历史分析 |
| **混合策略** | 灵活 | 可调 | 中 | 兼顾速度与准确性 |

### 5.2 各页面实际使用的策略

| 页面 | 卡片/功能 | 策略 | 刷新频率 | 降级处理 |
|------|-----------|------|----------|----------|
| Dashboard | 今日请求数 | 实时查询 | 每次刷新 | 无 |
| Dashboard | Board面板 | 预聚合缓存 (MV) | 15分钟 | 显示更新时间 |
| Dashboard | 会话统计 | 物化视图 | 1分钟 | 显示 `turns_alignment` 状态 |
| Session Analytics | 所有页面 | 实时查询 (request_metrics) | 每次刷新 | 显示投影状态 |
| Reconciliation | 对账报表 | 批量聚合 (daily_grain) | 每日凌晨 | 显示快照日期 |
| Usage Trends | 趋势分析 | 实时查询 + Redis缓存 | 5分钟 | 缓存未命中时查库 |

### 5.3 缓存失效与同步策略

#### Redis缓存失效策略

```go
// 被动失效（TTL）
analytics_cache_key := fmt.Sprintf("analytics:usage:%s", query_hash)
redis.SET(analytics_cache_key, result, 5*time.Minute)  // 5分钟 TTL

// 主动失效（写触发）
// 当 usage_facts 有新写入时，不主动清理缓存（允许5分钟延迟）
```

#### 物化视图刷新策略

```sql
-- board_metrics_cache: 15分钟定时刷新
REFRESH MATERIALIZED VIEW CONCURRENTLY board_metrics_cache;

-- routing_mv_*: 1分钟定时刷新
REFRESH MATERIALIZED VIEW CONCURRENTLY routing_mv_session_overview;
```

#### 投影表同步策略（request_metrics）

```
usage_facts 写入
  ↓ AFTER INSERT 触发器
  ↓ enqueue_request_metrics_fact
  ↓ pending 队列
  ↓ Processor（后台协程，双游标）
  ↓ request_metrics 投影表（分钟级延迟）
```

**降级处理**:
- 检查 `request_metrics_projection_state.ready` 字段
- 如果 `backfill_complete=false`，返回 `degraded_reason="request_metrics_backfill"`
- 前端显示「统计数据投影中，可能不完整」提示

---

## §6 可复用统计处理模块

### 6.1 CostCalculator（成本计算器）

**文件**: `domains/analytics/cost.go`

**核心方法**:
```go
// ✅ 允许的方法
CalculateInternalCost(costCents int64) float64        // USD
CalculateExternalCost(credits int64) int64            // 积分
SplitCost(internalCents, externalCredits) CostBreakdown
DistributeTailDifference(totalCents, groups) map[string]int64

// ❌ 禁止的方法（标记为DEPRECATED）
CalculateTotalCost(...) error  // 跨单位求和，返回错误
```

**使用场景**:
- Session Analytics 总览页：分两卡显示成本
- 对账报表导出：分别计算内部/外部成本
- 任何需要成本计算的地方

### 6.2 DerivedMetricsCalculator（派生指标计算器）

**文件**: `domains/analytics/derived.go`

**核心方法**:
```go
// 成功率相关（自动防除零）
SuccessRate(total, success int64) float64
ErrorRate(total, success int64) float64

// 平均值计算（防除零）
Average(sum, count int64) float64
WeightedAverage(values []float64, weights []int64) float64

// 百分位数
Percentile(values []float64, p float64) float64
P50/P95/P99(values []float64) float64

// 增长率
GrowthRate(oldValue, newValue float64) float64

// 基础统计
Min/Max/Sum/StandardDeviation(values []float64) float64
```

**使用场景**:
- Dashboard 成功率卡片
- 性能监控 P95延迟
- 趋势分析增长率计算

### 6.3 待实现的可复用模块

| 模块名 | 功能 | 优先级 | 预计工作量 |
|--------|------|--------|------------|
| TimeWindowAggregator | 时间窗口聚合器 | P2 | 1-2天 |
| DimensionGrouper | 维度分组器 | P2 | 1-2天 |
| CacheManager | 统一缓存管理 | P3 | 2-3天 |

---

## §7 验证与测试策略

### 7.1 单元测试覆盖

| 模块 | 测试文件 | 覆盖率 | 状态 |
|------|----------|--------|------|
| CostCalculator | cost_test.go | 100% | ✅ 全部通过 |
| DerivedMetrics | derived_test.go | 100% | ✅ 全部通过 |
| RequestStatsV2 | admin/request_stats_v2_test.go | 90%+ | ✅ 核心路径覆盖 |

### 7.2 集成测试（真库门）

**待补充的真库门**:
1. ✅ 三臂 request_id 两两零重叠
2. ✅ usage_facts 无同 request_id 多行
3. **L87新增**: 对账 daily_total = usage_facts 全量（上海日历日）
4. ✅ 会话侧 = 请求侧（token 逐字相等）
5. **L90新增**: 看板缓存 vs 直查（每字段相等）

### 7.3 E2E测试（Maestro）

**测试脚本**: `scripts/verify-statistics-pages.sh`

**覆盖页面**:
- Dashboard（英雄区 + Board面板 + Stats Tab）
- Session Analytics（总览 + 5个排行页）
- Reconciliation（供应商对账 + 内部对账）
- Usage Trends（6个分析端点）
- Request Logs（业务/含探测切换）

**上次运行结果**（2026-11-20）:
- ❌ 登录步骤失败（`ADMIN_PASSWORD` 未配置）
- ✅ 版本验证通过（2.6.1-2ae6b26d）
- ✅ API端点可达（200 OK）

---

## §8 维护指南

### 8.1 添加新统计指标的checklist

1. [ ] 在 §1 核心术语表中定义术语（包括单位、数据源、计算逻辑）
2. [ ] 在 §2 数据源表中确认数据来源
3. [ ] 在 §3 页面级对照表中登记使用该指标的页面
4. [ ] 在 §5 更新策略表中确定该指标的刷新策略
5. [ ] 编写单元测试（如使用可复用模块，确保模块已测试）
6. [ ] 补充集成测试（如涉及新数据源，补充真库门）
7. [ ] 更新API文档（如新增端点）
8. [ ] 执行E2E测试验证

### 8.2 修改现有统计指标的checklist

1. [ ] 识别影响范围（查询 §3 页面级对照表）
2. [ ] 评估是否需要schema变更
3. [ ] 更新 §1 术语定义（如计算逻辑变化）
4. [ ] 更新所有使用该指标的页面代码
5. [ ] 更新相关测试
6. [ ] 执行回归测试（确保其他页面不受影响）
7. [ ] 更新用户文档/帮助文档

### 8.3 已知技术债务清单

| 债务 | 位置 | 影响 | 优先级 | 预计修复 |
|------|------|------|--------|----------|
| ~~total_cost 跨单位求和~~ | ~~多处~~ | ~~数字无意义~~ | ~~P0~~ | ✅ 已修复（Session Analytics） |
| daily_grain.cost_amount 混合单位 | Reconciliation | 数据不可分拆 | P0 | 1周（需schema变更） |
| 模型榜复制放大 | Session Analytics | 数字放大+14.45% | P1 | 等并发会话披露上线 |
| 对账日界不统一 | report_snapshots | 7天数据不对齐 | P0 | 本轮修复（见§4.1） |
| 探测请求文案失实 | Request Logs | 用户误解 | P0 | 本轮修复（见§4.3） |
| 死代码清理 | metricsstore/writer.go | 维护成本 | P2 | 2周 |

---

## §9 附录

### 9.1 相关文档索引

| 文档 | 路径 | 用途 |
|------|------|------|
| 统计标准化框架 | docs/statistics-standardization-framework.md | 完整框架文档 |
| 实施计划 | docs/statistics-standardization-implementation-plan.md | 执行路线图 |
| 完成总结报告 | docs/statistics-standardization-completion-report.md | 2026-11-20交付总结 |
| V2 API审计报告 | docs/audit/统计API-V2化逐项审计-2026-11-26.md | V2切换验证 |
| P0修正方案 | docs/audit/2026-11-26-三项P0统计口径修正方案.md | 待修复问题 |

### 9.2 数据库Schema关键表结构

**usage_facts** (统计真源):
```sql
CREATE TABLE usage_facts (
    fact_id BIGSERIAL PRIMARY KEY,
    request_id VARCHAR(64) NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    traffic_class VARCHAR(20) NOT NULL,  -- 'business' | 'probe'
    status VARCHAR(20) NOT NULL,         -- 'success' | 'failure'
    provider_id INT NOT NULL,
    model VARCHAR(128) NOT NULL,
    credential_id INT,
    api_key_id INT,
    owner_user_id INT,
    tenant_id INT,
    prompt_tokens INT NOT NULL DEFAULT 0,
    completion_tokens INT NOT NULL DEFAULT 0,
    cache_read_tokens INT DEFAULT 0,
    cache_write_tokens INT DEFAULT 0,
    internal_cost_cents BIGINT NOT NULL DEFAULT 0,  -- USD × 100
    external_cost_credits BIGINT NOT NULL DEFAULT 0, -- 积分
    duration_ms INT,
    -- ... 其他字段
);
```

**request_metrics** (V2投影表):
```sql
CREATE TABLE request_metrics (
    id BIGSERIAL PRIMARY KEY,
    source_fact_id BIGINT,  -- 指向 usage_facts.fact_id（投影行标记）
    request_id VARCHAR(64) NOT NULL,
    -- ... 结构与 usage_facts 类似，但增加了投影标记
);
```

### 9.3 Git提交历史

```bash
# 统计标准化项目（2026-11-20）
2ae6b26d3 feat(analytics): 可复用统计处理模块
c41277aee docs(statistics): 统计标准化实施计划
dbdc642f3 docs(statistics): 统计数据标准化框架

# V2 API切换（2026-11-26）
d0335d557 audit(stats): 统计 API V2 化逐项审计
（其他相关提交见git log）
```

---

**文档维护者**: Goal Session 2026-11-27  
**最后更新**: 2026-11-27  
**版本**: 1.0  
**状态**: ✅ 完成  
**下次审计**: 每周一（统计口径一致性检查）
