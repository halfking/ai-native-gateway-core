# Request-Detail 性能优化审计与闭环报告

**日期**: 2026-08-28  
**分支**: `feat/request-detail-performance-opt`

## 结论

本次审计确认 Request-Detail 主流程为：

`GET /api/admin/request-detail/{request_id}` → admin 鉴权 → 本地 memory/file → request_logs → session_turns → tenant gate → JSON 响应。

性能优化代码未丢失：与 `main` 的变更集中在 Request-Detail 管理器、存储、locator、测试/benchmark 与实现文档；未发现覆盖或删除其他模块修改的情况。审计期间修复了 5 类闭环问题：

- 写入前按 10 MiB 限制 body，避免先落盘再失败。
- 读取使用打开后的同一文件描述符重新 stat，并限制读取上限，避免 stat/read TOCTOU。
- 超大本地快照按本地 miss 处理，继续数据库回退；HTTP 映射为 413 仅用于未能回退的明确请求错误。
- telemetry body 统一经过 `DecodeRaw`，兼容 plain-text 内容；捕获失败记录日志而不影响主请求。
- session_turns 补全使用 request_logs 返回的 canonical request ID；可选补全失败不再丢弃已经取得的主 body。

## 数据来源与去处

- **in-flight 来源**: telemetry `RequestLogEntry`。
- **本地去处**: memory 保存 Meta，文件保存 Meta + Bodies；文件以临时文件写入后 rename 发布。
- **持久化来源**: `request_logs_hot`/月视图、body hot/冷表、`session_turns` 与 `session_bodies`。
- **API 去处**: `Detail` JSON，按 source/persistence 标识实际来源。
- **清理**: terminal persist 后调用 `Clear`；TTL、最大条目和启动清理负责兜底。

## 关键审计要点

### 并发与资源

- Store map 生命周期由 mutex 保护。
- `PutBodies` 保持写锁覆盖 rename 与 memory 发布，避免半发布状态。
- `GetFile` 仅在 eviction/path 获取期间持锁，文件 I/O 和 JSON 解析不阻塞其他读。
- 文件描述符由 `defer Close` 释放；读取由 `io.LimitReader` 限制。
- 当前 telemetry capture 仍在 `onEmitted` 回调内执行 JSON 组装和文件写入；这属于既有架构风险，后续应改为有界异步队列或仅发布轻量 Meta，并明确丢弃/背压策略。

### 错误与闭环

- NotFound、数据库传输错误、非法 ID、超限 body 分别处理。
- malformed/oversized 本地快照不阻断持久化回退。
- session body 补全是 best-effort，不再把可用 request_logs 数据变成 500。
- capture/clear 错误需要日志可观测性；capture 已补日志，clear 错误仍建议后续增加日志和指标。

### 租户与兼容性

handler 仍有最终 tenant gate，但 `LookupScope` 尚未被 `pgBodyReader` 的 SQL 查询消费，数据库读取仍主要按 request ID，且 body 表本身没有 tenant_id 列。由于客户端 request ID 可能跨租户重用，这一层仍存在跨租户碰撞和错误回退风险，必须在后续架构任务中通过 canonical ID + tenant 关联查询或显式事务 RLS 上下文解决。该风险不能通过本次局部 patch 安全消除，因此明确记录为发布前安全项。

此外，`ValidateRequestID` 的 8–128 字符限制可能影响历史 client request ID；应在独立兼容性任务中将路径安全校验与数据库查询标识校验分离。

## 验证

- `go build ./...`：通过
- `go test ./...`：通过（审计修复后至少验证受影响包）
- `go test -race ./domains/requestdetail ./admin`：通过
- `go test ./domains/requestdetail -bench=. -benchmem`：通过
- `git diff --check`：代码变更通过；历史新增文档仍存在 Markdown 行尾空格，不影响构建，建议单独格式化。

## 遗留任务

1. 为 `pgBodyReader` 引入租户 scoped query/RLS 事务，并为 request ID collision 增加回归测试。
2. 将 capture 从同步 emitted 回调迁移到有界异步 worker，补充关闭、超时、队列满和重试策略。
3. 为 `Clear` 失败增加日志/指标，并补充敏感文件残留测试。
4. 评估本地 `/tmp` 跨进程共享和多副本可见性；必要时使用共享短期存储或 sticky routing。
5. 对历史 request ID 做兼容性验证。

## 2026-08-29 闭环进度（第三轮交付）

| # | 任务 | 状态 | 交付物 |
|---|---|---|---|
| 1 | pgBodyReader 租户 scoped | ✅ 完成（第二轮） | `pgBodyReader` 三条路径注入 `tenant_id = $N` 谓词 + post-scan 校验 |
| 2 | capture 异步 forwarder | ✅ 完成（第二轮） | `domains/requestdetail/capture_forwarder.go` 有界队列 + 单 consumer + FIFO eviction + drain-on-stop |
| 3 | Clear 失败日志/指标 + 残留测试 | ✅ **本轮完成** | `domains/requestdetail/metrics.go`（新文件）+ `store.go` 全部 os.Remove 失败点接入 + `store_metrics_test.go`（2 个测试，1 个真实触发 chmod 0 失败） |
| 4 | /tmp 跨副本可见性 | ✅ **本轮完成** | `docs/implementation/request-detail-cross-replica-visibility-20260828.md` 三阶段 recommendation（短期 read-your-writes / 中期 sticky / 长期 Redis） |
| 5 | 历史 request ID 兼容 | ✅ **本轮完成** | `safeRequestIDPattern` 扩展为 hex-only / uuid-dashed / prefixed 三族；`safe_id_test.go` 30 行矩阵 + 端到端 round-trip |

### 本轮新增文件

- `domains/requestdetail/metrics.go` — Prometheus 计数器（`requestdetail_store_clear_failures_total`、`requestdetail_store_eviction_failures_total`、`requestdetail_store_clear_success_total`）
- `domains/requestdetail/store_metrics_test.go` — Clear + eviction 失败路径测试（macOS 真触发 permission 拒绝）
- `domains/requestdetail/safe_id_test.go` — request-id 兼容性矩阵 + 端到端 round-trip
- `docs/implementation/request-detail-cross-replica-visibility-20260828.md` — 多副本可见性三阶段 recommendation

### 本轮新增/修改代码

- `domains/requestdetail/store.go` — `safeRequestID` → `safeRequestIDPattern`（三族）+ `ValidateRequestID` 增加 length 校验；Clear / evictLocked / cleanupFiles / GetFile TTL 路径接入失败计数器与 slog.Warn
- `docs/06-deployment/04-runbooks/ops/245-runbook.md` — §5 新增两个 Prom 监控行 + §12 新增 Request-Detail pre-release validation 章节

### 验证（2026-08-29 本地）

- `go build ./...`：✅
- `go vet ./...`：✅
- `go test ./domains/requestdetail/ -race -count=1`：✅（含 30+ safe-id 矩阵、Clear 失败、forwarder 等）
- `go test ./admin/... -count=1 -timeout 5m`：✅（含 5 个跨租户隔离测试）
- `go test ./bg/... -count=1 -timeout 3m`：✅
- 245 预发布 deploy：⏳ **待发起**（本会话未执行 deploy；前置条件已满足）

