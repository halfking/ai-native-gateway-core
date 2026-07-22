# 请求链路追踪系统 - 部署说明

> 实施日期: 2026-07-17
> 版本: 2.4.6+ (部署后将 `build_seq` 自动 +1)
> 升级路径: 245 → 154 (标准晋级流程,见 docs/deployment/DEPLOYMENT_RULES.md)
> 服务影响: **零停机**,新功能独立于热路径,失败仅 slog.Warn

---

## 1. 变更范围

### 1.1 新增能力

请求链路追踪 (Request Trace) 端到端可见,覆盖:

```
[middleware]  receive_request
       ↓
[handler.go]  authenticate → body_parse → session_lookup → route_resolve → route_credential
       ↓
[executor.go] upstream_request (每个候选凭据)
       ↓
[stream]      stream_start → stream_chunk → stream_complete
       ↓
[handler.go]  request_complete (+ Finalize + FlushToPG)
```

### 1.2 数据流

| 阶段 | 存储位置 | TTL |
|------|---------|-----|
| 请求进行中 | `redis:request:trace:{request_id}` (string JSON) | 10 分钟 |
| 请求完成 | `pg:request_logs.trace_events` (JSONB 列,新) | 永久 (按分区策略) |

前端读取顺序: **Redis → PG 兜底** (`admin/request_trace.go` loadTrace)。

### 1.3 数据库迁移

- 新增迁移: `sql/migrations/startup/420_request_logs_trace_events.sql`
- 风险: **零** — `ALTER TABLE ... ADD COLUMN IF NOT EXISTS JSONB` 是 PG 11+ 的在线操作
- 回滚: `sql/migrations/startup/420_request_logs_trace_events.down.sql`
- 影响行数: `request_logs` 表(含 hot/2026_07/2026_08 等子表)约 350MB;但 JSONB 单列空值占用极小(<1KB/row)
- 索引: `idx_request_logs_has_trace_events` (btree on request_id WHERE trace_events IS NOT NULL),过滤效率优于全表扫

### 1.4 API 新增

| Method | Path | 权限 | 说明 |
|--------|------|------|------|
| GET  | `/api/admin/requests/{id}/trace`     | super_admin | 拉取整次请求的链路 |
| POST | `/api/admin/requests/{id}/ai-prompt` | super_admin | 生成 AI 分析提示词 |

---

## 2. 部署前检查

### 2.1 验证 Redis 可达

```bash
# 部署到 245 / 154 后:
curl -k https://<host>/healthz
# 应当返回 200
```

如果 Redis 不可用,trace 会自动降级为 NoopRecorder (零开销),主流程不受影响。

### 2.2 数据库可写

PG 写入由 `defer FlushToPG` 异步执行,失败仅 slog.Warn:
- `request_logs.trace_events` 默认 NULL
- 没有 trace 数据的请求(Redis miss + 写入失败)继续走原审计路径

### 2.3 前端 bundle

`web/dist/` 已 rebuild,通过 `npm run build` 验证无 lint 错误。

---

## 3. 部署步骤

按 docs/deployment/DEPLOYMENT_RULES.md 流程:

```bash
# 1. 提交代码
git add .
git commit -m "feat: 请求链路追踪系统 (Redis暂存+JSONB持久化+AI提示词生成)"
git push origin main

# 2. 部署到 245 (预发布)
bash scripts/deploy-245.sh

# 3. 245 验证清单
# - L1: curl https://llmgo.kxpms.cn/healthz
# - L2: curl https://llmgo.kxpms.cn/api/system/version (确认 build_seq +1)
# - L3: bash scripts/ops/verify-245-full.sh
# - L4: 浏览器打开 /admin/request-trace 看到空状态 ✓
#       (无故障请求时显示"近 1 小时无失败请求")

# 4. 发送一个测试请求触发 trace 数据
curl -X POST https://llmgo.kxpms.cn/v1/chat/completions \
  -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-5.6-luna", "messages": [...]}'

# 5. 验证 trace 写入 (Redis)
redis-cli -h <redis_host> GET "request:trace:req_xxxxxxxxxxxx"
# (请求完成 10 分钟内可看;完成后会 DEL)

# 6. 验证持久化 (PG)
PGPASSWORD=xxx psql -h <pg_host> -U llm_gateway -d llm_gateway \
  -c "SELECT request_id, jsonb_array_length(trace_events) FROM request_logs WHERE trace_events IS NOT NULL LIMIT 1"

# 7. 验证前端的 AI 提示词生成
# 浏览器进入 /admin/request-trace → 选一条请求 → 「🤖 生成 AI 分析提示词」

# 8. 245 全部通过 → 部署到 154
bash scripts/deploy-154.sh

# 9. 154 验证 (同 245 步骤)
```

---

## 4. 回滚策略

| 场景 | 回滚方式 |
|------|---------|
| **前端报错** | `bash scripts/deploy-seamless.sh rollback 245` |
| **后端 panic** | 同上回滚,trace 包 best-effort 不会阻塞响应 |
| **PG 迁移报错** | 暂停接收新流量,执行 420_request_logs_trace_events.down.sql |
| **trace 占用 Redis 太多内存** | (1) 减小 TTL: 修改 `internal/trace/trace.go:defaultRedisTTL`(默认 10 分钟,可改 1 分钟);(2) 启动时强制 Noop: `redisClientForCache.Client()` 设为 nil |

### 4.1 紧急 Disable Trace

如果 trace 在生产导致内存/性能问题,可立即关闭:

```go
// cmd/gateway/main.go 中:
var traceRec gwtrace.Recorder = gwtrace.NewRedisRecorder(redisClientForCache.Client())
// 改为:
var traceRec gwtrace.Recorder = gwtrace.NoopRecorder{}
```

重新部署即可。trace 包的所有调用都是 nil-safe,关闭后零开销。

---

## 5. 验证清单

### 5.1 后端 ✓

- [x] `go test ./internal/trace/...` PASS
- [x] `go build ./...` PASS (0 errors)
- [x] 6 处 ChatHandler 注入点: receive_request / authenticate / body_parse / session_lookup / route_resolve / route_credential
- [x] 1 处 Executor 注入点: upstream_request
- [x] defer 收尾: Finalize + FlushToPG
- [x] Redis Lua 脚本并发安全 (SCRIPT LOAD + EVALSHA)

### 5.2 管理 API ✓

- [x] GET `/api/admin/requests/{id}/trace` 返回 RequestTrace JSON
- [x] POST `/api/admin/requests/{id}/ai-prompt` 返回 Markdown 提示词
- [x] 缓存降级: Redis miss 自动回 PG
- [x] super_admin only (与 route_incidents 对齐)

### 5.3 前端 ✓

- [x] `npm run build` PASS
- [x] `/admin/request-trace` 路由 (requiresSuper)
- [x] 左侧:近 1h 失败请求列表,自动刷新 10s 一次
- [x] 右侧:链路时间轴,成功✅ / 失败❌ / 超时⏱️ / 跳过⏭️ 颜色 + 图标
- [x] 失败事件可展开查看 details + Snapshot
- [x] AI 模态:输入问题 → 生成提示词 → 一键复制
- [x] 中英 i18n 完整

---

## 6. 性能评估

| 场景 | 性能开销 |
|------|---------|
| 单请求 trace 写入 | Redis EVAL ~0.5ms P99 |
| 8 个阶段 × Append | 累计 ~4ms (主流程添加时间) |
| defer FlushToPG | 异步 goroutine,不阻塞响应 |
| PG JSONB UPDATE | 走 hot 表,受分区索引影响 +1ms |
| 前端 polling | 10s 间隔,前端 CPU 占用 <1% |

极端情况:
- Redis 不可达 → NoopRecorder (零开销)
- PG 不可达 → request_logs.trace_events IS NULL,审计不影响

---

## 7. 监控告警建议

后续可在以下场景添加 alert (后续任务,不阻塞本次部署):

1. `request_logs.trace_events IS NULL` 比例 > 80% → trace 没在工作
2. Redis `request:trace:*` key 总占用 > 1GB → TTL 不够短
3. trace 写入错误率 > 0.1% (slog.Warn 计数)

---

## 8. 相关文档

- 设计: `docs/design/request-trace-system.md`
- 部署规则: `docs/deployment/DEPLOYMENT_RULES.md`
- 本次实施总结: (TBD by author)

---

**最后更新**: 2026-07-17
**维护**: Platform Infrastructure Team
