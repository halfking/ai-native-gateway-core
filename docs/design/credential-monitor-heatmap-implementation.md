# 凭据监控热力图实施总结

## 已完成工作

### 1. 需求文档（已完成 ✅）
- **文件**: `docs/credential-monitor-heatmap-requirements.md`
- **内容**: 完整的需求规格说明，包括：
  - 项目概述与目标
  - 功能需求（页面布局、数据源、交互功能）
  - 技术实现方案
  - 测试验证计划
  - 里程碑与交付清单

### 2. 后端 API 实现（已完成 ✅）
- **文件**: `admin/credential_monitor_heatmap.go`
- **端点**: `GET /api/credentials/heatmap`
- **功能**:
  - 时间序列聚合查询（按凭据、模型、时间桶）
  - 支持多种时间粒度（1m, 5m, 15m, 1h, 1d）
  - 支持筛选：凭据 ID、模型名称、时间范围
  - 排除自检数据（默认）
  - 租户隔离（tenant_admin 仅看自己的数据）
  - 错误分布统计
  - 失败请求采样（最多 10 条）
  - 状态推导（ready, degraded, unreachable, no_data）

- **数据结构**:
  ```go
  type HeatmapBucket struct {
    TimeBucket        string             `json:"time_bucket"`
    Status            string             `json:"status"`
    TotalRequests     int                `json:"total_requests"`
    SuccessCount      int                `json:"success_count"`
    FailedCount       int                `json:"failed_count"`
    SuccessRate       float64            `json:"success_rate"`
    AvgLatencyMs      *int               `json:"avg_latency_ms"`
    P95LatencyMs      *int               `json:"p95_latency_ms"`
    ErrorDistribution map[string]int     `json:"error_distribution"`
    SampleRequestIDs  []string           `json:"sample_request_ids"`
  }
  ```

- **查询性能优化**:
  - 使用 `date_trunc()` 进行时间桶聚合
  - FILTER 子句高效计算成功/失败数
  - `percentile_cont()` 计算 P95 延迟
  - `jsonb_object_agg()` 聚合错误分布
  - `array_agg()` 采样失败请求 ID

- **已注册路由**:
  ```go
  mux.HandleFunc("/api/credentials/heatmap", wrap(m.handleCredentialHeatmap))
  ```

---

## 待实施工作

### 3. 数据库索引优化（高优先级 🔴）
**目标**: 优化热力图查询性能

**需要创建的索引**:
```sql
-- 核心索引：支持时间范围 + 凭据 + 模型筛选
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_request_logs_heatmap 
ON request_logs (
  credential_id, 
  ts DESC, 
  LOWER(COALESCE(outbound_model, client_model)),
  success,
  is_self_test
)
WHERE ts >= NOW() - INTERVAL '30 days';

-- 覆盖索引：包含常用聚合字段
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_request_logs_heatmap_covering
ON request_logs (
  credential_id,
  ts DESC,
  LOWER(COALESCE(outbound_model, client_model)),
  success,
  latency_ms,
  error_kind,
  request_id
)
WHERE ts >= NOW() - INTERVAL '30 days' 
  AND COALESCE(is_self_test, FALSE) = FALSE;
```

**验证命令**:
```sql
EXPLAIN ANALYZE
SELECT
  credential_id,
  lower(COALESCE(outbound_model, client_model)) AS raw_model_name,
  date_trunc('minute', ts) AS time_bucket,
  COUNT(*) AS total_requests,
  COUNT(*) FILTER (WHERE success) AS success_count
FROM request_logs_with_current_month
WHERE ts >= NOW() - INTERVAL '1 day'
  AND ts < NOW()
  AND COALESCE(is_self_test, FALSE) = FALSE
GROUP BY credential_id, raw_model_name, time_bucket
ORDER BY credential_id, raw_model_name, time_bucket;
```

预期：Index Scan，执行时间 < 1000ms

---

### 4. 前端实现（高优先级 🔴）

#### 4.1 Tab 结构重构
**文件**: `web/src/views/CredentialMonitorView.vue`

**改动**:
```vue
<template>
  <div class="page-container">
    <!-- Tab 切换 -->
    <SegTabs v-model="activeTab" :tabs="tabs" />
    
    <!-- Tab 内容 -->
    <CredentialListView v-if="activeTab === 'list'" />
    <CredentialHeatmapView v-else-if="activeTab === 'heatmap'" />
    <RoutingLogView v-else-if="activeTab === 'routing-log'" />
  </div>
</template>

<script setup lang="ts">
const activeTab = ref<'list' | 'heatmap' | 'routing-log'>('list')
const tabs = [
  { value: 'list', label: '列表视图' },
  { value: 'heatmap', label: '热力图' },
  { value: 'routing-log', label: '路由记录' },
]
</script>
```

#### 4.2 热力图组件
**新建文件**:
- `web/src/views/CredentialHeatmapView.vue` (主容器)
- `web/src/components/heatmap/HeatmapToolbar.vue` (筛选栏)
- `web/src/components/heatmap/HeatmapGrid.vue` (热力图网格)
- `web/src/components/heatmap/HeatmapRow.vue` (单个凭据行)
- `web/src/components/heatmap/HeatmapCell.vue` (单个色块)
- `web/src/components/heatmap/HeatmapTimeline.vue` (时间轴)
- `web/src/components/heatmap/HeatmapDetailPopover.vue` (详情弹窗)

**核心逻辑**:
```typescript
// API 请求
interface HeatmapDataRequest {
  time_start: string        // RFC3339
  time_end: string          // RFC3339
  granularity: '1m' | '5m' | '15m' | '1h' | '1d'
  credential_ids?: number[]
  models?: string[]
  exclude_self_test: boolean
}

// 状态色块映射
const statusColors: Record<string, string> = {
  ready: '#10b981',       // 绿色
  degraded: '#f59e0b',    // 黄色
  cooling: '#f97316',     // 橙色
  rate_limited: '#f59e0b',
  unreachable: '#ef4444', // 红色
  auth_failed: '#dc2626',
  manual_disabled: '#6b7280', // 灰色
  no_data: '#e5e7eb',     // 浅灰
}

// 色块点击处理
function onCellClick(bucket: HeatmapBucket, credential: Credential, model: string) {
  showDetailPopover({
    bucket,
    credential,
    model,
    onCorrectStatus: (action) => {
      // 调用现有 API: toggleModelAvailability, demoteCredential, etc.
    }
  })
}
```

#### 4.3 API 集成
**文件**: `web/src/api/credential-monitor.ts`

**新增接口**:
```typescript
export interface HeatmapBucket {
  time_bucket: string
  status: string
  total_requests: number
  success_count: number
  failed_count: number
  success_rate: number
  avg_latency_ms: number | null
  p95_latency_ms: number | null
  error_distribution: Record<string, number>
  sample_request_ids: string[]
}

export interface HeatmapModel {
  raw_model_name: string
  buckets: HeatmapBucket[]
}

export interface HeatmapCredential {
  credential_id: number
  label: string
  provider_name: string
  models: HeatmapModel[]
}

export interface HeatmapResponse {
  meta: {
    time_start: string
    time_end: string
    granularity: string
    bucket_count: number
    cache_hit: boolean
    generated_at: string
    expires_at: string
    duration_ms: number
  }
  credentials: HeatmapCredential[]
}

export function getCredentialHeatmap(
  timeStart: string,
  timeEnd: string,
  granularity: '1m' | '5m' | '15m' | '1h' | '1d',
  options?: {
    credentialIds?: number[]
    models?: string[]
    excludeSelfTest?: boolean
  }
): Promise<HeatmapResponse> {
  const params = new URLSearchParams()
  params.set('time_start', timeStart)
  params.set('time_end', timeEnd)
  params.set('granularity', granularity)
  if (options?.excludeSelfTest !== undefined) {
    params.set('exclude_self_test', String(options.excludeSelfTest))
  }
  if (options?.credentialIds?.length) {
    params.set('credential_ids', options.credentialIds.join(','))
  }
  if (options?.models?.length) {
    params.set('models', options.models.join(','))
  }
  return req<HeatmapResponse>('GET', `/api/credentials/heatmap?${params}`)
}
```

#### 4.4 路由记录 Tab
**文件**: `web/src/views/RoutingLogView.vue`

**功能**:
- 显示 `routing_decision_log` + `model_probe_runs` + `routing_audit_log` 的合并视图
- 支持筛选：凭据、模型、事件类型、时间范围
- 表格字段：时间、凭据、模型、事件类型、状态变化、原因、操作人、请求 ID
- 点击请求 ID 跳转到 `/request-detail/:requestId`

**数据源**:
- 复用现有 API: `getModelHistory`, `getCredentialDecisions`
- 新增 API (可选): `GET /api/credentials/routing-log`（合并查询）

---

### 5. 性能优化（中优先级 🟡）

#### 5.1 虚拟滚动
**库**: `vue-virtual-scroller` 或自实现

**应用场景**:
- 凭据列表（100+ 凭据）
- 模型展开行（每个凭据 10+ 模型）

#### 5.2 Canvas 渲染（可选）
**应用场景**: 7 天 × 1 分钟粒度 = 10080 个色块

**伪代码**:
```typescript
const canvas = document.getElementById('heatmap-canvas') as HTMLCanvasElement
const ctx = canvas.getContext('2d')!
buckets.forEach((bucket, index) => {
  ctx.fillStyle = statusColors[bucket.status]
  ctx.fillRect(index * cellWidth, rowY, cellWidth, rowHeight)
})
```

#### 5.3 增量更新
```typescript
// 自动刷新时，仅加载最新时间桶
const lastBucketTime = heatmapData.meta.time_end
const now = new Date().toISOString()
const newData = await getCredentialHeatmap(lastBucketTime, now, granularity)
// 合并到现有数据
```

#### 5.4 Redis 缓存（后端）
```go
// Key 格式: heatmap:{time_start}:{time_end}:{granularity}:{hash(filters)}
// TTL: 1 分钟
cacheKey := fmt.Sprintf("heatmap:%s:%s:%s:%s", 
  timeStart.Format(time.RFC3339), 
  timeEnd.Format(time.RFC3339), 
  granularity, 
  hashFilters(credentialIDs, models))

if cached, ok := redisClient.Get(ctx, cacheKey).Result(); ok {
  var resp HeatmapResponse
  json.Unmarshal([]byte(cached), &resp)
  resp.Meta.CacheHit = true
  return resp
}
```

---

### 6. 测试验证（中优先级 🟡）

#### 6.1 单元测试
**文件**: `admin/credential_monitor_heatmap_test.go`

**测试用例**:
```go
func TestValidateGranularity(t *testing.T) {
  tests := []struct {
    input    string
    expected string
    wantErr  bool
  }{
    {"1m", "minute", false},
    {"5m", "5 minutes", false},
    {"1h", "hour", false},
    {"invalid", "", true},
  }
  for _, tt := range tests {
    got, err := validateGranularity(tt.input)
    if (err != nil) != tt.wantErr {
      t.Errorf("validateGranularity(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
    }
    if got != tt.expected {
      t.Errorf("validateGranularity(%q) = %q, want %q", tt.input, got, tt.expected)
    }
  }
}

func TestDeriveStatusFromRate(t *testing.T) {
  tests := []struct {
    successRate   float64
    totalRequests int
    expected      string
  }{
    {0.95, 100, "ready"},
    {0.75, 100, "degraded"},
    {0.30, 100, "unreachable"},
    {0.00, 0, "no_data"},
  }
  for _, tt := range tests {
    got := deriveStatusFromRate(tt.successRate, tt.totalRequests)
    if got != tt.expected {
      t.Errorf("deriveStatusFromRate(%f, %d) = %q, want %q", tt.successRate, tt.totalRequests, got, tt.expected)
    }
  }
}
```

#### 6.2 集成测试
**工具**: Postman / curl

**测试场景**:
```bash
# 场景 1: 基础查询（今天，1 分钟粒度）
curl "http://localhost:8782/api/credentials/heatmap?\
time_start=$(date -u -v-1H +%Y-%m-%dT%H:%M:%SZ)&\
time_end=$(date -u +%Y-%m-%dT%H:%M:%SZ)&\
granularity=1m&\
exclude_self_test=true"

# 场景 2: 筛选特定凭据
curl "http://localhost:8782/api/credentials/heatmap?\
time_start=2026-09-06T00:00:00Z&\
time_end=2026-09-06T23:59:59Z&\
granularity=5m&\
credential_ids=123,456"

# 场景 3: 筛选特定模型
curl "http://localhost:8782/api/credentials/heatmap?\
time_start=2026-09-06T00:00:00Z&\
time_end=2026-09-06T23:59:59Z&\
granularity=15m&\
models=gpt-4,claude-3"

# 场景 4: 包含自检数据
curl "http://localhost:8782/api/credentials/heatmap?\
time_start=2026-09-06T00:00:00Z&\
time_end=2026-09-06T23:59:59Z&\
granularity=1h&\
exclude_self_test=false"
```

**预期结果**:
- HTTP 200
- 响应时间 < 2000ms (1000+ 时间桶)
- 数据完整性：所有字段非空，时间桶连续
- 状态正确：success_rate 与 status 对应

#### 6.3 性能测试
**工具**: Apache Bench / k6

**测试命令**:
```bash
ab -n 100 -c 10 "http://localhost:8782/api/credentials/heatmap?\
time_start=2026-09-06T00:00:00Z&\
time_end=2026-09-06T23:59:59Z&\
granularity=1m"
```

**性能目标**:
- P50 延迟 < 500ms
- P95 延迟 < 2000ms
- P99 延迟 < 5000ms
- 吞吐量 > 10 req/s

#### 6.4 用户验收测试（UAT）
**测试清单**:
- [ ] 能一眼识别凭据的健康趋势（绿色连续 = 稳定）
- [ ] 能快速定位故障时间点（红色色块集中区域）
- [ ] 能对比多个凭据的稳定性（垂直扫视）
- [ ] 能点击色块查看详细错误信息
- [ ] 能直接修正错误状态（下线故障节点）
- [ ] 能过滤自检数据，仅关注真实流量
- [ ] 能展开/收起模型行，状态持久化
- [ ] 自动刷新不阻塞 UI，保持滚动位置

---

## 部署清单

### 前置条件
- [x] 后端 API 已实现
- [ ] 数据库索引已创建
- [ ] 前端组件已开发
- [ ] 单元测试通过（覆盖率 ≥ 80%）
- [ ] 集成测试通过（≥ 10 个用例）
- [ ] 性能测试达标

### 部署步骤
1. **数据库迁移**:
   ```bash
   psql -h <host> -U <user> -d llm_gateway < migrations/034_add_heatmap_index.sql
   ```

2. **后端部署**:
   ```bash
   # 构建
   make build
   
   # 部署到测试环境
   ./scripts/deploy-154.sh
   
   # 验证 API
   curl http://<test-host>/api/credentials/heatmap?time_start=...
   ```

3. **前端部署**:
   ```bash
   cd web
   pnpm build
   
   # 部署静态资源
   rsync -avz dist/ <prod-host>:/var/www/llm-gateway/
   ```

4. **灰度发布**:
   - 先在测试环境验证 1 天
   - 内部用户灰度 3 天
   - 全量发布

### 回滚计划
- 前端：回滚静态资源到上一版本
- 后端：注释掉路由注册，重启服务
- 数据库：DROP INDEX（不影响数据）

---

## 风险与缓解

| 风险                     | 影响   | 缓解措施                                     | 状态 |
|--------------------------|--------|---------------------------------------------|------|
| 数据库查询慢（大时间范围）| 高     | 添加索引 + 分页加载 + Redis 缓存            | ⚠️   |
| 前端渲染卡顿（大量色块）  | 中     | 虚拟滚动 + Canvas 渲染                      | 📝   |
| 状态映射逻辑不准确        | 高     | 与现有 `credentialDisplayState` 对齐 + 测试 | ✅   |
| 自检数据污染真实流量      | 中     | 默认排除 `is_self_test = true`               | ✅   |
| 时间粒度选择困难          | 低     | 根据时间范围自动推荐粒度 + 提示文案          | 📝   |

图例：
- ✅ 已解决
- ⚠️ 需关注
- 📝 计划中

---

## 后续优化方向

### 高级筛选（P1）
- 按错误类型筛选（仅显示 `rate_limit_exceeded`）
- 按成功率阈值筛选（仅显示成功率 < 80%）

### 对比模式（P2）
- 同时显示多个凭据的热力图（纵向堆叠）
- 支持拖拽调整顺序

### 异常检测（P2）
- 自动标记异常时间段（连续 5 个时间桶失败率 > 50%）
- 发送告警通知（Email / 钉钉 / Slack）

### 导出功能（P3）
- 导出热力图为 PNG 图片（html2canvas）
- 导出原始数据为 CSV

### AI 分析（P3）
- 调用 LLM 分析热力图，生成故障诊断报告
- 例如："凭据 #123 在 10:30-11:00 期间出现间歇性 `rate_limit_exceeded` 错误，建议调整并发限制或联系供应商"

---

## 参考资料

### 相关文档
- 需求规格: `docs/credential-monitor-heatmap-requirements.md`
- 现有凭据监控: `web/src/views/CredentialMonitorView.vue`
- API 文档: `web/src/api/credential-monitor.ts`
- 状态定义: `web/src/utils/credentialStatus.ts`

### 设计参考
- GitHub Actions 日志时间轴
- Datadog APM 热力图
- Grafana Heatmap Panel

### 数据库 Schema
```sql
-- request_logs 表（简化）
CREATE TABLE request_logs (
  id BIGSERIAL PRIMARY KEY,
  request_id TEXT NOT NULL,
  ts TIMESTAMPTZ NOT NULL,
  credential_id INT NOT NULL,
  client_model TEXT,
  outbound_model TEXT,
  success BOOLEAN NOT NULL,
  latency_ms INT,
  error_kind TEXT,
  error_class TEXT,
  is_self_test BOOLEAN DEFAULT FALSE,
  tenant_id TEXT
);
```

---

## 联系方式

- **负责人**: ZCode Agent
- **技术支持**: llm-gateway-go 开发团队
- **问题反馈**: GitHub Issues / 内部工单系统

---

**最后更新**: 2026-09-06  
**文档版本**: v1.0  
**状态**: 后端已完成，前端待实施
