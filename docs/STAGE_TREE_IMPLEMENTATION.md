# 请求阶段树功能实现文档

## 概述

请求阶段树 (Stage Tree) 是 LLM 网关实时监控的核心功能，用于可视化单个请求从鉴权到回复的完整管线执行过程。本文档说明已实现的功能和集成方式。

**实现日期**: 2026-11-16  
**设计原型**: `docs/audit/2026-11-16-live阶段树原型-完整紧凑双模式版.html`

## 架构设计

### 数据流

```
RouteTrace (routing decisions)
    ↓
ProjectStageTree() [投影函数]
    ↓
StageTreeFrame (complete pipeline view)
    ↓
SSE envelope
    ↓
Frontend StageTreeVisualization component
```

### 核心组件

1. **后端数据层**
   - `admin/route_trace.go` - 路由决策追踪
   - `admin/live_stage_tree_projection.go` - 数据投影层
   - `admin/live_stream_sse.go` - SSE推送集成

2. **前端展示层**
   - `web/src/types/stageTree.ts` - TypeScript类型定义
   - `web/src/components/StageTreeVisualization.vue` - 可视化组件

## 管线阶段

### 发送轨 (Send Rail, 7个阶段)
1. **auth** - 鉴权/限流
2. **approval** - 审批 (通常跳过)
3. **injection** - 注入检测 (通常跳过)
4. **sanitize** - 脱敏 (通常跳过)
5. **compress** - 压缩 (通常跳过)
6. **route** - 路由选择
7. **provider** - 供应商/令牌池

### 接收轨 (Receive Rail, 4个阶段)
1. **first_byte** - 首字节 (TTFT)
2. **restore** - 还原 (通常跳过)
3. **billing** - 计量
4. **reply** - 回复

## 阶段状态

- `pending` - 未到达 (紫色)
- `active` - 进行中 (蓝色)
- `done` - 完成 (绿色)
- `failed` - 失败 (红色)
- `skipped` - 跳过 (灰色斜纹)
- `warning` - 告警 (琥珀色)
- `unknown` - 未测量 (灰色)

## 后端集成

### 1. 创建路由追踪

```go
// 在请求开始时创建追踪
trace := admin.NewRouteTrace(requestID, tenantID, clientModel)

// 存入store
hub.routeTraceStore.Set(trace)
```

### 2. 更新追踪状态

```go
// 模型解析完成
trace.SetModelResolution(canonicalModel, canonicalID)

// 路由目标选择
trace.SetRoutingTarget(providerCode, credentialID, credentialLabel)

// 开始尝试
trace.StartAttempt(credentialID, credentialLabel, provider, model, isFallback)

// 收到首字节
trace.RecordTTFT(ttftMs)

// 设置token使用
trace.SetTokenUsage(promptTokens, completionTokens)

// 完成尝试
trace.CompleteAttempt(status, success, latencyMs, errorKind, errorMsg)

// 标记完成
trace.MarkDone(finalStatus, totalLatencyMs)
// 或标记失败
trace.MarkFailed(finalStatus, totalLatencyMs, errorKind, errorMsg)
```

### 3. 投影并推送

```go
// 投影为StageTreeFrame
frame := admin.ProjectStageTree(trace)

// 封装为SSE envelope
envelope := &admin.LiveStreamEnvelope{
    Type:      "stage_tree",
    Timestamp: time.Now(),
    StageTree: frame,
}

// 通过hub推送
hub.broadcast(envelope)
```

### 4. 清理

```go
// 请求完成后清理追踪
hub.routeTraceStore.Remove(requestID)

// 或定期清理过期追踪
removed := hub.routeTraceStore.Cleanup(5 * time.Minute)
```

## 前端集成

### 1. 导入组件和类型

```typescript
import StageTreeVisualization from '@/components/StageTreeVisualization.vue';
import type { StageTreeFrame, StageNode } from '@/types/stageTree';
```

### 2. 接收SSE数据

```typescript
// 在SSE消息处理函数中
function handleSSEMessage(event: MessageEvent) {
  const envelope = JSON.parse(event.data);
  
  if (envelope.type === 'stage_tree') {
    const frame: StageTreeFrame = envelope.stage_tree;
    updateStageTree(frame);
  }
}
```

### 3. 使用组件

```vue
<template>
  <div class="stage-tree-container">
    <!-- 完整模式 -->
    <StageTreeVisualization
      :frame="currentFrame"
      mode="full"
      @stage-click="handleStageClick"
      @attempt-click="handleAttemptClick"
    />
    
    <!-- 紧凑模式 -->
    <StageTreeVisualization
      :frame="currentFrame"
      mode="compact"
    />
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue';
import StageTreeVisualization from '@/components/StageTreeVisualization.vue';
import type { StageTreeFrame, StageNode } from '@/types/stageTree';

const currentFrame = ref<StageTreeFrame | null>(null);

function updateStageTree(frame: StageTreeFrame) {
  currentFrame.value = frame;
}

function handleStageClick(stage: StageNode) {
  console.log('Stage clicked:', stage);
  // 显示详情弹窗等
}

function handleAttemptClick(index: number) {
  console.log('Attempt clicked:', index);
  // 显示尝试详情等
}
</script>
```

### 4. 样式定制

组件使用CSS变量，可以通过覆盖这些变量来定制样式：

```css
:root {
  --kx-success: #3ecf8e;
  --kx-primary: #5b8cff;
  --kx-danger: #f07167;
  --kx-warning: #e0a53a;
  --kx-dormant: #a78bfa;
  --kx-muted: #8b9bb0;
  /* ... 其他变量 */
}
```

## 显示模式

### 完整模式 (Full Mode)

- 显示所有阶段的详细信息
- 包含阶段名称、状态图标、持续时间
- 显示所有路由尝试的完整信息
- 适合详细分析单个请求

### 紧凑模式 (Compact Mode)

- 单行显示所有11个阶段
- 瓦片式布局，28×28px图标
- 状态用角标字形表达（✓/◐/✕等）
- 关键指标显示在同一行
- 适合一屏查看多个请求

## 错误来源分类

系统自动根据 `error_kind` 判定错误来源：

- **gateway** (网关) - 鉴权拒绝、审批待定、注入拦截、脱敏异常、无可路由凭据等
- **provider** (供应商) - 上游429/5xx、超时、连接失败、模型不可用等
- **other** (其它) - 客户端取消、参数错误等

## 性能考虑

### 内存管理

- RouteTrace存储在内存中，每个追踪约1-2KB
- 建议定期清理完成的追踪（推荐5分钟）
- Store实现了并发安全的访问

### 网络优化

- StageTreeFrame采用增量更新机制
- 仅在状态变化时推送
- Version字段支持客户端去重

### 前端性能

- Vue组件使用computed优化渲染
- 紧凑模式支持虚拟滚动（在列表组件中）
- 避免频繁的深度对象比较

## 测试

### 后端测试

```bash
# 运行所有阶段树测试
go test -v -run TestProjectStageTree ./admin/

# 运行特定测试
go test -v -run TestProjectStageTree_CompletedRequest ./admin/
```

### 前端测试

```bash
# 运行组件测试
npm run test -- StageTreeVisualization.spec.ts

# 类型检查
npm run type-check
```

## 故障排除

### 阶段状态不更新

**问题**: 前端显示的阶段状态停留在某个状态
**原因**: 后端未正确调用trace更新方法
**解决**: 检查路由决策代码中的trace更新调用

### 错误来源分类不正确

**问题**: 错误被分类到错误的来源
**原因**: `error_kind` 不在预定义列表中
**解决**: 在 `classifyErrorSource` 中添加新的error_kind映射

### 紧凑模式图标不显示

**问题**: 紧凑模式下看不到阶段图标
**原因**: SVG内容未正确渲染
**解决**: 检查 `STAGE_ICONS` 定义和 `v-html` 指令

### 时间显示不正确

**问题**: 时间戳显示为无效值
**原因**: 时间格式不是ISO 8601
**解决**: 确保后端发送的是 `time.RFC3339Nano` 格式

## 扩展指南

### 添加新阶段

1. 在 `StageKey` 类型中添加新键
2. 更新 `projectSendStages` 或 `projectRecvStages`
3. 在 `STAGE_NAMES` 和 `STAGE_ICONS` 中添加对应条目
4. 更新组件样式（如需要）

### 添加阶段元数据

```go
// 在StageNode中设置metadata
stage.Metadata = map[string]interface{}{
    "custom_field": value,
    "another_field": anotherValue,
}
```

```typescript
// 在前端访问metadata
if (stage.metadata?.custom_field) {
  console.log(stage.metadata.custom_field);
}
```

### 自定义状态判定

修改 `phaseToOverallStatus` 函数来自定义状态映射逻辑。

## 相关文档

- 设计原型: `docs/audit/2026-11-16-live阶段树原型-完整紧凑双模式版.html`
- 路由追踪: `admin/route_trace.go`
- 数据投影: `admin/live_stage_tree_projection.go`
- SSE集成: `admin/live_stream_sse.go`
- 前端组件: `web/src/components/StageTreeVisualization.vue`

## 版本历史

- **2026-11-16**: 初始实现
  - 完整和紧凑两种显示模式
  - 11阶段管线可视化
  - 路由尝试追踪
  - 错误来源自动分类
  - SSE增量推送

## 待办事项

- [ ] 添加阶段详情弹窗
- [ ] 支持阶段性能趋势图
- [ ] 添加过滤和搜索功能
- [ ] 支持导出阶段树数据
- [ ] 添加更多单元测试和E2E测试
- [ ] 支持自定义阶段配置
