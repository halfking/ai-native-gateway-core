# 首页 Dashboard 会话 API 标准文档

**版本：** v1.0  
**日期：** 2026-07-10  
**状态：** 设计完成，待实施

---

## 一、API 概览

### 1.1 设计原则

1. **RESTful 设计** - 清晰的资源路径和 HTTP 方法
2. **统一响应格式** - success/error/code/message 标准结构
3. **完整参数支持** - 分页、排序、筛选、缓存控制
4. **权限控制** - 三层隔离（super_admin / tenant_admin / user）
5. **缓存友好** - 支持 ETag/Last-Modified/Cache-Control
6. **完整埋点** - 自动记录访问日志到 dashboard_access_events

### 1.2 API 列表

| API | 方法 | 路径 | 说明 |
|-----|------|------|------|
| 会话总览 | GET | `/api/admin/dashboard/session-overview` | 首页主面板数据 |
| 会话趋势 | GET | `/api/admin/dashboard/session-trend` | 时间序列趋势 |
| 健康度分布 | GET | `/api/admin/dashboard/session-health` | 健康度统计 |
| 活跃会话 | GET | `/api/admin/dashboard/session-active` | 当前活跃会话列表 |
| 最近会话 | GET | `/api/admin/dashboard/session-recent` | 最近创建的会话 |
| 异常会话 | GET | `/api/admin/dashboard/session-anomalies` | 异常会话告警 |
| 导出数据 | POST | `/api/admin/dashboard/session-export` | 导出报表 |

---

## 二、统一响应格式

### 2.1 成功响应

```json
{
  "success": true,
  "data": { ... },
  "metadata": {
    "total": 100,
    "page": 1,
    "size": 20,
    "pages": 5,
    "cache_hit": false,
    "generated_at": "2026-07-10T12:00:00Z",
    "took_ms": 45
  },
  "timestamp": "2026-07-10T12:00:00Z"
}
```

### 2.2 错误响应

```json
{
  "success": false,
  "code": "INVALID_PARAM",
  "message": "Invalid parameter: days",
  "error": {
    "code": "INVALID_PARAM",
    "message": "Invalid parameter: days",
    "details": "days must be between 1 and 90"
  },
  "timestamp": "2026-07-10T12:00:00Z"
}
```

### 2.3 标准错误码

| 错误码 | HTTP Status | 说明 |
|--------|-------------|------|
| `INVALID_PARAM` | 400 | 参数无效 |
| `UNAUTHORIZED` | 401 | 未认证 |
| `FORBIDDEN` | 403 | 无权限 |
| `NOT_FOUND` | 404 | 资源不存在 |
| `INTERNAL_ERROR` | 500 | 内部错误 |
| `DATABASE_ERROR` | 500 | 数据库错误 |
| `CACHE_ERROR` | 500 | 缓存错误 |
| `TOO_MANY_REQUESTS` | 429 | 请求过多 |
| `DATA_NOT_READY` | 503 | 数据未就绪 |

---

## 三、通用查询参数

所有 API 都支持以下通用参数：

| 参数 | 类型 | 必填 | 默认值 | 说明 |
|------|------|------|--------|------|
| `tenant_id` | string | 否 | 当前用户租户 | 租户 ID（超级管理员可指定） |
| `days` | int | 否 | 7 | 时间范围（1/7/30/90） |
| `page` | int | 否 | 1 | 页码 |
| `size` | int | 否 | 20 | 每页大小（最大 100） |
| `sort_by` | string | 否 | - | 排序字段 |
| `sort_dir` | string | 否 | desc | 排序方向（asc/desc） |
| `search` | string | 否 | - | 搜索关键词 |
| `refresh` | bool | 否 | false | 是否强制刷新缓存 |

---

## 四、API 详细说明

### 4.1 会话总览

**接口：** `GET /api/admin/dashboard/session-overview`

**用途：** 首页 Dashboard 主面板，一次性获取所有核心指标

**查询参数：**
```
days=7                    # 时间范围
tenant_id=default         # 租户 ID（可选）
refresh=false             # 是否强制刷新
```

**响应数据：**

```typescript
interface SessionOverviewData {
  // 核心指标
  total_sessions: number          // 总会话数
  active_sessions: number         // 活跃会话数（24小时内有请求）
  new_sessions_24h: number        // 24小时内新创建的会话
  closed_sessions_24h: number     // 24小时内关闭的会话

  // 健康度分布
  health_distribution: {
    total: number
    a: number                      // 90-100 分
    b: number                      // 75-89 分
    c: number                      // 60-74 分
    d: number                      // 40-59 分
    f: number                      // 0-39 分
    a_percent: number              // 百分比
    b_percent: number
    c_percent: number
    d_percent: number
    f_percent: number
    avg_score: number              // 平均分
  }

  // 合规状态
  compliance_stats: {
    total: number
    compliant: number              // 合规会话数
    warning: number                // 警告数
    violation: number              // 违规数
    prompt_injection_detected: number
    pii_detected: number
    toxic_output_detected: number
    compliance_rate: number        // 合规率（百分比）
  }

  // 成本统计
  cost_stats: {
    total_cost_usd: number         // 总成本
    avg_cost_per_session: number   // 平均每会话成本
    avg_cost_per_request: number   // 平均每请求成本
    max_cost_session: number       // 单会话最高成本
    input_cost_usd: number         // 输入成本
    output_cost_usd: number        // 输出成本
    cost_growth_pct: number        // 相比上周期增长率
  }

  // 模型使用
  model_usage: Array<{
    model: string
    session_count: number
    request_count: number
    total_cost: number
    avg_latency_ms: number
    success_rate: number
  }>

  // Top 排行
  top_clients: Array<{
    client_id: string
    session_count: number
    total_cost: number
    avg_health?: number
    last_activity: string
  }>

  top_tasks: Array<{
    task_id: string
    session_count: number
    total_cost: number
    avg_health?: number
    last_activity: string
  }>

  // 趋势数据
  cost_trend: Array<{
    date: string                   // YYYY-MM-DD
    cost: number
    sessions: number
    requests: number
  }>

  session_trend: Array<{
    date: string
    new_sessions: number
    active_count: number
    closed_count: number
  }>

  // 时间戳
  generated_at: string
  period_start: string
  period_end: string
}
```

**使用示例：**

```typescript
import { fetchSessionOverview } from '@/api/dashboard'

const { data, metadata } = await fetchSessionOverview({ days: 7 })
console.log('总会话数:', data.total_sessions)
console.log('响应时间:', metadata.took_ms, 'ms')
```

---

### 4.2 会话趋势

**接口：** `GET /api/admin/dashboard/session-trend`

**用途：** 获取时间序列趋势数据，用于绘制图表

**响应数据：**

```typescript
interface SessionTrendData {
  period: {
    start: string
    end: string
    days: number
  }
  trend: Array<{
    date: string                    // YYYY-MM-DD
    new_sessions: number
    active_sessions: number
    closed_sessions: number
    total_cost: number
    total_requests: number
  }>
  summary: {
    total_new: number
    total_active: number
    total_closed: number
    avg_daily_cost: number
    growth_rate: number             // 增长率（百分比）
  }
}
```

---

### 4.3 健康度分布

**接口：** `GET /api/admin/dashboard/session-health`

**用途：** 获取会话健康度分布统计

**响应数据：**

```typescript
interface HealthDistributionData {
  distribution: {
    a: number                       // 90-100 分
    b: number                       // 75-89 分
    c: number                       // 60-74 分
    d: number                       // 40-59 分
    f: number                       // 0-39 分
  }
  percentages: {
    a: number
    b: number
    c: number
    d: number
    f: number
  }
  total: number
  avg_score: number
  by_tenant: Array<{                // 按租户分组（超级管理员可见）
    tenant_id: string
    distribution: { a, b, c, d, f }
    avg_score: number
  }>
}
```

---

### 4.4 活跃会话

**接口：** `GET /api/admin/dashboard/session-active`

**用途：** 获取当前活跃的会话列表

**查询参数：**
```
size=20                  # 返回数量
tenant_id=default        # 租户 ID（可选）
```

**响应数据：**

```typescript
interface ActiveSessionsData {
  sessions: Array<{
    session_id: string
    tenant_id: string
    last_activity: string          // 最后活动时间
    duration_seconds: number       // 会话持续时间
    request_count: number          // 请求数
    total_cost: number
    primary_model: string
    health_grade?: string          // A/B/C/D/F
    health_score?: number
    client_id?: string
    task_id?: string
  }>
  total: number
}
```

---

### 4.5 最近会话

**接口：** `GET /api/admin/dashboard/session-recent`

**用途：** 获取最近创建的会话列表

**响应数据：**

```typescript
interface RecentSessionsData {
  sessions: Array<{
    session_id: string
    tenant_id: string
    first_request_at: string
    last_request_at: string
    request_count: number
    total_cost: number
    models_used: string[]
    health_grade?: string
    title?: string                 // LLM 生成的标题
  }>
  total: number
}
```

---

### 4.6 异常会话

**接口：** `GET /api/admin/dashboard/session-anomalies`

**用途：** 获取异常会话告警

**响应数据：**

```typescript
interface SessionAnomaliesData {
  anomalies: Array<{
    session_id: string
    tenant_id: string
    anomaly_type: string           // high_cost / high_latency / low_health / compliance_violation
    severity: 'low' | 'medium' | 'high' | 'critical'
    description: string
    detected_at: string
    metrics: Record<string, any>   // 相关指标
  }>
  total: number
}
```

---

### 4.7 导出数据

**接口：** `POST /api/admin/dashboard/session-export`

**用途：** 导出数据为 CSV/JSON/Excel

**请求体：**

```typescript
interface ExportRequest {
  export_type: 'overview' | 'trend' | 'health' | 'sessions'
  format: 'csv' | 'json' | 'excel'
  days: number
  tenant_id?: string
  filters?: Record<string, any>
}
```

**响应数据：**

```typescript
interface ExportResponse {
  export_id: string                // 导出任务 ID
  status: 'pending' | 'processing' | 'completed' | 'failed'
  download_url?: string            // 下载链接（完成后提供）
  expires_at?: string              // 链接过期时间
}
```

---

## 五、前端使用示例

### 5.1 使用 Composable

```typescript
import { useDashboard } from '@/composables/useDashboard'

export default {
  setup() {
    const {
      loading,
      error,
      overview,
      trend,
      activeSessions,
      totalStats,
      loadAll,
      refresh,
      changeDays,
    } = useDashboard({
      autoRefresh: true,
      refreshInterval: 5 * 60 * 1000,  // 5 分钟
      defaultDays: 7,
    })

    return {
      loading,
      error,
      overview,
      trend,
      activeSessions,
      totalStats,
      refresh,
      changeDays,
    }
  }
}
```

### 5.2 模板中使用

```vue
<template>
  <div class="dashboard">
    <!-- 加载状态 -->
    <div v-if="loading" class="loading">
      <el-skeleton :rows="5" animated />
    </div>

    <!-- 错误状态 -->
    <div v-else-if="error" class="error">
      <el-alert :title="error" type="error" :closable="false" />
      <el-button @click="refresh">重试</el-button>
    </div>

    <!-- 数据展示 -->
    <div v-else-if="overview">
      <!-- 总会话数 -->
      <el-statistic
        :value="totalStats.sessions"
        title="总会话数"
      />

      <!-- 活跃会话数 -->
      <el-statistic
        :value="totalStats.active"
        title="活跃会话"
      />

      <!-- 总成本 -->
      <el-statistic
        :value="totalStats.cost"
        title="总成本 (USD)"
        :precision="2"
      />

      <!-- 合规率 -->
      <el-statistic
        :value="totalStats.complianceRate"
        title="合规率"
        :precision="1"
        suffix="%"
      />

      <!-- 趋势图 -->
      <cost-trend-chart :data="trend" />

      <!-- 活跃会话列表 -->
      <active-sessions-table :sessions="activeSessions" />
    </div>

    <!-- 时间范围切换 -->
    <el-radio-group v-model="days" @change="changeDays">
      <el-radio-button :label="1">今日</el-radio-button>
      <el-radio-button :label="7">近 7 天</el-radio-button>
      <el-radio-button :label="30">近 30 天</el-radio-button>
      <el-radio-button :label="90">近 90 天</el-radio-button>
    </el-radio-group>

    <!-- 刷新按钮 -->
    <el-button @click="refresh" :loading="loading">
      🔄 刷新
    </el-button>
  </div>
</template>
```

---

## 六、数据埋点

### 6.1 自动埋点

所有 API 访问都会自动记录到 `dashboard_access_events` 表：

**记录的字段：**
- `event_id` - 事件唯一 ID
- `event_type` - api_access / query / export / error
- `tenant_id, user_id, user_role` - 用户信息
- `api_path, api_method` - API 信息
- `query_params` - 请求参数（脱敏）
- `status_code, response_time_ms` - 响应信息
- `cache_hit` - 是否命中缓存
- `db_query_time_ms, cache_query_time_ms` - 性能分解
- `client_ip, user_agent` - 客户端信息

### 6.2 埋点 SQL 视图

```sql
-- API 访问统计
SELECT * FROM v_dashboard_access_stats;

-- 慢查询监控
SELECT * FROM v_dashboard_slow_queries;

-- 错误监控
SELECT * FROM v_dashboard_errors;

-- 用户活跃度
SELECT * FROM v_dashboard_user_activity;
```

### 6.3 编程埋点

```go
// Go 后端
recorder.RecordAccess(
    tenantID,
    userID,
    userRole,
    sessionID,
    "/api/admin/dashboard/session-overview",
    "GET",
    200,
    responseTime,
    cacheHit,
)
```

---

## 七、权限控制

### 7.1 三层隔离

| 角色 | 可访问数据 |
|------|-----------|
| `super_admin` | 所有租户的全部数据，可通过 `tenant_id` 参数指定 |
| `tenant_admin` | 本租户的全部数据 |
| `user` | 本租户 + 仅自己 owner 的会话 |

### 7.2 权限验证逻辑

```go
// 后端示例
func (h *Handler) checkPermission(r *http.Request, tenantID string) error {
    ctx := GetAuthContext(r)
    
    if IsSuperAdmin(r) {
        // 超级管理员可访问所有
        return nil
    }
    
    if IsTenantAdmin(r) {
        // 租户管理员只能访问本租户
        if tenantID != ctx.TenantID {
            return ErrForbidden
        }
        return nil
    }
    
    // 普通用户只能访问自己 owner 的数据
    // 需要在查询时添加 owner_user 过滤条件
    return nil
}
```

---

## 八、性能优化

### 8.1 缓存策略

| 层级 | TTL | 说明 |
|------|-----|------|
| HTTP Cache-Control | 60 秒 | 客户端缓存 |
| Redis | 模块配置 TTL | 跨实例共享 |
| 模块执行记录 | 模块配置 TTL | 避免重复计算 |
| PostgreSQL | 7 天 | 持久化存储 |

### 8.2 慢查询优化

**索引：**
```sql
-- session_summaries 常用索引
CREATE INDEX idx_session_summaries_tenant_time 
    ON session_summaries(tenant_id, last_request_at DESC);

CREATE INDEX idx_session_summaries_health 
    ON session_summaries(tenant_id, health_grade) 
    WHERE health_score IS NOT NULL;

CREATE INDEX idx_session_summaries_cost 
    ON session_summaries(tenant_id, total_cost_usd DESC);

CREATE INDEX idx_session_summaries_first_request 
    ON session_summaries(tenant_id, first_request_at DESC);
```

### 8.3 批量加载

前端使用 `Promise.allSettled` 并行加载所有数据：

```typescript
const [overview, trend, active, health] = await Promise.allSettled([
  fetchSessionOverview(params),
  fetchSessionTrend(params),
  fetchActiveSessions(params),
  getHealthDistribution(params),
])
```

---

## 九、监控与告警

### 9.1 关键指标

- **API 可用性：** 成功率 > 99.9%
- **响应时间：** P95 < 500ms
- **缓存命中率：** > 50%
- **错误率：** < 1%

### 9.2 告警规则

```yaml
# Prometheus 告警规则
groups:
  - name: dashboard_api
    rules:
      - alert: DashboardAPISlow
        expr: histogram_quantile(0.95, dashboard_api_response_time) > 1000
        for: 5m
        annotations:
          summary: "Dashboard API 响应时间过长"

      - alert: DashboardAPIErrorRate
        expr: rate(dashboard_api_errors_total[5m]) > 0.01
        for: 5m
        annotations:
          summary: "Dashboard API 错误率过高"
```

---

## 十、迁移计划

### Phase 1: 基础设施 ✅
- [x] 数据库表结构（361 迁移）
- [x] 后端 Handler 框架
- [x] 前端 API 封装
- [x] 埋点系统

### Phase 2: API 实现（下一步）
- [ ] 完善 session-overview 接口
- [ ] 实现 session-trend 接口
- [ ] 实现 session-health 接口
- [ ] 实现其他接口

### Phase 3: 前端集成
- [ ] 改造 SessionStatsPanel.vue
- [ ] 改造 DashboardView.vue
- [ ] 实现 useDashboard composable
- [ ] 添加图表组件

### Phase 4: 监控与优化
- [ ] 部署 Prometheus 监控
- [ ] 配置告警规则
- [ ] 性能测试与调优

---

## 十一、相关文件

### 后端
- `sql/migrations/startup/361_dashboard_access_events.sql` - 埋点表
- `admin/dashboardapi/types.go` - 统一响应格式
- `admin/dashboardapi/session_overview.go` - 会话总览 Handler
- `telemetry/dashboard_events.go` - 埋点记录器

### 前端
- `web/src/api/dashboard.ts` - API 封装
- `web/src/composables/useDashboard.ts` - 数据管理 Composable

---

**文档维护者：** Backend & Frontend Teams  
**最后更新：** 2026-07-10