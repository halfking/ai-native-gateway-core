# V2 仪表盘泳道数据不显示问题分析

## 问题描述

v2 版本的仪表盘中，虽然有请求在进入，但多个维度的泳道（原厂/供应商/模型）中没有数据显示。

## 数据流分析

### 完整数据流路径

```
[LLM 请求到达 Gateway]
        │
        ▼
[Telemetry Pipeline] (cmd/gateway/main.go L836-841)
  │
  │  telemetryClient.SetOnRequestLogEmitted(func(entry) {
  │      hub.Publish(adminLiveRequestFromEntry(entry, hub))
  │  })
  │
  ▼
[LiveStreamSSEHub.Publish()] (live_stream_sse.go L766)
  │
  │  1. Redis 写入 (store.Record) — 200ms 超时
  │  2. 非阻塞写入 broadcast channel
  │
  ▼
[LiveStreamRedisStore.Record()] (live_stream_redis_store.go L121)
  │
  │  Redis Pipeline (原子执行):
  │    - ZADD llmgw:live:main <request_id>
  │    - ZADD llmgw:live:tenant:<tid>:main <request_id>
  │    - ZADD llmgw:live:dim:vendor:<vendor> <request_id>
  │    - ZADD llmgw:live:dim:provider:<provider> <request_id>
  │    - ZADD llmgw:live:dim:model:<model> <request_id>
  │    - ZADD llmgw:live:status:<status> <request_id>
  │    - SET  llmgw:live:req:<request_id> (JSON detail)
  │    - TTL = 8 hours (28800s)
  │
  ▼
[LiveStreamSSEHub.Run()] 主循环 (L247)
  │
  │  1. computeScopeDelta() → store.Snapshot() → BuildLiveStreamSnapshot()
  │  2. ComputeDelta(cached, new) → tenantDelta
  │  3. fanOut(LiveStreamEnvelope{...})
  │
  ▼
[fanOut()] (L642)
  │
  │  - 预序列化 JSON: tenantData / superData
  │  - writeEvent(): event: message\ndata: <json>\n\n
  │  - flusher.Flush() 实时推送到浏览器
  │
  ▼
[浏览器 EventSource] (liveStreamStore.ts L310)
  │
  │  es.onmessage → JSON.parse → handleEnvelope(env)
  │  handleEnvelope:
  │    env.type === "initial_data": applyInitialData(env.requests) + env.snapshot
  │    env.type === "request": pushOrQueue(env.request) + mergeDelta(env.delta)
  │    env.type === "idle_marker": pushOrQueue({ type: "idle_marker" })
  │
  ▼
[useSwimLane.ts]
  │
  │  lanes = snapshot.dimensions[groupBy]
  │
  ▼
[LiveRequestStreamV2.vue]
  │
  │  <SwimLane v-for="lane in lanes" :lane="lane" />
```

## 发现的问题

### 问题 1: Redis 配置未指向 252 服务器（高优先级）

**位置**: `cmd/gateway/main.go` L275-306

**问题描述**:
- Redis 已经移到了 252 服务器
- 但 K8s deployment 中的 `LLM_GATEWAY_REDIS_ADDR` 环境变量可能没有更新
- 当前代码中 Redis 地址来自 `cfg.RedisAddr`（config.go L22）

**影响**:
- 如果 Redis 连接失败，hub 会降级到 DB replay
- DB replay 只获取最近 1 小时的数据，且不包含维度分组信息
- 导致泳道无法正确显示维度数据

**验证方法**:
```bash
# 检查 K8s deployment 中的环境变量
ssh -p 25022 root@14.103.112.184 "kubectl get deployment llm-gateway-go-deployment -n pms-test -o yaml" | grep -A 5 LLM_GATEWAY_REDIS_ADDR
```

**修复方案**:
```bash
# 更新 K8s deployment 中的 Redis 地址
ssh -p 25022 root@14.103.112.184 "kubectl set env deployment/llm-gateway-go-deployment -n pms-test LLM_GATEWAY_REDIS_ADDR=172.31.x.x:6379"
```

### 问题 2: Redis 写入超时导致数据丢失（中优先级）

**位置**: `admin/live_stream_redis_store.go` L121-180

**问题描述**:
- Redis 写入有 200ms 超时限制
- 如果 252 服务器网络延迟较高，可能频繁超时
- 超时后数据不会写入 Redis，只在内存中保留

**影响**:
- 新连接无法通过 Redis replay 获取历史数据
- 维度分组数据可能不完整

**修复方案**:
1. 增加 Redis 写入超时时间（如 500ms）
2. 或者优化网络连接（使用内网IP）

### 问题 3: 维度 key 为空导致数据被过滤（中优先级）

**位置**: `admin/live_stream_redis_store.go` L488-539

**问题描述**:
- `liveStreamDimensionKey()` 函数在某些情况下返回空字符串
- 空 key 会被 `buildLiveStreamLanes()` 过滤掉
- 导致部分请求无法归类到任何维度

**可能原因**:
- `resolveVendorForRequest()` 返回空
- `req.ProviderCode` 为空
- `req.CanonicalName` 和 `req.Model` 都为空

**修复方案**:
- 增加更详细的日志，记录被过滤的请求
- 在 `resolveVendorForRequest()` 中增加更多 fallback 逻辑

### 问题 4: SSE 连接问题（低优先级）

**位置**: `web/src/composables/liveStreamStore.ts` L291-336

**问题描述**:
- 浏览器 EventSource 无法设置 Authorization header
- 使用 `?token=` 作为 fallback
- 但 token 可能过期或无效

**影响**:
- SSE 连接可能无法建立
- 泳道无法接收到实时数据

## 诊断步骤

### 1. 检查 Redis 连接状态

```bash
# 检查 Redis 是否可达
ssh -p 25022 root@14.103.112.184 "kubectl exec -n pms-test $(kubectl get pods -n pms-test -l app=llm-gateway-go -o jsonpath='{.items[0].metadata.name}') -- redis-cli -h <REDIS_HOST> -p 6379 ping"
```

### 2. 检查 Redis 数据

```bash
# 检查 Redis 中的 live-stream 数据
ssh -p 25022 root@14.103.112.184 "kubectl exec -n pms-test $(kubectl get pods -n pms-test -l app=llm-gateway-go -o jsonpath='{.items[0].metadata.name}') -- redis-cli -h <REDIS_HOST> -p 6379 keys 'llmgw:live:*'"
```

### 3. 检查 SSE 连接

```bash
# 测试 SSE 端点
curl -N -H "Cookie: llmgw_session=<token>" http://localhost:8781/api/admin/live-stream
```

### 4. 检查后端日志

```bash
# 检查 live-stream 相关日志
ssh -p 25022 root@14.103.112.184 "kubectl logs -n pms-test $(kubectl get pods -n pms-test -l app=llm-gateway-go -o jsonpath='{.items[0].metadata.name}') | grep -E 'live.*stream|redis|LiveStream'"
```

## 修复优先级

1. **立即修复**: 更新 K8s deployment 中的 `LLM_GATEWAY_REDIS_ADDR` 指向 252 服务器
2. **短期优化**: 增加 Redis 写入超时时间，优化网络配置
3. **长期改进**: 增强维度 key 的 fallback 逻辑，增加详细日志

## 相关文件

- `cmd/gateway/main.go` - Redis 初始化和 live-stream hub 接线
- `config/config.go` - Redis 配置读取
- `admin/live_stream_sse.go` - SSE hub 实现
- `admin/live_stream_redis_store.go` - Redis 存储层
- `web/src/composables/liveStreamStore.ts` - 前端 SSE 连接
- `web/src/composables/useSwimLane.ts` - 泳道数据处理
- `web/src/components/LiveRequestStreamV2.vue` - 泳道组件

---

**分析时间**: 2026-07-08
**分析工具**: codebase exploration + code review
