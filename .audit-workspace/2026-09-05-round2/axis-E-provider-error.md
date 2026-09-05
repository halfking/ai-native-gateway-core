# 轴E（round2）：供应商端错误处理闭环

基线：docs/audit-2026-09-05-eight-closures.md 闭环1/2/7/8 已读，已修复项不再重复报告。

## 发现列表

### E-#1 [P1] supplier_errors_hot FORCE RLS + 全链路未设置 app.tenant_id，非 superuser 角色部署下事实源整链静默为空
- 证据：
  - `deploy/sql/migrations/V371__supplier_errors_hot_and_stats.sql:84-88`：`ENABLE + FORCE ROW LEVEL SECURITY`，policy 为 `USING (tenant_id = current_setting('app.tenant_id', true))`——未设置时 `current_setting(...,true)` 返回 NULL，比较为 NULL→policy 不放行（ALL policy 的 USING 同时充当 INSERT 的 WITH CHECK）。注释声称"与 candidate_failure_logs_hot 同模式"，但 V359 的同位 policy 用的是 `get_current_tenant()`（`deploy/sql/schemas/baseline/01-schema.sql:1972-1975`，未设置时**默认 'default'**）且 V359 只 ENABLE 不 FORCE；V367:11-16 为 promote 场景给 policy 加了 `OR current_setting('app.bypass_rls',true)='true'` 旁路，V371 没有。
  - 写入端 `domains/streaming/executors/supplier_error_logger.go:79` `persistSupplierError` 直接 `pool.Exec`，无 set_config；聚合端 `bg/supplier_error_stats_aggregator.go:121` rollup 直接 `Exec` 读 hot 表，无 bypass；admin 读端 `admin/vendor_credential_error_handlers.go`、`admin/errors_trend.go` 查 `supplier_errors_unified` 也只把 tenant 放在 SQL 过滤参数里，未设置 RLS GUC。全仓 Go 代码无任何 `app.tenant_id` set_config（grep 证实；apihub 用的是另一个 GUC `app.current_tenant`）。
  - 对照组：同仓 `bg/provider_error_aggregator.go:130-138` 在聚合前显式 `set_config('app.current_role','super_admin')` + `app.bypass_rls='true'`——说明工程惯例是"非 superuser pool 需要显式旁路"，V371 链路三处都没做。
  - 部署 DSN 为 `postgres://llm_gateway:...`（`deploy/prometheus/docker-compose.yml:131`），专用应用角色；闭环1 的真实验证自述以 superuser 连接（"superuser 绕过 RLS，FORCE 仅约束属主"），生产等价路径未验证。candidate_failure_logs_hot 双写之所以一直成功，恰与"llm_gateway 是属主、V359 表未 FORCE"自洽；FORCE 只加在了 V371 新表上。
- 影响：若生产角色为属主（非 superuser），每个失败候选的 `persistSupplierError` INSERT 报 42501 仅记 warn，rollup SELECT 恒 0 行，凭据错误详情/趋势 API（含 fallback）全空——闭环1 唯一事实源整体失效，且旧表双写仍在进行，丢失被完全掩盖（无告警）。若生产角色恰为 superuser/BYPASSRLS 则降级为 P2 一致性债（policy 与 V359 模式背离、GUC 名不一致）。
- 最小修复：V371 两个 policy 补 `OR current_setting('app.bypass_rls', true) = 'true'`（对齐 V364/V367），或 persistSupplierError/rollup/admin 读端统一 set bypass；并在 `deploy/sql/verify` 加一条**以应用 DSN 角色**（非 superuser）的写入+聚合探针。工作量 S。

### E-#2 [P1]（遗留核实：仍存在）request_logs 错误路径 preview/body 未脱敏
- 证据：错误路径入口 `domains/streaming/request_log_pipeline.go:818 buildEntry`：`:856-859` `ResponseBody = string(c.ResponseBody)`（全量原始上游错误体）；`:878-881` `responsePreview(c.ResponseBody)` → `previewJSON`（`telemetry_summary.go:68-80`，纯截断无脱敏）。下游 `telemetry/client.go:1019/1766 sanitizeRequestLogEntry` → `sanitizeStringPtr` → `sanitizeUTF8`（client.go:2701-2717）**只修无效 UTF-8 和反斜杠转义，不做密钥脱敏**。对照：candidate_failure 路径入库前已 `errorsx.SanitizeErrorText`（`executors/candidate_failure_logger.go:237-244`），admin 读端也有 defense-in-depth——唯独 request_logs 主表没有。
- 影响：供应商 401/403 等错误回显 `Bearer`/`sk-*`/`api_key=` 时经 `request_logs.response_preview/response_body` 落库并在 admin 请求详情可见（安全：凭据泄漏入库，触发依赖上游回显）。
- 最小修复：buildEntry 出口对 ResponsePreview/ResponseBody 过 `errorsx.SanitizeErrorText`。工作量 S。

### E-#3 [P2]（遗留核实：E-#4 仍存在）provider_error_details 聚合键含 LEFT(error_message,200) 碎片化
- 证据：`bg/provider_error_aggregator.go:41`（staging 投影）与 `:261`（ON CONFLICT 键 `COALESCE(LEFT(error_message,200),'')`）。同一上游错误的消息措辞抖动即拆桶，occurrences/first_seen/last_seen 失真。新事实源链路（supplier_error_stats）键不含 message（正确），此问题仅影响旧 provider_error_details 读端。修复：把 error_message 移出唯一键，作样本列。工作量 M。

### E-#4 [P2] supplier_error_stats 只写 minute 粒度，趋势 API 默认窗口（24h/168h）恒走明细兜底
- 证据：`bg/supplier_error_stats_aggregator.go:121-122` 每次固定以 `granularity="minute"` UPSERT；`admin/errors_trend.go:80-89` 默认粒度 hours≤1→minute、≤24→**hour**、否则→**day**；`:121-132` stats 空则 fallback 全量扫 `supplier_errors_unified`。全仓无第二处写 stats。
- 影响：默认 24h 视图每次请求白打一次 stats 查询后仍扫 hot+columnar 明细（闭环1"趋势图不再扫明细表"对默认窗口不成立）；168h 大窗口明细聚合是潜在慢查询。
- 最小修复：聚合器做 minute→hour→day 二次 rollup（同一 UPSERT 幂等模式）。工作量 S/M。

### E-#5 [P2] foldCandidateOutcomes 的 tracker 恢复只覆盖 OpenAI 出口，anthropic 候选失败不进 RoutingTracker
- 证据：全仓唯一 `RoutingTracker.Add` 在 `executors/executor_chat.go:808`（含 `ProviderName: cand.CatalogCode`、Stage/ErrorKind/Retryable，闭环2 字段齐全）；`executeAnthropic`（`executor_anthropic.go:692`）与其 `executeAnthropicOnce:909` 全程无 Add（grep 计数 0）。`execute_attempt.go:149-179` 的 tracker 兜底因此对 anthropic 协议候选拿不到任何真实失败 → 落入 `:181-204` 合成单条 no_available_channel。
- 影响：anthropic 候选的多候选失败在聚合器眼里退化回 `no_candidate_outcomes`（fail-closed）且丢失全部 per-candidate 诊断——正是闭环2 修的缺口，只在 openai 出口闭合。
- 最小修复：executeAnthropicOnce 错误分支补对称 Add。工作量 S/M。

### E-#6 [P2] 服务质量评估缺口：可重试率/阶段分布无聚合载体，错误率分母是占位
- 证据：rollup SQL（`bg/supplier_error_stats_aggregator.go:30-57`）与 V371 stats 表均无 is_retryable/stage/http_status 维度；凭据详情 summary 仅按 error_type 聚合（`vendor_credential_error_handlers.go:176-179`），retryable/stage 只在最近 10 条明细行可见（`:230 LIMIT 10`）；`success_count=0、total_requests=COUNT(*)`（aggregator 注释自认 error_rate 需接 usage_ledger 后才有意义）。
- 影响：按凭据聚合错误率/可重试率/阶段分布目前只有**错误计数×类型**可用；可重试率与阶段分布只能肉眼看 10 条样本，无法量化支撑凭据服务质量评估/下线决策。
- 最小修复：rollup 增加 retryable_count/stage 分桶列 + 凭据详情 summary 按 (error_type, retryable, stage) 聚合。工作量 M。

### E-#7 [P3] domains/routing 遗留重复 CandidateFailureWriter（死代码 footgun）
- 证据：`domains/routing/candidate_failure_logger.go` 与 executors 版同名同表：`LogFailure` 直接 INSERT（:107），**无双写 supplier_errors_hot、无 SanitizeErrorText**（:155 原始 `execErr.Error()`、:179-189 原始 body[:1024]/preview[:320]）。生产无 wiring（唯一引用是自身 test:11；生产 wiring 在 `cmd/gateway/main.go:1933` 用 executors 版）。修复：删除或 deprecated 转发。工作量 S。

### E-#8 [P3] supplier_error_stats 无生命周期清理
- 证据：`admin/data_lifecycle_hot_partition.go:31-48` 与 V371 均无 stats 的 retention/删除任务；minute 行按 supplier×credential×error_type×model 基数无限累积。修复：纳入保留策略（如 90d）。工作量 S。（注：已并入 SQL 修复包 D-2#5 一并处理）

### E-#9 [P3] admin/candidate_failure_handlers 三读端未切 unified，缺结构化字段
- 证据：`admin/candidate_failure_handlers.go:95/:177/:246` 仍读 `candidate_failure_logs_with_current_month`，DTO 无 supplier/stage/error_code。双写过渡期数据一致，但与"读端已统一切换"表述不一致；旧表按 V359 治理下线时该端点静默变空。修复：切 `supplier_errors_unified` + 字段透传。工作量 S。

## 已确认闭环（本次验证通过，不再列为发现）

1. **写入闭环**：4 个调用点全部收敛 `logFailure` 唯一入口（`executor_dispatch.go:511/:768/:785`、`logDispatchPreflightRejection:969`）；fpSlot 饱和/熔断/并发限流/key 耗尽四类 preflight 拒绝均记录且带 `supplier` 注入；全仓 grep 证实 `supplier_errors_hot` 仅 `persistSupplierError` 一处 INSERT；error_message/metadata/body 入库前经 `SanitizeErrorText`，低基数维度有防自由文本守卫，配套脱敏测试存在。
2. **双写与生命周期**：supplier_errors_hot promote 已注册（`data_lifecycle_hot_partition.go:41`），SupplierErrorStatsAggregator 已接入 PartitionManager Start/Stop（`partition_manager.go:127/167/192`）。（round2 补充：后台 promoteSpecs 调度漏注册，见 axis-D D-2#1，已列入修复包）
3. **think 模式非中断返回**：三路接入确认——dispatch notices（`bridgeDispatchNotice`→`OnNodeJump`→`preStream.writeThinking`，`executor_dispatch.go:353-365`+`handler.go:4004-4008`）、网关侧重试（`handler.go:4278-4281`）、restart/node_switch（`stream_recovery.go:841-877`）及 streamretry keepalive（`retry.go:348-359`），全部 SSE comment（Zod-safe，无 data: 行）；桥后丢弃有 `llmgw_dispatch_notice_dropped_total{non_streaming|prestream_uninit}` 计数兜底。无备用候选：f0447666c 已核实加 `len(candidates)` 守卫，客户端收 `writePrewarmedStreamErrorWithKind` 干净 envelope（`handler.go:4844`）；非流式请求 notice 按设计无通道、最终走标准 JSON error envelope。
4. **诊断字段与聚合**：`foldCandidateOutcomes` tracker 恢复（openai 路径，缺口见 E-#5）；CandidateOutcome/AttemptRecord/RoutingAttempt 结构化字段齐全；凭据错误详情已切 `supplier_errors_unified` 且 supplier/error_code/retryable/stage 透传 + 读端二次脱敏（`vendor_credential_error_handlers.go:269-278`）；`/api/errors/trend` 已注册（`admin/handler.go:1197`），stats/fallback 双路径、空数据返回空数组非 null。

## 统计

P0=0，P1=2（E-#1、E-#2），P2=4（E-#3~#6），P3=3（E-#7~#9）。
