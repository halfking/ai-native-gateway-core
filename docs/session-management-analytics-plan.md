# 会话管理与分析产品视图 — 技术审计与方案文档

> 基于 agentsview 代码库学习与 llm-gateway-go 现状交叉分析

---

## 一、审计概述

### 1.1 审计目标

对 llm-gateway-go 现有会话管理模块（domains/session/ + admin/session_analytics_* + admin/session_panorama_*）进行全面审计，以 agentsview 为行业参考基准（成熟的开源 AI 代理会话管理与分析产品），识别能力差距、架构短板与产品缺失，输出完整的改进方案。

### 1.2 审计方法

| 方法 | 说明 |
|------|------|
| 代码逆向分析 | 通读 agentsview 完整代码库（Go 后端 + Svelte 前端），提取所有会话管理、分析、健康信号相关的特性 |
| 现状映射 | 逐项对照 llm-gateway-go 现有功能，标记已有/缺失/不完整 |
| 差距量化 | 对每个缺失项评估实现成本、影响范围和优先级 |
| 方案设计 | 将 agentsview 的核心设计理念适配到我们的多租户 SaaS 网关架构 |

### 1.3 被审计代码库

| 代码库 | 路径 | 角色 |
|--------|------|------|
| agentsview | `/Users/xutaohuang/workspace/ai/agentsview` | 行业参考基准 |
| llm-gateway-go | `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go` | 被审计目标 |

---

## 二、agentsview 核心特性对照表

### 2.1 会话管理 (Session Management)

| 特性 | agentsview | 我们现有 | 差距 |
|------|-----------|---------|------|
| 会话创建/获取/删除 | ✅ Redis CRUD | ✅ Manager 完整实现 | 无差距 |
| 会话状态机 | ❌（无显式状态机） | ✅ 14 状态状态机 | 我们更强 |
| 会话存储 | SQLite + PostgreSQL + DuckDB | Redis（热）+ PostgreSQL（温） | 架构不同但功能等价 |
| 会话过滤 | 项目/代理/机器/日期/消息数/终止/健康 | 合规状态/意图/成本/搜索 | 我们的过滤器维度少 |
| 会话列表分页 | ✅ 游标分页 + 虚拟滚动 | ✅ page/offset 分页 | 功能等价 |
| 会话语义搜索 | ✅ 全文/正则/模糊搜索 | ❌ 仅 ID 搜索 | **严重差距** |
| 会话批量操作 | ✅ 多选 + 批量删除/恢复 | ❌ 不支持 | 差距 |
| 侧边栏会话分组 | ✅ 按状态(working/waiting/idle/stale) 分组 | ❌ 无分组 | 差距 |
| 会话实时更新 | ✅ SSE 推送 | ❌ 前端未消费 SSE | 差距 |
| 会话软删除（回收站） | ✅ 回收站 + 10s 撤销 | ❌ 直接删除 | 差距 |
| 会话分层展示 | ✅ 瘦行 + 懒加载水合 | ✅ 一次性加载 | 体验差距 |

### 2.2 会话健康与智能 (Session Health & Intelligence)

| 特性 | agentsview | 我们现有 | 差距 |
|------|-----------|---------|------|
| 健康评分 (0-100) | ✅ 基于多信号 | ❌ | **严重差距** |
| 健康等级 (A-F) | ✅ Grade 映射 | ❌ | **严重差距** |
| 结果分类 (outcome) | ✅ complete/abandon/error/unknown | ❌ | **严重差距** |
| 工具失败检测 | ✅ tool_failure_signal_count | ❌ | **严重差距** |
| 上下文压力监控 | ✅ context_pressure_max | ❌ | **严重差距** |
| 编辑改动率 | ✅ edit_churn_count | ❌ | **严重差距**（但我们没有编辑器上下文） |
| 质量信号 | ✅ 短提示/未结构化开始/重复提示/无代码上下文 | ❌ | **严重差距** |
| 秘密泄露检测 | ✅ secret_leak_count | ✅ （output_compliance 模块） | 功能等价，数据模型不同 |
| 合规状态 | ❌ | ✅ compliant/warning/violation | 我们更强 |

### 2.3 分析与仪表板 (Analytics & Dashboard)

| 特性 | agentsview | 我们现有 | 差距 |
|------|-----------|---------|------|
| 摘要统计卡片 | ✅ 6 张（会话/消息/项目/天数/均值/集中度） | ✅ 4 张（总数/活跃/成本/合规率） | 基本等价 |
| 活动时间线 | ✅ 柱状图（日/周/月粒度） | ❌ | **严重差距** |
| 日历热力图 | ✅ GitHub 风格 | ❌ | **严重差距** |
| 项目分解 | ✅ 按项目多维统计 | ❌ | 差距（我们场景不同） |
| 星期几/小时热力图 | ✅ 24x7 网格 | ❌ | **严重差距** |
| 会话形态分布 | ✅ 时长/消息数直方图 | ❌ | **严重差距** |
| 速度指标 | ✅ 轮次周期/首响应/每分钟指标 | ❌ | **严重差距** |
| 工具使用统计 | ✅ 按类别/代理/趋势 | ❌ | **严重差距** |
| 技能使用统计 | ✅ 按技能名/调用次数/趋势 | ❌ | 差距（我们无技能系统） |
| 热门会话排名 | ✅ 按消息/时长/输出 token | ❌ | **严重差距** |
| 成本分解 | ✅ 按模型/提供商表格 | ✅ 按模型的文本列表 | 部分实现，缺乏可视化 |
| 缓存节省分析 | ✅ cache_read_tokens + 按模型估算 | ✅ panaroma 中有 basic | 基本覆盖 |
| 压缩节省分析 | ✅ 压缩请求数 + token 估算 | ✅ panaroma 中有 basic | 基本覆盖 |
| 模型切换可视化 | ❌ | ✅ 时间线 | 我们更强 |
| 合规问题详情 | ❌ | ✅ 表格 + 类型/严重度 | 我们更强 |

### 2.4 会话详情与全景 (Session Detail & Panorama)

| 特性 | agentsview | 我们现有 | 差距 |
|------|-----------|---------|------|
| 会话元数据概览 | ✅ 基本信息 + 统计 | ✅ AnalyticsSessionSummary | 基本等价 |
| 消息列表 | ✅ 分页加载 | ✅ 通过 request_logs | 功能等价 |
| 工具调用展示 | ✅ 专用工具调用列表 | ❌ 仅在消息 preview 中有 | 差距 |
| 逐步摘要 | ✅ 请求/回复摘要 | ✅ session_request_summaries | 功能等价 |
| 标签系统 | ❌ | ✅ session_tags | 我们更强 |
| 优化建议 | ❌ | ✅ session_optimization_suggestions | 我们更强 |
| 聚类/分组 | ❌ | ✅ session_clusters | 我们更强 |
| 会话对比 | ❌ | ✅ SessionCompareView | 我们更强 |
| Handoff | ❌ | ✅ 上下文超限交接 | 我们更强 |

### 2.5 使用与成本 (Usage & Cost)

| 特性 | agentsview | 我们现有 | 差距 |
|------|-----------|---------|------|
| 总成本统计 | ✅ 随时间变化 | ✅ 基础聚合 | 基本等价 |
| 按模型成本 | ✅ 堆叠面积/柱状/折线 | ✅ 文本列表 | 可视化差距 |
| 按提供商成本 | ✅ 归属视图 | ❌ | 差距 |
| 同比/环比对比 | ✅ | ❌ | 差距 |
| 成对比较 | ✅ 两种配置并排 | ❌ | 差距 |
| 成本预测/趋势 | ❌ | ❌ | 差距（P3） |

---

## 三、现状架构分析

### 3.1 现有架构优势

```
┌─────────────────────────────────────────────────────┐
│                   现有架构优势                        │
├─────────────────────────────────────────────────────┤
│ 1. 双存储架构：Redis 热层 + PostgreSQL 持久化        │
│    - Redis：实时会话状态、毫秒级 CRUD                 │
│    - PostgreSQL：历史数据、复杂分析查询               │
│ 2. 完整的会话状态机（14 状态，Lua 原子更新）          │
│ 3. 凭证轮换追踪（CredRotationEntry）                  │
│ 4. 异步写入器确保 Redis 不丢数据                      │
│ 5. 模块化设计（admin/modules.go 16 个功能模块）        │
│ 6. 多租户隔离（tenant_id 贯穿所有查询）               │
│ 7. 全景图数据聚合层（一次性返回所有信息）              │
│ 8. SessionContext 丰富的请求级上下文（30+ 字段）      │
└─────────────────────────────────────────────────────┘
```

### 3.2 现有架构短板

```
┌─────────────────────────────────────────────────────┐
│                   现有架构短板                        │
├─────────────────────────────────────────────────────┤
│ 1. 缺乏统一的分析过滤系统                            │
│    - session_analytics 列表过滤器独立于全景图         │
│    - 无跨视图过滤器同步机制                          │
│    - 无持久化过滤器状态                              │
│ 2. 分析维度不足                                      │
│    - 仅有聚合统计和列表                              │
│    - 无时间序列趋势                                  │
│    - 无分布/分解分析                                 │
│ 3. 会话健康体系缺失                                  │
│    - 无健康评分/等级                                 │
│    - 无质量信号追踪                                  │
│    - 无结果分类                                      │
│ 4. 前端可视化能力弱                                  │
│    - 仅有 Element Plus 统计卡片 + 表格               │
│    - 无 Chart.js 图表使用（依赖已在但未用）           │
│    - 仪表板布局简单                                  │
│ 5. 数据管道待完善                                    │
│    - request_logs 索引不完整                         │
│    - 缺少定时聚合任务                                │
│    - 会话清理时引用完整性待确认                      │
│ 6. 实时能力未释放                                    │
│    - 后端有 SSE broadcaster                          │
│    - 前端未消费实时事件                              │
└─────────────────────────────────────────────────────┘
```

---

## 四、改进方案

### 4.1 阶段一：分析后端 API 增强

#### 4.1.1 新增分析端点

**端点 1：活动时间线**
```http
GET /api/admin/session-analytics/activity?date_from=&date_to=&granularity=day|week|month&model=&provider=
```
```json
{
  "series": [
    {"date": "2026-07-01", "request_count": 42, "success_count": 40, "error_count": 2, "total_cost_usd": 1.23, "total_tokens": 15000},
    ...
  ]
}
```
- 数据源：`request_logs` 按 `ts` 日期聚合
- 用途：活动趋势柱状图

**端点 2：模型/提供商分解**
```http
GET /api/admin/session-analytics/model-breakdown?date_from=&date_to=
```
```json
{
  "by_model": [
    {"model": "gpt-4o", "request_count": 100, "total_cost_usd": 5.0, "total_tokens": 50000, "avg_latency_ms": 1200},
    ...
  ],
  "by_provider": [
    {"provider": "openai", "request_count": 150, "total_cost_usd": 8.0, ...},
    ...
  ]
}
```
- 数据源：`request_logs` 按 `outbound_model` / `provider` 分组
- 用途：模型/提供商饼图/环形图

**端点 3：成本趋势**
```http
GET /api/admin/session-analytics/cost-trend?date_from=&date_to=&group_by=day|week|model|provider
```
```json
{
  "series": [
    {"period": "2026-07-01", "total_cost_usd": 12.5, "input_cost_usd": 5.0, "output_cost_usd": 7.5},
    ...
  ]
}
```
- 数据源：`request_logs` + `session_summaries`
- 用途：折线图/堆叠面积图

**端点 4：会话健康分布**
```http
GET /api/admin/session-analytics/health-distribution?date_from=&date_to=
```
```json
{
  "compliance_distribution": {"compliant": 80, "warning": 15, "violation": 5},
  "quality_distribution": {"90-100": 20, "80-89": 35, "70-79": 25, "60-69": 15, "<60": 5},
  "latency_distribution": {"<1s": 30, "1-3s": 40, "3-5s": 20, "5-10s": 8, ">10s": 2},
  "error_rate_distribution": {"0%": 50, "1-10%": 30, "10-25%": 15, ">25%": 5}
}
```
- 数据源：`session_summaries`（聚合质量/合规/延迟/错误率）
- 用途：健康柱状图

**端点 5：热门会话排名**
```http
GET /api/admin/session-analytics/top-sessions?metric=cost|tokens|latency|duration&limit=10&date_from=&date_to=
```
- 数据源：`session_summaries`
- 用途：热门会话表格，可跳转到全景图

**端点 6：会话形态分布**
```http
GET /api/admin/session-analytics/session-shape?date_from=&date_to=
```
```json
{
  "request_count_buckets": [
    {"range": "1-5", "count": 100},
    {"range": "6-20", "count": 80},
    {"range": "21-50", "count": 40},
    {"range": "51-100", "count": 20},
    {"range": ">100", "count": 10}
  ],
  "duration_buckets": [
    {"range": "<1min", "count": 50},
    {"range": "1-5min", "count": 100},
    {"range": "5-30min", "count": 80},
    {"range": "30-60min", "count": 30},
    {"range": ">1h", "count": 10}
  ]
}
```
- 数据源：`session_summaries`
- 用途：会话形态直方图

#### 4.1.2 增强现有列表 API

在 `HandleSessionAnalyticsList` 中增加过滤参数：

| 参数 | 类型 | 说明 |
|------|------|------|
| `model` | string | 模型名（逗号分隔多选） |
| `provider` | string | 提供商 |
| `date_from` | string | 开始日期范围 |
| `date_to` | string | 结束日期范围 |
| `latency_min` | int | 最小延迟 (ms) |
| `latency_max` | int | 最大延迟 (ms) |
| `token_min` | int | 最小总 token |
| `token_max` | int | 最大总 token |
| `request_count_min` | int | 最少请求数 |

#### 4.1.3 会话健康评分逻辑

```go
// admin/session_health.go

type HealthGrade string
const (
    GradeA HealthGrade = "A"
    GradeB HealthGrade = "B"
    GradeC HealthGrade = "C"
    GradeD HealthGrade = "D"
    GradeF HealthGrade = "F"
)

type SessionHealth struct {
    HealthScore     int          `json:"health_score"`     // 0-100
    HealthGrade     HealthGrade  `json:"health_grade"`     // A-F
    Outcome         string       `json:"outcome"`          // completed / error / abandoned / unknown
    ErrorRate       float64      `json:"error_rate"`       // 错误请求占比
    AvgLatency      int          `json:"avg_latency_ms"`
    ModelStability  float64      `json:"model_stability"`  // 模型切换频率逆指标
    CostEfficiency  float64      `json:"cost_efficiency"`  // tokens per dollar
}

// 计算逻辑
func computeSessionHealth(summary *AnalyticsSessionSummary, timeline []RequestEvent) SessionHealth {
    // 基础分 = 100
    score := 100

    // 扣分项
    if summary.ErrorCount > 0 {
        ratio := float64(summary.ErrorCount) / float64(summary.RequestCount)
        score -= int(ratio * 50) // 错误率最多扣 50 分
    }
    if summary.ComplianceIssuesCount > 0 {
        score -= summary.ComplianceIssuesCount * 10 // 每个合规问题扣 10 分
    }
    if summary.AvgLatencyMs > 5000 {
        score -= 15 // 高延迟扣 15 分
    }
    if summary.ModelSwitchCount > 3 {
        score -= 10 // 频繁模型切换扣 10 分
    }

    // 扣分封顶
    if score < 0 { score = 0 }

    // 等级映射
    grade := GradeF
    switch {
    case score >= 90: grade = GradeA
    case score >= 75: grade = GradeB
    case score >= 60: grade = GradeC
    case score >= 40: grade = GradeD
    }

    // 结果推断
    outcome := "unknown"
    if summary.ErrorCount == 0 && summary.RequestCount >= 2 {
        outcome = "completed"
    } else if float64(summary.ErrorCount) / float64(summary.RequestCount) > 0.5 {
        outcome = "error"
    } else if summary.RequestCount <= 1 {
        outcome = "abandoned"
    }

    return SessionHealth{
        HealthScore:    score,
        HealthGrade:    grade,
        Outcome:        outcome,
        ErrorRate:      float64(summary.ErrorCount) / float64(summary.RequestCount),
        AvgLatency:     summary.AvgLatencyMs,
        ModelStability: 1.0 - float64(summary.ModelSwitchCount) / float64(summary.RequestCount),
        CostEfficiency: float64(summary.TotalTokens) / summary.TotalCostUSD,
    }
}
```

#### 4.1.4 新增文件

| 文件 | 说明 |
|------|------|
| `admin/session_health.go` | 会话健康评分逻辑 |
| `admin/session_analytics_activity.go` | 活动时间线端点 |
| `admin/session_analytics_breakdown.go` | 模型/提供商分解端点 |
| `admin/session_analytics_trends.go` | 成本/健康趋势端点 |
| `admin/session_analytics_shape.go` | 会话形态分布端点 |
| `admin/session_analytics_top.go` | 热门会话排名端点 |

---

### 4.2 阶段二：前端分析仪表板重构

#### 4.2.1 组件架构

```
SessionAnalyticsDashboardView.vue
├── DashboardModuleGate.vue          # 模块启用检测
├── DashboardStatsRow.vue             # 统计卡片行（7 张卡片）
├── DashboardFilterBar.vue            # 统一过滤器栏
│   ├── DateRangePicker
│   ├── ModelSelect
│   ├── ProviderSelect
│   ├── ComplianceSelect
│   ├── IntentSelect
│   └── SearchInput
├── DashboardGrid.vue                 # 2 列图表网格
│   ├── DashboardActivityTimeline.vue # 活动时间线（柱状图）
│   ├── DashboardModelBreakdown.vue   # 模型分解（环形图）
│   ├── DashboardCostTrend.vue        # 成本趋势（折线图）
│   ├── DashboardLatencyTrend.vue     # 延迟趋势（折线图）
│   ├── DashboardHealthDistribution.vue # 健康分布（柱状图）
│   ├── DashboardSessionShape.vue     # 会话形态（直方图）
│   └── DashboardTopSessions.vue      # 热门会话（表格）
└── SessionListTable.vue             # 增强会话列表
```

#### 4.2.2 布局设计

```
┌──────────────────────────────────────────────────┐
│ DashboardHeader: "会话分析 Dashboard" [刷新]     │
├──────────────────────────────────────────────────┤
│ [模块启用检测] 未启用时显示引导提示                 │
├──────────────────────────────────────────────────┤
│ StatsRow: 7 张指标卡片                            │
│ 会话总数 | 活跃会话 | 总成本 | 合规率 | 健康分    │
│ 平均延迟 | 总请求数                               │
├──────────────────────────────────────────────────┤
│ FilterBar: 日期 | 模型 | 提供商 | 合规 | 意图     │
│          成本范围 | 搜索 [查询] [重置]             │
├──────────────────┬───────────────────────────────┤
│ 活动时间线        │   模型分解                      │
│ (Chart.js 柱状图) │   (Chart.js 环形图)            │
├──────────────────┼───────────────────────────────┤
│ 成本趋势          │   延迟趋势                      │
│ (Chart.js 折线图) │   (Chart.js 折线图)            │
├──────────────────┴───────────────────────────────┤
│ 健康分布（柱状图：合规/质量/延迟等级）              │
├──────────────────┬───────────────────────────────┤
│ 会话形态分布      │   热门会话列表                  │
│ (直方图)          │   (可排序表格)                  │
├──────────────────┴───────────────────────────────┤
│ 会话列表（增强版，含健康指标列）                   │
│ 标题 | 开始时间 | 时长 | 成本 | 健康 | 合规 | 操作 │
│ 分页 + 排序 + 过滤                                 │
└──────────────────────────────────────────────────┘
```

#### 4.2.3 Chart.js 使用规范

创建统一的 Chart.js Vue 3 composable：

```typescript
// web/src/composables/useChart.ts
import { ref, onMounted, onUnmounted, type Ref } from 'vue'
import { Chart, registerables } from 'chart.js'
Chart.register(...registerables)

export function useChart(
  canvasRef: Ref<HTMLCanvasElement | null>,
  config: () => ChartConfiguration
) {
  let chart: Chart | null = null

  const render = () => {
    if (chart) chart.destroy()
    if (!canvasRef.value) return
    chart = new Chart(canvasRef.value, config())
  }

  onMounted(render)
  onUnmounted(() => { if (chart) chart.destroy() })

  return { render }
}
```

图表类型选择：

| 数据特性 | 推荐图表 | Chart.js 类型 |
|---------|---------|---------------|
| 时间序列趋势 | 折线图/面积图 | `line` |
| 分类占比 | 环形图/饼图 | `doughnut`/`pie` |
| 活动分布 | 柱状图 | `bar` |
| 多分类对比 | 分组柱状图 | `bar` (grouped) |
| 分布直方图 | 柱状图 | `bar` |

#### 4.2.4 统一过滤器系统

```typescript
// web/src/composables/useAnalyticsFilters.ts
export interface AnalyticsFilters {
  dateFrom: string        // ISO date string
  dateTo: string
  model: string[]         // 多选模型
  provider: string[]      // 多选提供商
  complianceStatus: string // compliant / warning / violation
  userIntent: string      // chat / code / tool_use / ...
  minCost: number | null
  maxCost: number | null
  search: string
  latencyMin: number | null
  latencyMax: number | null
  tokenMin: number | null
  tokenMax: number | null
  requestCountMin: number | null
}

export function useAnalyticsFilters() {
  // 从 sessionStorage 恢复
  const filters = reactive<AnalyticsFilters>(loadFilters())

  // 持久化到 sessionStorage
  watchEffect(() => saveFilters(filters))

  // 构建 API 请求参数
  const apiParams = computed(() => buildApiParams(filters))

  // 重置到默认值
  const reset = () => { ... }

  return { filters, apiParams, reset }
}
```

#### 4.2.5 文件清单

| 文件 | 说明 |
|------|------|
| `web/src/components/dashboard/DashboardStatsRow.vue` | 统计卡片行 |
| `web/src/components/dashboard/DashboardFilterBar.vue` | 统一过滤器栏 |
| `web/src/components/dashboard/DashboardActivityTimeline.vue` | 活动时间线柱状图 |
| `web/src/components/dashboard/DashboardModelBreakdown.vue` | 模型分解环形图 |
| `web/src/components/dashboard/DashboardCostTrend.vue` | 成本趋势折线图 |
| `web/src/components/dashboard/DashboardLatencyTrend.vue` | 延迟趋势折线图 |
| `web/src/components/dashboard/DashboardHealthDistribution.vue` | 健康分布柱状图 |
| `web/src/components/dashboard/DashboardSessionShape.vue` | 会话形态直方图 |
| `web/src/components/dashboard/DashboardTopSessions.vue` | 热门会话排名 |
| `web/src/composables/useAnalyticsFilters.ts` | 过滤器 composable |
| `web/src/composables/useChart.ts` | Chart.js 封装 |

---

### 4.3 阶段三：会话全景图增强

#### 4.3.1 增强的 Panorama API 返回

在 `SessionPanorama` 中增加健康数据和可视化数据：

```go
type SessionPanorama struct {
    // 现有字段
    Summary       AnalyticsSessionSummary     `json:"summary"`
    Timeline      []RequestEvent              `json:"timeline"`
    StepSummaries []SessionStepSummary        `json:"step_summaries"`
    Tags          []SessionTag                `json:"tags"`
    Suggestions   []SessionOptimizationSugg   `json:"suggestions"`
    Cluster       *SessionClusterMembership   `json:"cluster,omitempty"`
    Analysis      SessionAnalysis             `json:"analysis"`
    ModuleEnabled bool                        `json:"module_enabled"`

    // 新增字段
    Health        SessionHealth               `json:"health"`          // 健康评分
    CostFlow      []CostFlowNode              `json:"cost_flow"`       // 成本流向图数据
    QualitySignals []QualitySignal            `json:"quality_signals"` // 质量信号
}
```

#### 4.3.2 前端 Panorama 增强

在现有 `SessionPanoramaView.vue` 中增加：

- **健康评分面板**：显示健康分 + 等级（A-F 带颜色）+ 结果标签
- **质量信号面板**：列出每次请求的质量指标
- **成本流向可视化**：基于 Chart.js 的成本分解图
- **增强摘要**：如果 LLM 摘要缺失，显示规则生成的基础摘要
- **导出增强**：新增 CSV 导出按钮

---

### 4.4 阶段四：会话列表增强

#### 4.4.1 增强 SessionListView

| 增强项 | 说明 |
|--------|------|
| 状态标签 | active（绿色）/ stopped（灰色）/ recovered（蓝色） |
| 健康指标列 | 健康等级（A-F 带颜色）+ 评分数字 |
| 实时状态 | 活跃会话显示 "正在运行" 标签 |
| 富过滤器 | 模型、提供商、延迟、token、请求数范围 |
| 排序增强 | 按健康评分、成本效率、延迟排序 |
| 批量操作 | 批量停止/恢复 |
| 会话分组 | 按状态分组（活跃/已停止/已恢复） |

---

### 4.5 阶段五：数据管道增强

#### 4.5.1 request_logs 索引

```sql
CREATE INDEX IF NOT EXISTS idx_request_logs_gw_session_id ON request_logs(gw_session_id);
CREATE INDEX IF NOT EXISTS idx_request_logs_ts ON request_logs(ts);
CREATE INDEX IF NOT EXISTS idx_request_logs_tenant_ts ON request_logs(tenant_id, ts);
```

#### 4.5.2 定时聚合任务

新增后台 Worker（`bg/session_analytics_agg.go`），定期执行：

```
每 5 分钟：
  从 request_logs 聚合最近数据到 session_summaries

每小时：
  更新会话健康评分
  清理过期 session_optimization_suggestions

每天：
  清理 >90 天的 request_logs 原始数据
  更新 session_clusters（如果启用自动聚类）
```

#### 4.5.3 session_db_writer 增强

在 `WriteSnapshot` 方法中增加健康评分写入：

```go
func (w *DBWriter) WriteSnapshot(ctx context.Context, sessionID string, session *Session, health *SessionHealth) error {
    // 现有逻辑
    // 新增：写入健康评分到 session_summaries 的相关列
}
```

---

## 五、优先级与路线图

### 5.1 优先级矩阵

| 阶段 | 内容 | 优先级 | 工作量估计 | 业务价值 |
|------|------|--------|-----------|---------|
| 一 | 分析后端 API 增强 | P0 | 5-7 人日 | 高 |
| 二 | 前端分析仪表板 | P0 | 8-10 人日 | 高 |
| 三 | 会话全景图增强 | P1 | 3-5 人日 | 中 |
| 四 | 会话列表增强 | P1 | 3-5 人日 | 中 |
| 五 | 数据管道增强 | P2 | 3-5 人日 | 低（前置依赖） |

### 5.2 推荐实施顺序

```
第一阶段 ──→ 第二阶段 ──→ 第三阶段 ──→ 第四阶段 ──→ 第五阶段
(后端 API)    (前端仪表板)  (全景图)    (列表)      (管道)

备注：
- 阶段一和五有数据依赖，建议阶段五先于或并行于阶段一
- 阶段二的 UI 组件可复用至阶段三和四
```

### 5.3 里程碑

| 里程碑 | 交付物 | 目标时间 |
|--------|--------|---------|
| M1 | 后端 6 个新分析端点 + 健康评分逻辑 | Day 5 |
| M2 | 前端仪表板全部面板可渲染 | Day 12 |
| M3 | Panorama 视图增强 + 健康面板 | Day 15 |
| M4 | 会话列表增强 + 数据管道 | Day 18 |

---

## 六、技术风险评估

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|---------|
| request_logs 数据量大导致查询慢 | 中 | 高 | 确保索引、LIMIT 约束、时间范围强制 |
| session_summaries 数据不完整 | 低 | 高 | 增加数据完整性检查、后台补数据任务 |
| Chart.js 与 Element Plus 样式冲突 | 低 | 中 | 使用 scoped CSS + 统一主题变量 |
| 新增 API 增加后端负载 | 中 | 中 | 分析端点设置 15s 超时、增加缓存 |
| 多租户数据隔离漏洞 | 低 | 高 | tenant_id 参数化查询、审计所有新增 SQL |

---

## 七、与 agentsview 的关键架构差异说明

| 维度 | agentsview | 我们 | 设计理由 |
|------|-----------|------|---------|
| **数据来源** | 本地文件解析 + 静态分析 | 网关请求日志 + 实时 Redis | 我们是代理间网关，天然有请求日志流 |
| **健康评分** | 基于解析器信号（tool_failure, compaction 等） | 基于网关信号（错误率、延迟、合规、模型切换） | 我们的数据源不包含编辑器工具信号 |
| **会话分组** | 按 project/agent/machine 自然分组 | 按 tenant_id 多租户隔离 | SaaS 架构要求租户隔离 |
| **实时性** | 文件 watch + EventSource | Redis → 异步写入 → 轮询查询 | 我们的写路径有异步层，轮询更可靠 |
| **会话 ID** | 文件路径哈希 | gw_xxx 格式 | UUID 更适合分布式系统 |
| **标签系统** | 无 | 手动 + 自动标签 | 运营管理需要 |
| **聚类** | 无 | 基于标签/向量聚类 | 企业级平台需要 |
| **优化建议** | 无 | session_optimization_suggestions | 运营优化需要 |

---

## 八、附录

### 8.1 agentsview 参考端点清单

| agentsview 端点 | 用途 | 是否映射到我们的方案 |
|----------------|------|-------------------|
| `/api/v1/analytics/summary` | 聚合指标 | ✅ 已有 stats 等价 |
| `/api/v1/analytics/activity` | 活动时间线 | ✅ 方案 4.1.1 |
| `/api/v1/analytics/heatmap` | 日历热力图 | ⏳ 阶段二增强项 |
| `/api/v1/analytics/projects` | 项目分解 | ❌ 不适用（无项目概念） |
| `/api/v1/analytics/hour-of-week` | 小时热力图 | ⏳ 阶段二增强项 |
| `/api/v1/analytics/sessions` | 会话形态分布 | ✅ 方案 4.1.1 |
| `/api/v1/analytics/velocity` | 速度指标 | ⏳ 阶段二增强项 |
| `/api/v1/analytics/tools` | 工具使用 | ❌ 不适用（无工具概念） |
| `/api/v1/analytics/skills` | 技能使用 | ❌ 不适用（无技能系统） |
| `/api/v1/analytics/top-sessions` | 热门会话 | ✅ 方案 4.1.1 |
| `/api/v1/analytics/signals` | 会话健康 | ✅ 方案 4.1.3 |
| `/api/v1/analytics/signal-sessions` | 信号示例 | ⏳ 阶段三增强项 |

### 8.2 现有数据库表结构参考

| 表 | 用途 | 阶段使用 |
|---|------|---------|
| `session_summaries` | 会话级聚合摘要 | 所有阶段 |
| `request_logs` | 请求级日志 | 阶段一、二 |
| `session_tags` | 会话标签 | 阶段三 |
| `session_optimization_suggestions` | 优化建议 | 阶段三 |
| `session_clusters` | 聚类组 | 阶段三 |
| `session_cluster_members` | 聚类成员 | 阶段三 |
| `session_request_summaries` | 逐步摘要 | 阶段三 |
| `prompt_injection_detections` | 提示注入检测 | 阶段二（合规面板） |
| `output_compliance_audit` | 输出合规审计 | 阶段二（合规面板） |
| `session_credential_rotations` | 凭证轮换历史 | 阶段三（切换可视化） |

### 8.3 术语对照表

| agentsview 术语 | 我们的术语 | 说明 |
|----------------|-----------|------|
| project | tenant_id | 租户隔离 |
| agent | provider | LLM 提供商 |
| health_score | health_score (新建) | 健康评分 |
| outcome | outcome (新建) | 结果分类 |
| tool_failure | request_logs.error | 请求错误 |
| compaction | compression_strategy | 压缩策略 |
| context_pressure | avg_latency_ms | 上下文压力近似指标 |

---

*文档版本：v1.0*
*编制日期：2026-07-06*
*基于 agentsview (kenn-io) 代码审计*
