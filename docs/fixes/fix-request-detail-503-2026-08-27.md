# Request Detail API 503 修复与查询优化（2026-08-27）

**环境**： 245 预发布（llmgo.kxpms.cn）　**端点**： `GET /api/admin/request-detail/{id}`

## 事件

请求详情页 API 返回 `503 {"error":{"detail":"request detail store not configured"}}`。

## 根因（审计后修正）

- 245 当时运行的是**旧二进制**（systemd unit 描述为 e37f7a8c），早于上游 2026-08-25 提交 `d542a2caa`——该提交首次把 `requestdetail.Store` 完整接入 `cmd/gateway/main.go`（`requestdetail.SetGlobal` + `adminHandler.SetRequestDetailStore`，位于 dbConn 启用块内，`LLM_GATEWAY_REQUEST_DETAIL_DIR` 可配置）。
- 会话工作基线 `b8ee5ddbd` 同样早于该提交，因此当时源码层面也确实缺少 wiring。
- 重新部署（包含上游 wiring）即消除 503。首轮修复曾在 main.go 额外添加一个**缺 `SetGlobal` 的重复初始化块**；与上游 merge 后形成两处初始化，2026-08-27 审计已删除冗余块，仅保留上游唯一 wiring。

## 查询优化（保留）

`admin/unified_detail.go`：

- `loadRequestLogMeta`：单条 `OR + CASE` 查询拆为级联 4 查（`request_logs_hot.request_id` → `request_logs_hot.client_request_id` → `request_logs_with_current_month.request_id` → `...client_request_id`），每步都是单索引等值/前缀查询；级联顺序保持原语义（`request_id` 精确命中优先、hot 表优先于分区视图）。
- `loadOutboundBody`：增加 `request_logs_bodies_with_current_month` 回退（252 生产库已确认视图存在）；该函数错误在上游本就被调用方忽略（`outbound, _ :=`），视图缺失也不会 500。
- 代价：命中路径 1 次 DB 往返；完全未命中最多 4 次（原 2 次）。请求详情页以近期请求（hot 命中）为主，实测 245 本地 8–17ms。

## 验证

- 245 实测：`?omit_body=1` → 200 / 17ms；含 300KB body → 200 / 8.7ms；公网链路（252 nginx → 245 nginx → gateway）→ 200 / 57ms。
- `request_logs_bodies_with_current_month` / `request_logs_with_current_month` / `session_turns_with_current_month` 三个视图已在 252 库逐一确认存在。
- `go test ./admin/... ./domains/requestdetail/...` 通过（pgxmock 期望已更新为级联查询序列）。
- 租户隔离行为由既有测试族 `TestHandleUnifiedRequestDetail_TenantIsolation_*` 覆盖（本轮未改动该逻辑）。

## 配置

- 环境变量 `LLM_GATEWAY_REQUEST_DETAIL_DIR`；默认 `os.TempDir()/llmgw-request-detail`（245 上即 `/tmp/llmgw-request-detail`）。
- 生命周期：telemetry 侧 `CaptureFromEntry` 写入内存元数据 + 本地 body 文件；DB 持久化成功后 `ClearAfterPersist` 清理。

## 审计发现与处置

| # | 发现 | 处置 |
|---|---|---|
| 1 | merge 后 main.go 存在两处 store 初始化（本轮补丁 + 上游 `d542a2caa`），本轮块缺 `SetGlobal`，运行时被上游块覆盖，属死代码且日志误导 | 删除本轮冗余块，保留上游完整 wiring |
| 2 | 查询拆分后未同步更新 sqlmock 测试即部署（`TestPGBodyReaderResolvesClientRequestIDToCanonicalID` 失败） | 更新期望序列为「request_id 空结果 → client_request_id 命中」，补跑测试 |
| 3 | 初版文档含未经证实的 "8x faster" 声明、错误的默认目录（/tmp/llm-gateway-bodies）、不准确的根因描述 | 重写为本文档 |
| 4 | outbound 回退视图当时未在生产库核实 | 已在 252 确认三个视图存在 |
