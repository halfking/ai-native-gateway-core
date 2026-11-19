# 用户会话分析增强 - 交付文档

**日期**: 2026-11-19  
**任务**: 在 `/admin/session-analytics/users` 页面增强用户分析功能

## 交付内容

### ✅ 已完成功能

#### 1. 后端 API 实现

**新增端点**: `GET /api/admin/session-analytics/users/:owner/models`

**文件**: `admin/user_profile.go`

**功能特性**:
- 按模型名称和供应商分组统计用户的模型使用情况
- 支持时间范围查询（`start_date`, `end_date` 参数）
  - 支持 ISO 8601 和 YYYY-MM-DD 格式
  - 默认最近 30 天
  - 最长支持 90 天
- 统计维度：
  - 请求次数
  - Token 用量（总数/输入/输出）
  - 内部成本（基于租户定价或 cost_display）
  - 外部成本（供应商实际成本 cost_usd）
  - 首次/最后使用时间
- 权限控制：继承现有三层隔离机制
  - 普通用户只能查看自己
  - tenant_admin 查看本租户
  - super_admin 全局查看
- 降级处理：`request_logs` 表不可用时返回空数据并标记降级

**SQL 查询**:
```sql
SELECT 
  COALESCE(rl.outbound_model, rl.client_model, 'unknown') as model_name,
  COALESCE(p.display_name, rl.provider_id::text, 'unknown') as provider,
  COUNT(*) as request_count,
  COALESCE(SUM(rl.prompt_tokens), 0) as input_tokens,
  COALESCE(SUM(rl.completion_tokens), 0) as output_tokens,
  COALESCE(SUM(rl.prompt_tokens + rl.completion_tokens), 0) as total_tokens,
  COALESCE(SUM(rl.cost_display), SUM(rl.cost_usd), 0)::float8 as internal_cost_usd,
  COALESCE(SUM(rl.cost_usd), 0)::float8 as external_cost_usd,
  MIN(rl.created_at) as first_used_at,
  MAX(rl.created_at) as last_used_at
FROM request_logs rl
LEFT JOIN providers p ON p.id = rl.provider_id
WHERE rl.owner_user = $1 AND rl.created_at >= $2 AND rl.created_at < $3
GROUP BY model_name, provider
ORDER BY external_cost_usd DESC
LIMIT 100
```

**路由注册**: 修改 `handleUserAnalyticsDetail` 支持 `/models` 子路由

#### 2. 前端实现

**文件**: `web/src/views/UserProfileView.vue`

**新增功能**:

1. **模型使用明细表格**
   - 显示列：模型名称、供应商、请求次数、总Token数、输入Token、输出Token、内部成本、外部成本
   - Token 数字格式化（千分位）
   - 成本显示 4 位小数
   - 支持加载状态和错误提示
   - 支持降级模式提示

2. **帮助面板**
   - 右侧抽屉式面板（50% 宽度）
   - 内容包括：
     - Token 统计公式说明
     - 成本核算公式说明（内部成本 vs 外部成本）
     - 数据来源说明
     - 时间范围使用说明
   - 点击"查看统计说明"按钮打开
   - 点击遮罩或关闭按钮关闭

3. **时间范围联动**
   - 复用现有的 `KxDateRangePicker` 组件
   - 支持 7天/30天/90天 预设选项
   - 模型使用数据随时间范围自动刷新

**新增组件引入**:
- `ElDrawer` - 帮助面板
- `ElAlert` - 降级提示
- `QuestionFilled` - 帮助图标

#### 3. API 类型定义

**文件**: `web/src/api/admin.ts`

**新增类型**:
```typescript
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
```

**新增函数**:
```typescript
export function getUserModelUsage(
  owner: string,
  startDate?: string,
  endDate?: string
): Promise<{ models: UserModelUsage[]; ... }>
```

#### 4. 国际化支持

**文件**: 
- `web/src/locales/zh-CN/sessions.ts`
- `web/src/locales/en-US/sessions.ts`

**新增文本**:
- `modelUsage` - 模型使用明细
- `modelName` - 模型名称
- `provider` - 供应商
- `requests` - 请求次数
- `totalTokens` - 总 Token 数
- `inputTokens` - 输入 Token
- `outputTokens` - 输出 Token
- `internalCost` - 内部成本
- `externalCost` - 外部成本
- `helpTitle` - 统计说明
- `showHelp` - 查看统计说明
- `helpContent.*` - 帮助内容各项

#### 5. 设计文档

**文件**: `docs/design/user-analytics-enhancement.md`

包含完整的：
- 需求分析
- 数据模型设计
- API 设计
- SQL 查询设计
- 前端组件设计
- 实现步骤
- 降级和容错策略
- 验收标准

#### 6. Maestro 测试

**文件**: `maestro/flows/14-user-analytics-enhancement.yaml`

**验证内容**:
- 用户画像列表页面可访问
- 用户详情页面加载
- 模型使用明细区块存在
- 所有列标题正确显示
- 帮助按钮和面板功能
- 时间范围选择器
- 页面导航和返回

## 技术亮点

### 1. 成本核算精确性

- **内部成本**：优先使用 `cost_display`（租户定价），回退到 `cost_usd`
- **外部成本**：直接使用 `cost_usd`（供应商实际成本）
- 公式透明，帮助面板有详细说明

### 2. 权限控制一致性

完全继承现有的三层隔离机制：
- 普通用户：`IsRegularUser(r)` → 只能查看自己
- 租户管理员：`IsTenantAdmin(r)` → 查看本租户
- 超级管理员：`IsSuperAdminOrLegacy(r)` → 全局查看

### 3. 降级处理健壮性

- 检测 `request_logs` 表是否存在
- 失败时返回 `degraded: true` 和原因说明
- 前端显示降级提示，而不是完全失败

### 4. 时间范围灵活性

- 支持两种日期格式：ISO 8601 和 YYYY-MM-DD
- 最长 90 天限制，防止性能问题
- 与现有时间选择器无缝集成

### 5. 数据聚合效率

- 使用 `LEFT JOIN providers` 获取供应商名称
- `COALESCE` 处理 NULL 值，避免前端崩溃
- 按外部成本降序排序，优先显示高成本模型
- 限制返回 100 条，避免过大响应

## 数据流程

```
用户选择时间范围
    ↓
前端调用 getUserModelUsage(owner, startDate, endDate)
    ↓
后端 handleUserModelUsage 处理
    ↓
查询 request_logs 表，LEFT JOIN providers
    ↓
按 model_name + provider 分组聚合
    ↓
返回 JSON: { models: [...], start_date, end_date, degraded? }
    ↓
前端渲染表格，格式化数字
    ↓
用户点击帮助按钮
    ↓
显示帮助抽屉，说明统计公式
```

## 文件清单

### 后端
- ✅ `admin/user_profile.go` - 新增 `handleUserModelUsage` 和 `UserModelUsageItem` 类型

### 前端
- ✅ `web/src/views/UserProfileView.vue` - 模型使用明细表格 + 帮助面板
- ✅ `web/src/api/admin.ts` - 新增 API 类型和函数
- ✅ `web/src/locales/zh-CN/sessions.ts` - 中文文本
- ✅ `web/src/locales/en-US/sessions.ts` - 英文文本

### 文档和测试
- ✅ `docs/design/user-analytics-enhancement.md` - 完整设计文档
- ✅ `maestro/flows/14-user-analytics-enhancement.yaml` - E2E 测试流程

## 验收检查表

| 项目 | 状态 | 说明 |
|------|------|------|
| 后端 API 实现 | ✅ | `handleUserModelUsage` 完成 |
| 前端模型使用表格 | ✅ | 8 列完整显示 |
| 帮助面板 | ✅ | 右侧抽屉 + 完整说明 |
| 时间范围选择 | ✅ | 复用现有组件 |
| 权限控制 | ✅ | 三层隔离 |
| 降级处理 | ✅ | 错误提示和降级标记 |
| 国际化 | ✅ | 中英文完整 |
| 编译通过 | ✅ | Go + TypeScript |
| Maestro 测试 | ✅ | 完整流程覆盖 |

## 未完成项（后续工作）

### 1. 单元测试
**后端**:
- `admin/user_profile_model_usage_test.go` - 测试 `handleUserModelUsage`
  - 正常查询
  - 时间范围边界
  - 权限控制
  - 降级场景

**前端**:
- `web/src/views/UserProfileView.test.ts` - 测试模型使用组件
  - 数据加载和显示
  - 帮助面板交互
  - 错误处理

### 2. 集成测试
- 真实数据库测试
- 多用户场景测试
- 大数据量性能测试（100+ 模型）

### 3. 数据库索引优化
建议添加复合索引以提升查询性能：
```sql
CREATE INDEX idx_request_logs_owner_time_model 
ON request_logs (owner_user, created_at, outbound_model, provider_id);
```

### 4. 用户列表页时间范围
目前列表页 (`UserProfileListView.vue`) 尚未添加时间范围选择器。
可选增强：支持按时间范围筛选用户列表。

### 5. 数据导出功能
可选增强：支持导出模型使用统计为 CSV/Excel。

## 使用指南

### 访问路径
1. 导航到 `/admin/session-analytics/users`
2. 点击任意用户的"详情"按钮
3. 在详情页找到"模型使用明细"卡片
4. 点击"查看统计说明"查看帮助

### API 调用示例

**获取用户模型使用（最近 30 天）**:
```bash
curl -H "Authorization: Bearer <token>" \
  "http://localhost:9090/api/admin/session-analytics/users/username/models"
```

**指定时间范围**:
```bash
curl -H "Authorization: Bearer <token>" \
  "http://localhost:9090/api/admin/session-analytics/users/username/models?start_date=2026-10-01&end_date=2026-11-01"
```

**响应示例**:
```json
{
  "models": [
    {
      "model_name": "gpt-4",
      "provider": "OpenAI",
      "request_count": 150,
      "total_tokens": 45000,
      "input_tokens": 30000,
      "output_tokens": 15000,
      "internal_cost_usd": 1.35,
      "external_cost_usd": 1.20,
      "first_used_at": "2026-10-01T00:00:00Z",
      "last_used_at": "2026-10-31T23:59:59Z"
    }
  ],
  "start_date": "2026-10-01T00:00:00Z",
  "end_date": "2026-11-01T00:00:00Z"
}
```

## 运行 Maestro 测试

```bash
cd maestro
./run.sh flows/14-user-analytics-enhancement.yaml
```

或使用自定义参数：
```bash
maestro test flows/14-user-analytics-enhancement.yaml \
  --env BASE_URL=http://localhost:9090 \
  --env ADMIN_USER=admin \
  --env ADMIN_PASSWORD=your_password \
  --screen-size 1440x2200
```

## Git 提交记录

1. **主功能提交**: `feat: 增强用户会话分析 - 模型使用统计与帮助面板`
   - 完整的后端 API
   - 前端表格和帮助面板
   - 国际化文本
   - 设计文档

2. **测试提交**: `test: 添加用户分析增强的 Maestro 验证流程`
   - E2E 测试流程

## 总结

本次增强完成了用户会话分析页面的核心功能，提供了：

1. **透明的成本核算**：清晰区分内部成本和外部成本
2. **详细的 Token 统计**：输入、输出、总量完整展示
3. **灵活的时间范围**：支持自定义查询周期
4. **友好的帮助系统**：右侧抽屉展示完整说明
5. **健壮的错误处理**：降级模式和错误提示

所有代码已经：
- ✅ 编译通过（Go + TypeScript）
- ✅ 推送到远程仓库
- ✅ 包含完整的设计文档
- ✅ 包含 Maestro E2E 测试

**下一步建议**：
1. 运行 Maestro 测试验证功能
2. 补充单元测试和集成测试
3. 考虑添加数据库索引优化性能
4. 根据实际使用反馈进行迭代优化
