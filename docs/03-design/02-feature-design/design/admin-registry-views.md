# Admin 注册表视图：请求注册表与连接注册台

> 最后更新：2026-08-22  
> 关联页面：`/admin/request-registry`、`/admin/connection-registry`  
> 关联设计：`会话优化v4/客户端会话保持.md` §13.5

## 1. 总览

| 页面 | 路由 | 核心问题 | 数据时效 |
|---|---|---|---|
| **请求注册表** | `/admin/request-registry` | 网关当前处理/近期完成了哪些请求？处于 pending / in_flight / completed 哪一态？ | 进程内 FIFO 热窗口 + Redis 共享投影；详情可回落 PostgreSQL |
| **连接注册台** | `/admin/connection-registry` | 哪些流式请求仍持有客户端 SSE 连接？保活/思考帧侧写通道是否存活？ | **仅当前进程**；关闭连接保留有限审计环 |

两者均为 **super_admin** 管理面只读观测，不暴露请求正文、凭据或帧内容。

## 2. 请求注册表（Request Registry）

### 2.1 功能

- 三列卡片：**待请求（pending）**、**正在请求（in_flight）**、**已完成（completed）**。
- 实时增量：Admin Live Stream SSE（`liveStreamStore.actions`）推送 lifecycle 动作。
- REST 校准：每 15s 拉取 `GET /api/admin/request-journeys/queues?view=total` 全量快照，SSE 先行、API 权威。
- 点击卡片（租户范围内）→ `/admin/request-registry/journey/:requestId` 旅程详情。
- **按 request_id 查询**：页头搜索框可直接跳转详情页；详情 API 会合并 memory / Redis / PostgreSQL，**不在热窗口内的历史请求仍可查**（受 PG 保留策略约束）。

### 2.2 数据来源

| 层级 | Owner | 容量/范围 | 用途 |
|---|---|---|---|
| 进程内投影 | `domains/requestjourney.Projection` | total 默认 **100** 条 FIFO | 列表主快照、低延迟 |
| Redis 共享投影 | `domains/requestjourney.RedisStore` | 与配置一致 | 跨实例观测（degraded 时仍可用） |
| PostgreSQL | `domains/requestjourney.PostgresRepository` | 持久化 journey events | **Detail 按 request_id 查询** |
| SSE | `admin` live stream | 短期 action 流 | 列表实时增量 |

列表 API 返回的 `RequestSnapshot` 携带 additive 字段：

- `lifecycle_state`: `pending` | `in_flight` | `completed`
- `retry_at`: pending 重试截止时间

前端 completed 列本地 cap **200**（与 probe 三态队列 trim 语义对齐）；超出后最旧 completed 卡片被丢弃，**不代表 PG 无记录**。

### 2.3 历史能力边界

| 能力 | 是否支持 | 说明 |
|---|---|---|
| 查看当前热窗口内请求 | ✅ | 列表三态 + SSE |
| 查看近期 completed（~100–200） | ✅ | FIFO 环，非全量历史 |
| 按 request_id 查完整旅程 | ✅ | `GET /api/admin/request-journeys/{id}` → PG 回落 |
| 按时间范围/租户批量翻历史 | ❌ | 请用 **请求日志** / **调度瀑布** 等持久化查询面 |
| super_admin `scope=all` 全局入站 | ⚠️ | 仅 ingress 快照，详情 events 可能为空 |

### 2.4 API

```
GET /api/admin/request-journeys/queues?view=total[&lifecycle_state=pending|in_flight|completed]
GET /api/admin/request-journeys/{request_id}
```

认证：现有 admin JWT + tenant scope；`scope=all` 需 super_admin。

## 3. 连接注册台（Connection Registry）

### 3.1 功能

- 展示 **流式客户端连接**（`request_id → SerializedStreamWriter` 侧写通道），非 URSM 节点健康三态。
- **在线连接**：协议、客户端类型、注册/最后帧时间、帧计数。
- **近期关闭**：进程内审计环（默认最近 **256** 条，列表 API 返回最近 **50** 条），含 `close_reason`（`stream_end` / `write_deadline` / `replaced` 等）。
- **按 request_id 查询**：页头搜索 → `GET /api/admin/connection-registry/{request_id}`；在线优先，否则查关闭审计环。
- 点击 request_id → 请求旅程详情（与注册表联动）。

> **节点健康**（credential connected/connecting/disconnected、恢复时间线）归属 `node-health` 投影与 Dashboard 节点卡，**不在**本页展示；本页只关心「客户端 SSE 连接是否仍注册在网关进程内」。

### 3.2 数据来源

| 字段 | 来源 |
|---|---|
| `live[]` | `domains/streaming.ConnectionRegistry` 进程 map（容量默认 **4096**） |
| `closed[]` | 同 registry 内 bounded ring（`DefaultClosedHistoryDepth=256`） |
| 帧/字节计数 | registry 写路径维护，只读投影 |

**不写入 Redis / PostgreSQL**。网关重启或进程切换后，连接注册表 **清零**；关闭审计仅在同进程存活期内有效。

### 3.3 历史能力边界

| 能力 | 是否支持 | 说明 |
|---|---|---|
| 查看当前在线流式连接 | ✅ | `live` |
| 查看本进程近期关闭连接 | ✅ | `closed`（最多 256 条环，API 列表 50 条） |
| 按 request_id 查单条（含已关闭） | ✅ | GET by id，搜索 live + closed 环 |
| 跨进程/跨重启历史 | ❌ | 无持久化；需结合 request journey / request logs |
| 查看节点 credential 健康三态 | ❌ | 见 Dashboard / 未来 node-health API |

### 3.4 API

```
GET /api/admin/connection-registry
GET /api/admin/connection-registry/{request_id}
```

响应示例（列表）：

```json
{
  "live": [ { "request_id": "...", "protocol": "openai_chat", "frames_written": 12, "closed": false } ],
  "live_count": 1,
  "capacity": 4096,
  "closed": [ { "request_id": "...", "close_reason": "stream_end", "closed": true } ]
}
```

安全：仅元数据；禁止返回 SSE 帧正文、Authorization、请求 body。

## 4. 前端实现要点

| 模块 | 文件 | 说明 |
|---|---|---|
| 请求注册表 API | `web/src/api/request-journeys.ts` | queues + detail |
| 连接注册表 API | `web/src/api/connection-registry.ts` | 对接真实 admin API（非 mock） |
| 请求注册表视图 | `web/src/views/RequestRegistryView.vue` | SSE + REST 双通道、request_id 搜索 |
| 连接注册台视图 | `web/src/views/ConnectionRegistryView.vue` | live/closed 两区、request_id 搜索 |
| 旅程详情 | `web/src/views/RequestJourneyDetailView.vue` | 路由/动作/追踪三面板 |

## 5. 与其他观测面的关系

```
请求进入 → request journey 投影（列表/详情，可持久化）
         → connection registry（仅流式 + 在线侧写）
         → live stream SSE（实时动作）
         → request_logs / dispatch waterfall（长期审计）
```

- **要看「请求走到哪一步、为何失败」** → 请求注册表 + 旅程详情（或调度瀑布）。
- **要看「客户端 SSE 是否仍连着、保活帧是否还能写」** → 连接注册台。
- **要看「节点是否熔断、何时恢复」** → Dashboard 节点卡 / node-health（非本页）。

## 6. 已知限制与后续

1. 连接注册表无跨实例聚合；多副本部署时每实例独立视图。
2. 请求注册表列表为观测 FIFO，非审计全量；completed 前端 cap 200。
3. `node-health` 专用 admin API（§13.5）尚未独立落地；节点三态仍在 Dashboard 投影。
4. 列表页不支持复杂筛选（租户/模型/时间）；复杂查询走 request-logs API。
