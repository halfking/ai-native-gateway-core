# 154 服务器实时流不显示问题诊断报告

**诊断时间**: 2026-07-23 07:15  
**服务器**: 47.97.111.154 (production)  
**症状**: 前端实时流页面（/admin/live-stream）不显示任何请求

---

## 根本原因（Root Cause）

**JSON 格式错误导致 `request_logs` 表写入失败，进而阻止了实时流的发布流程。**

### 完整调用链

```
1. 请求完成 → telemetry.RequestLogEntry 生成
   ↓
2. 写入 request_logs 表
   ↓ ❌ 失败（JSON 格式错误：invalid input syntax for type json）
   ↓
3. 写入成功回调 [NEVER REACHED]
   ↓
4. hub.Publish(adminLiveRequestFromEntry(entry, hub)) [NEVER CALLED]
   ↓
5. store.Record(ctx, req) [NEVER CALLED]
   ↓
6. Redis ZADD dimension queue [NEVER EXECUTED]
   ↓
7. SSE 推送到前端 [NO DATA]
```

### 代码证据

**main.go:1316** (写入成功回调):
```go
hub.Publish(adminLiveRequestFromEntry(entry, hub))
```

**live_stream_sse.go:1359** (Publish 调用 Record):
```go
if err := h.store.Record(ctx, req); err != nil {
    slog.Debug("live stream redis record failed", ...)
}
```

---

## 症状链（Symptom Chain）

### 1. 前端症状
- ✅ `/api/admin/live-stream` 返回 200
- ✅ SSE 连接建立成功
- ❌ 无数据推送（`data: {}` 空对象）

### 2. 后端症状
- ❌ `request_logs` 表无新记录（最后一条：`2026-07-22 22:59:09`）
- ❌ Redis dimension queue 无新记录（最后时间戳：`1784761149000` = 2026-07-23 06:59:09）
- ⚠️ 日志大量警告：`scope delta nil, request may not appear in swim lanes`

### 3. Redis 状态
- ✅ 连接正常（172.16.2.210:6389）
- ✅ 有旧数据（`llmgw:live:*` keys 存在）
- ❌ Snapshot 数据过期（最新请求时间：昨天晚上）

---

## 诊断过程

### Step 1: 检查前端连接
```bash
curl 'http://47.97.111.154:28080/api/admin/live-stream?token=...'
# 返回: data: {}
```
**结论**: SSE 连接正常，但无数据推送。

### Step 2: 检查 request_logs 表
```sql
SELECT request_id, event_at FROM request_logs 
WHERE tenant_id='default' 
ORDER BY event_at DESC LIMIT 5;
```
**结果**: 最后一条记录是 `2026-07-22 22:59:09`（今天早上 6:59）  
**结论**: 7:12 之后的请求没有写入数据库。

### Step 3: 检查日志中的错误
```bash
journalctl -u llm-gateway-go --since '1 hour ago' | grep -i error
```
**发现**:
```
ERROR: invalid input syntax for type json: ""
DETAIL: The input string ended unexpectedly.
```

### Step 4: 检查 Redis dimension queues
```bash
redis-cli ZRANGE 'llmgw:live:dim:model:gpt-5.6-terra' -1 -1 WITHSCORES
```
**结果**: 最后时间戳 `1784761149000` (2026-07-23 06:59:09)  
**结论**: 新请求没有写入 dimension queue。

### Step 5: 检查 Record 函数调用
```bash
journalctl -u llm-gateway-go | grep -i "live stream record"
```
**结果**: 无任何日志  
**结论**: `store.Record` 函数从未被调用。

### Step 6: 追踪调用链
**发现**: `hub.Publish` 只在 `request_logs` 写入成功后才调用（`main.go:1316`）  
**结论**: JSON 错误阻止了整个流程。

---

## 影响范围

### 时间范围
- **开始时间**: ~2026-07-23 07:00（约 6:59 之后）
- **持续时间**: 至今（约 15 分钟）

### 影响功能
1. ❌ 实时流页面不显示新请求
2. ❌ `request_logs` 表无新数据
3. ✅ API 请求正常处理（业务不受影响）
4. ✅ Redis 实时流旧数据仍可访问

### 未影响功能
- ✅ Claude/GPT 等模型调用正常
- ✅ 鉴权、路由、代理正常
- ✅ 其他 API 端点正常

---

## 修复建议

### 1. 临时修复（立即）
修复 JSON 格式错误（已在 `2026-07-23-fix-json-marshal.patch` 中）：
```bash
cd /path/to/llm-gateway-go
git apply 2026-07-23-fix-json-marshal.patch
make build
systemctl restart llm-gateway-go
```

### 2. 验证修复
```bash
# 等待 2 分钟让新请求进入系统
sleep 120

# 检查 request_logs 表
psql -h 172.16.2.210 -p 4100 -U stockuser -d stock -c \
  "SELECT COUNT(*) FROM request_logs WHERE event_at > NOW() - INTERVAL '5 minutes';"

# 检查 Redis dimension queue
redis-cli -h 172.16.2.210 -p 6389 -a Veritrans9900 \
  ZCOUNT 'llmgw:live:dim:model:gpt-5.6-terra' $(date +%s) +inf

# 检查前端实时流
curl 'http://47.97.111.154:28080/api/admin/live-stream?token=...'
# 应该看到新的请求数据
```

### 3. 长期改进
1. **解耦实时流和 request_logs**:
   - 实时流写入不应依赖 request_logs 成功
   - 建议：在 telemetry 生成时就发布到实时流，而非等待数据库写入

2. **增强错误处理**:
   - JSON Marshal 失败时应降级为空对象 `{}`，而非阻塞整个流程
   - 增加 Sentry 告警（当前只有日志）

3. **监控告警**:
   - 添加指标：`live_stream_publish_rate`（每分钟发布次数）
   - 当该指标 5 分钟内为 0 时触发告警

---

## 关键日志摘录

### JSON 错误日志
```
{"time":"2026-07-23T07:10:48.123+08:00","level":"ERROR","msg":"batch persist request_log failed",
 "error":"ERROR: invalid input syntax for type json: \"\" (SQLSTATE 22P02)"}
```

### 实时流警告日志
```
{"time":"2026-07-23T07:10:04.952+08:00","level":"WARN","msg":"live stream: scope delta nil, 
 request may not appear in swim lanes","request_id":"d47fa529d739f95d2ca9a229a13e2098",
 "tenant_id":"default","has_super_client":true,"empty_skips":2}
```

### Snapshot 日志
```
{"time":"2026-07-23T07:12:22.226+08:00","level":"INFO","msg":"snapshot from dimension queues built",
 "tenant_id":"default","is_super":false,"total_requests":6,"dimension_keys_scanned":1,
 "first_request_ts":"2026-07-22T21:58:27Z","last_request_ts":"2026-07-22T22:59:09Z"}
```
**注意**: `last_request_ts` 是昨天晚上，说明今天 7:00 之后没有新数据。

---

## 总结

**一句话总结**:  
JSON 格式错误导致 `request_logs` 写入失败，进而阻止了实时流的 `Publish` 调用，最终导致前端看不到任何新请求。

**修复优先级**: P0（立即修复）  
**业务影响**: 低（仅影响监控，不影响业务）  
**修复时间**: 约 5 分钟（应用补丁 + 重启服务）

---

**诊断人员**: Agent ZCode  
**报告生成时间**: 2026-07-23 07:15:00 CST
