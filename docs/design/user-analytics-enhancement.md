# 用户会话分析增强设计方案

## 1. 需求分析

### 1.1 核心需求
在 `/admin/session-analytics/users` 页面增强用户分析功能，提供：

1. **时间范围选择**：与 Dashboard 一致的时间范围选择器
2. **模型使用统计**：每个员工在指定时间段内：
   - 使用了哪些模型
   - 每个模型的 Token 用量
   - 内部成本核算
   - 外部成本
3. **统计说明**：帮助按钮 → 右侧弹出统计公式及说明
4. **逐层深入分析**：用户 → 项目 → 任务 → 会话 → 详情
5. **多维度分析**：按用户、模型等维度进行逐层深入

### 1.2 现有系统分析

#### 后端现状
- `GET /api/admin/session-analytics/users` - 用户列表
- `GET /api/admin/session-analytics/users/:owner` - 用户详情（支持 `days` 参数）
- 数据源：`session_owners` 聚合表
- 权限：三层隔离（super_admin/tenant_admin/普通用户）

#### 前端现状
- `UserProfileListView.vue` - 用户列表
- `UserProfileView.vue` - 用户详情（已有时间范围选择）
- 已实现：会话统计、成本趋势图、热门任务/终端用户/最近会话

### 1.3 需要增强的功能

1. **用户列表页增强**：
   - 添加时间范围选择器
   - 显示时间范围内的统计数据

2. **用户详情页增强**：
   - 添加模型使用明细表
   - 每个模型显示：
     - 模型名称
     - Token 用量（input/output/total）
     - 请求次数
     - 内部成本
     - 外部成本
   - 添加帮助面板

3. **新增端点**：
   - `GET /api/admin/session-analytics/users/:owner/models` - 用户的模型使用明细

## 2. 数据模型设计

### 2.1 API 响应结构

```typescript
// 用户模型使用明细
interface UserModelUsage {
  model_name: string
  provider: string
  request_count: number
  total_tokens: number
  input_tokens: number
  output_tokens: number
  internal_cost_usd: number  // 内部成本核算
  external_cost_usd: number  // 供应商成本
  first_used_at: string
  last_used_at: string
}

// 用户详情响应增强
interface UserProfileDetailEnhanced extends UserProfileDetailResponse {
  model_usage: UserModelUsage[]
}
```

### 2.2 统计公式

#### Token 统计
- `total_tokens = input_tokens + output_tokens`
- 来源：`request_logs` 表的 `input_token_count` 和 `output_token_count`

#### 成本核算
- **外部成本**（`external_cost_usd`）：供应商实际收费
  - 来源：`request_logs.total_cost_usd`
  - 公式：`SUM(request_logs.total_cost_usd)`
  
- **内部成本**（`internal_cost_usd`）：网关内部定价
  - 来源：根据租户定价规则计算
  - 公式：基于 `pricing_rules` 表的定价策略
  - 如果无租户定价，则等于外部成本

## 3. 后端实现

### 3.1 SQL 查询设计

```sql
-- 用户模型使用统计（时间范围）
SELECT 
  rl.model_name,
  rl.provider,
  COUNT(*) as request_count,
  COALESCE(SUM(rl.input_token_count), 0) as input_tokens,
  COALESCE(SUM(rl.output_token_count), 0) as output_tokens,
  COALESCE(SUM(rl.input_token_count + rl.output_token_count), 0) as total_tokens,
  COALESCE(SUM(rl.total_cost_usd), 0) as external_cost_usd,
  COALESCE(SUM(rl.internal_cost_usd), 0) as internal_cost_usd,
  MIN(rl.created_at) as first_used_at,
  MAX(rl.created_at) as last_used_at
FROM request_logs rl
WHERE rl.owner_user = $1
  AND rl.created_at >= $2
  AND rl.created_at < $3
  AND rl.tenant_id = $4  -- 租户隔离
GROUP BY rl.model_name, rl.provider
ORDER BY external_cost_usd DESC
LIMIT 100
```

### 3.2 新增 API 端点

```go
// handleUserModelUsage GET /api/admin/session-analytics/users/:owner/models
// 查询用户在指定时间范围内的模型使用统计
func (h *Handler) handleUserModelUsage(w http.ResponseWriter, r *http.Request) {
	// 权限检查：同 handleUserAnalyticsDetail
	// 时间范围：start_date, end_date 参数（ISO 8601）
	// 默认最近 30 天
}
```

### 3.3 现有端点增强

修改 `handleUserAnalyticsList` 支持时间范围参数：
- `start_date` (可选)
- `end_date` (可选)
- 默认全时间范围

## 4. 前端实现

### 4.1 组件增强

#### UserProfileView.vue
1. 添加模型使用明细卡片
2. 添加帮助按钮和侧边栏
3. 使用 `KxDateRangePicker`（已存在）

```vue
<!-- 模型使用明细 -->
<el-card class="section-card">
  <template #header>
    <div class="card-header-with-help">
      <span>{{ t('sessions.userProfile.modelUsage') }}</span>
      <el-button @click="showHelp = true" type="text" icon="QuestionFilled">
        {{ t('common.help') }}
      </el-button>
    </div>
  </template>
  
  <el-table :data="modelUsage" stripe>
    <el-table-column prop="model_name" :label="t('sessions.userProfile.modelName')" />
    <el-table-column prop="provider" :label="t('sessions.userProfile.provider')" />
    <el-table-column prop="request_count" :label="t('sessions.userProfile.requests')" align="right" />
    <el-table-column prop="total_tokens" :label="t('sessions.userProfile.totalTokens')" align="right" />
    <el-table-column prop="input_tokens" :label="t('sessions.userProfile.inputTokens')" align="right" />
    <el-table-column prop="output_tokens" :label="t('sessions.userProfile.outputTokens')" align="right" />
    <el-table-column prop="internal_cost_usd" :label="t('sessions.userProfile.internalCost')" align="right">
      <template #default="scope">${{ scope.row.internal_cost_usd.toFixed(4) }}</template>
    </el-table-column>
    <el-table-column prop="external_cost_usd" :label="t('sessions.userProfile.externalCost')" align="right">
      <template #default="scope">${{ scope.row.external_cost_usd.toFixed(4) }}</template>
    </el-table-column>
  </el-table>
</el-card>

<!-- 帮助侧边栏 -->
<el-drawer v-model="showHelp" :title="t('sessions.userProfile.helpTitle')" size="50%">
  <div class="help-content">
    <!-- 统计公式和说明 -->
  </div>
</el-drawer>
```

#### UserProfileListView.vue
1. 添加时间范围选择器
2. 更新 API 调用传递时间范围

### 4.2 国际化文本

```typescript
// web/src/locales/zh-CN/sessions.ts
{
  userProfile: {
    // ... 现有文本
    modelUsage: '模型使用明细',
    modelName: '模型名称',
    provider: '供应商',
    requests: '请求次数',
    totalTokens: '总Token数',
    inputTokens: '输入Token',
    outputTokens: '输出Token',
    internalCost: '内部成本',
    externalCost: '外部成本',
    helpTitle: '统计说明',
    timeRange: '时间范围',
    
    help: {
      tokenCalculation: 'Token 统计公式',
      tokenDesc: '总Token数 = 输入Token + 输出Token，数据来源于每次请求的实际用量',
      costCalculation: '成本核算公式',
      internalCostDesc: '内部成本：根据租户定价规则计算的成本，用于内部核算',
      externalCostDesc: '外部成本：向供应商支付的实际成本',
      dataSource: '数据来源：request_logs 表的聚合统计'
    }
  }
}
```

### 4.3 API 调用

```typescript
// web/src/api/admin.ts

export interface UserModelUsage {
  model_name: string
  provider: string
  request_count: number
  total_tokens: number
  input_tokens: number
  output_tokens: number
  internal_cost_usd: number
  external_cost_usd: number
  first_used_at: string
  last_used_at: string
}

export function getUserModelUsage(
  owner: string, 
  startDate?: string, 
  endDate?: string
): Promise<{ models: UserModelUsage[] }> {
  const params = new URLSearchParams()
  if (startDate) params.set('start_date', startDate)
  if (endDate) params.set('end_date', endDate)
  return req<{ models: UserModelUsage[] }>(
    'GET', 
    `/api/admin/session-analytics/users/${owner}/models?${params}`
  )
}
```

## 5. 实现步骤

### Phase 1: 后端实现
1. 添加 `handleUserModelUsage` 处理器
2. 修改 `handleUserAnalyticsList` 支持时间范围
3. 添加 SQL 查询和数据聚合
4. 添加单元测试

### Phase 2: 前端实现
1. 更新 API 类型定义
2. 增强 UserProfileView 组件
3. 添加帮助面板组件
4. 添加国际化文本
5. 更新 UserProfileListView 时间范围

### Phase 3: 测试验证
1. 单元测试
2. 集成测试
3. Maestro 端到端测试
4. 性能测试（大数据量）

## 6. 降级和容错

### 6.1 数据库降级
- 如果 `request_logs` 表不存在或查询失败
- 返回 `degraded: true` 和空数组
- 前端显示降级提示

### 6.2 权限控制
- 继承现有三层隔离机制
- 普通用户只能查看自己的数据
- tenant_admin 只能查看本租户数据

### 6.3 性能优化
- 时间范围限制：最多 90 天
- 结果集限制：最多 100 个模型
- 添加数据库索引：`(owner_user, created_at, model_name)`

## 7. 未来扩展

### 7.1 项目维度分析
- 新增 `/api/admin/session-analytics/users/:owner/projects` 端点
- 按项目聚合统计

### 7.2 任务维度分析
- 已存在 `top_tasks` 数据
- 可点击任务跳转到任务详情页

### 7.3 导出功能
- 支持导出 CSV/Excel
- 包含完整的模型使用明细

## 8. 验收标准

1. ✅ 时间范围选择器正常工作
2. ✅ 模型使用明细正确显示
3. ✅ Token 统计准确
4. ✅ 成本核算准确（内部/外部）
5. ✅ 帮助面板显示统计说明
6. ✅ 权限控制正确
7. ✅ 降级处理正常
8. ✅ 所有测试通过
9. ✅ Maestro 验证通过
