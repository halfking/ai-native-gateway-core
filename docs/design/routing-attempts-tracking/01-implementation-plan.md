# 路由尝试追踪 - 实施计划

## ✅ 阶段 1: 数据库迁移（已完成）

### 执行结果
```
Migration V350 completed successfully
- ALTER TABLE request_logs_hot ADD COLUMN routing_attempts jsonb
- ALTER TABLE request_logs_hot ADD COLUMN routing_summary text
- ALTER TABLE request_logs ADD COLUMN routing_attempts jsonb
- ALTER TABLE request_logs ADD COLUMN routing_summary text
- CREATE INDEX idx_request_logs_hot_routing_attempts
```

### 验证
```sql
SELECT column_name, data_type, is_nullable
FROM information_schema.columns
WHERE table_name='request_logs_hot'
  AND column_name IN ('routing_attempts', 'routing_summary');
```

结果：
- routing_attempts: jsonb, nullable
- routing_summary: text, nullable

---

## 🔄 阶段 2: 后端实现（进行中）

### 2.1 定义数据结构

**文件**: `domains/streaming/executors/types.go` (新建)

```go
package executors

import "sync"

// RoutingAttempt 记录单次 upstream 尝试
type RoutingAttempt struct {
	Seq           int    `json:"seq"`
	ProviderID    int64  `json:"provider_id"`
	ProviderName  string `json:"provider_name"`
	CredentialID  int64  `json:"credential_id"`
	RawModel      string `json:"raw_model"`
	UpstreamURL   string `json:"upstream_url"`
	Result        string `json:"result"` // success/canceled/timeout/model_not_found/error
	LatencyMs     int64  `json:"latency_ms"`
	HTTPStatus    int    `json:"http_status,omitempty"`
	ErrorMessage  string `json:"error_message,omitempty"`
}

// RoutingAttemptsTracker 累积所有路由尝试
type RoutingAttemptsTracker struct {
	Attempts []RoutingAttempt `json:"attempts"`
	mu       sync.Mutex
}

// Add 线程安全地添加尝试记录
func (t *RoutingAttemptsTracker) Add(attempt RoutingAttempt) {
	t.mu.Lock()
	defer t.mu.Unlock()
	attempt.Seq = len(t.Attempts) + 1
	t.Attempts = append(t.Attempts, attempt)
}

// ToJSON 序列化为 JSONB
func (t *RoutingAttemptsTracker) ToJSON() ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.Attempts) == 0 {
		return nil, nil
	}
	return json.Marshal(map[string]interface{}{
		"attempts": t.Attempts,
	})
}

// Summary 生成人类可读摘要
func (t *RoutingAttemptsTracker) Summary() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.Attempts) == 0 {
		return ""
	}

	parts := make([]string, len(t.Attempts))
	for i, a := range t.Attempts {
		latency := float64(a.LatencyMs) / 1000.0
		parts[i] = fmt.Sprintf("候选%d: %s(%d) %s %.1fs",
			a.Seq, a.ProviderName, a.ProviderID,
			translateResult(a.Result), latency)
	}
	return strings.Join(parts, " → ")
}

func translateResult(result string) string {
	switch result {
	case "success":
		return "成功"
	case "canceled":
		return "取消"
	case "timeout":
		return "超时"
	case "model_not_found":
		return "模型未找到"
	case "error":
		return "错误"
	default:
		return result
	}
}
```

### 2.2 在请求上下文中传递 Tracker

**文件**: `domains/streaming/handler.go`

在 `executeStreamingChatRequest` 函数开始处：

```go
// 创建路由尝试追踪器
routingTracker := &executors.RoutingAttemptsTracker{}

// 传递到执行层
execParams.RoutingTracker = routingTracker
```

### 2.3 在 executor_chat.go 中记录每次尝试

**文件**: `domains/streaming/executors/executor_chat.go`

在 `attemptLog` 函数附近（行 472-499）：

```go
// 记录到追踪器
if params.RoutingTracker != nil {
	attempt := RoutingAttempt{
		ProviderID:   cand.ProviderID,
		ProviderName: getProviderName(cand.ProviderID), // 需要从 provider cache 读取
		CredentialID: cand.CredentialID,
		RawModel:     cand.RawModel,
		UpstreamURL:  req.URL.String(),
		LatencyMs:    upstreamLatency.Milliseconds(),
	}

	// 根据结果设置 Result 和其他字段
	if uErr == nil {
		attempt.Result = "success"
		attempt.HTTPStatus = status
	} else if strings.Contains(uErr.Message, "context canceled") {
		attempt.Result = "canceled"
	} else if strings.Contains(uErr.Message, "timeout") {
		attempt.Result = "timeout"
	} else if status == 404 {
		attempt.Result = "model_not_found"
		attempt.HTTPStatus = status
	} else {
		attempt.Result = "error"
		attempt.HTTPStatus = status
		attempt.ErrorMessage = uErr.Message
	}

	params.RoutingTracker.Add(attempt)
}
```

### 2.4 在 telemetry 中写入数据库

**文件**: `domains/hooks/observability/telemetry/client.go`

在 `EmitRequestLogInsert` 函数中：

```go
// 序列化 routing_attempts
var routingAttemptsJSON []byte
var routingSummary string
if params.RoutingTracker != nil {
	routingAttemptsJSON, _ = params.RoutingTracker.ToJSON()
	routingSummary = params.RoutingTracker.Summary()
}

// 在 INSERT 语句中增加两个字段
_, err = tx.Exec(ctx, `
	INSERT INTO request_logs_hot (
		request_id, ts, tenant_id, ...,
		routing_attempts, routing_summary
	) VALUES (
		$1, $2, $3, ...,
		$84::jsonb, $85
	)
	...
`,
	...,
	string(routingAttemptsJSON),
	routingSummary,
)
```

---

## 📱 阶段 3: 前端展示（待实施）

### 3.1 API 返回字段

确保 `/api/admin/requests/:id` 接口返回：
- routing_attempts
- routing_summary

### 3.2 前端组件

**文件**: `pms-web/src/components/RoutingAttemptsTimeline.vue` (新建)

```vue
<template>
  <el-card v-if="hasAttempts" class="routing-attempts-card">
    <template #header>
      <div class="card-header">
        <span>路由决策追踪</span>
        <el-tag type="info" size="small">{{ attempts.length }} 次尝试</el-tag>
      </div>
    </template>

    <div v-if="summary" class="summary">
      <strong>摘要：</strong>{{ summary }}
    </div>

    <el-timeline class="attempts-timeline">
      <el-timeline-item
        v-for="(attempt, idx) in attempts"
        :key="idx"
        :type="getTimelineType(attempt.result)"
        :timestamp="`${formatLatency(attempt.latency_ms)}`"
        placement="top"
      >
        <el-card :class="getCardClass(attempt.result)">
          <div class="attempt-header">
            <strong>候选 {{ attempt.seq }}</strong>
            <el-tag :type="getResultTagType(attempt.result)" size="small">
              {{ translateResult(attempt.result) }}
            </el-tag>
          </div>

          <div class="attempt-detail">
            <div><strong>供应商：</strong>{{ attempt.provider_name }} (ID: {{ attempt.provider_id }})</div>
            <div><strong>模型：</strong>{{ attempt.raw_model }}</div>
            <div><strong>凭据：</strong>{{ attempt.credential_id }}</div>
            <div class="url"><strong>上游URL：</strong>{{ attempt.upstream_url }}</div>
            <div v-if="attempt.http_status">
              <strong>HTTP状态：</strong>{{ attempt.http_status }}
            </div>
            <div v-if="attempt.error_message" class="error-msg">
              <strong>错误：</strong>{{ attempt.error_message }}
            </div>
          </div>
        </el-card>
      </el-timeline-item>
    </el-timeline>
  </el-card>
</template>

<script setup>
import { computed } from 'vue'

const props = defineProps({
  routingAttempts: {
    type: Object,
    default: null
  },
  routingSummary: {
    type: String,
    default: ''
  }
})

const attempts = computed(() => {
  return props.routingAttempts?.attempts || []
})

const hasAttempts = computed(() => attempts.value.length > 0)
const summary = computed(() => props.routingSummary)

function getTimelineType(result) {
  switch (result) {
    case 'success': return 'success'
    case 'canceled': return 'warning'
    case 'timeout': return 'warning'
    default: return 'danger'
  }
}

function getResultTagType(result) {
  switch (result) {
    case 'success': return 'success'
    case 'canceled': return 'warning'
    case 'timeout': return 'warning'
    case 'model_not_found': return 'danger'
    default: return 'danger'
  }
}

function getCardClass(result) {
  return `attempt-card attempt-${result}`
}

function translateResult(result) {
  const map = {
    'success': '成功',
    'canceled': '取消',
    'timeout': '超时',
    'model_not_found': '模型未找到',
    'error': '错误'
  }
  return map[result] || result
}

function formatLatency(ms) {
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}
</script>

<style scoped>
.routing-attempts-card {
  margin-top: 16px;
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.summary {
  margin-bottom: 16px;
  padding: 8px;
  background: var(--el-fill-color-light);
  border-radius: 4px;
  font-size: 14px;
}

.attempts-timeline {
  margin-top: 16px;
}

.attempt-card {
  margin-bottom: 8px;
}

.attempt-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}

.attempt-detail {
  font-size: 13px;
  line-height: 1.8;
}

.attempt-detail .url {
  word-break: break-all;
  color: var(--el-text-color-secondary);
}

.error-msg {
  color: var(--el-color-danger);
  margin-top: 8px;
  padding: 4px;
  background: var(--el-color-danger-light-9);
  border-radius: 4px;
}

.attempt-success {
  border-left: 3px solid var(--el-color-success);
}

.attempt-canceled, .attempt-timeout {
  border-left: 3px solid var(--el-color-warning);
}

.attempt-error, .attempt-model_not_found {
  border-left: 3px solid var(--el-color-danger);
}
</style>
```

### 3.3 在请求详情页使用

**文件**: `pms-web/src/views/admin/RequestDetail.vue`

```vue
<template>
  <div class="request-detail">
    <!-- 现有的请求基本信息 -->
    <el-card>...</el-card>

    <!-- 新增：路由决策追踪 -->
    <RoutingAttemptsTimeline
      :routing-attempts="request.routing_attempts"
      :routing-summary="request.routing_summary"
    />

    <!-- 现有的其他卡片 -->
  </div>
</template>

<script setup>
import RoutingAttemptsTimeline from '@/components/RoutingAttemptsTimeline.vue'
// ...
</script>
```

---

## 📋 实施清单

### P0 - 数据库层
- [x] 编写迁移脚本 V350
- [x] 在 252 服务器执行迁移
- [x] 验证字段创建成功
- [ ] 在其他环境执行（154/245/本地）

### P1 - 后端层
- [ ] 创建 `domains/streaming/executors/types.go`
- [ ] 在 `handler.go` 中创建 tracker
- [ ] 在 `executor_chat.go` 中记录尝试
- [ ] 在 `telemetry/client.go` 中写入数据库
- [ ] 编写单元测试
- [ ] 本地验证

### P2 - 前端层
- [ ] 创建 `RoutingAttemptsTimeline.vue` 组件
- [ ] 在请求详情页集成
- [ ] 测试不同场景渲染
- [ ] UI/UX 调整

### P3 - 部署
- [ ] 合并代码到 dev 分支
- [ ] 部署到 kaixuan-1 测试
- [ ] 部署到 245 预生产
- [ ] 部署到 154 生产

---

## 🧪 测试计划

### 测试场景 1: 单次成功
- 发起请求，首个候选成功
- 预期: routing_attempts = null (优化存储)

### 测试场景 2: 多次尝试后成功
- 发起请求，前 2 个候选失败，第 3 个成功
- 预期: routing_attempts 包含 3 条记录

### 测试场景 3: 全部失败
- 发起请求到不存在的模型
- 预期: routing_attempts 包含所有尝试记录

### 测试场景 4: 超时取消
- 发起请求，客户端 120 秒后取消
- 预期: 最后一条记录 result="canceled"

---

## 📊 预期效果

### 用户体验改进
- ✅ 清晰看到"尝试了哪些供应商"
- ✅ 每个候选的失败原因一目了然
- ✅ 不再困惑"为什么泳道和URL不一致"

### 运维价值
- ✅ 快速定位问题供应商
- ✅ 分析路由策略有效性
- ✅ 优化候选优先级

---

下一步：开始实施 P1 后端层
