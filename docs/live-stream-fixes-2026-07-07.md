# 实时请求流（Live Stream）深度审计与修复报告

**日期**: 2026-07-07
**范围**: 仪表盘"实时请求流"分维列表的全部显示异常

## 一、问题清单（用户反馈）

1. 分维列表出现"其它（Other）"队列 —— 设计中已取消
2. 出现"未知（unknown）"队列
3. 出现专门的"idle"队列 —— 空闲请求应在各维度队列中，而非独立队列
4. 原厂维度把 mimo 归入"其它"，应为 xiaomi
5. 供应商维度所有请求都并入"unknown"，应有真实供应商
6. 队列过一会儿消失全空，刷新后又出现 —— 应至少保持 8 小时
7. 错误信息不够明细，难以定位

## 二、根因分析（深度审计结论）

经全链路只读审计（写入路径、读取路径、前端消费路径），确认了以下**根本性架构缺陷**，远超表层 fallback 值问题：

### 根因 A：超级管理员视角队列抖动/消失（最严重）

`LiveStreamSSEHub.Run()` 的广播分支**硬编码 `isSuper=false`**，对每个到达的请求只计算**该请求所属租户作用域**的 snapshot 并写入共享缓存 `cachedSnapshot[tenantID]`，然后 fanOut 给所有客户端。

后果：超级管理员客户端收到的 delta 是"最近一个发请求的租户"的视图。多个租户交替发请求时，`mergeDelta` 在前端整列替换 lanes，导致**超级管理员视角下队列不断消失/重现**。刷新时走 `initial_data`（用真实 isSuper 读全局队列），数据恢复 —— 完全吻合用户描述的"消失→刷新恢复"。

### 根因 B：Redis 瞬时空读覆盖缓存

广播分支在 `snapshot` 为空时仍执行 `cachedSnapshot[tenantID] = snapshot`（空 snapshot），下次真实请求到来前该租户缓存一直是空，前端持续收到空 delta。Redis 一次 200ms 抖动即可触发。

### 根因 C：idle marker 整套是死代码

`ScanAndRecordIdleMarkers` 把 idle marker 写入**维度队列**（`dim:vendor:*`/`dim:provider:*`/`dim:model:*`），但全项目**只有 `Replay()` 读取 main 队列**并在内存重算分维，维度队列从不被读取。因此所有维度 idle marker 永远不可见 —— 纯粹消耗 Redis 内存与 CPU。只有 `main` 维度的 idle marker 写入 main 队列，但又被 `BuildLiveStreamSnapshot` 的 `if item.Type=="idle_marker" { continue }` 在 summary 中跳过。

### 根因 D：idle marker 跨维度身份污染

旧实现给 idle marker 设置 `Model="__idle__"`、`Provider="系统心跳"` 等固定占位值，导致它们在多个维度视图下都生成虚假 lane。

### 根因 E：表层 fallback 值

`liveStreamDimensionKey` 对空维度返回 `"__unknown__"`/`"unknown"`；`classifyModelCategoryFallback` 默认返回 `"other"`；`liveRequestTile` 用 `emptyAs` 填充 `"unknown"`。这些直接产生"未知/其它"队列。

### 根因 F：mimo 数据库 vendor 缺失

`model_families` 中 `xiaomi-mimo` 的 `vendor` 字段为 NULL，DB 查询返回空后 fallback 模式匹配虽能命中 `mimo→xiaomi`，但 `ModelVendorFor` 对空模型返回 `"other"`，且 vendor 未做大小写归一化（DB 里是"小米"，前端期望"xiaomi"）。

## 三、修复方案

### 修复 1：广播双作用域 delta（根因 A）

`admin/live_stream_sse.go`：
- 新增 `computeScopeDelta(ctx, tenantID, isSuper)`：按作用域独立读取 snapshot、计算 delta、缓存。缓存 key 用 `__super__` 哨兵隔离超级作用域，避免与名为 "default" 的租户冲突。
- 广播分支同时计算 `tenantDelta`（租户作用域）和 `superDelta`（全局作用域，仅当有超级客户端连接时计算，避免 2x Redis 读）。
- `LiveStreamEnvelope` 新增非序列化字段 `superDelta`。
- `fanOut` 重写：预序列化租户/超级两份 JSON，超级客户端收 superDelta，租户客户端收 tenantDelta。
- 新增 `hasSuperClient()` 快速判断是否需要计算超级 delta。

### 修复 2：空 snapshot 不覆盖缓存（根因 B）

`computeScopeDelta` 中：当 `snapshot == nil || snapshot.Summary.Total == 0` 时直接返回 nil，**绝不**用空 snapshot 覆盖缓存。Redis 抖动不再清空仪表盘。

### 修复 3：idle marker 写入 main 队列（根因 C）

`admin/live_stream_redis_store.go`：
- `idleMarkerQueueKeys` 简化为始终返回 `[liveStreamMainKey, tenantMainKey]`。idle marker 现在被 `Replay()` 读到并显示。
- `maybeEmitIdleMarker` 在写入 Redis 后调用 `computeScopeDelta(ctx,"",true)` 并把 `superDelta` 附到 idle_marker 事件，超级客户端立即看到 lane 更新。租户侧靠下次请求自然刷新（idle 即代表该租户无请求，推迟刷新可接受）。

### 修复 4：idle marker 维度身份隔离（根因 D）

`createIdleMarkerForDimension`：每个 idle marker **只携带自身维度的身份**：
- vendor 空闲标记：仅 `ModelCategory=key`
- provider 空闲标记：仅 `ProviderCode=key`
- model 空闲标记：仅 `Model=key`
- main 空闲标记：全空（不进任何分维 lane，仅作心跳）

这样每个 marker 只在一个维度视图出现，不会污染其它视图。

### 修复 5：lane 统计一致性（根因 D 衍生）

`buildLiveStreamLanes`：idle marker 仍作为 tile 显示在 lane 中，但**不计入** lane 的 `LiveStreamStats`（success/failure/in_progress），与全局 `Summary` 口径一致。idle-only lane 仍获得空 stats entry 以出现在图例。

`liveRequestTile`：idle marker 的空字段显示为 `[空闲]`，Status 固定为 `idle`。

### 修复 6：取消 unknown/other 队列（根因 E）

- `liveStreamDimensionKey`：维度值为空时返回 `""`（不再返回 `"__unknown__"`/`"unknown"`）。
- `buildLiveStreamLanes`：跳过空 key，并过滤 `"unknown"/"__unknown__"/"other"/"__idle__"`。
- `liveRequestQueueKeys`：仅当维度值有效且非占位时才加入对应维度队列。
- `classifyModelCategoryFallback`：默认返回 `""` 而非 `"other"`。
- `ModelVendorFor`：空模型返回 `""`；vendor 归一化为小写。

### 修复 7：mimo → xiaomi（根因 F）

- `sql/schema/02-seed.sql`：`xiaomi-mimo` family 的 `vendor` 由 NULL 改为 `'Xiaomi'`。
- `adminLiveRequestFromEntry`（`cmd/gateway/main.go`）：hub 为 nil 的 fallback 路径移除 `ModelCategory: "other"`。

### 修复 8：增强诊断日志（根因 G）

- `Record()`：记录 missing model_category/provider_code/model；记录 queue_count；错误信息含完整上下文。
- `ProviderCodeFor`/`ProviderCodeForCredential`：zero ID、空结果、查询失败各有 debug 日志。
- `adminLiveRequestFromEntry`：当 credential_id/provider_id 都缺失、或解析返回空时记录 debug 日志，便于定位 provider unknown 的数据源。
- `LiveRequestFromTelemetry`：missing provider_code/model_category 各有日志。

## 四、测试

- 重写 `TestLiveStreamRedisStore_IdleMarkerWritesTenantDimensionQueues` → `TestLiveStreamRedisStore_IdleMarkerWritesMainQueue`，验证新行为：idle marker 进 main 队列、每个 marker 只携带自身维度身份。
- `go build ./admin ./cmd/gateway` 通过。
- `go vet ./admin` 通过。
- `go test ./admin` 全部通过。

## 五、修改文件

| 文件 | 改动 |
|------|------|
| `admin/live_stream_redis_store.go` | 核心数据流修复（双作用域、idle marker、维度身份、统计一致性、日志） |
| `admin/live_stream_sse.go` | 广播双 delta、fanOut 分流、computeScopeDelta、hasSuperClient、idle marker 推送 |
| `admin/live_stream_redis_store_test.go` | 更新 idle marker 测试以匹配新行为 |
| `cmd/gateway/main.go` | provider 解析诊断日志、移除 fallback "other" |
| `sql/schema/02-seed.sql` | xiaomi-mimo vendor 修复 |

## 六、部署后验证要点

1. 超级管理员视角：队列不再抖动/消失（多租户并发下稳定）。
2. 分维列表：无 other/unknown/idle 独立队列；空闲标记出现在对应真实 lane 内。
3. mimo 模型归类到 xiaomi。
4. 供应商维度显示真实供应商（若仍 unknown，查 debug 日志 "provider resolution returned empty"）。
5. Redis 抖动后仪表盘不再清空。
