# 凭据监控热力图需求规格说明

## 1. 项目概述

### 1.1 背景
当前凭据监控页面 (`/routing-v2/credentials`) 提供了凭据状态的列表视图和详情抽屉，但缺乏对**时间维度**上状态变化的可视化能力。运维人员需要：
- 快速识别凭据在特定时间段内的健康趋势
- 发现间歇性故障和异常模式
- 对比多个凭据/模型的稳定性
- 追踪状态变化的时间点和原因

### 1.2 目标
优化 `/routing-v2/credentials` 页面，新增**热力图视图**（Heatmap View），以时间轴和状态色块的形式展示凭据的健康状况，并支持：
- 多时间粒度查看（今天、7天、本月、自定义）
- 模型维度展开/收起
- 点击色块查看详情和错误信息
- 状态修正操作入口
- 自检测试与真实使用数据分离

---

## 2. 功能需求

### 2.1 页面布局

#### 2.1.1 Tab 结构
在当前页面顶部新增 Tab 切换：
```
[ 列表视图 (List) ] [ 热力图视图 (Heatmap) ] [ 路由记录 (Routing Log) ]
```

- **列表视图**：保持现有的表格+详情抽屉功能（默认 tab）
- **热力图视图**：新增的时间序列可视化（本需求核心）
- **路由记录**：所有模型的路由选择记录和自检测试记录（状态变化日志）

#### 2.1.2 热力图视图布局
```
┌─────────────────────────────────────────────────────────────────┐
│ 筛选栏                                                            │
│ [ Provider: 全部 ▼] [ 模型: 全部 ▼] [ 时间范围: 今天 ▼]         │
│ [ 粒度: 1分钟 ▼] [ □ 仅显示异常 ] [ □ 排除自检 ✓]               │
│ [ 自动刷新 ○ ] [ 刷新间隔: 30秒 ▼] [手动刷新 ↻]                │
└─────────────────────────────────────────────────────────────────┘
┌─────────────────────────────────────────────────────────────────┐
│ 热力图主区域                                                      │
│                                                                   │
│ 凭据 #1 (Provider A)                          [▼ 展开模型]       │
│ ████████████████████████████████████████████  汇总状态条          │
│   ├─ gpt-4 ████████░░░░████████████████████  [详情]             │
│   ├─ gpt-3.5-turbo ████████████████████████  [详情]             │
│   └─ claude-3 ░░░░████████████████████████░  [详情]             │
│                                                                   │
│ 凭据 #2 (Provider B)                          [▶ 收起]            │
│ ████████████████████████████████████████████  汇总状态条          │
│                                                                   │
│ 时间轴 ─────────────────────────────────────────────────────→    │
│       09:00    10:00    11:00    12:00    13:00    14:00        │
│                                                                   │
│ 图例: ■ 健康 ■ 降级 ■ 冷却 ■ 限流 ■ 不可达 ■ 认证失败 □ 无数据  │
└─────────────────────────────────────────────────────────────────┘
```

---

### 2.2 数据源与粒度

#### 2.2.1 数据来源
1. **真实使用记录**（默认显示）
   - 来源：`request_logs` 表，`is_self_test = false`
   - 含义：真实用户请求的路由结果和错误
   - 按 `(credential_id, raw_model_name, time_bucket)` 聚合

2. **自检测试记录**（可选显示）
   - 来源：`model_probe_runs` 表或 `request_logs` 中 `is_self_test = true`
   - 含义：系统自动探测的健康检查结果
   - 独立维度展示，避免与真实流量混淆

#### 2.2.2 时间粒度
支持动态调整，根据时间范围自动推荐，也可手动选择：
- **1 分钟**：适合"今天"、"最近 1 小时"
- **5 分钟**：适合"最近 6 小时"、"今天"
- **15 分钟**：适合"最近 24 小时"、"昨天"
- **1 小时**：适合"最近 7 天"
- **1 天**：适合"本月"、"自定义长周期"

粒度计算逻辑：
```sql
-- PostgreSQL 时间桶示例（1 分钟粒度）
SELECT 
  credential_id,
  raw_model_name,
  date_trunc('minute', created_at) AS time_bucket,
  COUNT(*) AS total_requests,
  SUM(CASE WHEN success THEN 1 ELSE 0 END) AS success_count,
  AVG(latency_ms) AS avg_latency,
  ARRAY_AGG(DISTINCT error_class) FILTER (WHERE error_class IS NOT NULL) AS error_types
FROM request_logs
WHERE 
  created_at >= $1 AND created_at < $2
  AND is_self_test = false  -- 排除自检
GROUP BY credential_id, raw_model_name, time_bucket
ORDER BY time_bucket ASC
```

---

### 2.3 状态映射与色块渲染

#### 2.3.1 状态定义
基于现有的 `CredentialMonitorSummary.availability_state` 和 `CredentialModelStatus.effective_state`，映射到色块颜色：

| 状态          | 英文标识           | 颜色代码    | 含义                         |
|---------------|-------------------|------------|------------------------------|
| 健康          | ready / available | `#10b981`  | 成功率 ≥ 90%                 |
| 降级          | degraded          | `#f59e0b`  | 成功率 50%-90%               |
| 冷却          | cooling           | `#f97316`  | 临时限流，等待恢复           |
| 限流          | rate_limited      | `#f59e0b`  | 触发供应商限流               |
| 不可达        | unreachable       | `#ef4444`  | 网络不可达或超时             |
| 认证失败      | auth_failed       | `#dc2626`  | API Key 无效或过期           |
| 手工禁用      | manual_disabled   | `#6b7280`  | 运维人员手动下线             |
| 无数据        | no_data           | `#e5e7eb`  | 该时间桶内无请求记录         |

#### 2.3.2 色块渲染规则
1. **汇总行**（凭据级别）：
   - 取该凭据下所有模型在该时间桶内的**最差状态**
   - 例如：若 `gpt-4` 健康，`claude-3` 不可达，则汇总行显示红色

2. **模型行**（展开后）：
   - 每个模型独立显示其在该时间桶内的状态
   - 无数据时显示灰色空白块

3. **色块宽度**：
   - 响应式：根据时间范围和粒度自动计算
   - 最小宽度 4px（避免过窄）
   - 最大宽度 60px（避免过宽）

---

### 2.4 交互功能

#### 2.4.1 色块点击 - 详情弹窗
点击任意色块，弹出 **Popover** 或 **Drawer**，显示：

**基础信息**
- 时间范围：`2026-09-06 10:35:00 - 10:36:00`
- 凭据：`#123 (OpenAI Official)`
- 模型：`gpt-4`
- 状态：`降级 (degraded)`

**统计指标**
- 总请求数：`42`
- 成功数：`38`
- 失败数：`4`
- 成功率：`90.5%`
- 平均延迟：`1234ms`
- P95 延迟：`2100ms`

**错误分布**
```
rate_limit_exceeded  2 次
timeout              1 次
model_overload       1 次
```

**失败请求列表**（最多 10 条）
```
| 请求 ID        | 时间      | 错误类型          | HTTP 状态 | 延迟   |
|---------------|-----------|------------------|----------|--------|
| req_abc123    | 10:35:12  | rate_limit_exc.. | 429      | 1200ms |
| req_def456    | 10:35:45  | timeout          | 504      | 30000ms|
```

**操作按钮**
- `查看完整请求日志` → 跳转到 `/request-logs?credential_id=123&model=gpt-4&time_start=...`
- `修正状态` → 打开状态修正对话框（见 2.4.2）
- `关闭`

#### 2.4.2 状态修正操作
在详情弹窗中，提供以下操作（需 super_admin 权限）：

1. **手动上线/下线模型**
   - 调用现有 API：`toggleModelAvailability(credentialId, rawModel, action, reason)`
   - 立即生效，反映到热力图

2. **临时降级凭据**
   - 调用现有 API：`demoteCredential(credentialId, reason, recoverAfterHours)`
   - 设置冷却期

3. **恢复凭据**
   - 调用现有 API：`promoteCredential(credentialId, reason)`

4. **调整并发限制**
   - 调用现有 API：`setConcurrencyAuto(credentialId, value, reason)`

#### 2.4.3 模型展开/收起
- 点击凭据行右侧的 `[▼ 展开模型]` / `[▶ 收起]` 按钮
- 展开后显示该凭据下所有模型的独立热力图行
- 状态持久化到 `localStorage`（key: `credential_heatmap_expanded_{credentialId}`）

#### 2.4.4 时间范围选择
支持以下预设和自定义：
- **今天**：`00:00 - 当前时间`
- **最近 1 小时**：`当前时间 - 1h`
- **最近 6 小时**：`当前时间 - 6h`
- **最近 24 小时**：`当前时间 - 24h`
- **昨天**：`昨天 00:00 - 昨天 23:59`
- **最近 7 天**：`当前时间 - 7d`
- **本月**：`本月 1 号 00:00 - 当前时间`
- **自定义**：弹出日期时间选择器

#### 2.4.5 模型筛选
- 下拉选择器，支持多选
- 选项：`全部 / gpt-4 / gpt-3.5-turbo / claude-3 / ...`
- 仅显示已选模型的热力图行

#### 2.4.6 自动刷新
- 开关控件：`[ 自动刷新 ○ ]`
- 刷新间隔：`10秒 / 30秒 / 60秒`
- 刷新时保持当前滚动位置和展开状态
- 使用增量更新（仅加载最新时间桶数据）

---

### 2.5 路由记录 Tab（新增）

显示所有凭据和模型的路由决策记录，包含：

#### 2.5.1 数据来源
- `routing_decision_log` 表（路由选择记录）
- `model_probe_runs` 表（自检测试记录）
- `model_history` API（状态变化历史）

#### 2.5.2 表格字段
| 字段            | 说明                                    |
|-----------------|-----------------------------------------|
| 时间            | `created_at`                            |
| 凭据            | `credential_id + label`                 |
| 模型            | `raw_model_name`                        |
| 事件类型        | `路由成功 / 路由失败 / 自检成功 / 自检失败 / 状态变化` |
| 状态变化        | `ready → degraded`                      |
| 原因            | `error_class / error_message`           |
| 操作人          | `actor` (手动操作) 或 `系统` (自动)     |
| 请求 ID         | `request_id`（可点击跳转到详情）        |

#### 2.5.3 筛选器
- 凭据筛选
- 模型筛选
- 事件类型筛选
- 时间范围筛选
- 仅显示异常（失败 + 状态变化）

---

## 3. 技术实现

### 3.1 前端架构

#### 3.1.1 组件拆分
```
CredentialMonitorView.vue (主页面)
├── CredentialListView.vue (现有列表视图)
├── CredentialHeatmapView.vue (新增热力图视图)
│   ├── HeatmapToolbar.vue (筛选栏)
│   ├── HeatmapGrid.vue (热力图网格)
│   │   ├── HeatmapRow.vue (单个凭据行)
│   │   │   ├── HeatmapCell.vue (单个时间桶色块)
│   │   │   └── ModelRowExpanded.vue (展开的模型行)
│   │   └── HeatmapTimeline.vue (时间轴)
│   └── HeatmapDetailPopover.vue (色块详情弹窗)
└── RoutingLogView.vue (新增路由记录视图)
```

#### 3.1.2 数据流
```typescript
// API 请求
interface HeatmapDataRequest {
  time_start: string        // RFC3339
  time_end: string          // RFC3339
  granularity: '1m' | '5m' | '15m' | '1h' | '1d'
  credential_ids?: number[] // 可选，筛选特定凭据
  models?: string[]         // 可选，筛选特定模型
  exclude_self_test: boolean // 默认 true
}

// API 响应
interface HeatmapDataResponse {
  meta: {
    time_start: string
    time_end: string
    granularity: string
    bucket_count: number
    cache_hit: boolean
  }
  credentials: Array<{
    credential_id: number
    label: string
    provider_name: string
    models: Array<{
      raw_model_name: string
      buckets: Array<{
        time_bucket: string  // RFC3339
        status: 'ready' | 'degraded' | 'cooling' | 'rate_limited' | 'unreachable' | 'auth_failed' | 'no_data'
        total_requests: number
        success_count: number
        failed_count: number
        success_rate: number
        avg_latency_ms: number
        p95_latency_ms: number
        error_distribution: Record<string, number>
        sample_request_ids: string[] // 失败请求 ID（最多 10 个）
      }>
    }>
  }>
}
```

### 3.2 后端 API 设计

#### 3.2.1 新增端点
```go
// GET /api/credentials/heatmap
// 返回指定时间范围内的凭据热力图数据
func handleCredentialHeatmap(w http.ResponseWriter, r *http.Request) {
    // 解析参数
    timeStart := r.URL.Query().Get("time_start")
    timeEnd := r.URL.Query().Get("time_end")
    granularity := r.URL.Query().Get("granularity") // 1m, 5m, 15m, 1h, 1d
    excludeSelfTest := r.URL.Query().Get("exclude_self_test") == "true"
    
    // 查询数据库
    // SELECT credential_id, raw_model_name, 
    //        date_trunc(granularity, created_at) AS time_bucket,
    //        COUNT(*) AS total, SUM(success::int) AS success, ...
    // FROM request_logs
    // WHERE created_at >= $1 AND created_at < $2
    //   AND (exclude_self_test = false OR is_self_test = false)
    // GROUP BY credential_id, raw_model_name, time_bucket
    
    // 状态推断逻辑
    // - success_rate >= 0.9 → ready
    // - success_rate >= 0.5 → degraded
    // - success_rate < 0.5 → unreachable (需结合 error_class 细分)
    // - 检查是否有 manual_disabled / cooling
    
    // 返回 JSON
}
```

#### 3.2.2 查询优化
1. **索引**
   ```sql
   CREATE INDEX CONCURRENTLY idx_request_logs_heatmap 
   ON request_logs (credential_id, raw_model_name, created_at, success, is_self_test)
   WHERE created_at >= NOW() - INTERVAL '30 days';
   ```

2. **缓存策略**
   - 使用 Redis 缓存最近 1 小时的热力图数据（TTL 1 分钟）
   - Key 格式：`heatmap:{time_start}:{time_end}:{granularity}:{hash(filters)}`

3. **分页加载**
   - 初次加载返回前 20 个凭据
   - 滚动到底部时懒加载下一批

### 3.3 前端渲染优化

#### 3.3.1 虚拟滚动
使用 `vue-virtual-scroller` 或自实现虚拟列表，仅渲染可见区域的凭据行：
```typescript
// 仅渲染 viewport 内的 10-20 行
<virtual-scroller :items="credentials" :item-height="60">
  <template #default="{ item }">
    <HeatmapRow :credential="item" />
  </template>
</virtual-scroller>
```

#### 3.3.2 Canvas 渲染（可选）
对于大量色块（如 7 天 × 1 分钟粒度 = 10080 个），考虑使用 Canvas 绘制：
```typescript
// 伪代码
const canvas = document.getElementById('heatmap-canvas')
const ctx = canvas.getContext('2d')
buckets.forEach((bucket, index) => {
  ctx.fillStyle = getColorForStatus(bucket.status)
  ctx.fillRect(index * cellWidth, rowY, cellWidth, rowHeight)
})
```

#### 3.3.3 增量更新
```typescript
// 自动刷新时，仅加载最新的时间桶
const lastBucketTime = heatmapData.meta.time_end
const now = new Date().toISOString()
const newData = await fetchHeatmapData({
  time_start: lastBucketTime,
  time_end: now,
  granularity: currentGranularity
})
// 合并到现有数据
heatmapData.credentials.forEach(cred => {
  const newCred = newData.credentials.find(c => c.credential_id === cred.credential_id)
  if (newCred) {
    cred.models.forEach(model => {
      const newModel = newCred.models.find(m => m.raw_model_name === model.raw_model_name)
      if (newModel) {
        model.buckets.push(...newModel.buckets)
      }
    })
  }
})
```

---

## 4. 测试验证

### 4.1 单元测试
- 状态映射逻辑（success_rate → status）
- 时间桶聚合（不同粒度下的 date_trunc 结果）
- 色块点击事件处理
- 模型展开/收起状态持久化

### 4.2 集成测试
- 热力图 API 返回正确数据
- 筛选器联动（provider + model + time_range）
- 详情弹窗显示完整错误信息
- 状态修正操作调用正确的后端 API

### 4.3 性能测试
- 1000+ 凭据 × 10+ 模型 × 7 天 × 5 分钟粒度（≈200,000 色块）的渲染性能
- 自动刷新不阻塞 UI
- 虚拟滚动流畅度

### 4.4 用户验收测试（UAT）
- [ ] 能一眼识别凭据的健康趋势（绿色连续 = 稳定）
- [ ] 能快速定位故障时间点（红色色块集中区域）
- [ ] 能对比多个凭据的稳定性（垂直扫视）
- [ ] 能点击色块查看详细错误信息
- [ ] 能直接修正错误状态（下线故障节点）
- [ ] 能过滤自检数据，仅关注真实流量

---

## 5. 里程碑与交付物

### 5.1 里程碑
| 阶段 | 任务                     | 交付物                         | 时间  |
|------|--------------------------|--------------------------------|-------|
| P0   | 后端 API 开发            | `/api/credentials/heatmap`     | 2 天  |
| P1   | 前端基础框架             | Tab 切换 + 空白热力图容器       | 1 天  |
| P2   | 热力图渲染（DOM 版）     | 色块显示 + 时间轴              | 2 天  |
| P3   | 交互功能                 | 点击详情 + 展开模型 + 筛选器   | 2 天  |
| P4   | 状态修正集成             | 调用现有 API 修正状态          | 1 天  |
| P5   | 路由记录 Tab             | 状态变化日志表格               | 1 天  |
| P6   | 性能优化                 | 虚拟滚动 + 缓存 + 增量更新      | 1 天  |
| P7   | 测试与验收               | 单元测试 + 集成测试 + UAT      | 2 天  |

**总计：12 天**

### 5.2 交付清单
- [ ] 后端 API：`/api/credentials/heatmap`
- [ ] 前端组件：`CredentialHeatmapView.vue` + 子组件
- [ ] 数据库索引：`idx_request_logs_heatmap`
- [ ] 单元测试覆盖率 ≥ 80%
- [ ] 集成测试用例 ≥ 10 个
- [ ] 性能测试报告（10,000+ 色块渲染 < 500ms）
- [ ] 用户手册（Markdown）

---

## 6. 风险与缓解

| 风险                     | 影响   | 缓解措施                                     |
|--------------------------|--------|---------------------------------------------|
| 数据库查询慢（大时间范围）| 高     | 添加索引 + 分页加载 + Redis 缓存            |
| 前端渲染卡顿（大量色块）  | 中     | 虚拟滚动 + Canvas 渲染 + Web Worker         |
| 状态映射逻辑不准确        | 高     | 与现有 `credentialDisplayState` 对齐 + 测试 |
| 自检数据污染真实流量      | 中     | 默认排除 `is_self_test = true` + 独立展示   |
| 时间粒度选择困难          | 低     | 根据时间范围自动推荐粒度 + 提示文案          |

---

## 7. 后续优化方向

### 7.1 高级筛选
- 按错误类型筛选（仅显示 `rate_limit_exceeded` 的色块）
- 按成功率阈值筛选（仅显示成功率 < 80% 的时间桶）

### 7.2 对比模式
- 同时显示多个凭据的热力图（纵向堆叠）
- 支持拖拽调整顺序

### 7.3 异常检测
- 自动标记异常时间段（连续 5 个时间桶失败率 > 50%）
- 发送告警通知

### 7.4 导出功能
- 导出热力图为 PNG 图片
- 导出原始数据为 CSV

### 7.5 AI 分析
- 调用 LLM 分析热力图，生成故障诊断报告
- 例如："凭据 #123 在 10:30-11:00 期间出现间歇性 `rate_limit_exceeded` 错误，建议调整并发限制或联系供应商"

---

## 8. 附录

### 8.1 相关文档
- 现有凭据监控页面：`/routing-v2/credentials`
- API 文档：`web/src/api/credential-monitor.ts`
- 状态定义：`web/src/utils/credentialStatus.ts`

### 8.2 参考设计
- GitHub Actions 日志时间轴
- Datadog APM 热力图
- Grafana Heatmap Panel

### 8.3 术语表
| 术语        | 英文                  | 说明                                 |
|-------------|-----------------------|--------------------------------------|
| 凭据        | Credential            | 供应商 API Key                       |
| 热力图      | Heatmap               | 时间序列色块可视化                   |
| 时间桶      | Time Bucket           | 聚合时间粒度的最小单位               |
| 自检        | Self Test / Probe     | 系统自动健康检查                     |
| 路由决策    | Routing Decision      | 为请求选择凭据和模型的过程           |
| 状态修正    | State Correction      | 手动调整凭据/模型的可用性状态        |
