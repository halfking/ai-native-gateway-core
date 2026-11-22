# 会话分析系统整合实施指南

> ⚠️ **2026-11-23 审计轮修正 —— 本文档的「Dashboard 侧接 session-analytics 取 Top5」方案已被否决**，不要照此实施。
>
> 否决理由（三条结构性缺陷，详见 `docs/audit/2026-11-23-会话分析整合-批判式审计与修正报告.md`）：
> ① **权限错配**——session-analytics 端点限 admin 角色，而 Dashboard 对所有登录用户可见
>    ⇒ 普通用户必然 403，只会看到空卡片；
> ② **双数据源混排**——Dashboard 实时腿与物化视图缓存腿并排展示，同区块两种时间口径；
> ③ **供应商口径错**——`overview.top_models` 只有 3 条且 `model` 列是纯模型名（无 provider 前缀），
>    前端 `split('/')[0]` 会把模型名当供应商。
>
> **现行方案**：Top5 四件套（模型/供应商/用户/任务）落在 `/admin/session-analytics/users`，
> 四维全部来自 session-analytics 物化视图族；供应商与任务由**新增的后端端点**按真实列聚合
> （`/provider-ranking`、`/task-ranking`）；Dashboard 回到单一数据源。
> 本文档保留作为「前一轮怎么想的」的记录，其代码示例**不可直接照抄**。


## 当前状态

### ✅ 已完成项
1. **数据口径已对齐**
   - Dashboard 的 `overview.aligned` 字段通过 `domains/analytics.QueryOverviewSummary` 与 session-analytics 同源
   - 时区处理统一（上海时区日历日）
   - 探测流量口径统一（默认排除探测）

2. **API 基础设施已完备**
   - Session Analytics API 已封装完整 (`web/src/api/sessionAnalytics.ts`)
   - 包含 7 个端点：overview, user-ranking, user-detail, user-projects, project-tasks, agent-ranking, model-ranking
   - 物化视图已建立（迁移 981）

3. **前端组件架构清晰**
   - `SessionStatsPanel.vue` - Dashboard 统计面板
   - `SessionStatsRankings.vue` - Top 排行展示
   - `DashboardStatsRow.vue` - KPI 指标卡片
   - `SessionTrendChart.vue` - 趋势图表

### ❌ 待实现项

1. **Dashboard 增强**
   - 添加用户 Top5 显示
   - 添加供应商 Top5 显示
   - 实现完整的点击跳转功能

2. **页面整合**
   - 创建统一的会话分析入口页
   - 调整路由结构
   - 更新菜单配置

## 详细实施步骤

### 步骤 1: 扩展 SessionStatsRankings 组件

#### 1.1 添加 Props

```typescript
// web/src/components/analytics/SessionStatsRankings.vue
const props = defineProps<{
  // 现有
  topClients: Array<{ client_id: string; session_count: number; total_cost: number; avg_health?: number | null }>
  topTasks: Array<{ task_id: string; session_count: number; total_cost: number; avg_health?: number | null }>
  modelUsage?: Array<{
    model: string
    session_count: number
    request_count: number
    total_cost: number
    success_rate: number
    avg_latency_ms: number
  }>
  trendWindow?: { start: string; end: string }
  
  // 新增
  topUsers?: Array<{
    owner: string
    display_name: string
    session_count: number
    total_tokens: number
    total_cost: number
  }>
  topProviders?: Array<{
    provider: string
    model_count: number
    total_requests: number
    total_cost: number
  }>
}>()
```

#### 1.2 添加用户排行模板

```vue
<section class="sa-list">
  <h3 class="sa-section__title">{{ t('sessions.stats.topUsers') }}</h3>
  <DataTable
    :loading="false"
    :empty="!topUsers || topUsers.length === 0"
    :empty-text="t('sessions.stats.emptyNoResultTopUsers')"
    min-width="520px"
  >
    <table class="sa-table">
      <thead>
        <tr>
          <th>{{ t('sessions.stats.user') }}</th>
          <th>{{ t('sessions.stats.sessions') }}</th>
          <th>{{ t('sessions.stats.tokens') }}</th>
          <th>{{ t('sessions.stats.cost') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="user in topUsers" :key="user.owner">
          <td>
            <button
              type="button"
              class="link-btn"
              @click="openUserDetail(user.owner)"
            >
              {{ user.display_name || user.owner }}
            </button>
          </td>
          <td class="num">{{ user.session_count }}</td>
          <td class="num">{{ user.total_tokens.toLocaleString() }}</td>
          <td class="num">{{ formatUsd(user.total_cost) }}</td>
        </tr>
      </tbody>
    </table>
  </DataTable>
</section>
```

#### 1.3 添加供应商排行模板

```vue
<section class="sa-list">
  <h3 class="sa-section__title">{{ t('sessions.stats.topProviders') }}</h3>
  <DataTable
    :loading="false"
    :empty="!topProviders || topProviders.length === 0"
    :empty-text="t('sessions.stats.emptyNoResultTopProviders')"
    min-width="520px"
  >
    <table class="sa-table">
      <thead>
        <tr>
          <th>{{ t('sessions.stats.provider') }}</th>
          <th>{{ t('sessions.stats.models') }}</th>
          <th>{{ t('sessions.stats.requests') }}</th>
          <th>{{ t('sessions.stats.cost') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="provider in topProviders" :key="provider.provider">
          <td>
            <button
              type="button"
              class="link-btn"
              @click="openProviderTrend(provider.provider)"
            >
              {{ provider.provider }}
            </button>
          </td>
          <td class="num">{{ provider.model_count }}</td>
          <td class="num">{{ provider.total_requests.toLocaleString() }}</td>
          <td class="num">{{ formatUsd(provider.total_cost) }}</td>
        </tr>
      </tbody>
    </table>
  </DataTable>
</section>
```

#### 1.4 添加跳转函数

```typescript
function openUserDetail(owner: string) {
  const w = props.trendWindow
  const query: Record<string, string> = {}
  if (w?.start && w?.end) {
    query.start_date = w.start
    query.end_date = w.end
  }
  void router.push({ 
    path: `/admin/session-analytics/user-ranking/${encodeURIComponent(owner)}`,
    query 
  })
}

function openProviderTrend(provider: string) {
  const w = props.trendWindow
  const query: Record<string, string> = { provider }
  if (w?.start && w?.end) {
    query.start = w.start
    query.end = w.end
  }
  void router.push({ path: '/admin/usage-trends', query })
}
```

### 步骤 2: 修改 SessionStatsPanel 集成数据

#### 2.1 添加 session-analytics API 调用

```typescript
// web/src/components/SessionStatsPanel.vue
import { getOverview as getSessionAnalyticsOverview, defaultDateRange } from '../api/sessionAnalytics'
import type { SessionAnalyticsOverview } from '../api/sessionAnalytics'

// 添加状态
const analyticsData = ref<SessionAnalyticsOverview | null>(null)
const analyticsLoading = ref(false)
const analyticsError = ref<string | null>(null)

// 添加获取函数
async function loadAnalyticsData() {
  if (!trendWindow.value) return
  
  analyticsLoading.value = true
  analyticsError.value = null
  
  try {
    const data = await getSessionAnalyticsOverview({
      start_date: trendWindow.value.start,
      end_date: trendWindow.value.end,
    })
    analyticsData.value = data
  } catch (err) {
    console.error('Failed to load analytics data:', err)
    analyticsError.value = String(err)
  } finally {
    analyticsLoading.value = false
  }
}

// 在数据加载时调用
watch(trendWindow, () => {
  void loadAnalyticsData()
}, { immediate: true })
```

#### 2.2 计算 Top5 数据

```typescript
const topUsers = computed(() => {
  return analyticsData.value?.top_users?.slice(0, 5) || []
})

const topProviders = computed(() => {
  const models = analyticsData.value?.top_models || []
  const providerMap = new Map<string, {
    provider: string
    model_count: number
    total_requests: number
    total_cost: number
  }>()
  
  models.forEach(m => {
    const provider = m.model.split('/')[0]
    const existing = providerMap.get(provider) || {
      provider,
      model_count: 0,
      total_requests: 0,
      total_cost: 0,
    }
    existing.model_count++
    existing.total_requests += m.count
    providerMap.set(provider, existing)
  })
  
  return Array.from(providerMap.values())
    .sort((a, b) => b.total_requests - a.total_requests)
    .slice(0, 5)
})
```

#### 2.3 传递给 Rankings 组件

```vue
<SessionStatsRankings
  v-if="overview"
  :top-clients="overview.top_clients || []"
  :top-tasks="overview.top_tasks || []"
  :model-usage="overview.model_usage || []"
  :top-users="topUsers"
  :top-providers="topProviders"
  :trend-window="trendWindow"
/>
```

### 步骤 3: 添加国际化文本

#### 3.1 中文 (zh-CN)

```typescript
// web/src/locales/zh-CN/sessions.ts
export default {
  stats: {
    // 现有...
    topUsers: '活跃用户 Top 5',
    topProviders: '供应商 Top 5',
    emptyNoResultTopUsers: '暂无用户数据',
    emptyNoResultTopProviders: '暂无供应商数据',
    user: '用户',
    provider: '供应商',
    models: '模型数',
    // ...
  }
}
```

#### 3.2 英文 (en-US)

```typescript
// web/src/locales/en-US/sessions.ts
export default {
  stats: {
    // existing...
    topUsers: 'Top 5 Active Users',
    topProviders: 'Top 5 Providers',
    emptyNoResultTopUsers: 'No user data',
    emptyNoResultTopProviders: 'No provider data',
    user: 'User',
    provider: 'Provider',
    models: 'Models',
    // ...
  }
}
```

### 步骤 4: 页面整合（可选，根据需求）

如果需要移除 `/admin/session-analytics` 并整合到其他页面：

#### 4.1 创建统一入口页

```vue
<!-- web/src/views/admin/session-analytics/SessionAnalyticsHubView.vue -->
<template>
  <div class="session-analytics-hub">
    <PageHeader :title="t('sessionAnalytics.title')">
      <!-- 时间范围选择器 -->
    </PageHeader>
    
    <!-- 总览卡片（从 Overview 移植） -->
    <StatsRow>
      <!-- KPI 卡片 -->
    </StatsRow>
    
    <!-- 导航卡片 -->
    <div class="nav-cards">
      <RouterLink to="/admin/session-analytics/user-ranking" class="nav-card">
        <h3>用户排行</h3>
        <p>查看用户活跃度和使用情况</p>
      </RouterLink>
      <RouterLink to="/admin/session-analytics/model-ranking" class="nav-card">
        <h3>模型排行</h3>
        <p>查看模型使用统计</p>
      </RouterLink>
      <RouterLink to="/admin/session-analytics/agent-ranking" class="nav-card">
        <h3>智能体排行</h3>
        <p>查看智能体使用情况</p>
      </RouterLink>
    </div>
  </div>
</template>
```

#### 4.2 调整路由

```typescript
// web/src/router.ts

// 移除或注释掉
// { path: '/admin/session-analytics', name: 'session-analytics-overview', component: SessionAnalyticsOverviewView, meta: { requiresAuth: true } },

// 修改或添加
{ path: '/admin/session-analytics', redirect: '/admin/session-analytics/user-ranking' },
// 或
{ path: '/admin/session-analytics', name: 'session-analytics-hub', component: SessionAnalyticsHubView, meta: { requiresAuth: true } },
```

### 步骤 5: 数据验证

#### 5.1 创建验证脚本

```typescript
// scripts/validate-session-analytics.ts
// 验证 Dashboard 和 Session Analytics 数据一致性

import { fetchSessionOverview } from '../web/src/api/dashboard'
import { getOverview } from '../web/src/api/sessionAnalytics'

async function validate() {
  const days = 7
  const dashboardData = await fetchSessionOverview({ days })
  const analyticsData = await getOverview(defaultDateRange(days))
  
  console.log('Dashboard aligned:', dashboardData.data.aligned)
  console.log('Analytics summary:', analyticsData.summary)
  
  // 对比数据
  if (dashboardData.data.aligned) {
    const aligned = dashboardData.data.aligned
    const summary = analyticsData.summary
    
    console.log('Total sessions match:', aligned.total_sessions === summary.total_sessions)
    console.log('Active users match:', aligned.active_users === summary.active_users)
    // ... 更多对比
  }
}
```

### 步骤 6: 测试清单

#### 功能测试
- [ ] Dashboard 显示用户 Top5
- [ ] Dashboard 显示供应商 Top5
- [ ] 点击用户名跳转到用户详情页
- [ ] 点击供应商名跳转到使用趋势页
- [ ] 点击模型名跳转到使用趋势页（已有）
- [ ] 时间范围变化时数据正确更新

#### 数据一致性测试
- [ ] Dashboard aligned 数据与 session-analytics summary 一致
- [ ] 趋势图数据匹配
- [ ] Top 排行数据准确

#### UI/UX 测试
- [ ] 移动端响应式布局正常
- [ ] 空数据状态显示正确
- [ ] 加载状态显示正确
- [ ] 错误状态处理得当

#### 性能测试
- [ ] 页面加载时间 < 2秒
- [ ] API 并发调用不阻塞
- [ ] 大数据量下渲染流畅

## 已知问题和注意事项

1. **物化视图延迟**
   - Session Analytics 使用物化视图，有 15 分钟刷新周期
   - Dashboard 是实时数据
   - 两者可能存在短暂不一致

2. **路径冲突**
   - `/admin/session-analytics/users` 已被旧的用户画像功能占用
   - 如需使用该路径需要先迁移旧功能

3. **成本单位**
   - 内部成本使用美元（USD）
   - 外部成本使用积分（credits）
   - 不能直接相加

4. **探测流量**
   - 默认排除探测流量
   - 用户可通过开关包含探测
   - 两种模式数据差异较大

## 预估工作量

- 步骤 1-2（组件增强和数据集成）: 2-3 小时
- 步骤 3（国际化）: 0.5 小时
- 步骤 4（页面整合，可选）: 2-3 小时
- 步骤 5-6（验证和测试）: 2 小时

**总计**: 6.5-8.5 小时

## 下一步行动

1. 确认是否需要完整实施所有步骤
2. 确认页面整合的具体方案
3. 开始编码实现
4. 逐步测试和验证
5. 提交代码审查

## 相关文件清单

### 需要修改的文件
- `web/src/components/analytics/SessionStatsRankings.vue`
- `web/src/components/SessionStatsPanel.vue`
- `web/src/locales/zh-CN/sessions.ts`
- `web/src/locales/en-US/sessions.ts`
- `web/src/router.ts` (如果需要路由调整)

### 可能需要新建的文件
- `web/src/views/admin/session-analytics/SessionAnalyticsHubView.vue` (如果需要整合页面)
- `scripts/validate-session-analytics.ts` (验证脚本)

### 相关后端文件（无需修改）
- `admin/session_analytics/handlers.go`
- `admin/session_analytics/queries.go`
- `admin/dashboard_session_stats.go`
