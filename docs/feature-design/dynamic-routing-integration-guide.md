# 动态路由可视化功能集成指南

## 概述

本文档提供了将已实现的路由追踪后端基础设施集成到 llm-gateway-go 请求处理流程的详细指南。

**前置条件**: 后端基础设施已完成（`admin/route_trace.go`, `admin/route_trace_publish.go`）

---

## 第一部分：后端事件采集集成

### 1.1 在请求处理入口创建路由追踪

**文件**: `domains/streaming/handler.go`

在 `ChatHandler.ServeHTTP()` 方法中，请求开始时创建路由追踪：

```go
// 在 ServeHTTP 开始处添加
func (h *ChatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // ... 现有的请求 ID 生成逻辑 ...
    requestID := extractOrGenerateRequestID(r)
    
    // 创建路由追踪（如果 LiveStreamHub 可用）
    var routeTrace *admin.RouteTrace
    if h.liveStreamHub != nil {
        tenantID := extractTenantID(r) // 从上下文或认证信息提取
        clientModel := extractModelFromRequest(r) // 从请求体提取
        routeTrace = h.liveStreamHub.StartRouteTrace(requestID, tenantID, clientModel)
    }
    
    // 将 routeTrace 存入请求上下文
    if routeTrace != nil {
        ctx := context.WithValue(r.Context(), routeTraceKey, routeTrace)
        r = r.WithContext(ctx)
    }
    
    // ... 继续现有逻辑 ...
}
```

### 1.2 在模型解析阶段记录

**文件**: `autoroute/` 或模型解析相关文件

在模型解析完成后记录：

```go
func resolveModel(ctx context.Context, clientModel string) (canonical string, id int, err error) {
    // ... 现有的模型解析逻辑 ...
    
    // 记录模型解析结果
    if trace := routeTraceFromContext(ctx); trace != nil {
        trace.SetModelResolution(canonical, id)
        // 推送更新
        if hub := liveStreamHubFromContext(ctx); hub != nil {
            hub.PublishRouteTraceUpdate(trace)
        }
    }
    
    return canonical, id, nil
}
```

### 1.3 在凭据选择阶段记录

**文件**: `autoroute/selector.go` 或类似文件

在选择凭据后记录：

```go
func (s *Selector) SelectCredential(ctx context.Context, model string) (*Credential, error) {
    // 更新路由阶段
    if trace := routeTraceFromContext(ctx); trace != nil {
        trace.UpdatePhase(admin.RoutePhaseSelecting)
        publishRouteTraceUpdate(ctx, trace)
    }
    
    // ... 现有的凭据选择逻辑 ...
    cred, err := s.selectBest(model)
    
    // 记录选择结果
    if trace := routeTraceFromContext(ctx); trace != nil && cred != nil {
        trace.SetRoutingTarget(cred.Provider, cred.ID, cred.Label)
        publishRouteTraceUpdate(ctx, trace)
    }
    
    return cred, err
}
```

### 1.4 在上游请求阶段记录

**文件**: `domains/streaming/handler.go` 或 HTTP 客户端包装器

在向上游发送请求前：

```go
func (h *ChatHandler) sendUpstreamRequest(ctx context.Context, cred *Credential, req *Request) (*Response, error) {
    // 开始新尝试
    var attemptSeq int
    if trace := routeTraceFromContext(ctx); trace != nil {
        isFallback := trace.CurrentAttempt > 0 // 第一次尝试不是 fallback
        attemptSeq = trace.StartAttempt(
            cred.ID,
            cred.Label,
            cred.Provider,
            req.Model,
            isFallback,
        )
        publishRouteTraceUpdate(ctx, trace)
    }
    
    startTime := time.Now()
    
    // 发送请求
    resp, err := h.upstreamClient.Do(req)
    
    latencyMs := time.Since(startTime).Milliseconds()
    
    // 记录尝试结果
    if trace := routeTraceFromContext(ctx); trace != nil {
        if err != nil {
            trace.CompleteAttempt(0, false, latencyMs, classifyError(err), sanitizeError(err))
        } else {
            trace.CompleteAttempt(resp.StatusCode, resp.StatusCode < 400, latencyMs, "", "")
        }
        publishRouteTraceUpdate(ctx, trace)
    }
    
    return resp, err
}
```

### 1.5 记录首 Token 时间（TTFT）

在流式响应开始时：

```go
func (h *ChatHandler) streamResponse(ctx context.Context, resp *Response) error {
    firstToken := true
    startTime := time.Now()
    
    scanner := bufio.NewScanner(resp.Body)
    for scanner.Scan() {
        if firstToken {
            ttftMs := time.Since(startTime).Milliseconds()
            if trace := routeTraceFromContext(ctx); trace != nil {
                trace.RecordTTFT(ttftMs)
                publishRouteTraceUpdate(ctx, trace)
            }
            firstToken = false
        }
        
        // ... 处理流式数据 ...
    }
    
    return scanner.Err()
}
```

### 1.6 记录 Token 使用量

在响应完成后：

```go
func (h *ChatHandler) finalizeRequest(ctx context.Context, usage *TokenUsage) {
    if trace := routeTraceFromContext(ctx); trace != nil {
        trace.SetTokenUsage(usage.PromptTokens, usage.CompletionTokens)
        publishRouteTraceUpdate(ctx, trace)
    }
}
```

### 1.7 标记请求完成或失败

在请求最终完成时：

```go
func (h *ChatHandler) completeRequest(ctx context.Context, finalStatus int, totalLatency time.Duration) {
    requestID := requestIDFromContext(ctx)
    latencyMs := totalLatency.Milliseconds()
    
    if hub := liveStreamHubFromContext(ctx); hub != nil {
        if finalStatus >= 200 && finalStatus < 400 {
            hub.CompleteRouteTrace(requestID, finalStatus, latencyMs)
        } else {
            errorKind := classifyStatusCode(finalStatus)
            errorMsg := sanitizeErrorMessage(ctx)
            hub.FailRouteTrace(requestID, finalStatus, latencyMs, errorKind, errorMsg)
        }
    }
}
```

### 1.8 辅助函数

添加上下文提取辅助函数：

```go
// context_helpers.go

type contextKey string

const (
    routeTraceKey     contextKey = "route_trace"
    liveStreamHubKey  contextKey = "live_stream_hub"
)

func routeTraceFromContext(ctx context.Context) *admin.RouteTrace {
    if trace, ok := ctx.Value(routeTraceKey).(*admin.RouteTrace); ok {
        return trace
    }
    return nil
}

func liveStreamHubFromContext(ctx context.Context) *admin.LiveStreamSSEHub {
    if hub, ok := ctx.Value(liveStreamHubKey).(*admin.LiveStreamSSEHub); ok {
        return hub
    }
    return nil
}

func publishRouteTraceUpdate(ctx context.Context, trace *admin.RouteTrace) {
    if hub := liveStreamHubFromContext(ctx); hub != nil {
        hub.PublishRouteTraceUpdate(trace)
    }
}
```

---

## 第二部分：前端可视化组件实现

### 2.1 创建路由可视化组件

**文件**: `web/src/components/RouteVisualization.vue`

```vue
<template>
  <div class="route-visualization" :class="{ compact: compact }">
    <!-- 紧凑模式：单行展示 -->
    <div v-if="compact" class="route-compact">
      <span class="route-id">{{ routeTrace.request_id }}</span>
      <span class="route-arrow">→</span>
      <span class="route-provider">{{ routeTrace.provider || '...' }}</span>
      <el-tag :type="getPhaseType(routeTrace.phase)" size="small">
        {{ ROUTE_PHASE_LABELS[routeTrace.phase] }}
      </el-tag>
      <span v-if="routeTrace.latency_ms" class="route-latency">
        {{ formatLatency(routeTrace.latency_ms) }}
      </span>
    </div>

    <!-- 详细模式：完整决策树 -->
    <div v-else class="route-detailed">
      <div class="route-header">
        <h3>路由决策链</h3>
        <el-tag :type="getPhaseType(routeTrace.phase)">
          {{ ROUTE_PHASE_LABELS[routeTrace.phase] }}
        </el-tag>
      </div>

      <!-- 模型解析 -->
      <div class="route-section">
        <h4>1. 模型选择</h4>
        <div class="route-item">
          <span class="label">客户端请求:</span>
          <span class="value">{{ routeTrace.client_model }}</span>
        </div>
        <div v-if="routeTrace.canonical_model" class="route-item">
          <span class="label">解析为:</span>
          <span class="value">{{ routeTrace.canonical_model }}</span>
        </div>
      </div>

      <!-- 凭据选择 -->
      <div v-if="routeTrace.provider" class="route-section">
        <h4>2. 凭据选择</h4>
        <div class="route-item">
          <span class="label">供应商:</span>
          <span class="value">{{ routeTrace.provider }}</span>
        </div>
        <div v-if="routeTrace.credential_label" class="route-item">
          <span class="label">凭据:</span>
          <span class="value">{{ routeTrace.credential_label }}</span>
        </div>
      </div>

      <!-- 尝试历史 -->
      <div v-if="routeTrace.attempts.length > 0" class="route-section">
        <h4>3. 请求尝试</h4>
        <div
          v-for="(attempt, index) in routeTrace.attempts"
          :key="index"
          class="attempt-item"
          :class="{ success: attempt.success, fallback: attempt.is_fallback }"
        >
          <div class="attempt-header">
            <span class="attempt-seq">尝试 #{{ attempt.sequence }}</span>
            <el-tag v-if="attempt.is_fallback" type="warning" size="small">
              Fallback
            </el-tag>
            <el-tag :type="attempt.success ? 'success' : 'danger'" size="small">
              {{ attempt.success ? '成功' : '失败' }}
            </el-tag>
          </div>
          <div class="attempt-details">
            <div class="detail-row">
              <span class="label">目标:</span>
              <span class="value">
                {{ attempt.provider }}/{{ attempt.model }}
                ({{ attempt.credential_label }})
              </span>
            </div>
            <div class="detail-row">
              <span class="label">状态:</span>
              <span class="value">{{ attempt.status || '-' }}</span>
            </div>
            <div class="detail-row">
              <span class="label">延迟:</span>
              <span class="value">{{ formatLatency(attempt.latency_ms) }}</span>
            </div>
            <div v-if="attempt.ttft_ms" class="detail-row">
              <span class="label">TTFT:</span>
              <span class="value">{{ formatLatency(attempt.ttft_ms) }}</span>
            </div>
            <div v-if="attempt.error_message" class="detail-row error">
              <span class="label">错误:</span>
              <span class="value">{{ attempt.error_message }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- 最终结果 -->
      <div class="route-section route-summary">
        <h4>最终状态</h4>
        <div class="summary-grid">
          <div class="summary-item">
            <span class="label">状态:</span>
            <span class="value">{{ routeTrace.status }}</span>
          </div>
          <div class="summary-item">
            <span class="label">总延迟:</span>
            <span class="value">{{ formatLatency(routeTrace.latency_ms) }}</span>
          </div>
          <div v-if="routeTrace.tokens_used" class="summary-item">
            <span class="label">Token:</span>
            <span class="value">{{ formatTokens(routeTrace.tokens_used) }}</span>
          </div>
          <div class="summary-item">
            <span class="label">尝试次数:</span>
            <span class="value">{{ routeTrace.attempts.length }}</span>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { RouteTrace } from '@/types/routeTrace'
import {
  ROUTE_PHASE_LABELS,
  formatLatency,
  formatTokens,
} from '@/types/routeTrace'

interface Props {
  routeTrace: RouteTrace
  compact?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  compact: false,
})

function getPhaseType(phase: string): string {
  const typeMap: Record<string, string> = {
    started: 'info',
    selecting: 'info',
    attempting: 'warning',
    responding: 'success',
    fallback: 'warning',
    done: 'success',
    failed: 'danger',
  }
  return typeMap[phase] || 'info'
}
</script>

<style scoped lang="scss">
.route-visualization {
  padding: 16px;
  background: var(--el-bg-color);
  border-radius: 8px;
  border: 1px solid var(--el-border-color);
}

.route-compact {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 14px;

  .route-id {
    color: var(--el-text-color-secondary);
    font-family: monospace;
  }

  .route-arrow {
    color: var(--el-text-color-placeholder);
  }

  .route-provider {
    font-weight: 500;
  }

  .route-latency {
    margin-left: auto;
    color: var(--el-text-color-secondary);
  }
}

.route-detailed {
  .route-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 16px;

    h3 {
      margin: 0;
      font-size: 16px;
      font-weight: 600;
    }
  }

  .route-section {
    margin-bottom: 20px;

    h4 {
      margin: 0 0 12px 0;
      font-size: 14px;
      font-weight: 600;
      color: var(--el-text-color-primary);
    }

    .route-item {
      display: flex;
      gap: 8px;
      margin-bottom: 8px;
      font-size: 14px;

      .label {
        color: var(--el-text-color-secondary);
        min-width: 80px;
      }

      .value {
        color: var(--el-text-color-primary);
        font-weight: 500;
      }
    }
  }

  .attempt-item {
    padding: 12px;
    margin-bottom: 12px;
    background: var(--el-fill-color-light);
    border-radius: 6px;
    border-left: 3px solid var(--el-color-info);

    &.success {
      border-left-color: var(--el-color-success);
    }

    &.fallback {
      background: var(--el-color-warning-light-9);
    }

    .attempt-header {
      display: flex;
      align-items: center;
      gap: 8px;
      margin-bottom: 8px;

      .attempt-seq {
        font-weight: 600;
        font-size: 14px;
      }
    }

    .attempt-details {
      .detail-row {
        display: flex;
        gap: 8px;
        margin-bottom: 4px;
        font-size: 13px;

        &.error {
          color: var(--el-color-danger);
        }

        .label {
          color: var(--el-text-color-secondary);
          min-width: 60px;
        }

        .value {
          color: var(--el-text-color-primary);
        }
      }
    }
  }

  .route-summary {
    padding-top: 16px;
    border-top: 1px solid var(--el-border-color);

    .summary-grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
      gap: 12px;

      .summary-item {
        display: flex;
        flex-direction: column;
        gap: 4px;

        .label {
          font-size: 12px;
          color: var(--el-text-color-secondary);
        }

        .value {
          font-size: 16px;
          font-weight: 600;
          color: var(--el-text-color-primary);
        }
      }
    }
  }
}
</style>
```

### 2.2 在 LiveStream Store 中集成

**文件**: `web/src/composables/liveStreamStore.ts`

添加路由追踪状态管理：

```typescript
// 在 liveStreamStore 中添加
interface LiveStreamState {
  // ... 现有字段 ...
  routeTraces: Map<string, RouteTrace> // request_id -> RouteTrace
}

// 在 handleEnvelope 中添加
function handleEnvelope(env: LiveStreamEnvelope) {
  if (env.type === 'route_trace' && env.route_trace) {
    // 更新或添加路由追踪
    state.routeTraces.set(env.route_trace.request_id, env.route_trace)
    
    // 如果对应的请求也在显示，更新其路由追踪引用
    const request = findRequestById(env.route_trace.request_id)
    if (request) {
      request.routeTrace = env.route_trace
    }
  }
  
  // ... 现有的其他类型处理 ...
}

// 导出获取路由追踪的方法
export function getRouteTrace(requestId: string): RouteTrace | undefined {
  return state.routeTraces.get(requestId)
}
```

### 2.3 在 Live 页面中集成

**文件**: `web/src/views/LiveTopology.vue` 或相关页面

在请求列表项中添加路由可视化：

```vue
<template>
  <!-- 在请求列表项中 -->
  <div class="request-item" v-for="req in requests" :key="req.request_id">
    <!-- 现有的请求信息 -->
    <div class="request-info">
      <!-- ... -->
    </div>
    
    <!-- 新增：路由追踪图标和弹窗 -->
    <el-button
      v-if="req.routeTrace"
      link
      type="primary"
      @click="showRouteDetail(req)"
    >
      <el-icon><Histogram /></el-icon>
      路由详情
    </el-button>
  </div>

  <!-- 路由详情抽屉 -->
  <el-drawer
    v-model="routeDetailVisible"
    title="路由决策详情"
    size="600px"
  >
    <RouteVisualization
      v-if="selectedRouteTrace"
      :route-trace="selectedRouteTrace"
    />
  </el-drawer>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import RouteVisualization from '@/components/RouteVisualization.vue'
import type { RouteTrace } from '@/types/routeTrace'

const routeDetailVisible = ref(false)
const selectedRouteTrace = ref<RouteTrace | null>(null)

function showRouteDetail(req: LiveRequest) {
  selectedRouteTrace.value = req.routeTrace
  routeDetailVisible.value = true
}
</script>
```

---

## 第三部分：配置和开关

### 3.1 添加配置选项

在配置文件中添加开关：

```yaml
# config.yaml
live_stream:
  enable_route_tracing: true  # 默认启用
  route_trace_cleanup_interval: 10m  # 清理间隔
```

### 3.2 在代码中检查配置

```go
func (h *LiveStreamSSEHub) isRouteTracingEnabled() bool {
    return h.cfg.EnableRouteTracing
}

func (h *ChatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // 只在启用时创建路由追踪
    if h.liveStreamHub != nil && h.liveStreamHub.isRouteTracingEnabled() {
        routeTrace = h.liveStreamHub.StartRouteTrace(requestID, tenantID, clientModel)
        // ...
    }
    // ...
}
```

---

## 第四部分：测试

### 4.1 单元测试

已完成 ✅ - 参见 `admin/route_trace_test.go`

### 4.2 集成测试

创建集成测试验证端到端流程：

```go
// admin/route_trace_integration_test.go

func TestRouteTraceIntegration(t *testing.T) {
    // 设置测试环境
    hub := admin.NewLiveStreamSSEHub(testDB, testConfig)
    go hub.Run()
    defer hub.Stop()
    
    // 模拟请求流程
    trace := hub.StartRouteTrace("test-req-1", 1, "gpt-4")
    
    // 模拟路由阶段
    trace.SetModelResolution("gpt-4-turbo", 101)
    hub.PublishRouteTraceUpdate(trace)
    
    trace.UpdatePhase(admin.RoutePhaseSelecting)
    trace.SetRoutingTarget("openai", 10, "Key 1")
    hub.PublishRouteTraceUpdate(trace)
    
    // 模拟成功的尝试
    trace.StartAttempt(10, "Key 1", "openai", "gpt-4-turbo", false)
    hub.PublishRouteTraceUpdate(trace)
    
    trace.RecordTTFT(250)
    hub.PublishRouteTraceUpdate(trace)
    
    trace.CompleteAttempt(200, true, 1234, "", "")
    hub.PublishRouteTraceUpdate(trace)
    
    hub.CompleteRouteTrace("test-req-1", 200, 1234)
    
    // 验证最终状态
    finalTrace := hub.GetRouteTrace("test-req-1")
    assert.NotNil(t, finalTrace)
    assert.Equal(t, admin.RoutePhaseDone, finalTrace.Phase)
    assert.Equal(t, 1, len(finalTrace.Attempts))
}
```

### 4.3 前端测试

```typescript
// web/src/components/__tests__/RouteVisualization.spec.ts

import { mount } from '@vue/test-utils'
import RouteVisualization from '../RouteVisualization.vue'
import type { RouteTrace } from '@/types/routeTrace'

describe('RouteVisualization', () => {
  const mockTrace: RouteTrace = {
    request_id: 'test-1',
    tenant_id: 1,
    start_time: new Date().toISOString(),
    phase: 'done',
    client_model: 'gpt-4',
    canonical_model: 'gpt-4-turbo',
    provider: 'openai',
    attempts: [
      {
        sequence: 1,
        credential_id: 10,
        provider: 'openai',
        model: 'gpt-4-turbo',
        start_time: new Date().toISOString(),
        success: true,
        latency_ms: 1234,
        is_fallback: false,
      },
    ],
    status: 'success',
    latency_ms: 1234,
    updated_at: new Date().toISOString(),
    version: 1,
  }

  it('renders compact mode', () => {
    const wrapper = mount(RouteVisualization, {
      props: {
        routeTrace: mockTrace,
        compact: true,
      },
    })
    
    expect(wrapper.find('.route-compact').exists()).toBe(true)
    expect(wrapper.text()).toContain('test-1')
    expect(wrapper.text()).toContain('openai')
  })

  it('renders detailed mode', () => {
    const wrapper = mount(RouteVisualization, {
      props: {
        routeTrace: mockTrace,
        compact: false,
      },
    })
    
    expect(wrapper.find('.route-detailed').exists()).toBe(true)
    expect(wrapper.text()).toContain('模型选择')
    expect(wrapper.text()).toContain('请求尝试')
  })
})
```

---

## 第五部分：部署和监控

### 5.1 性能监控

添加 Prometheus 指标：

```go
var (
    routeTracesActive = promauto.NewGauge(prometheus.GaugeOpts{
        Name: "llm_gateway_route_traces_active",
        Help: "Number of active route traces",
    })
    
    routeTracesCreated = promauto.NewCounter(prometheus.CounterOpts{
        Name: "llm_gateway_route_traces_created_total",
        Help: "Total number of route traces created",
    })
    
    routeTracesCleaned = promauto.NewCounter(prometheus.CounterOpts{
        Name: "llm_gateway_route_traces_cleaned_total",
        Help: "Total number of route traces cleaned up",
    })
)

// 在相关位置更新指标
func (h *LiveStreamSSEHub) StartRouteTrace(...) *RouteTrace {
    trace := NewRouteTrace(...)
    h.routeTraceStore.Set(trace)
    routeTracesCreated.Inc()
    routeTracesActive.Set(float64(h.routeTraceStore.Len()))
    return trace
}
```

### 5.2 日志记录

```go
func (h *LiveStreamSSEHub) StartRouteTrace(requestID string, tenantID int, clientModel string) *RouteTrace {
    trace := NewRouteTrace(requestID, tenantID, clientModel)
    h.routeTraceStore.Set(trace)
    
    slog.Debug("route trace started",
        "request_id", requestID,
        "tenant_id", tenantID,
        "client_model", clientModel)
    
    h.publishRouteTrace(trace)
    return trace
}
```

---

## 附录：完整的实施清单

### 后端清单

- [ ] 在 `ChatHandler` 中集成路由追踪创建
- [ ] 在模型解析处记录模型信息
- [ ] 在凭据选择处记录路由目标
- [ ] 在上游请求处记录尝试开始和完成
- [ ] 在流式响应处记录 TTFT
- [ ] 在响应完成处记录 Token 使用
- [ ] 在请求结束处标记完成或失败
- [ ] 添加配置开关
- [ ] 添加 Prometheus 指标
- [ ] 编写集成测试

### 前端清单

- [ ] 实现 `RouteVisualization.vue` 组件
- [ ] 在 `liveStreamStore` 中集成路由追踪状态
- [ ] 在 Live 页面添加路由详情入口
- [ ] 实现路由详情抽屉/弹窗
- [ ] 添加悬浮预览功能（可选）
- [ ] 编写组件单元测试
- [ ] 添加 E2E 测试

### 文档清单

- [ ] 更新 API 文档
- [ ] 更新用户使用文档
- [ ] 更新部署文档
- [ ] 添加故障排查指南

---

## 预期效果

完成集成后，用户将能够：

1. **实时查看路由决策**: 在 live 页面中看到每个请求的路由过程
2. **诊断路由问题**: 快速识别失败的尝试和 fallback 原因
3. **优化路由策略**: 基于统计数据调整凭据权重和路由规则
4. **监控系统健康**: 通过 Prometheus 指标监控路由追踪系统状态

**示例截图位置**（集成完成后添加）:
- Live 页面请求列表视图
- 路由详情抽屉展开视图
- 路由决策树可视化

---

**文档版本**: v1.0  
**创建日期**: 2026-11-11  
**作者**: AI Assistant  
**状态**: 集成指南
