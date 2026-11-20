# 运营看板详情分析系统设计方案

## 一、需求背景

对于 https://llm.itestu.cn/admin/board-operational 中的 top5 模型、供应商、客户端、租户块区:
1. 需要增加"更多"按钮,展示完整列表
2. 列表中每行数据可点击,查看该对象一段时间内的完整统计数据
3. 详情页支持多维交叉分析
4. 不同入口的相同分析需要复用,避免重复开发

## 二、架构设计

### 2.1 路由结构

```
运营看板总览: /admin/board-operational

完整列表页(4个):
- /admin/board-operational/models
- /admin/board-operational/providers  
- /admin/board-operational/clients
- /admin/board-operational/tenants

详情分析页(4个,参数化复用同一组件):
- /admin/analytics/model/:id
- /admin/analytics/provider/:id
- /admin/analytics/client/:id
- /admin/analytics/tenant/:id
```

### 2.2 分析入口注册表

`web/src/config/analyticsRegistry.ts` - 中央化管理所有分析入口

```typescript
export type AnalyticsDimension = 'model' | 'provider' | 'client' | 'tenant'

export interface AnalyticsRoute {
  dimension: AnalyticsDimension
  basePath: string
  listPath: string
  detailPath: (id: string) => string
  apiEndpoint: (id: string, params: any) => string
  titleKey: string
  listTitleKey: string
  entryPoints: string[] // 所有可进入此分析的页面
}

export const ANALYTICS_REGISTRY: Record<AnalyticsDimension, AnalyticsRoute>
```

**设计原则**: 
- 单一真源: 所有路由、API端点、标题都从此注册表生成
- 可追溯性: 记录每个分析的所有入口点
- 类型安全: TypeScript严格类型约束

### 2.3 后端API端点

#### 现有端点(复用)
```
GET /api/admin/dashboard/board?days=7
- 已包含 pies.models/providers/clients/tenants 完整数据
- 无需新增列表端点
```

#### 新增端点
```
GET /api/admin/analytics/:dimension/:identifier
参数:
  - dimension: model/provider/client/tenant
  - identifier: 对象ID (URL编码)
  - days/start/end: 时间范围
  - tenant_id: 可选租户过滤

返回: AnalyticsDetailPayload (见2.4)
```

### 2.4 数据结构

```typescript
// 详情分析完整数据
export interface AnalyticsDetailPayload {
  dimension: AnalyticsDimension
  identifier: string
  display_name?: string
  time_range: {
    start: string
    end: string  
    days: number
  }
  
  // 核心汇总指标
  summary: {
    total_requests: number
    success_count: number
    success_rate: number
    error_count: number
    total_tokens: number
    total_prompt_tokens: number
    total_completion_tokens: number
    total_cost_usd: number
    total_credits: number
    avg_latency_ms: number
    p95_latency_ms?: number
    p99_latency_ms?: number
  }
  
  // 时间序列趋势
  trends: AnalyticsTrendPoint[]
  
  // 交叉分析维度
  cross_analysis: {
    by_model?: BoardPieItem[]
    by_provider?: BoardPieItem[]
    by_client?: BoardPieItem[]
    by_tenant?: BoardPieItem[]
    by_error?: BoardPieItem[]
    by_status?: BoardPieItem[]
  }
  
  // 降级标记
  degraded?: boolean
  degraded_reason?: string
}

export interface AnalyticsTrendPoint {
  bucket: string              // ISO 8601 timestamp
  requests: number
  success: number
  errors: number
  tokens: number
  cost_usd: number
  credits: number
  avg_latency_ms: number
}
```

### 2.5 前端组件层次

```
views/
  analytics/
    AnalyticsDetailView.vue          主容器,路由级组件
  
  board/
    BoardOperationalView.vue         现有看板(需改造)
    BoardOperationalListView.vue     完整列表页(复用同一组件)

components/
  analytics/
    AnalyticsSummaryCards.vue        核心指标卡片网格
    AnalyticsTrendChart.vue          ECharts趋势图
    AnalyticsCrossTable.vue          交叉分析表格
    AnalyticsEmptyState.vue          空态/降级态
    
  board/
    BoardPieCard.vue                 现有饼图卡片(需改造)
      - 增加 showMore 属性
      - 增加 @more 事件
      - 增加 @item-click 事件
```

### 2.6 状态管理

不引入Vuex/Pinia,使用组合式API + URL状态:
- 时间范围: URL query参数 (days/start/end)
- 租户过滤: URL query参数 (tenant_id)
- 列表排序: 本地组件状态
- 图表配置: 本地组件状态

### 2.7 导航流程

```
运营看板 BoardOperationalView
  ↓ 点击 "更多" 按钮
完整列表 BoardOperationalListView?dimension=models&days=7
  ↓ 点击某一行 (如 "gpt-4")
详情分析 AnalyticsDetailView/model/gpt-4?days=7
  ↓ 可切换时间范围,查看交叉分析
  ↓ 点击交叉分析中的其他维度
详情分析 AnalyticsDetailView/provider/openai?days=7
```

### 2.8 复用策略

#### 组件复用
- `BoardOperationalListView`: 通过 route.params.dimension 区分维度
- `AnalyticsDetailView`: 通过 route.params.dimension + route.params.id 加载数据
- `BoardPieCard`: 通过 props 控制是否显示"更多"和点击行为

#### API复用  
- 列表数据: 复用现有 `/api/admin/dashboard/board`
- 详情数据: 新端点 `/api/admin/analytics/:dimension/:identifier`

#### 样式复用
- 继承现有 BoardOperationalView 的设计语言
- 复用现有的 CSS 变量和卡片样式
- 图表使用相同的 ECharts 主题配置

## 三、实施步骤

### Phase 1: 基础设施 (核心)
1. 创建 `analyticsRegistry.ts` 注册表
2. 新增后端 `/api/admin/analytics/:dimension/:identifier` 端点
3. 定义 TypeScript 类型定义

### Phase 2: 列表页 (完整列表)
1. 创建 `BoardOperationalListView.vue`
2. 改造 `BoardPieCard.vue` 增加"更多"按钮
3. 添加路由配置

### Phase 3: 详情页 (核心分析)
1. 创建 `AnalyticsDetailView.vue` 主容器
2. 创建 `AnalyticsSummaryCards.vue` 指标卡片
3. 创建 `AnalyticsTrendChart.vue` 趋势图
4. 创建 `AnalyticsCrossTable.vue` 交叉分析表

### Phase 4: 集成与测试
1. 在 BoardOperationalView 中集成"更多"按钮
2. 国际化文案
3. 单元测试
4. E2E 测试

### Phase 5: 优化
1. 性能优化 (虚拟滚动、图表防抖)
2. 缓存策略
3. 错误处理与降级
4. 响应式适配

## 四、技术要点

### 4.1 URL 状态管理
使用 URL query 作为单一真源:
```typescript
// 从 URL 读取
const timeRange = computed(() => ({
  days: Number(route.query.days) || 7,
  start: route.query.start as string,
  end: route.query.end as string
}))

// 更新 URL
router.push({
  query: { ...route.query, days: 30 }
})
```

### 4.2 数据降级处理
遵循现有的降级模式:
```typescript
// 检查降级态
if (data.degraded) {
  showWarning(data.degraded_reason)
}

// 部分维度降级
if (data.cross_analysis.by_model === undefined) {
  // 显示该维度不可用
}
```

### 4.3 跨维度导航
点击交叉分析表中的项目,自动导航到对应详情:
```typescript
function onCrossItemClick(dimension: string, id: string) {
  const route = ANALYTICS_REGISTRY[dimension].detailPath(id)
  router.push({ 
    path: route, 
    query: { days: currentDays.value } 
  })
}
```

### 4.4 国际化
新增文案键:
```
analytics.detail.title
analytics.detail.summary
analytics.detail.trends
analytics.detail.crossAnalysis
analytics.dimension.model
analytics.dimension.provider
analytics.dimension.client
analytics.dimension.tenant
board.operational.more
```

## 五、验收标准

### 功能完整性
- [ ] Top5块区显示"更多"按钮
- [ ] 点击"更多"进入完整列表页
- [ ] 列表页显示所有数据(不只是Top5)
- [ ] 列表页支持排序(按请求数/成本/成功率)
- [ ] 点击列表行进入详情页
- [ ] 详情页显示核心指标卡片
- [ ] 详情页显示趋势图(ECharts)
- [ ] 详情页显示交叉分析表
- [ ] 交叉分析表支持点击导航
- [ ] 时间范围选择器工作正常
- [ ] URL状态同步正常

### 复用性
- [ ] 4个维度复用同一套详情组件
- [ ] 注册表正确管理所有路由
- [ ] 不同入口进入相同分析显示一致

### 性能
- [ ] 列表页大数据量(>100行)无卡顿
- [ ] 图表渲染流畅
- [ ] 路由切换无闪烁

### 国际化
- [ ] 所有文案支持中英文
- [ ] 数字格式化正确(千分位、货币符号)

### 降级处理
- [ ] API失败时显示友好错误
- [ ] 部分数据缺失时正确提示
- [ ] 空数据时显示空态

## 六、风险与依赖

### 后端依赖
- 需要新增 `/api/admin/analytics/:dimension/:identifier` 端点
- 数据库查询性能(特别是交叉分析)
- 可能需要新的数据库索引

### 前端风险  
- ECharts实例管理(多图表共存)
- 大列表性能(可能需要虚拟滚动)
- 路由复杂度增加

### 兼容性
- 需要保持与现有 BoardOperationalView 的样式一致性
- 不能破坏现有的降级处理逻辑
- URL参数需要向后兼容

## 七、后续扩展

### 短期
- 导出功能(CSV/Excel)
- 自定义时间范围
- 对比功能(对比两个对象)

### 长期
- 告警配置(某指标超阈值)
- 异常检测
- 预测趋势
