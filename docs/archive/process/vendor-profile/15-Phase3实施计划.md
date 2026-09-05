# Phase 3 实施计划 - API 实现

**开始时间**: 2026-07-19 03:30
**预计耗时**: 3 天
**状态**: 🚧 进行中

---

## 1. 目标

提供 HTTP API 供前端调用，展示供应商质量画像数据。

---

## 2. API 设计

### 2.1 API 列表

| # | Method | Path | 说明 | 权限 |
|---|--------|------|------|------|
| 1 | GET | `/api/providers/:id/quality` | 查询单个供应商质量画像 | 需登录 |
| 2 | GET | `/api/providers/quality/ranking` | 质量排行榜（Top 20） | 需登录 |
| 3 | POST | `/api/providers/:id/quality/recalculate` | 手动重算质量画像 | 管理员 |
| 4 | GET | `/api/providers/:id/quality/history` | 质量历史趋势（可选） | 需登录 |

### 2.2 API 详细设计

#### API 1: 查询单个供应商质量画像

**请求**:
```
GET /api/providers/:id/quality?model_name=claude-3-opus
```

**Query 参数**:
- `model_name` (可选): 模型名称，不传则返回该供应商所有模型

**响应**:
```json
{
  "code": 0,
  "message": "success",
  "data": {
    "provider_id": 1,
    "provider_name": "Anthropic",
    "models": [
      {
        "model_name": "claude-3-opus",
        "quality_score": 95.5,
        "quality_grade": "S",
        "scores": {
          "availability": 98.0,
          "performance": 92.0,
          "reliability": 96.0,
          "stability": 94.0,
          "cost_efficiency": 85.0
        },
        "metrics_24h": {
          "success_rate": 99.5,
          "error_rate_5xx": 0.3,
          "latency_p95": 450,
          "ttft_p95": 180,
          "cost_per_1k_tokens": 0.015
        },
        "calculated_at": "2026-07-19T03:00:00Z",
        "last_request_at": "2026-07-19T03:25:00Z"
      }
    ]
  }
}
```

#### API 2: 质量排行榜

**请求**:
```
GET /api/providers/quality/ranking?model_name=claude-3-opus&limit=20&order_by=quality_score
```

**Query 参数**:
- `model_name` (可选): 过滤模型名称
- `limit` (可选): 返回数量，默认 20
- `order_by` (可选): 排序字段（quality_score / availability_score / performance_score），默认 quality_score
- `min_score` (可选): 最低分数过滤，默认 0

**响应**:
```json
{
  "code": 0,
  "message": "success",
  "data": {
    "total": 50,
    "ranking": [
      {
        "rank": 1,
        "provider_id": 1,
        "provider_name": "Anthropic",
        "model_name": "claude-3-opus",
        "quality_score": 95.5,
        "quality_grade": "S",
        "availability_score": 98.0,
        "performance_score": 92.0,
        "calculated_at": "2026-07-19T03:00:00Z"
      },
      {
        "rank": 2,
        "provider_id": 2,
        "provider_name": "OpenAI",
        "model_name": "gpt-4",
        "quality_score": 93.2,
        "quality_grade": "S",
        "availability_score": 95.0,
        "performance_score": 90.0,
        "calculated_at": "2026-07-19T03:00:00Z"
      }
    ]
  }
}
```

#### API 3: 手动重算质量画像

**请求**:
```
POST /api/providers/:id/quality/recalculate
Content-Type: application/json

{
  "model_name": "claude-3-opus"
}
```

**响应**:
```json
{
  "code": 0,
  "message": "质量画像计算成功",
  "data": {
    "provider_id": 1,
    "model_name": "claude-3-opus",
    "quality_score": 95.5,
    "quality_grade": "S",
    "calculated_at": "2026-07-19T03:30:00Z"
  }
}
```

#### API 4: 质量历史趋势（可选）

**请求**:
```
GET /api/providers/:id/quality/history?model_name=claude-3-opus&duration=7d
```

**Query 参数**:
- `model_name` (必填): 模型名称
- `duration` (可选): 时间范围（1d/7d/30d），默认 7d

**响应**:
```json
{
  "code": 0,
  "message": "success",
  "data": {
    "provider_id": 1,
    "model_name": "claude-3-opus",
    "history": [
      {
        "date": "2026-07-13",
        "quality_score": 94.0,
        "quality_grade": "S",
        "availability_score": 97.0,
        "performance_score": 91.0
      },
      {
        "date": "2026-07-14",
        "quality_score": 95.0,
        "quality_grade": "S",
        "availability_score": 98.0,
        "performance_score": 92.0
      }
    ]
  }
}
```

---

## 3. 实施步骤

### Step 1: 创建 API handler 文件 ✅

```
internal/handlers/
└── quality_handler.go    # 质量画像 API handler
```

### Step 2: 实现 4 个 API handler

- `GetProviderQuality` - API 1
- `GetQualityRanking` - API 2
- `RecalculateQuality` - API 3
- `GetQualityHistory` - API 4（可选）

### Step 3: 添加路由

在 `cmd/gateway/main.go` 或独立路由文件中添加：

```go
// 质量画像 API
router.GET("/api/providers/:id/quality", handlers.GetProviderQuality)
router.GET("/api/providers/quality/ranking", handlers.GetQualityRanking)
router.POST("/api/providers/:id/quality/recalculate", middleware.AdminOnly, handlers.RecalculateQuality)
router.GET("/api/providers/:id/quality/history", handlers.GetQualityHistory)
```

### Step 4: 单元测试

- 测试每个 API 的正常流程
- 测试边界条件（不存在的 provider_id、无数据等）
- 测试权限控制

### Step 5: 集成测试

- 启动本地环境
- 使用 curl / Postman 测试
- 验证响应格式正确

### Step 6: 前端集成（可选，Phase 3 范围外）

- 供应商详情页展示质量分
- 质量排行榜页面
- 雷达图展示五个维度

---

## 4. 代码结构

```
internal/handlers/
└── quality_handler.go         # API handler（新增）

internal/quality/
├── [Phase 1 & 2 文件...]
└── api_service.go             # API 业务逻辑（可选，封装复杂查询）

cmd/gateway/
└── main.go                    # 添加路由
```

---

## 5. 数据库查询优化

### 5.1 常用查询

**查询单个供应商**:
```sql
SELECT * FROM provider_quality_profiles
WHERE provider_id = $1 AND model_name = $2;
```

**查询供应商所有模型**:
```sql
SELECT * FROM provider_quality_profiles
WHERE provider_id = $1
ORDER BY quality_score DESC;
```

**质量排行榜**:
```sql
SELECT
    p.provider_id,
    p.model_name,
    p.quality_score,
    p.quality_grade,
    p.availability_score,
    p.performance_score,
    p.calculated_at,
    pr.name as provider_name
FROM provider_quality_profiles p
LEFT JOIN providers pr ON p.provider_id = pr.id
WHERE ($1::text IS NULL OR p.model_name = $1)
  AND p.quality_score >= $2
ORDER BY p.quality_score DESC
LIMIT $3;
```

### 5.2 索引优化

已有索引（Phase 0 创建）：
```sql
CREATE INDEX idx_quality_profiles_score ON provider_quality_profiles(quality_score DESC);
CREATE INDEX idx_quality_profiles_provider_model ON provider_quality_profiles(provider_id, model_name);
```

**查询性能**：
- 单个供应商查询：< 5ms
- 排行榜查询：< 20ms

---

## 6. 错误处理

| 错误场景 | HTTP 状态码 | 错误码 | 错误信息 |
|---------|-----------|--------|---------|
| provider_id 不存在 | 404 | 40401 | "供应商不存在" |
| 无质量数据 | 404 | 40402 | "暂无质量数据" |
| 参数错误 | 400 | 40001 | "参数错误: {detail}" |
| 权限不足 | 403 | 40301 | "权限不足，需要管理员权限" |
| 服务器错误 | 500 | 50001 | "服务器内部错误" |

---

## 7. 响应格式统一

所有 API 使用统一响应格式：

```go
type Response struct {
    Code    int         `json:"code"`     // 0=成功，非0=错误
    Message string      `json:"message"`  // 错误信息或成功提示
    Data    interface{} `json:"data"`     // 业务数据
}
```

**成功响应**:
```json
{
  "code": 0,
  "message": "success",
  "data": { ... }
}
```

**错误响应**:
```json
{
  "code": 40401,
  "message": "供应商不存在",
  "data": null
}
```

---

## 8. 权限控制

### 8.1 权限级别

| API | 权限要求 |
|-----|---------|
| 查询质量画像 | 需登录（任何用户） |
| 查询排行榜 | 需登录（任何用户） |
| 手动重算 | 管理员（role=admin） |
| 查询历史 | 需登录（任何用户） |

### 8.2 中间件

```go
// 已有中间件（复用）
middleware.Auth()        // 验证登录
middleware.AdminOnly()   // 验证管理员权限
```

---

## 9. 缓存策略（可选）

### 9.1 缓存场景

| 数据 | 缓存时间 | 缓存键 |
|------|---------|--------|
| 单个供应商质量 | 5 分钟 | `quality:provider:{id}:{model}` |
| 排行榜 | 10 分钟 | `quality:ranking:{model}:{limit}` |

### 9.2 缓存实现

```go
// 伪代码
func GetProviderQuality(c *gin.Context) {
    cacheKey := fmt.Sprintf("quality:provider:%d:%s", providerID, modelName)

    // 尝试从 Redis 获取
    if cached, err := redis.Get(cacheKey); err == nil {
        c.JSON(200, cached)
        return
    }

    // 从数据库查询
    data := queryFromDB(...)

    // 写入缓存
    redis.Set(cacheKey, data, 5*time.Minute)

    c.JSON(200, data)
}
```

**注意**：Phase 3 可以不实现缓存，作为性能优化预留。

---

## 10. 测试用例

### 10.1 单元测试

```go
func TestGetProviderQuality(t *testing.T) {
    tests := []struct {
        name       string
        providerID int64
        modelName  string
        wantCode   int
        wantErr    string
    }{
        {
            name:       "正常查询",
            providerID: 1,
            modelName:  "claude-3-opus",
            wantCode:   0,
            wantErr:    "",
        },
        {
            name:       "供应商不存在",
            providerID: 999,
            modelName:  "claude-3-opus",
            wantCode:   40401,
            wantErr:    "供应商不存在",
        },
        {
            name:       "无质量数据",
            providerID: 1,
            modelName:  "non-existent-model",
            wantCode:   40402,
            wantErr:    "暂无质量数据",
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // 测试逻辑
        })
    }
}
```

### 10.2 集成测试

```bash
# API 1: 查询单个供应商
curl -X GET "http://localhost:8080/api/providers/1/quality?model_name=claude-3-opus" \
  -H "Authorization: Bearer $TOKEN"

# API 2: 排行榜
curl -X GET "http://localhost:8080/api/providers/quality/ranking?limit=10" \
  -H "Authorization: Bearer $TOKEN"

# API 3: 手动重算
curl -X POST "http://localhost:8080/api/providers/1/quality/recalculate" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"model_name": "claude-3-opus"}'

# API 4: 质量历史
curl -X GET "http://localhost:8080/api/providers/1/quality/history?model_name=claude-3-opus&duration=7d" \
  -H "Authorization: Bearer $TOKEN"
```

---

## 11. 前端集成示例（参考）

### 11.1 供应商详情页

```vue
<template>
  <div class="provider-quality">
    <h3>质量画像</h3>

    <!-- 综合评分 -->
    <div class="score-card">
      <div class="score">{{ quality.quality_score }}</div>
      <div class="grade">{{ quality.quality_grade }}</div>
    </div>

    <!-- 五个维度 -->
    <div class="dimensions">
      <div class="dimension">
        <span>可用性</span>
        <el-progress :percentage="quality.scores.availability" />
      </div>
      <div class="dimension">
        <span>性能</span>
        <el-progress :percentage="quality.scores.performance" />
      </div>
      <!-- ... -->
    </div>

    <!-- 雷达图 -->
    <div ref="radarChart" style="width: 400px; height: 400px;"></div>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import * as echarts from 'echarts'

const quality = ref({})

onMounted(async () => {
  // 获取质量数据
  const res = await fetch(`/api/providers/${providerId}/quality?model_name=${modelName}`)
  quality.value = await res.json()

  // 绘制雷达图
  drawRadarChart()
})

function drawRadarChart() {
  const chart = echarts.init(this.$refs.radarChart)
  chart.setOption({
    radar: {
      indicator: [
        { name: '可用性', max: 100 },
        { name: '性能', max: 100 },
        { name: '可信度', max: 100 },
        { name: '稳定性', max: 100 },
        { name: '成本效益', max: 100 },
      ]
    },
    series: [{
      type: 'radar',
      data: [{
        value: [
          quality.value.scores.availability,
          quality.value.scores.performance,
          quality.value.scores.reliability,
          quality.value.scores.stability,
          quality.value.scores.cost_efficiency,
        ]
      }]
    }]
  })
}
</script>
```

### 11.2 质量排行榜页面

```vue
<template>
  <div class="quality-ranking">
    <h2>供应商质量排行榜</h2>

    <el-table :data="ranking" stripe>
      <el-table-column label="排名" prop="rank" width="80" />
      <el-table-column label="供应商" prop="provider_name" />
      <el-table-column label="模型" prop="model_name" />
      <el-table-column label="质量分" prop="quality_score" width="100">
        <template #default="{ row }">
          <el-tag :type="getGradeType(row.quality_grade)">
            {{ row.quality_score }} ({{ row.quality_grade }})
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="可用性" prop="availability_score" width="100" />
      <el-table-column label="性能" prop="performance_score" width="100" />
      <el-table-column label="更新时间" prop="calculated_at" width="180" />
    </el-table>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'

const ranking = ref([])

onMounted(async () => {
  const res = await fetch('/api/providers/quality/ranking?limit=20')
  const data = await res.json()
  ranking.value = data.data.ranking
})

function getGradeType(grade) {
  const types = { S: 'success', A: 'success', B: 'warning', C: 'info', D: 'danger' }
  return types[grade] || 'info'
}
</script>
```

---

## 12. 验收标准

Phase 3 完成的验收标准：

- [ ] API 1: 查询单个供应商质量画像实现并测试通过
- [ ] API 2: 质量排行榜实现并测试通过
- [ ] API 3: 手动重算实现并测试通过
- [ ] API 4: 质量历史实现（可选）
- [ ] 单元测试覆盖率 > 80%
- [ ] 集成测试通过（curl 验证）
- [ ] 本地环境验证：前端可以调用 API 并展示数据
- [ ] 252 服务器验证：部署后 API 正常工作

---

## 13. 时间估算

| 任务 | 预计耗时 |
|------|---------|
| 创建 handler 文件和结构定义 | 30 分钟 |
| 实现 API 1（查询单个） | 1 小时 |
| 实现 API 2（排行榜） | 1.5 小时 |
| 实现 API 3（手动重算） | 1 小时 |
| 实现 API 4（历史，可选） | 1 小时 |
| 添加路由 | 30 分钟 |
| 单元测试 | 2 小时 |
| 集成测试 | 1 小时 |
| 文档编写 | 1 小时 |

**总计**: 约 9.5 小时（1.2 天）

---

**下一步**: 开始实现 API handler
