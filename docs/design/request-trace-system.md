# 请求链路追踪系统设计方案

> **创建时间**: 2026-07-16
> **状态**: DRAFT
> **优先级**: P0 (紧急)

## 1. 问题清单

### 1.1 Probe相关问题（P0）

| 问题ID | 描述 | 现状 | 目标 |
|--------|------|------|------|
| P1 | probe direct failed 大量错误 | 错误信息不详细，无法定位根因 | 记录完整请求详情（URL、headers、body、response） |
| P2 | gpt-5.6-luna 无可用节点后未触发全量探测 | 单节点失败不会触发同模型所有节点探测 | 无可用节点时触发 fan-out 全量探测 |
| P3 | glm-5.2 无可用节点但直连可用 | 代理问题导致误判 | 已修复（480afcbd2），需验证 |
| P4 | nvidia nim glm-5.2 每分钟探测一次 | 缺少回退规则 | 实现 5s→30s→60s→5m→1h→2h→24h 指数退避 |
| P5 | probe 错误信息不完整 | 缺少标准模型名、延迟、超时时长 | 补全所有字段到 node_probe_runs 表 |

### 1.2 请求链路追踪问题（P0）

| 问题ID | 描述 | 现状 | 目标 |
|--------|------|------|------|
| T1 | 请求链路记录不完整 | 只有开始/结束，缺少中间阶段 | 记录完整链路：接收→路由→凭据→上游→响应 |
| T2 | 实时请求流无法查看详情 | 缺少"流程详情"弹窗 | 点击查看完整链路可视化 |
| T3 | 失败请求无法定位停在哪一步 | 只有最终错误，无阶段信息 | 标注失败阶段 + 红色高亮 |
| T4 | 无实时追踪能力 | 结束后才落库 | Redis 暂存实时链路 → 结束打包入库 |

---

## 2. 技术方案

### 2.1 Probe问题修复

#### 2.1.1 完善 node_probe_runs 表结构

```sql
-- 新增字段到 node_probe_runs
ALTER TABLE node_probe_runs ADD COLUMN IF NOT EXISTS
  api_model TEXT,                    -- 标准模型名（如 gpt-5.6-luna）
  request_url TEXT,                  -- 完整请求URL
  request_headers JSONB,             -- 请求头（脱敏）
  request_body TEXT,                 -- 请求body
  response_status INT,               -- HTTP状态码
  response_body TEXT,                -- 响应body（前256字节）
  timeout_ms INT,                    -- 超时时长（如果超时）
  network_error TEXT,                -- 网络错误详情
  created_at TIMESTAMPTZ DEFAULT NOW();
```

#### 2.1.2 无可用节点时触发全量探测

**当前逻辑**：
```go
// bg/node_probe.go:456
if direct.ok {
    updateBindingAvailability(true)  // 单节点恢复
}
```

**改进方案**：
```go
// 新增：当模型所有节点都不可用时，触发 fan-out 探测
func (w *NodeProbeWorker) triggerFanOutProbe(ctx context.Context, model string) {
    // 1. 查询该模型的所有凭据节点
    // 2. 并发探测所有节点（direct + gateway）
    // 3. 标记至少一个成功的节点为 available
}

// 在路由失败时调用
// domains/streaming/executors/chat_executor.go
if len(candidates) == 0 {
    go nodeProbe.TriggerFanOutProbe(ctx, req.Model)
    return errors.New("no available nodes, triggered full probe")
}
```

#### 2.1.3 实现探测回退规则

**当前问题**：node_probe_state 表的 `next_retry_at` 计算逻辑已有，但需验证是否正确应用。

```go
// bg/node_probe.go:80
var NodeProbeBackoffChain = []time.Duration{
    5 * time.Second,
    30 * time.Second,
    60 * time.Second,
    5 * time.Minute,
    1 * time.Hour,
    2 * time.Hour,
    24 * time.Hour,
}
```

**验证点**：
- `bg/node_probe.go:Submit()` 是否正确设置 `next_retry_at`？
- 是否有逻辑绕过了 backoff（如每分钟强制探测）？

---

### 2.2 请求链路追踪系统

#### 2.2.1 数据模型设计

**Option A: request_logs.trace_events JSONB 字段**

```sql
-- 在 request_logs 表新增字段
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS
  trace_events JSONB;  -- 完整链路事件数组

-- 示例数据结构
{
  "request_id": "req_abc123",
  "events": [
    {
      "seq": 1,
      "stage": "receive_request",
      "timestamp": "2026-07-16T14:30:00.123Z",
      "duration_ms": 2,
      "status": "success",
      "details": {
        "method": "POST",
        "path": "/v1/chat/completions",
        "client_ip": "1.2.3.4"
      }
    },
    {
      "seq": 2,
      "stage": "route_credential",
      "timestamp": "2026-07-16T14:30:00.125Z",
      "duration_ms": 15,
      "status": "success",
      "details": {
        "model": "gpt-5.6-luna",
        "provider": "apigpt",
        "credential_id": 2451
      }
    },
    {
      "seq": 3,
      "stage": "upstream_request",
      "timestamp": "2026-07-16T14:30:00.140Z",
      "duration_ms": 1200,
      "status": "success",
      "details": {
        "url": "https://apiclaude.cc/v1/chat/completions",
        "via_proxy": true,
        "http_status": 200
      }
    },
    {
      "seq": 4,
      "stage": "stream_response",
      "timestamp": "2026-07-16T14:30:01.340Z",
      "duration_ms": 3500,
      "status": "success",
      "details": {
        "chunks": 42,
        "total_tokens": 856
      }
    }
  ],
  "final_status": "success",
  "failed_at_stage": null
}
```

**Option B: 独立表 request_trace_events**

```sql
CREATE TABLE request_trace_events (
    id BIGSERIAL PRIMARY KEY,
    request_id TEXT NOT NULL,
    seq INT NOT NULL,
    stage TEXT NOT NULL,
    timestamp TIMESTAMPTZ NOT NULL,
    duration_ms INT,
    status TEXT NOT NULL,  -- success / failed / timeout
    details JSONB,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(request_id, seq)
);
CREATE INDEX idx_request_trace_events_request_id ON request_trace_events(request_id);
```

**推荐**：Option A（JSONB字段），原因：
1. 单请求的链路事件不多（通常5-10个），JSONB 查询性能足够
2. 与 request_logs 主表在同一分区，避免跨表JOIN
3. Redis暂存 → 打包 → 单次写入，原子性好

#### 2.2.2 Redis实时暂存设计

**Key 设计**：
```
request:trace:{request_id} → JSONB string
TTL: 600秒（10分钟）
```

**写入时机**：
1. **请求开始** - middleware 记录 `receive_request`
2. **路由选择** - executor 记录 `route_credential`
3. **上游请求** - upstream client 记录 `upstream_request`
4. **流式响应** - streaming handler 记录 `stream_chunk` / `stream_complete`
5. **请求结束** - defer 打包Redis → PG

**实现伪代码**：

```go
// internal/trace/trace.go
package trace

type TraceEvent struct {
    Seq        int       `json:"seq"`
    Stage      string    `json:"stage"`
    Timestamp  time.Time `json:"timestamp"`
    DurationMs int       `json:"duration_ms,omitempty"`
    Status     string    `json:"status"`
    Details    map[string]interface{} `json:"details,omitempty"`
}

type RequestTrace struct {
    RequestID    string       `json:"request_id"`
    Events       []TraceEvent `json:"events"`
    FinalStatus  string       `json:"final_status,omitempty"`
    FailedAtStage string      `json:"failed_at_stage,omitempty"`
}

// 追加事件到Redis
func AppendEvent(ctx context.Context, requestID string, event TraceEvent) error {
    key := fmt.Sprintf("request:trace:%s", requestID)

    // 从Redis读取现有trace
    trace, err := getOrCreateTrace(ctx, key, requestID)
    if err != nil {
        return err
    }

    // 追加事件
    event.Seq = len(trace.Events) + 1
    trace.Events = append(trace.Events, event)

    // 写回Redis
    data, _ := json.Marshal(trace)
    return rdb.Set(ctx, key, data, 10*time.Minute).Err()
}

// 请求结束时打包入库
func FlushToPG(ctx context.Context, requestID string, db *pgxpool.Pool) error {
    key := fmt.Sprintf("request:trace:%s", requestID)

    // 从Redis读取
    data, err := rdb.Get(ctx, key).Result()
    if err != nil {
        return err
    }

    // 更新到 request_logs.trace_events
    _, err = db.Exec(ctx, `
        UPDATE request_logs
        SET trace_events = $1::jsonb
        WHERE request_id = $2
    `, data, requestID)

    // 删除Redis key
    rdb.Del(ctx, key)
    return err
}
```

#### 2.2.3 追踪点注入位置

| 阶段 | 文件位置 | 事件名 | 关键字段 |
|------|---------|--------|---------|
| 1. 接收请求 | `middleware/request_logger.go` | `receive_request` | method, path, client_ip |
| 2. 鉴权 | `middleware/auth.go` | `authenticate` | api_key_id, tenant_id |
| 3. 限流检查 | `ratelimit/limiter.go` | `rate_limit_check` | limit, remaining |
| 4. 路由选择 | `domains/streaming/executors/chat_executor.go` | `route_credential` | model, provider, credential_id |
| 5. 上游请求 | `upstream/client.go` | `upstream_request` | url, via_proxy, http_status |
| 6. 流式响应 | `domains/streaming/handler.go` | `stream_start` | - |
| 7. 响应chunk | `domains/streaming/handler.go` | `stream_chunk` | chunk_count |
| 8. 请求完成 | `middleware/request_logger.go` (defer) | `request_complete` | total_tokens, final_status |

---

## 3. 实施计划

### Phase 1: Probe修复（1-2天）

**Day 1**:
- [ ] P1: 补全 node_probe_runs 表字段（migration + 代码）
- [ ] P4: 验证回退规则是否生效（查日志 + 代码审计）
- [ ] P5: 完善probe错误信息记录

**Day 2**:
- [ ] P2: 实现无可用节点时触发 fan-out 探测
- [ ] 测试：模拟所有节点失败 → 验证触发全量探测
- [ ] 部署到 245 验证

### Phase 2: 请求链路追踪（3-5天）

**Day 3**:
- [ ] T1: 设计并实施 trace 包（`internal/trace/`）
- [ ] 添加 request_logs.trace_events 字段（migration）
- [ ] 实现Redis暂存逻辑

**Day 4**:
- [ ] T1: 在8个关键点注入追踪事件
- [ ] T4: 实现请求结束时打包入库
- [ ] 单元测试

**Day 5**:
- [ ] T2: 后端API：`GET /api/admin/requests/:id/trace`
- [ ] T3: 返回格式化链路 + 失败阶段标注
- [ ] T2/T7: 前端实现"流程详情"弹窗（绿色/红色可视化）

---

## 4. API设计

### 4.1 获取请求链路详情

**Endpoint**: `GET /api/admin/requests/:request_id/trace`

**Response**:
```json
{
  "request_id": "req_abc123",
  "final_status": "failed",
  "failed_at_stage": "upstream_request",
  "total_duration_ms": 1250,
  "events": [
    {
      "seq": 1,
      "stage": "receive_request",
      "stage_name": "接收请求",
      "timestamp": "2026-07-16T14:30:00.123Z",
      "duration_ms": 2,
      "status": "success",
      "icon": "✅",
      "details": { ... }
    },
    {
      "seq": 3,
      "stage": "upstream_request",
      "stage_name": "上游请求",
      "timestamp": "2026-07-16T14:30:00.140Z",
      "duration_ms": 0,
      "status": "failed",
      "icon": "❌",
      "error": "dial tcp: i/o timeout",
      "details": {
        "url": "https://apiclaude.cc/v1/chat/completions",
        "via_proxy": true
      }
    }
  ]
}
```

---

## 5. 前端UI设计（流程详情弹窗）

### 5.1 列表页改动

在"实时请求流"表格中，每行增加"流程详情"按钮：

```
| 请求ID | 模型 | 状态 | 耗时 | 操作 |
| req_abc123 | gpt-5.6-luna | ❌ 失败 | 1.2s | [原始详情] [流程详情⭐] |
```

### 5.2 弹窗内容

**标题**: 请求链路详情 - req_abc123

**布局**:
```
┌─────────────────────────────────────────────┐
│  [1] 接收请求           ✅  2ms              │
│  ───────────────────────────────────────────│
│  [2] 鉴权              ✅  5ms              │
│  ───────────────────────────────────────────│
│  [3] 路由选择          ✅  15ms             │
│  │  模型: gpt-5.6-luna                      │
│  │  提供商: apigpt                          │
│  │  凭据ID: 2451                            │
│  ───────────────────────────────────────────│
│  [4] 上游请求          ❌  超时 (15000ms)   │  ← 红色高亮
│  │  URL: https://apiclaude.cc/v1/...       │
│  │  通过代理: 是                            │
│  │  错误: dial tcp: i/o timeout            │
│  ───────────────────────────────────────────│
│  总耗时: 15.02s | 失败于: 上游请求          │
└─────────────────────────────────────────────┘
```

**颜色规则**:
- ✅ 绿色：成功
- ❌ 红色：失败
- ⏱️ 黄色：超时

---

## 6. 风险与依赖

### 6.1 性能影响

| 操作 | 影响 | 缓解措施 |
|------|------|---------|
| Redis写入 | 每请求8次写入 | 使用Pipeline批量写入 |
| 打包入库 | 单次UPDATE | 异步执行，失败不阻塞响应 |
| JSONB查询 | GIN索引 | `CREATE INDEX ON request_logs USING GIN (trace_events);` |

### 6.2 依赖项

- [ ] Redis连接池（已有）
- [ ] request_logs表结构变更（需migration）
- [ ] 前端组件开发（需前端协作）

---

## 7. 验收标准

### 7.1 Probe问题

- [ ] probe失败时能看到完整请求URL、body、响应
- [ ] gpt-5.6-luna无可用节点时触发全量探测
- [ ] nvidia nim探测间隔符合退避规则（5s→30s→...）

### 7.2 请求追踪

- [ ] 成功请求：能看到完整绿色链路，每阶段耗时
- [ ] 失败请求：能看到停在哪一步，红色标注
- [ ] 超时请求：能看到超时时长（如15000ms）
- [ ] 实时性：请求进行中能在Redis看到实时链路

---

## 8. 参考资料

- OpenTelemetry Trace Spec: https://opentelemetry.io/docs/specs/otel/trace/api/
- Jaeger UI: https://www.jaegertracing.io/docs/1.47/frontend-ui/
- 现有代码:
  - `bg/node_probe.go` - probe逻辑
  - `domains/streaming/executors/chat_executor.go` - 路由逻辑
  - `upstream/client.go` - 上游请求
  - `middleware/request_logger.go` - 请求日志

---

**作者**: ACC Agent
**审核**: 待定
**最后更新**: 2026-07-16
