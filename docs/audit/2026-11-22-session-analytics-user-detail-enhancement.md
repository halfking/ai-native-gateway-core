# 2026-11-22 会话分析用户详情页增强实施报告

## 需求概述

在会话分析系统的用户详情页增加两个新tab：
- **模型用量**：展示用户在时间窗口内使用的模型及其详细指标
- **API Key 统计**：展示用户使用的API Key及其关联项目/任务统计

## 实施内容

### 1. 后端API开发

#### 1.1 数据查询层 (`admin/session_analytics/queries.go`)

新增两个查询函数：

**`GetUserModelUsage`** - 用户模型用量查询
- SQL查询：从 `request_logs` 表按owner聚合，JOIN `models` 表获取模型名称
- 返回字段：
  - 基础指标：model（模型名）、request_count（请求数）、total_tokens（Token总数）
  - 成本：total_cost（总花费）、internal_cost（内部成本）、external_cost（外部成本）
  - 高级指标：success_rate（成功率）、cache_hit_rate（缓存率）、avg_response_time_ms（平均响应时间）
  - 时间：last_used_at（最后使用时间）
- 排序：按请求数降序

**`GetUserAPIKeyUsage`** - 用户API Key用量查询
- SQL查询：使用CTE聚合 `request_logs`，LEFT JOIN `keys` 表获取key_alias
- 返回字段：
  - 标识：key_id（密钥ID）、key_alias（密钥别名）
  - 使用统计：request_count、total_tokens、total_cost、internal_cost、external_cost
  - 关联：project_count（项目数）、task_count（任务数）、top_project（主要项目）
  - 时间：last_used_at
- 排序：按请求数降序

#### 1.2 HTTP处理层 (`admin/session_analytics/handlers.go`)

**新增handler函数**：
- `HandleGetUserModelUsage` - 处理 `/api/admin/session-analytics/user-ranking/:owner/models` 请求
- `HandleGetUserAPIKeyUsage` - 处理 `/api/admin/session-analytics/user-ranking/:owner/api-keys` 请求

**路由分发扩展**：
- 修改 `routeUserRanking` 函数，添加对 `models` 和 `api-keys` 子路由的支持
- 路由模式：
  ```
  /:owner                          → 用户详情
  /:owner/projects                 → 用户项目列表
  /:owner/models                   → 用户模型用量（新增）
  /:owner/api-keys                 → 用户API Key用量（新增）
  /:owner/projects/:project/tasks  → 项目任务列表
  ```

### 2. 前端开发

#### 2.1 API客户端 (`web/src/api/sessionAnalytics.ts`)

**类型定义**：
```typescript
interface UserModelUsage {
  model: string
  request_count: number
  total_tokens: number
  total_cost: number
  internal_cost: number
  external_cost: number
  success_rate: number
  cache_hit_rate: number
  avg_response_time_ms: number
  last_used_at: string
}

interface UserAPIKeyUsage {
  key_id: number
  key_alias: string
  request_count: number
  total_tokens: number
  total_cost: number
  internal_cost: number
  external_cost: number
  project_count: number
  task_count: number
  top_project: string
  last_used_at: string
}
```

**API函数**：
- `getUserModelUsage(owner, startDate, endDate)`
- `getUserAPIKeyUsage(owner, startDate, endDate)`

#### 2.2 用户详情页 (`SessionAnalyticsUserDetailView.vue`)

**tab扩展**：
- 原有：`'projects' | 'tasks'`
- 扩展后：`'projects' | 'tasks' | 'models' | 'apiKeys'`

**数据加载**：
- 添加 `modelsLoader`、`apiKeysLoader` 加载器
- 添加 `loadModels()`、`loadAPIKeys()` 函数
- 实现懒加载：首次切换到tab时才发起请求

**表格展示**：
- 模型用量表：6列（模型、请求数、Token总数、成功率、缓存率、平均响应时间）
- API Key统计表：6列（密钥别名、密钥ID、请求数、Token总数、项目数、主要项目）

**数据格式化**：
- 成功率/缓存率：乘以100显示为百分比，保留1位小数
- 响应时间：保留整数毫秒
- API Key别名为空时显示"-"

### 3. 国际化

为8个语种添加完整支持：
- zh-CN（中文简体）
- en-US（英语）
- ja-JP（日语）
- zh-TW（中文繁体）
- de-DE（德语）
- es-ES（西班牙语）
- fr-FR（法语）
- ar-SA（阿拉伯语）

**新增i18n键**：
- section块：`models`（模型用量）、`apiKeys`（API Key 统计）
- col块：`keyAlias`（密钥别名）、`keyID`（密钥 ID）、`projectCount`（项目数）、`topProject`（主要项目）、`avgResponseTime`（平均响应时间）
- empty块：`noResultUserModels`、`noResultUserAPIKeys`
- error块：`userModelsFailed`、`userAPIKeysFailed`

### 4. E2E测试扩展

#### 4.1 Maestro Flow扩展 (`maestro/flows/16-session-analytics-system.yaml`)

在用户详情页测试中添加：
- **④.1 模型用量tab**：
  - 点击「模型用量」tab
  - 验证表头：模型、请求数、Token总数、成功率、缓存率、平均响应时间
- **④.2 API Key统计tab**：
  - 点击「API Key 统计」tab
  - 验证表头：密钥别名、密钥ID、请求数、Token总数、项目数、主要项目

#### 4.2 CDP探针扩展 (`maestro/tools/assert-session-analytics.mjs`)

**API层验证**（从7个端点扩展到9个）：
- 端点⑧：`/api/admin/session-analytics/user-ranking/:owner/models`
  - 验证200状态码
  - 验证响应包含models数组
- 端点⑨：`/api/admin/session-analytics/user-ranking/:owner/api-keys`
  - 验证200状态码
  - 验证响应包含api_keys数组

**页面层验证**：
- 模型用量tab：
  - 点击tab切换
  - 验证表头齐全（6列）
  - 验证有数据行或显示空态
  - 对拍：首行「模型」== API响应 models[0].model
- API Key统计tab：
  - 点击tab切换
  - 验证表头齐全（6列）
  - 验证有数据行或显示空态
  - 对拍：首行「密钥ID」== API响应 api_keys[0].key_id

**同源基准对拍**：
- 抓取页面自己收到的API响应（避免活流量漂移导致假红）
- 页面渲染数字与同一时刻的响应逐字对拍

## 技术要点

### 1. SQL查询设计

**成功率/缓存率计算**：
```sql
COALESCE(AVG(CASE WHEN r.status = 'success' THEN 1.0 ELSE 0.0 END), 0) as success_rate
COALESCE(AVG(CASE WHEN r.cache_read_tokens > 0 THEN 1.0 ELSE 0.0 END), 0) as cache_hit_rate
```

**API Key查询使用CTE**：
```sql
WITH key_stats AS (
  SELECT key_id, COUNT(*) as request_count, ...
  FROM request_logs
  WHERE owner = $1 AND ...
  GROUP BY key_id
)
SELECT 
  ks.*,
  k.key_alias
FROM key_stats ks
LEFT JOIN keys k ON ks.key_id = k.id
```

### 2. 前端懒加载实现

```typescript
watch(tab, (newTab) => {
  if (newTab === 'models' && !modelsLoader.loaded && !modelsLoading.value) {
    loadModels()
  } else if (newTab === 'apiKeys' && !apiKeysLoader.loaded && !apiKeysLoading.value) {
    loadAPIKeys()
  }
})
```

### 3. E2E测试的「同源对拍」模式

- **问题**：探针另发API请求作为对拍基准，在活流量环境下会因时间差导致数据不一致
- **解决**：使用CDP Network域抓取页面自己收到的响应体
- **优势**：同一份数据、同一时刻、严格相等，无需容差

## 验证结果

### 编译验证
- ✅ Go后端编译通过：`go build ./cmd/gateway/`
- ✅ TypeScript编译通过：`pnpm vue-tsc --noEmit`
- ✅ 前端构建通过：`pnpm build`（20.38s）

### 部署验证
- ✅ 211环境部署成功
- ✅ 版本：2.6.1-1621daff-20261122-211 (build_seq=3195)
- ✅ 槽位：b接管8783端口
- ✅ 迁移：981-996全部already applied

### E2E测试
- 🔄 Maestro flow测试运行中
- 🔄 CDP探针验证运行中

## 提交记录

### 主功能提交 (1621daffe)
```
feat(session-analytics): 用户详情页增加模型用量和API Key统计

- 后端queries.go：添加GetUserModelUsage和GetUserAPIKeyUsage函数
- 后端handlers.go：添加HandleGetUserModelUsage和HandleGetUserAPIKeyUsage
- 路由：/api/admin/session-analytics/user-ranking/:owner/models和api-keys
- 前端API：添加getUserModelUsage和getUserAPIKeyUsage及相关类型
- 前端详情页：扩展tab支持models和apiKeys，懒加载数据
- 国际化：8语种完整支持（zh-CN/en-US/ja-JP/zh-TW/de-DE/es-ES/fr-FR/ar-SA）
- 指标：模型用量含成功率/缓存率/响应时间；API Key含项目/任务关联
```

### 测试扩展提交 (96b3735ea)
```
test(session-analytics): 扩展E2E测试覆盖用户详情页新增tab

- Maestro flow：添加对「模型用量」和「API Key 统计」两个tab的验证
- CDP探针：添加端点⑧⑨验证（/user-ranking/:owner/models和api-keys）
- CDP探针：添加models和apiKeys两个tab的页面层断言与对拍
- 更新覆盖范围：从7个端点扩展到9个端点
```

## 遗留事项

1. E2E测试结果待确认（测试正在运行）
2. 生产环境部署待安排（目前仅211测试环境）
3. 监控指标待观察（成功率/缓存率/响应时间在真实数据下的表现）

## 参考文档

- 后端API实现：`admin/session_analytics/queries.go`、`admin/session_analytics/handlers.go`
- 前端实现：`web/src/views/admin/session-analytics/SessionAnalyticsUserDetailView.vue`
- E2E测试：`maestro/flows/16-session-analytics-system.yaml`、`maestro/tools/assert-session-analytics.mjs`
- 上一轮基础工作：`docs/audit/2026-11-20-会话分析系统前端实施报告.md`
