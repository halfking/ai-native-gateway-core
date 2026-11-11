# 动态路由可视化功能设计文档

## 1. 背景与目标

### 1.1 背景
从 magpie 项目中学习其路由事件系统的优秀设计，将动态路由的可视化功能整合到 llm-gateway-go 中。

### 1.2 目标
- 在 live 实时请求流页面中集成动态路由示意图
- 展示每个请求的路由决策过程
- 支持点击查看详细的路由路径和决策依据
- 实时更新路由统计信息

## 2. magpie 项目的优秀特性分析

### 2.1 路由追踪系统 (Route Tracing)
magpie 的 `internal/gateway/trace.go` 实现了完整的路由追踪：

**核心特性：**
- 每个请求都有唯一的 Route ID
- 记录完整的路由决策链：
  - 模型选择 (Model)
  - Provider 选择
  - 账号权重排序 (Order []Weighed)
  - 每次尝试的详细信息 (Tries []Try)
  - 失败重试和 fallback 机制
- 实时状态追踪：
  - 请求阶段 (Phase)
  - 响应时间指标 (TTFT, Millis, Tokens)
  - 内容流统计 (Bytes, EstTok)

### 2.2 路由事件 SSE 推送 (Route Events)
magpie 的 `internal/gateway/route_events.go` 实现了路由事件流：

**核心特性：**
- SSE (Server-Sent Events) 实时推送
- 路由活动状态机：
  - `started` - 开始路由
  - `attempt` - 尝试请求
  - `responding` - 接收响应
  - `attempt_done` - 尝试完成
  - `done` - 请求完成
  - `dropped` - 请求丢弃
- 轻量级数据结构 `routeActivity`：
  - 只包含必要的展示字段
  - 隐藏敏感信息（账号、密钥等）
- 环形缓冲区保持最近 512 个事件

### 2.3 关键设计模式

1. **分离追踪与展示**
   - `Route` 结构：完整的内部追踪数据
   - `routeActivity` 结构：安全的展示数据
   - 通过 `activityOf()` 函数转换

2. **增量更新机制**
   - 只推送变化的数据
   - 使用 cursor 机制支持断线重连
   - 心跳保活 (15秒)

3. **安全性设计**
   - 移除敏感字段（账号ID、密钥指纹等）
   - 基于权限的过滤
   - 只展示当前租户的数据

## 3. llm-gateway-go 现有功能分析

### 3.1 实时流系统
llm-gateway-go 已有完善的实时流基础设施：

**现有组件：**
- `admin/live_stream_sse.go` - SSE Hub 实现
- `web/src/types/liveTopology.ts` - 拓扑类型定义
- `web/src/composables/liveStreamStore.ts` - 前端状态管理

**现有能力：**
- 请求生命周期追踪
- 多维度过滤（客户端/供应商/模型/节点）
- 实时指标聚合
- 事件订阅机制

### 3.2 路由相关功能
- `domains/routeincident/` - 路由事件诊断
- `autoroute/` - 自动路由逻辑
- `metrics/` - 路由指标统计

## 4. 设计方案

### 4.1 后端扩展

#### 4.1.1 新增路由追踪数据结构

```go
// admin/route_trace.go
type RouteTrace struct {
    RequestID       string            `json:"request_id"`
    StartTime       time.Time         `json:"start_time"`
    Phase           string            `json:"phase"`
    Model           string            `json:"model"`
    Provider        string            `json:"provider"`
    CredentialID    int               `json:"credential_id,omitempty"`
    Attempts        []RouteAttempt    `json:"attempts"`
    CurrentAttempt  int               `json:"current_attempt"`
    Status          string            `json:"status"`
    LatencyMs       int64             `json:"latency_ms,omitempty"`
    TTFT            int64             `json:"ttft_ms,omitempty"`
    TokensUsed      int               `json:"tokens_used,omitempty"`
    Error           string            `json:"error,omitempty"`
}

type RouteAttempt struct {
    Sequence        int               `json:"sequence"`
    CredentialID    int               `json:"credential_id"`
    Provider        string            `json:"provider"`
    Model           string            `json:"model"`
    StartTime       time.Time         `json:"start_time"`
    EndTime         *time.Time        `json:"end_time,omitempty"`
    Status          int               `json:"status,omitempty"`
    Success         bool              `json:"success"`
    LatencyMs       int64             `json:"latency_ms,omitempty"`
    TTFT            int64             `json:"ttft_ms,omitempty"`
    ErrorKind       string            `json:"error_kind,omitempty"`
    IsFallback      bool              `json:"is_fallback"`
}
```

#### 4.1.2 扩展 LiveStreamEnvelope

在现有的 `LiveStreamEnvelope` 中新增路由追踪字段：

```go
type LiveStreamEnvelope struct {
    // ... 现有字段 ...
    
    // RouteTrace (新增) 携带路由决策过程的详细信息
    RouteTrace *RouteTrace `json:"route_trace,omitempty"`
}
```

#### 4.1.3 路由事件采集点

在请求处理的关键节点采集路由事件：

1. **路由开始** - `handler.go` 中的请求入口
2. **凭据选择** - `autoroute/` 中的凭据选择逻辑
3. **尝试开始** - 每次向上游发送请求前
4. **首字节时间** - 接收到第一个响应内容
5. **尝试完成** - 每次尝试结束（成功或失败）
6. **fallback 决策** - 决定是否 fallback 到下一个凭据
7. **路由完成** - 整个请求完成

### 4.2 前端扩展

#### 4.2.1 新增路由可视化组件

```typescript
// web/src/components/RouteVisualization.vue
// 动态路由示意图组件
interface RouteVisualizationProps {
  requestId: string
  routeTrace: RouteTrace
  compact?: boolean  // 紧凑模式用于列表展示
}
```

**组件功能：**
- 流程图展示路由决策链
- 时间轴展示各阶段耗时
- 高亮失败节点和 fallback 路径
- 支持展开/收起详细信息

#### 4.2.2 集成到 Live 页面

在现有的实时请求流页面中集成：

```typescript
// web/src/views/LiveTopology.vue 或相关页面
// 1. 在请求列表项中显示小型路由图标
// 2. 点击图标弹出详细路由可视化
// 3. 在请求详情侧边栏中嵌入完整路由追踪
```

**交互设计：**
- 列表视图：显示简化的路由状态图标
- 悬浮预览：鼠标悬停显示路由概要
- 详情视图：点击展开完整的路由决策树
- 实时更新：路由状态变化时动态更新视图

#### 4.2.3 数据类型定义

```typescript
// web/src/types/routeTrace.ts
export interface RouteTrace {
  request_id: string
  start_time: string
  phase: RoutePhase
  model: string
  provider: string
  credential_id?: number
  attempts: RouteAttempt[]
  current_attempt: number
  status: string
  latency_ms?: number
  ttft_ms?: number
  tokens_used?: number
  error?: string
}

export type RoutePhase = 
  | 'started' 
  | 'selecting' 
  | 'attempting' 
  | 'responding' 
  | 'fallback' 
  | 'done' 
  | 'failed'

export interface RouteAttempt {
  sequence: number
  credential_id: number
  provider: string
  model: string
  start_time: string
  end_time?: string
  status?: number
  success: boolean
  latency_ms?: number
  ttft_ms?: number
  error_kind?: string
  is_fallback: boolean
}
```

### 4.3 可视化设计

#### 4.3.1 紧凑模式（列表项）
```
[请求ID] → [Provider1] ✓ → 200 OK (123ms)
```

#### 4.3.2 展开模式（详情视图）
```
请求路由决策树
├─ 阶段1: 模型选择
│  └─ gpt-4 → openai/gpt-4-turbo
│
├─ 阶段2: 凭据选择
│  ├─ 候选: [凭据A(权重80), 凭据B(权重60), 凭据C(权重40)]
│  └─ 选中: 凭据A
│
├─ 阶段3: 尝试 #1
│  ├─ 目标: openai/gpt-4-turbo (凭据A)
│  ├─ 开始: 2026-11-11 11:30:00.123
│  ├─ TTFT: 245ms
│  └─ 结果: ✗ 429 Too Many Requests
│
├─ 阶段4: Fallback 决策
│  └─ 触发: 配额耗尽 → 尝试下一个凭据
│
├─ 阶段5: 尝试 #2
│  ├─ 目标: openai/gpt-4-turbo (凭据B)
│  ├─ 开始: 2026-11-11 11:30:00.456
│  ├─ TTFT: 189ms
│  ├─ 耗时: 1,234ms
│  └─ 结果: ✓ 200 OK (2,156 tokens)
│
└─ 最终状态: 成功 (总耗时 1,567ms)
```

## 5. 实施计划

### 5.1 阶段一：后端基础 (Phase 1)
- [ ] 定义路由追踪数据结构
- [ ] 实现路由事件采集器
- [ ] 扩展 LiveStreamEnvelope
- [ ] 在关键路由节点插桩采集

### 5.2 阶段二：SSE 推送 (Phase 2)
- [ ] 实现路由追踪的 SSE 推送
- [ ] 添加路由事件类型到现有 Hub
- [ ] 实现增量更新机制

### 5.3 阶段三：前端组件 (Phase 3)
- [ ] 实现 RouteVisualization 组件
- [ ] 添加类型定义
- [ ] 实现状态管理逻辑

### 5.4 阶段四：集成与优化 (Phase 4)
- [ ] 集成到 Live 页面
- [ ] 实现交互逻辑
- [ ] 性能优化
- [ ] 测试和修复

## 6. 技术考量

### 6.1 性能
- 路由追踪数据应异步采集，不阻塞主请求路径
- SSE 推送使用环形缓冲区限制内存占用
- 前端使用虚拟滚动处理大量请求

### 6.2 安全性
- 移除敏感信息（密钥、内部ID等）
- 基于租户隔离
- 遵循现有的权限模型

### 6.3 兼容性
- 向后兼容现有的实时流协议
- 新字段使用 `omitempty` 标签
- 老客户端忽略新增的路由追踪数据

### 6.4 可测试性
- 独立的路由追踪模块
- 模拟路由事件用于测试
- E2E 测试覆盖关键路径

## 7. 参考资料

### 7.1 magpie 相关文件
- `internal/gateway/trace.go` - 路由追踪核心
- `internal/gateway/route_events.go` - 路由事件 SSE
- `internal/gateway/gateway.go` - 网关主逻辑

### 7.2 llm-gateway-go 相关文件
- `admin/live_stream_sse.go` - 实时流 Hub
- `web/src/types/liveTopology.ts` - 拓扑类型
- `domains/routeincident/` - 路由事件诊断
- `autoroute/` - 自动路由逻辑

## 8. 后续优化方向

### 8.1 高级分析
- 路由决策的统计分析
- 凭据健康度评分
- 智能路由建议

### 8.2 可视化增强
- 3D 拓扑视图
- 动画过渡效果
- 自定义主题

### 8.3 集成扩展
- 导出路由追踪数据
- 与告警系统集成
- 历史路由回放

---

**文档版本：** v1.0  
**创建日期：** 2026-11-11  
**作者：** AI Assistant  
**状态：** 设计阶段
