# 运营看板详情分析系统 - 实施进度报告

## 已完成的工作 (2026-11-20)

### 1. 基础设施 ✅

#### 1.1 分析入口注册表
- **文件**: `web/src/config/analyticsRegistry.ts`
- **功能**: 中央化管理所有分析路由、API端点、标题
- **支持维度**: model, provider, client, tenant
- **设计原则**: 单一真源、可追溯性、类型安全

#### 1.2 TypeScript 类型定义
- **文件**: `web/src/api/board.ts`
- **新增类型**:
  - `AnalyticsTrendPoint`: 趋势数据点
  - `AnalyticsSummary`: 核心指标汇总
  - `AnalyticsCrossAnalysis`: 交叉分析数据
  - `AnalyticsDetailPayload`: 完整详情数据
  - `AnalyticsQuery`: 查询参数
- **新增函数**: `fetchAnalyticsDetail()` - 调用后端API获取详情

### 2. 前端组件 ✅

#### 2.1 列表页组件
- **文件**: `web/src/views/BoardOperationalListView.vue`
- **功能**:
  - 参数化复用(通过 route.params.dimension 区分维度)
  - 完整数据列表展示
  - 支持按请求数/Token/成本/积分排序
  - 点击行导航到详情页
  - 时间范围选择器
- **路由**: `/admin/board-operational/:dimension` (models/providers/clients/tenants)

#### 2.2 详情分析页主容器
- **文件**: `web/src/views/analytics/AnalyticsDetailView.vue`
- **功能**:
  - 参数化复用(通过 route.params 区分维度和对象)
  - 面包屑导航
  - 集成子组件(指标卡片、趋势图、交叉分析表)
  - 降级处理
  - 时间范围选择器
- **路由**: `/admin/analytics/:dimension/:id`

#### 2.3 核心指标卡片
- **文件**: `web/src/components/analytics/AnalyticsSummaryCards.vue`
- **功能**:
  - 6个核心指标卡片网格
  - 总请求数、成功率、Token、成本、积分、延迟
  - 响应式布局

#### 2.4 趋势图表
- **文件**: `web/src/components/analytics/AnalyticsTrendChart.vue`
- **功能**:
  - ECharts 多轴折线图
  - 4条曲线: 请求数、成功率、花费、延迟
  - 响应式调整
  - 平滑曲线和面积填充

#### 2.5 交叉分析表格
- **文件**: `web/src/components/analytics/AnalyticsCrossTable.vue`
- **功能**:
  - 多维度交叉分析(按模型/供应商/客户端/租户/错误/状态)
  - 自动排除当前维度
  - 点击行导航到对应详情页
  - 降级提示
  - 显示Top 10

### 3. 运营看板改造 ✅

#### 3.1 BoardOperationalView 增强
- **文件**: `web/src/views/BoardOperationalView.vue`
- **新增功能**:
  - 4个Top5卡片添加"更多"按钮
  - 卡片项支持点击导航到详情页
  - 悬停效果和视觉反馈
- **样式**: 新增 `.bo-more-btn`, `.bo-pie-item-clickable` 等样式

### 4. 路由配置 ✅

#### 4.1 新增路由
- **文件**: `web/src/router.ts`
- **路由**:
  - `/admin/board-operational/:dimension` → BoardOperationalListView
  - `/admin/analytics/:dimension/:id` → AnalyticsDetailView
- **权限**: 均为 `requiresSuper: true`

### 5. 国际化 ✅

#### 5.1 中文文案
- **文件**: `web/src/locales/zh-CN/dashboard.ts`
- **新增命名空间**:
  - `analytics.dimension.*`: 维度名称
  - `analytics.list.*`: 列表页文案
  - `analytics.detail.*`: 详情页文案
  - `analytics.summary.*`: 指标卡片文案
  - `analytics.trend.*`: 趋势图文案
  - `analytics.cross.*`: 交叉分析文案
  - `board.operational.more`: "更多"按钮文案

## 待实现的后端工作 ⚠️

### 后端API端点

需要实现以下端点:

```
GET /api/admin/analytics/:dimension/:identifier
```

**参数**:
- `dimension`: model | provider | client | tenant
- `identifier`: 对象ID (URL编码)
- Query参数:
  - `days`: 时间范围(天数)
  - `start`, `end`: 或使用开始/结束时间
  - `tenant_id`: 可选租户过滤

**返回结构** (参考 `web/src/api/board.ts` 中的类型定义):
```typescript
{
  dimension: string
  identifier: string
  display_name?: string
  time_range: { start, end, days }
  
  summary: {
    total_requests: number
    success_count: number
    success_rate: number
    error_count: number
    total_tokens: number
    total_cost_usd: number
    total_credits: number
    avg_latency_ms: number
    // 可选: p95_latency_ms, p99_latency_ms
  }
  
  trends: [{
    bucket: string (ISO 8601)
    requests: number
    success: number
    errors: number
    tokens: number
    cost_usd: number
    credits: number
    avg_latency_ms: number
  }]
  
  cross_analysis: {
    by_model?: [...],
    by_provider?: [...],
    by_client?: [...],
    by_tenant?: [...],
    by_error?: [...],
    by_status?: [...]
  }
  
  // 降级标记
  degraded?: boolean
  degraded_reason?: string
  degraded_trends?: boolean
  degraded_cross?: string[]
}
```

### 数据库查询建议

1. **核心汇总查询**:
   - 从 `usage_facts` 或 `request_stats_minute` 聚合
   - 根据 dimension 和 identifier 过滤
   - WHERE 条件: dimension列 = identifier AND time_range

2. **趋势数据查询**:
   - 按时间桶(bucket)分组
   - 建议使用15分钟或1小时粒度
   - 返回时间序列数据

3. **交叉分析查询**:
   - 对于每个其他维度,执行 GROUP BY 查询
   - 例如查询某个模型时,按 provider/client/tenant 分组
   - 返回Top 10或20

4. **性能优化**:
   - 考虑添加复合索引
   - 可以参考现有的 `dashboard_board_queries.go` 实现
   - 复用 board 端点的查询逻辑

### 实施建议

**后端文件位置**:
```
admin/
  analytics_handlers.go        # 新增: 处理 /api/admin/analytics/* 路由
  analytics_queries.go          # 新增: 数据库查询逻辑
  analytics_handlers_test.go   # 单元测试
```

**复用现有代码**:
- 参考 `admin/dashboard_board.go` 和 `admin/dashboard_board_queries.go`
- 复用时间范围解析逻辑 (`boardTimeRange`)
- 复用降级处理模式
- 复用 `BoardPieItem` 类型作为交叉分析的返回格式

**权限**:
- 与 `/admin/dashboard/board` 保持一致: `superAdmin` 或 `admin_key`
- 可选: 支持 `tenant_id` 参数进行租户过滤

## 测试计划

### 前端测试
1. ✅ TypeScript 编译通过
2. ⏳ 单元测试(需要后端API)
3. ⏳ E2E 测试(需要后端API)

### 后端测试
1. ⏳ 单元测试: 各维度的查询逻辑
2. ⏳ 集成测试: 端点返回正确数据
3. ⏳ 性能测试: 大数据量下的响应时间

### 完整流程测试
1. ⏳ 从运营看板点击"更多" → 进入列表页
2. ⏳ 列表页排序功能
3. ⏳ 点击列表行 → 进入详情页
4. ⏳ 详情页各区块正常显示
5. ⏳ 交叉分析表点击 → 跨维度导航
6. ⏳ 时间范围切换 → 数据正确更新
7. ⏳ 降级场景 → 友好提示

## 部署清单

### 前端部署
- [x] 组件文件已创建
- [x] 路由已配置
- [x] 国际化文案已添加(中文)
- [ ] 需要补充其他7种语言的文案
- [ ] TypeScript 编译验证
- [ ] 构建验证

### 后端部署
- [ ] 实现 analytics 端点
- [ ] 添加路由注册
- [ ] 单元测试
- [ ] 更新 API 文档

### 数据库
- [ ] 评估是否需要新索引
- [ ] 性能测试

## 文件清单

### 新增文件
```
web/src/config/analyticsRegistry.ts
web/src/views/BoardOperationalListView.vue
web/src/views/analytics/AnalyticsDetailView.vue
web/src/components/analytics/AnalyticsSummaryCards.vue
web/src/components/analytics/AnalyticsTrendChart.vue
web/src/components/analytics/AnalyticsCrossTable.vue
ANALYTICS_DESIGN.md                    # 详细设计文档
ANALYTICS_IMPLEMENTATION_STATUS.md     # 本文档
```

### 修改文件
```
web/src/api/board.ts                   # 新增类型和API函数
web/src/views/BoardOperationalView.vue # 增加"更多"按钮和导航
web/src/router.ts                      # 新增路由
web/src/locales/zh-CN/dashboard.ts     # 新增文案
web/src/locales/zh-CN/index.ts         # 导出board命名空间
```

## 下一步行动

1. **立即**: 实现后端 `/api/admin/analytics/:dimension/:identifier` 端点
2. **然后**: 补充其他7种语言的国际化文案
3. **接着**: 进行完整的功能测试
4. **最后**: 性能优化和部署

## 设计亮点

1. **参数化复用**: 4个维度共用同一套组件,减少代码重复
2. **中央注册表**: 所有路由和端点集中管理,易于维护
3. **类型安全**: 完整的TypeScript类型定义
4. **降级处理**: 遵循现有的降级模式
5. **复用现有设计**: 继承 BoardOperationalView 的设计语言
6. **交叉导航**: 支持在不同维度之间跳转分析

---

**创建时间**: 2026-11-20  
**状态**: 前端完成,等待后端实现
