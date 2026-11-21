# 会话分析系统整合实施方案

## 目标

1. 完善优化 `/dashboard` 中的会话与统计模块
2. 确认数据统计标准一致并且正确（对比 `/admin/session-analytics`）
3. 对 top5 的模型、用户、供应商、任务等进行统计，并能点击查看详细列表
4. 核实"会话趋势"数据与 `/admin/session-analytics` 一致
5. 取消 `/admin/session-analytics` 页面
6. 将该页内容重新组织后移到 `/admin/session-analytics/users`

## 当前状况分析

### 1. 数据源分析

#### Dashboard (`/dashboard`)
- **API**: `/api/admin/dashboard/session-overview`
- **数据源**: `session_summaries` + `session_dim` 表（实时数据）
- **特点**: 
  - 实时统计，无缓存
  - 包含 topClients、topTasks、modelUsage
  - **缺少**: 用户排行（topUsers）、供应商排行（topProviders）
- **已有对齐机制**: `aligned` 字段指向 session-analytics 数据

#### Session Analytics (`/admin/session-analytics`)
- **API**: `/api/admin/session-analytics/overview`
- **数据源**: 物化视图 `user_session_aggregates`、`model_usage_aggregates` 等
- **特点**:
  - 15分钟刷新缓存
  - 完整的统计数据（summary、topModels、topUsers、topProjects、trends）
  - 数据更全面但有延迟

### 2. 数据一致性现状

**✅ 已对齐**:
- Dashboard 的 `overview.aligned` 字段已经通过 `domains/analytics.QueryOverviewSummary` 与 session-analytics 对齐
- 两者使用相同的 SQL 查询实现
- 窗口语义统一（上海时区日历日）

**❌ 待整合**:
- Dashboard 缺少用户排行数据
- Dashboard 缺少供应商排行数据
- Top5 数据未实现点击跳转到详情页

### 3. 路由结构

当前路由:
```
/dashboard                              - Dashboard 主页（包含 stats tab）
/admin/session-analytics                - 会话分析总览（待移除）
/admin/session-analytics/users          - 用户画像列表（旧功能，路径被占用）
/admin/session-analytics/user-ranking   - 用户排行（新功能）
/admin/session-analytics/user-ranking/:owner  - 用户详情
/admin/session-analytics/agent-ranking  - 智能体排行
/admin/session-analytics/model-ranking  - 模型排行
```

## 实施方案

### 阶段1: 增强 Dashboard 数据展示

#### 1.1 添加用户 Top5 排行
- **前端**: 在 `SessionStatsRankings.vue` 中添加 topUsers 部分
- **后端**: Dashboard API 已有对齐数据，直接使用 `aligned` 字段或新增调用
- **实现方式**: 
  ```typescript
  // 新增 props
  topUsers?: Array<{
    owner: string
    display_name: string
    session_count: number
    total_tokens: number
    total_cost: number
  }>
  ```

#### 1.2 添加供应商 Top5 排行
- **后端**: 需要新增 API 或扩展现有 API
- **数据源**: `model_usage_aggregates` 物化视图（按 provider 分组）
- **实现**: 
  ```sql
  SELECT 
    SPLIT_PART(model, '/', 1) as provider,
    COUNT(DISTINCT model) as model_count,
    SUM(request_count) as total_requests,
    SUM(total_cost) as total_cost
  FROM model_usage_aggregates
  WHERE date >= $1 AND date <= $2
  GROUP BY provider
  ORDER BY total_requests DESC
  LIMIT 5
  ```

#### 1.3 实现点击跳转功能
- **用户**: 点击跳转到 `/admin/session-analytics/user-ranking/:owner`
- **供应商**: 点击跳转到 `/admin/usage-trends?provider=xxx`
- **模型**: 已实现（跳转到 `/admin/usage-trends?model=xxx`）
- **任务**: 已实现（跳转到 `/admin/session-analytics/tasks/:id`）

### 阶段2: 数据口径验证

#### 2.1 会话趋势对比
- Dashboard 的 `session_trend` vs Session Analytics 的 `trends.sessions`
- 验证数据点是否一致
- 确认时区处理是否统一（上海时区）

#### 2.2 成本数据对比
- 确认内部成本（美元）和外部成本（积分）分离显示
- 禁止跨单位相加

### 阶段3: 页面整合与路由调整

#### 3.1 创建新的统一视图
创建 `SessionAnalyticsHubView.vue`，整合：
- 原 `/admin/session-analytics` 的总览内容
- 用户排行入口
- 模型排行入口
- 智能体排行入口

#### 3.2 调整路由
```typescript
// 移除
- { path: '/admin/session-analytics', component: SessionAnalyticsOverviewView }

// 修改
{ path: '/admin/session-analytics/users', component: SessionAnalyticsHubView }

// 保持
{ path: '/admin/session-analytics/user-ranking', component: SessionAnalyticsUserRankingView }
{ path: '/admin/session-analytics/user-ranking/:owner', component: SessionAnalyticsUserDetailView }
{ path: '/admin/session-analytics/agent-ranking', component: SessionAnalyticsAgentRankingView }
{ path: '/admin/session-analytics/model-ranking', component: SessionAnalyticsModelRankingView }
```

#### 3.3 更新菜单配置
- 将 `/admin/session-analytics` 菜单项指向新路径
- 或者移除该菜单项，仅保留 Dashboard 入口

## 技术细节

### 数据获取策略

1. **Dashboard 页面**:
   - 主数据: `/api/admin/dashboard/session-overview` (实时)
   - 对齐数据: 使用 `overview.aligned` 字段
   - Top5 用户/供应商: 新增 API 或扩展现有 API

2. **Session Analytics Hub**:
   - 完整数据: `/api/admin/session-analytics/overview`
   - 提供导航入口到各个排行页面

### API 扩展需求

#### Option A: 扩展 Dashboard API
```go
// admin/dashboard_session_stats.go
type SessionOverviewResponse struct {
    // ... 现有字段
    TopUsers     []UserRankItem     `json:"top_users"`      // 新增
    TopProviders []ProviderRankItem `json:"top_providers"`  // 新增
}
```

#### Option B: 在前端调用多个 API
```typescript
// 前端组合多个 API 结果
const overview = await fetchSessionOverview()
const analytics = await fetchSessionAnalytics() // session-analytics API
const topUsers = analytics.top_users.slice(0, 5)
const topProviders = computeTopProviders(analytics.top_models)
```

**推荐**: Option B - 前端组合，减少后端改动，利用现有 API

## 实施步骤

### 第1步: 增强前端组件 ✓
1. 修改 `SessionStatsRankings.vue`，添加 topUsers 和 topProviders
2. 实现点击跳转逻辑
3. 添加相应的 i18n 文本

### 第2步: 数据集成
1. 在 `useDashboard.ts` 中添加调用 session-analytics API
2. 处理数据转换和合并
3. 提取 top5 数据

### 第3步: 验证数据一致性
1. 对比 Dashboard 和 Session Analytics 的趋势数据
2. 确认口径一致
3. 编写测试用例

### 第4步: 页面整合
1. 创建 `SessionAnalyticsHubView.vue`
2. 整合原 overview 页面内容
3. 添加导航链接

### 第5步: 路由调整
1. 更新 `router.ts`
2. 移除旧路由
3. 更新菜单配置

### 第6步: 测试与验证
1. 功能测试
2. 数据一致性测试
3. 用户体验测试

## 风险与注意事项

1. **数据延迟**: Session Analytics 使用物化视图，有15分钟延迟
2. **路径冲突**: `/admin/session-analytics/users` 已被旧功能占用
3. **向后兼容**: 需要考虑已有的外部链接和书签
4. **性能影响**: 前端调用多个 API 可能影响加载速度

## 时间估算

- 阶段1: 2-3小时
- 阶段2: 1小时
- 阶段3: 2-3小时
- 测试: 1-2小时

**总计**: 6-9小时

## 成功标准

1. ✅ Dashboard 显示用户 Top5 和供应商 Top5
2. ✅ 所有 Top 项支持点击查看详情
3. ✅ 会话趋势数据与 session-analytics 一致
4. ✅ `/admin/session-analytics` 路由已移除或重定向
5. ✅ 新的统一页面功能完整
6. ✅ 所有测试通过
