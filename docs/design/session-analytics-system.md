# 会话分析系统设计文档

## 1. 概述

### 1.1 目标

构建一个全面的会话分析与统计系统，为管理员提供对组织内 AI 使用情况的深入洞察，包括：
- 人员工作量与效率分析
- 项目与任务追踪
- 模型使用与成本优化
- 智能体（终端）使用统计

### 1.2 核心价值

- **可见性**：清晰了解团队 AI 使用情况
- **优化**：发现成本与效率优化机会
- **管理**：识别最活跃的项目、人员和任务
- **决策**：基于数据的资源分配与模型选择

### 1.3 用户角色

- **超级管理员**：查看所有租户数据
- **租户管理员**：查看本租户数据
- **团队负责人**：查看团队成员数据

---

## 2. 功能模块

### 2.1 总体统计仪表板

**路由**: `/admin/session-analytics/overview`

#### 功能说明

提供组织级或租户级的会话统计总览。

#### 关键指标

**核心指标卡片**（4-6个）：
1. **总会话数**（排除探测）
   - 当前周期总数
   - 环比增长率
   - 趋势 sparkline

2. **平均会话指标**
   - 平均 token 数
   - 平均上下文大小
   - 平均耗时
   - 平均轮次

3. **总成本**
   - 内部成本
   - 外部成本
   - 成本趋势

4. **活跃用户数**
   - 当前周期活跃用户
   - 环比变化

5. **项目与任务统计**
   - 活跃项目数
   - 活跃任务数
   - 项目分布

6. **模型使用概览**
   - 最常用模型 Top 3
   - 成功率
   - 缓存率

**趋势图表**：
- 会话量时间趋势（折线图，按日/周/月聚合）
- 成本时间趋势（双 Y 轴：内部 vs 外部）
- Token 使用趋势
- 用户活跃度热力图

**排行榜**（简化版，点击查看详情）：
- 最活跃人员 Top 10
- 最活跃项目 Top 10
- 最常用模型 Top 10

**时间范围选择器**：
- 今天、昨天、近 7 天、近 30 天、本月、上月
- 自定义范围

---

### 2.2 人员会话排行

**路由**: `/admin/session-analytics/user-ranking`

#### 功能说明

展示所有用户的会话统计，支持排序和筛选。

#### 列表字段

| 列名 | 说明 | 排序 |
|------|------|------|
| 排名 | 根据当前排序规则的排名 | - |
| 用户名 | 用户标识 (owner) | ✓ |
| 姓名/昵称 | 用户显示名 | ✓ |
| 会话数 | 总会话数（排除探测） | ✓ |
| 项目数 | 参与的项目数 | ✓ |
| 任务数 | 完成的任务数 | ✓ |
| 总 Token | 输入 + 输出 token 总和 | ✓ |
| 总成本 | 内部 + 外部成本 | ✓ |
| 最后活跃 | 最新会话的时间 | ✓ |
| 最新任务 | 最新任务简述 | - |

#### 筛选器

- **租户**（超级管理员可见）
- **时间范围**（统一组件）
- **活跃状态**：全部 / 活跃（7天内有会话）/ 不活跃
- **项目**：下拉选择具体项目

#### 排序模式

- **默认**：按总 Token 降序
- **工作量**：按会话数降序
- **时间**：按最后活跃时间降序
- **成本**：按总成本降序

#### 操作

- **点击行**：进入人员详情页

---

### 2.3 人员详情页

**路由**: `/admin/session-analytics/user-ranking/:owner`

#### 功能说明

展示单个用户的详细会话分析，包括趋势、项目/任务排行、会话详情。

#### 页面结构

**顶部信息卡**：
- 用户头像、姓名、用户名
- 关键指标（当前时间范围）：
  - 总会话数
  - 总任务数
  - 总成本
  - 排名及变化（↑↓）

**趋势图表**（折线图，按日聚合）：
- 会话数趋势
- 任务数趋势
- 成本趋势（双 Y 轴）
- 排名变化趋势（倒置 Y 轴，排名越小越高）

**Tab 1: 项目与任务排行**

两个子 Tab：

**a. 按工作量排序**

项目列表：
- 项目名
- 会话数
- 任务数
- Token 总量
- 成本
- 操作：查看详情

点击"查看详情"展开：
- 任务列表（该项目下的任务）
  - 任务名
  - 会话数
  - 请求数
  - Token 总量
  - 成本
  - 操作：查看会话列表

点击"查看会话列表"：
- 跳转到会话列表页，预填项目、任务筛选

**b. 按时间排序**

项目列表（按最新活动时间排序）：
- 项目名
- 最后活跃时间
- 会话数
- 任务数
- 操作：查看详情

**Tab 2: 模型使用统计**

模型使用明细表格（已实现）：
- 模型名称
- 供应商
- 请求次数
- 输入 tokens
- 输出 tokens
- 总 tokens
- 内部成本
- 外部成本

两个子 Tab：
- 按工作量排序（请求次数降序）
- 按时间排序（最后使用时间降序）

**Tab 3: 会话列表**

会话列表：
- 会话 ID
- 项目
- 任务
- 开始时间
- 持续时间
- 轮次
- 总 Token
- 成本
- 状态
- 操作：查看详情

筛选：
- 项目
- 任务
- 日期范围
- 状态

---

### 2.4 终端（智能体）排行

**路由**: `/admin/session-analytics/agent-ranking`

#### 功能说明

统计不同智能体（终端）的使用情况。

#### 列表字段

| 列名 | 说明 | 排序 |
|------|------|------|
| 排名 | 根据当前排序规则的排名 | - |
| 智能体名称 | Agent 标识 | ✓ |
| 使用用户数 | 使用该智能体的用户数 | ✓ |
| 会话数 | 总会话数 | ✓ |
| 任务数 | 完成的任务数 | ✓ |
| 总 Token | Token 总量 | ✓ |
| 总成本 | 成本总和 | ✓ |
| 平均耗时 | 平均会话耗时 | ✓ |
| 最后使用 | 最新使用时间 | ✓ |

#### 两个子 Tab

- **按工作量排序**（会话数降序）
- **按时间排序**（最后使用时间降序）

#### 操作

- **点击行**：展开详情，显示使用该智能体的用户排行

---

### 2.5 模型使用排行

**路由**: `/admin/session-analytics/model-ranking`

#### 功能说明

统计不同模型的使用情况与性能指标。

#### 列表字段

| 列名 | 说明 | 排序 |
|------|------|------|
| 排名 | 根据当前排序规则的排名 | - |
| 模型名称 | 模型标识 | ✓ |
| 供应商 | Provider | ✓ |
| 请求数 | 总请求次数 | ✓ |
| 成功数 | 成功请求数 | ✓ |
| 成功率 | 成功数 / 请求数 × 100% | ✓ |
| 总 Token | 输入 + 输出 token | ✓ |
| 缓存命中数 | 使用缓存的请求数 | ✓ |
| 缓存率 | 缓存命中数 / 请求数 × 100% | ✓ |
| 平均响应时间 | 平均请求耗时（ms） | ✓ |
| 总成本 | 内部 + 外部成本 | ✓ |
| 使用用户数 | 使用该模型的用户数 | ✓ |

#### 两个子 Tab

- **按工作量排序**（请求数降序）
- **按时间排序**（最后使用时间降序）

#### 操作

- **点击行**：展开详情，显示使用该模型的用户排行

---

## 3. 数据模型设计

### 3.1 数据来源

**主表**：
- `sessions` - 会话表
- `session_turns` - 会话轮次表
- `request_logs` / `request_logs_with_current_month` - 请求日志
- `users` / `user_profiles` - 用户信息

**需要新增的元数据提取**：
- 会话的项目名称
- 会话的任务描述
- 会话的任务分类

### 3.2 聚合视图设计

#### a. 用户会话聚合视图 `user_session_aggregates`

```sql
CREATE MATERIALIZED VIEW user_session_aggregates AS
SELECT
    owner,
    tenant_id,
    DATE(created_at) as date,
    COUNT(*) as session_count,
    COUNT(DISTINCT project) as project_count,
    COUNT(DISTINCT task) as task_count,
    SUM(total_tokens) as total_tokens,
    SUM(input_tokens) as input_tokens,
    SUM(output_tokens) as output_tokens,
    SUM(internal_cost) as internal_cost,
    SUM(external_cost) as external_cost,
    AVG(duration_seconds) as avg_duration,
    AVG(turn_count) as avg_turns,
    MAX(created_at) as last_active_at
FROM sessions
WHERE is_probe = false
GROUP BY owner, tenant_id, DATE(created_at);

CREATE INDEX idx_user_session_agg_owner_date ON user_session_aggregates(owner, date DESC);
CREATE INDEX idx_user_session_agg_tenant_date ON user_session_aggregates(tenant_id, date DESC);
```

#### b. 项目任务聚合视图 `project_task_aggregates`

```sql
CREATE MATERIALIZED VIEW project_task_aggregates AS
SELECT
    owner,
    tenant_id,
    project,
    task,
    DATE(created_at) as date,
    COUNT(*) as session_count,
    SUM(turn_count) as turn_count,
    SUM(total_tokens) as total_tokens,
    SUM(internal_cost) as internal_cost,
    SUM(external_cost) as external_cost,
    MAX(created_at) as last_active_at
FROM sessions
WHERE is_probe = false AND project IS NOT NULL
GROUP BY owner, tenant_id, project, task, DATE(created_at);

CREATE INDEX idx_project_task_agg_owner ON project_task_aggregates(owner, date DESC);
CREATE INDEX idx_project_task_agg_project ON project_task_aggregates(project, date DESC);
```

#### c. 智能体使用聚合视图 `agent_usage_aggregates`

```sql
CREATE MATERIALIZED VIEW agent_usage_aggregates AS
SELECT
    agent_name,
    tenant_id,
    DATE(created_at) as date,
    COUNT(DISTINCT owner) as user_count,
    COUNT(*) as session_count,
    COUNT(DISTINCT task) as task_count,
    SUM(total_tokens) as total_tokens,
    SUM(internal_cost) as internal_cost,
    SUM(external_cost) as external_cost,
    AVG(duration_seconds) as avg_duration,
    MAX(created_at) as last_used_at
FROM sessions
WHERE is_probe = false AND agent_name IS NOT NULL
GROUP BY agent_name, tenant_id, DATE(created_at);

CREATE INDEX idx_agent_usage_agg_agent ON agent_usage_aggregates(agent_name, date DESC);
```

#### d. 模型使用聚合视图 `model_usage_aggregates`

```sql
CREATE MATERIALIZED VIEW model_usage_aggregates AS
SELECT
    model,
    provider,
    tenant_id,
    DATE(created_at) as date,
    COUNT(*) as request_count,
    COUNT(*) FILTER (WHERE status = 'success') as success_count,
    COUNT(*) FILTER (WHERE cache_hit = true) as cache_hit_count,
    COUNT(DISTINCT owner) as user_count,
    SUM(prompt_tokens) as input_tokens,
    SUM(completion_tokens) as output_tokens,
    SUM(total_tokens) as total_tokens,
    SUM(internal_cost) as internal_cost,
    SUM(external_cost) as external_cost,
    AVG(duration_ms) as avg_duration_ms,
    MAX(created_at) as last_used_at
FROM request_logs_with_current_month
GROUP BY model, provider, tenant_id, DATE(created_at);

CREATE INDEX idx_model_usage_agg_model ON model_usage_aggregates(model, date DESC);
CREATE INDEX idx_model_usage_agg_tenant ON model_usage_aggregates(tenant_id, date DESC);
```

### 3.3 刷新策略

物化视图刷新：
- **增量刷新**：每小时刷新当天数据
- **全量刷新**：每天凌晨刷新历史数据
- **手动刷新**：管理员可触发立即刷新

```sql
-- 增量刷新（仅当天）
REFRESH MATERIALIZED VIEW CONCURRENTLY user_session_aggregates
WHERE date = CURRENT_DATE;

-- 全量刷新
REFRESH MATERIALIZED VIEW CONCURRENTLY user_session_aggregates;
```

---

## 4. API 接口设计

> ### ⚠ 4.0 命名空间约定（2026-11-20 修订，**实现前必读**）
>
> 本系统的端点**不能**直接用 `users` / `agents` / `models` 这类裸复数名——
> **仓内早就有同名路径，且前端在用**：
>
> | 既有路径（2026-07-07 旧族，勿动） | 归属 | 前端消费者 |
> |---|---|---|
> | `GET /api/admin/session-analytics/clients` | `admin/handler.go:1328` | 客户端维度分析 |
> | `GET /api/admin/session-analytics/tasks` | `admin/handler.go:1330` | 任务维度分析 |
> | `GET /api/admin/session-analytics/users` | `admin/handler.go:1334` | **用户画像**（`web/src/api/admin.ts:239` `fetchUserProfiles()`、`UserProfileListView.vue`、菜单 `appNav.ts:237`） |
> | `GET /api/admin/session-analytics/users/{owner}` | `admin/handler.go:1335` | 用户画像详情 |
>
> 前端路由同理：`/admin/session-analytics/users` 已是旧族「用户画像」的列表页
> （`web/src/router.ts:327-328`）。
>
> ### ★ 撞车的后果不是 404，是**进程起不来**
>
> Go 的 `net/http.ServeMux` 对**同一路径重复注册**直接 panic
> （`pattern "..." conflicts with pattern "..."`），
> 而 `cmd/gateway/main.go` 的 panic 兜底会把启动失败变成**进程退出(1)** ⇒
> 蓝绿部署的候选槽起不来 ⇒ 部署回退 ⇒ **新端点永远上不了线**。
>
> 实测（2026-11-20，211 部署）：首版按本文档原始路径实现 `/users`，
> 候选槽启动即 panic，部署 120s 就绪探测失败后回退，
> 容器日志留下 `main: panic during startup`。
>
> ### ⇒ 本系统的路径约定
>
> | 本系统 | 路径 |
> |---|---|
> | 总体统计仪表板 | `GET /api/admin/session-analytics/overview` |
> | 人员排行 | `GET /api/admin/session-analytics/user-ranking` |
> | 人员详情 | `GET /api/admin/session-analytics/user-ranking/{owner}` |
> | 智能体排行 | `GET /api/admin/session-analytics/agent-ranking` |
> | 模型排行 | `GET /api/admin/session-analytics/model-ranking` |
>
> 前端路由同构：`/admin/session-analytics/{overview,user-ranking,user-ranking/:owner,agent-ranking,model-ranking}`。
>
> ### ⇒ 守卫（别指望人肉记住）
>
> `cmd/gateway/session_analytics_routes_guard_test.go` 会
> ① 用 `go/ast` 扫出**全仓**已注册的路由字面量，与本系统的路径清单比对；
> ② 把扫出来的既有路径**真的注册**进同一个 mux（生产同形），
> 再注册本系统路由 ⇒ **必须不 panic**。
>
> ⇒ **新增/改名任何端点，先跑这条测试。**

### 4.1 总体统计 API

#### `GET /api/admin/session-analytics/overview`

**查询参数**：
- `tenant_id` (可选，超级管理员用)
- `start_date` (必需)
- `end_date` (必需)

**响应**：
```json
{
  "summary": {
    "total_sessions": 12345,
    "total_sessions_growth": 15.5,
    "avg_tokens": 2567,
    "avg_context_size": 8192,
    "avg_duration_seconds": 45.2,
    "avg_turns": 3.8,
    "total_cost": 1234.56,
    "total_cost_growth": -5.2,
    "active_users": 156,
    "active_users_growth": 8.3,
    "active_projects": 42,
    "active_tasks": 89,
    "top_models": [
      {"model": "gpt-4", "count": 5678},
      {"model": "claude-3", "count": 3456},
      {"model": "gemini-pro", "count": 2345}
    ],
    "overall_success_rate": 98.5,
    "overall_cache_rate": 45.2
  },
  "trends": {
    "sessions": [
      {"date": "2024-01-01", "count": 123},
      {"date": "2024-01-02", "count": 145}
    ],
    "cost": [
      {"date": "2024-01-01", "internal": 12.34, "external": 23.45},
      {"date": "2024-01-02", "internal": 15.67, "external": 28.90}
    ],
    "tokens": [
      {"date": "2024-01-01", "total": 345678},
      {"date": "2024-01-02", "total": 412345}
    ]
  },
  "top_users": [
    {
      "owner": "user123",
      "display_name": "张三",
      "session_count": 234,
      "total_tokens": 567890,
      "total_cost": 123.45
    }
  ],
  "top_projects": [
    {
      "project": "llm-gateway-enhancement",
      "session_count": 89,
      "user_count": 12,
      "total_cost": 234.56
    }
  ]
}
```

---

### 4.2 人员排行 API

#### `GET /api/admin/session-analytics/user-ranking`

**查询参数**：
- `tenant_id` (可选)
- `start_date` (必需)
- `end_date` (必需)
- `project` (可选，筛选特定项目)
- `sort_by` (可选：`sessions` | `tokens` | `cost` | `last_active`，默认 `tokens`)
- `order` (可选：`asc` | `desc`，默认 `desc`)
- `limit` (可选，默认 50)
- `offset` (可选，默认 0)

**响应**：
```json
{
  "total": 156,
  "users": [
    {
      "rank": 1,
      "owner": "user123",
      "display_name": "张三",
      "email": "zhang@example.com",
      "session_count": 234,
      "project_count": 12,
      "task_count": 45,
      "total_tokens": 567890,
      "total_cost": 123.45,
      "last_active_at": "2024-01-15T10:30:00Z",
      "latest_task": "用户分析系统设计"
    }
  ]
}
```

---

### 4.3 人员详情 API

#### `GET /api/admin/session-analytics/user-ranking/:owner`

**查询参数**：
- `start_date` (必需)
- `end_date` (必需)

**响应**：
```json
{
  "user": {
    "owner": "user123",
    "display_name": "张三",
    "email": "zhang@example.com",
    "tenant_id": "tenant1"
  },
  "summary": {
    "session_count": 234,
    "task_count": 45,
    "total_cost": 123.45,
    "rank": 3,
    "rank_change": -1
  },
  "trends": {
    "sessions": [
      {"date": "2024-01-01", "count": 12},
      {"date": "2024-01-02", "count": 15}
    ],
    "tasks": [
      {"date": "2024-01-01", "count": 3},
      {"date": "2024-01-02", "count": 5}
    ],
    "cost": [
      {"date": "2024-01-01", "internal": 5.67, "external": 12.34},
      {"date": "2024-01-02", "internal": 6.78, "external": 14.56}
    ],
    "rank": [
      {"date": "2024-01-01", "rank": 5},
      {"date": "2024-01-02", "rank": 3}
    ]
  }
}
```

#### `GET /api/admin/session-analytics/user-ranking/:owner/projects`

**查询参数**：
- `start_date` (必需)
- `end_date` (必需)
- `sort_by` (可选：`sessions` | `cost` | `last_active`，默认 `sessions`)
- `order` (可选，默认 `desc`)

**响应**：
```json
{
  "projects": [
    {
      "project": "llm-gateway-enhancement",
      "session_count": 89,
      "task_count": 23,
      "total_tokens": 234567,
      "total_cost": 56.78,
      "last_active_at": "2024-01-15T10:30:00Z"
    }
  ]
}
```

#### `GET /api/admin/session-analytics/user-ranking/:owner/projects/:project/tasks`

**查询参数**：
- `start_date` (必需)
- `end_date` (必需)
- `sort_by` (可选：`sessions` | `cost` | `last_active`，默认 `sessions`)

**响应**：
```json
{
  "tasks": [
    {
      "task": "用户分析系统设计",
      "session_count": 12,
      "request_count": 45,
      "total_tokens": 123456,
      "total_cost": 23.45,
      "last_active_at": "2024-01-15T10:30:00Z"
    }
  ]
}
```

#### `GET /api/admin/session-analytics/user-ranking/:owner/models`

（已实现，见当前代码）

---

### 4.4 终端排行 API

#### `GET /api/admin/session-analytics/agent-ranking`

**查询参数**：
- `tenant_id` (可选)
- `start_date` (必需)
- `end_date` (必需)
- `sort_by` (可选：`sessions` | `users` | `cost` | `last_used`，默认 `sessions`)
- `order` (可选，默认 `desc`)

**响应**：
```json
{
  "agents": [
    {
      "rank": 1,
      "agent_name": "coding-assistant",
      "user_count": 56,
      "session_count": 1234,
      "task_count": 345,
      "total_tokens": 2345678,
      "total_cost": 456.78,
      "avg_duration_seconds": 45.2,
      "last_used_at": "2024-01-15T10:30:00Z"
    }
  ]
}
```

---

### 4.5 模型排行 API

#### `GET /api/admin/session-analytics/model-ranking`

**查询参数**：
- `tenant_id` (可选)
- `start_date` (必需)
- `end_date` (必需)
- `sort_by` (可选：`requests` | `tokens` | `cost` | `success_rate` | `cache_rate` | `last_used`，默认 `requests`)
- `order` (可选，默认 `desc`)

**响应**：
```json
{
  "models": [
    {
      "rank": 1,
      "model": "gpt-4",
      "provider": "openai",
      "request_count": 12345,
      "success_count": 12156,
      "success_rate": 98.5,
      "total_tokens": 23456789,
      "cache_hit_count": 5678,
      "cache_rate": 46.0,
      "avg_duration_ms": 1234.5,
      "total_cost": 2345.67,
      "user_count": 89,
      "last_used_at": "2024-01-15T10:30:00Z"
    }
  ]
}
```

---

## 5. 前端实现

### 5.1 组件结构

```
web/src/views/admin/session-analytics/
├── OverviewView.vue              # 总体统计仪表板
├── UserRankingView.vue           # 人员排行
├── UserDetailView.vue            # 人员详情
│   ├── UserSummaryCard.vue       # 用户信息卡片
│   ├── UserTrendsChart.vue       # 趋势图表
│   ├── ProjectTaskTab.vue        # 项目与任务 Tab
│   ├── ModelUsageTab.vue         # 模型使用 Tab（已实现）
│   └── SessionListTab.vue        # 会话列表 Tab
├── AgentRankingView.vue          # 终端排行
├── ModelRankingView.vue          # 模型排行
└── components/
    ├── KxDateRangePicker.vue     # 统一时间范围选择器（已实现）
    ├── MetricCard.vue            # 指标卡片
    ├── TrendChart.vue            # 趋势图表组件
    ├── RankingTable.vue          # 排行榜表格
    └── SessionStatsSummary.vue   # 会话统计摘要
```

### 5.2 路由配置

```javascript
{
  path: '/admin/session-analytics',
  component: SessionAnalyticsLayout,
  children: [
    {
      path: 'overview',
      name: 'SessionAnalyticsOverview',
      component: () => import('@/views/admin/session-analytics/OverviewView.vue'),
      meta: { title: '总体统计', requiresAuth: true, requiredRole: 'admin' }
    },
    {
      path: 'user-ranking',
      name: 'UserRanking',
      component: () => import('@/views/admin/session-analytics/UserRankingView.vue'),
      meta: { title: '人员排行', requiresAuth: true, requiredRole: 'admin' }
    },
    {
      path: 'user-ranking/:owner',
      name: 'UserDetail',
      component: () => import('@/views/admin/session-analytics/UserDetailView.vue'),
      meta: { title: '人员详情', requiresAuth: true, requiredRole: 'admin' }
    },
    {
      path: 'agent-ranking',
      name: 'AgentRanking',
      component: () => import('@/views/admin/session-analytics/AgentRankingView.vue'),
      meta: { title: '终端排行', requiresAuth: true, requiredRole: 'admin' }
    },
    {
      path: 'model-ranking',
      name: 'ModelRanking',
      component: () => import('@/views/admin/session-analytics/ModelRankingView.vue'),
      meta: { title: '模型排行', requiresAuth: true, requiredRole: 'admin' }
    }
  ]
}
```

### 5.3 状态管理

```javascript
// stores/sessionAnalytics.ts
import { defineStore } from 'pinia'

export const useSessionAnalyticsStore = defineStore('sessionAnalytics', {
  state: () => ({
    dateRange: {
      start: '',
      end: ''
    },
    tenantId: null as string | null,
    selectedProject: null as string | null
  }),
  actions: {
    setDateRange(start: string, end: string) {
      this.dateRange = { start, end }
    },
    setTenantId(tenantId: string | null) {
      this.tenantId = tenantId
    },
    setSelectedProject(project: string | null) {
      this.selectedProject = project
    }
  }
})
```

### 5.4 API 客户端

```typescript
// api/sessionAnalytics.ts
import { http } from '@/utils/http'

export interface DateRange {
  start_date: string
  end_date: string
}

export interface OverviewSummary {
  total_sessions: number
  total_sessions_growth: number
  avg_tokens: number
  // ... 其他字段
}

export interface UserRanking {
  rank: number
  owner: string
  display_name: string
  session_count: number
  // ... 其他字段
}

export const sessionAnalyticsApi = {
  // 总体统计
  getOverview(params: DateRange & { tenant_id?: string }) {
    return http.get<OverviewResponse>('/api/admin/session-analytics/overview', { params })
  },
  
  // 人员排行
  getUserRanking(params: DateRange & { 
    tenant_id?: string
    project?: string
    sort_by?: string
    order?: 'asc' | 'desc'
    limit?: number
    offset?: number
  }) {
    return http.get<UserRankingResponse>('/api/admin/session-analytics/user-ranking', { params })
  },
  
  // 人员详情
  getUserDetail(owner: string, params: DateRange) {
    return http.get<UserDetailResponse>(`/api/admin/session-analytics/user-ranking/${owner}`, { params })
  },
  
  // 项目列表
  getUserProjects(owner: string, params: DateRange & {
    sort_by?: string
    order?: 'asc' | 'desc'
  }) {
    return http.get<UserProjectsResponse>(`/api/admin/session-analytics/user-ranking/${owner}/projects`, { params })
  },
  
  // 任务列表
  getProjectTasks(owner: string, project: string, params: DateRange) {
    return http.get<ProjectTasksResponse>(
      `/api/admin/session-analytics/user-ranking/${owner}/projects/${encodeURIComponent(project)}/tasks`,
      { params }
    )
  },
  
  // 终端排行
  getAgentRanking(params: DateRange & {
    tenant_id?: string
    sort_by?: string
    order?: 'asc' | 'desc'
  }) {
    return http.get<AgentRankingResponse>('/api/admin/session-analytics/agent-ranking', { params })
  },
  
  // 模型排行
  getModelRanking(params: DateRange & {
    tenant_id?: string
    sort_by?: string
    order?: 'asc' | 'desc'
  }) {
    return http.get<ModelRankingResponse>('/api/admin/session-analytics/model-ranking', { params })
  }
}
```

---

## 6. 国际化

### 6.1 i18n 键结构

```json
{
  "sessionAnalytics": {
    "overview": {
      "title": "会话分析总览",
      "totalSessions": "总会话数",
      "avgTokens": "平均 Token",
      "totalCost": "总成本",
      "activeUsers": "活跃用户"
    },
    "userRanking": {
      "title": "人员排行",
      "rank": "排名",
      "displayName": "姓名",
      "sessionCount": "会话数",
      "projectCount": "项目数"
    },
    "userDetail": {
      "title": "人员详情",
      "summary": "统计摘要",
      "trends": "趋势分析",
      "projects": "项目与任务",
      "models": "模型使用",
      "sessions": "会话列表"
    },
    "agentRanking": {
      "title": "终端排行",
      "agentName": "智能体名称",
      "userCount": "使用用户数"
    },
    "modelRanking": {
      "title": "模型排行",
      "modelName": "模型名称",
      "requestCount": "请求数",
      "successRate": "成功率",
      "cacheRate": "缓存率"
    },
    "common": {
      "sortByWorkload": "按工作量排序",
      "sortByTime": "按时间排序",
      "noData": "暂无数据",
      "loading": "加载中..."
    }
  }
}
```

---

## 7. 元数据提取设计

### 7.1 项目与任务识别

**策略 1：基于会话标题**

从会话标题中提取项目和任务信息：
- 格式：`[项目名] 任务描述`
- 示例：`[llm-gateway] 实现会话分析系统`

**策略 2：基于首轮请求内容**

使用 LLM 从首轮用户消息中提取：
```json
{
  "project": "llm-gateway-enhancement",
  "task": "设计并实现会话分析统计系统",
  "category": "feature_development"
}
```

**策略 3：用户显式标注**

在会话创建时允许用户选择或输入项目和任务：
- 前端提供项目下拉选择
- 任务描述输入框

### 7.2 数据库 Schema 调整

```sql
-- 为 sessions 表添加元数据列
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS project VARCHAR(255);
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS task TEXT;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS task_category VARCHAR(100);

-- 创建索引
CREATE INDEX IF NOT EXISTS idx_sessions_project ON sessions(project) WHERE project IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_sessions_task_category ON sessions(task_category) WHERE task_category IS NOT NULL;
```

### 7.3 后台提取任务

```go
// 后台任务：提取会话元数据
func ExtractSessionMetadata(sessionID string) error {
    session, err := GetSession(sessionID)
    if err != nil {
        return err
    }
    
    // 从标题提取
    project, task := extractFromTitle(session.Title)
    
    // 如果标题没有，从首轮请求提取
    if project == "" {
        firstTurn, err := GetFirstTurn(sessionID)
        if err == nil {
            metadata, err := extractFromContent(firstTurn.UserMessage)
            if err == nil {
                project = metadata.Project
                task = metadata.Task
            }
        }
    }
    
    // 更新数据库
    return UpdateSessionMetadata(sessionID, project, task)
}
```

---

## 8. 性能优化

### 8.1 查询优化

- **物化视图**：预聚合数据，减少实时计算
- **索引优化**：关键字段建立复合索引
- **分页查询**：限制单次返回数据量
- **缓存策略**：Redis 缓存热点数据（1小时 TTL）

### 8.2 前端优化

- **虚拟滚动**：大列表使用 `vue-virtual-scroller`
- **懒加载**：图表按需加载
- **数据分页**：避免一次加载全部数据
- **请求去重**：相同参数的请求去重
- **骨架屏**：提升加载体验

---

## 9. 实施计划

### Phase 1: 基础设施（2 周）

**Week 1-2**:
- [ ] 数据库 Schema 调整（添加 project, task 列）
- [ ] 创建物化视图（4 个聚合视图）
- [ ] 元数据提取逻辑实现
- [ ] 后台刷新任务

**交付物**:
- 迁移脚本
- 物化视图 SQL
- 元数据提取服务

### Phase 2: 总体统计与人员排行（2 周）

**Week 3**:
- [ ] 总体统计仪表板 API
- [ ] 人员排行 API
- [ ] 前端总体统计页面

**Week 4**:
- [ ] 前端人员排行页面
- [ ] 时间范围选择器集成
- [ ] 筛选与排序功能

**交付物**:
- 总体统计页面
- 人员排行页面
- API 文档

### Phase 3: 人员详情（2 周）

**Week 5**:
- [ ] 人员详情 API（趋势、项目、任务）
- [ ] 前端人员详情页面框架
- [ ] 趋势图表组件

**Week 6**:
- [ ] 项目与任务 Tab
- [ ] 会话列表 Tab
- [ ] 详情页交互优化

**交付物**:
- 人员详情页面
- 项目任务展示

### Phase 4: 终端与模型排行（1 周）

**Week 7**:
- [ ] 终端排行 API
- [ ] 模型排行 API
- [ ] 前端终端排行页面
- [ ] 前端模型排行页面

**交付物**:
- 终端排行页面
- 模型排行页面

### Phase 5: 测试与优化（1 周）

**Week 8**:
- [ ] 单元测试
- [ ] 集成测试
- [ ] E2E 测试（Maestro）
- [ ] 性能优化
- [ ] 文档完善

**交付物**:
- 测试报告
- 性能优化报告
- 用户手册

---

## 10. 测试策略

### 10.1 单元测试

**后端**:
- API 处理器测试
- 数据聚合逻辑测试
- 元数据提取测试

**前端**:
- 组件单元测试
- API 客户端测试
- 工具函数测试

### 10.2 集成测试

- API 端到端测试
- 物化视图刷新测试
- 数据一致性测试

### 10.3 E2E 测试（Maestro）

```yaml
# maestro/flows/15-session-analytics-overview.yaml
# 验证总体统计页面
- openLink: ${BASE_URL}/admin/session-analytics/overview
- assertVisible: "会话分析总览"
- assertVisible: "总会话数"
- assertVisible: "平均 Token"
# ...
```

---

## 11. 监控与日志

### 11.1 性能监控

- API 响应时间（P50, P95, P99）
- 物化视图刷新时间
- 查询慢日志（> 1s）

### 11.2 业务监控

- 日活跃管理员数
- 各页面访问量
- 数据查询频率

### 11.3 错误监控

- API 错误率
- 物化视图刷新失败
- 元数据提取失败

---

## 12. 安全与权限

### 12.1 权限控制

- **超级管理员**：查看所有租户数据
- **租户管理员**：仅查看本租户数据
- **团队负责人**：仅查看团队成员数据

### 12.2 数据脱敏

- 用户邮箱部分隐藏
- 会话内容不在统计页面直接显示
- 敏感项目名称脱敏选项

---

## 13. 未来扩展

### 13.1 高级分析

- **异常检测**：识别异常高成本会话
- **趋势预测**：预测未来使用量与成本
- **智能推荐**：推荐最优模型组合

### 13.2 导出功能

- 导出 CSV / Excel 报表
- 定时邮件报告
- 自定义报表生成

### 13.3 集成

- 与 Slack / 飞书集成，发送周报
- 与 BI 工具集成（Metabase, Superset）
- Webhook 通知异常情况

---

## 14. 参考资料

- [Mixpanel Analytics](https://mixpanel.com/)
- [Amplitude Product Analytics](https://amplitude.com/)
- [Grafana Dashboards](https://grafana.com/)
- [PostgreSQL Materialized Views](https://www.postgresql.org/docs/current/sql-creatematerializedview.html)

---

## 附录 A: 关键决策记录

### A.1 为什么使用物化视图而不是实时计算？

**原因**:
1. 聚合查询复杂，实时计算性能差
2. 统计数据不要求实时性（延迟 1 小时可接受）
3. 物化视图可以预计算，查询速度快 10-100 倍

**权衡**:
- 优点：查询快，负载低
- 缺点：数据有延迟，需维护刷新任务

### A.2 为什么按日聚合而不是按小时？

**原因**:
1. 业务需求以天为单位（日报、周报）
2. 按日聚合数据量可控
3. 可以在前端按需聚合到周/月

**权衡**:
- 如需小时级分析，可单独创建小时聚合视图

---

## 附录 B: SQL 示例

### B.1 用户排行查询

```sql
SELECT
    owner,
    display_name,
    SUM(session_count) as total_sessions,
    SUM(total_tokens) as total_tokens,
    SUM(internal_cost + external_cost) as total_cost,
    COUNT(DISTINCT project) as project_count,
    MAX(last_active_at) as last_active_at
FROM user_session_aggregates
WHERE date BETWEEN :start_date AND :end_date
    AND tenant_id = :tenant_id
GROUP BY owner, display_name
ORDER BY total_sessions DESC
LIMIT 50;
```

### B.2 趋势查询

```sql
SELECT
    date,
    SUM(session_count) as sessions,
    SUM(total_tokens) as tokens,
    SUM(internal_cost + external_cost) as cost
FROM user_session_aggregates
WHERE owner = :owner
    AND date BETWEEN :start_date AND :end_date
GROUP BY date
ORDER BY date;
```

---

**文档版本**: 1.0  
**最后更新**: 2026-11-19  
**作者**: Kiro AI Assistant  
**状态**: 待审核
