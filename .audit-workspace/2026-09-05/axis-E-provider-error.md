# 轴 E 审计报告：供应商端请求错误处理闭环（错误记录 / 非中断反馈 / 凭据 QoS 呈现）

- 仓库：`/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-5`（只读审计，未改任何源码）
- 日期：2026-09-05
- 24h 变更范围：`32cdf5479`（fold_candidate_outcomes 补 request_id）、`8b22a2e75`（outage fallback 审计收口）、`5d925d5f5`（URSM outage/恢复边界）
- 参考文档已速读：`docs/ERROR-EVIDENCE-MATRIX-20260905.md`、`docs/error-analysis-and-fixes.md`、`docs/error-handling-improvements.md`（已修问题不重复报）

---

## 一、发现清单（按严重度排序，P0/P1 无）

| # | 级别 | 新旧 | 发现 | 证据（文件:行号） |
|---|------|------|------|-------------------|
| 1 | P2 | 存量（2026-09-01 `4c794c252` 引入，非 24h 三提交） | **provider_error_aggregator 的 Scan 列数失配，自检与统计全部致盲**。聚合 SQL 末尾 SELECT 返回 5 列（新增 `i.endpoint_unknown`），但 Go 侧 `rows.Scan` 仍只传 4 个目标 → pgx 每行报 "number of field descriptions must equal number of destinations"（pgx v5 rows.go:372）→ `groups`/`total` 恒为 0、`newAggregationID` 恒为 0 → 两道水位自检（`watermark regressed`、`watermark did not advance`）成为死代码，`aggregation completed` Info 永不打印，每个 tick 刷 "scan failed" Warn。聚合数据本身（SQL 侧 upsert + 水位推进）仍正确提交，故数据不丢、但聚合器的可观测性与回归保护失效。契约测试是字符串包含式（`bg/provider_error_aggregator_contract_test.go:11-55`），无法捕获此类失配。修复：Scan 补第 5 个 dest（或 SELECT 去掉 endpoint_unknown） | `bg/provider_error_aggregator.go:247`（SELECT 5 列）、`:267`（Scan 4 dest）、`:299`、`:305`（失效的自检）；`vendor/github.com/jackc/pgx/v5/rows.go:372` |
| 2 | P2 | 存量 | **request_logs 错误路径未脱敏，与 candidate_failure_logs 不对称**。供应商错误体进入 request_logs：`logCtx.SetResponseBody(ue.Body)`（原文，最多 4KB）+ `preview=ue.Body[:320]`（未过 `errorsx.SanitizeErrorText`）+ `enrichedErrMsg` 内嵌 preview → `request_logs.response_preview`/response body/error_message 落库裸奔。而 candidate_failure_logs 写入前脱敏（`candidate_failure_logger.go:204,232`）、admin 读取时再脱敏（`vendor_credential_error_handlers.go:217-226`、`admin/candidate_failure_handlers.go:138-140`）。供应商在 401/403 错误体回显 API key（常见行为）时会经此路径泄入 request_logs 及其下游展示 | `domains/streaming/handler.go:4854-4882`（SetResponseBody:4861、裸 preview:4862-4865、failAndMark:4882）对照 `domains/streaming/executors/candidate_failure_logger.go:232` |
| 3 | P2 | 存量（matrix 文档已列"确认需修复"，32cdf5479 只留注释占位、未实际修） | **XML 工具调用片段溢出静默丢弃，无任何记录**。流式 `<>tool_call>` 片段超 64KB 时直接 `c.fragment=""; continue`，溢出日志整段被注释。中途静默丢工具调用属于供应商内容/解析错误，却不产生 slog、candidate_failure 行或 integrity 事件 —— 记录闭环的例外空洞 | `domains/streaming/tool_call_xml.go:145-149`（注释掉的 slog.Warn）；`docs/ERROR-EVIDENCE-MATRIX-20260905.md:72` |
| 4 | P2 | 存量 | **error_message 参与聚合键，高基数消息导致 provider_error_details 碎片化**。`LEFT(error_message,200)` 进入 DISTINCT ON / ON CONFLICT 键：供应商消息含请求 ID、时间戳、变化计数字时，同一 (tenant,credential,model,kind,status) 被拆成海量 10 分钟桶行，凭据详情页"错误集合"退化为噪声明细，QoS 评估（occurrences/first_seen）被稀释。防护仅有 advisory lock、5min tx 超时、TTL 30d（默认）+ cleanup index；无消息归一化（数字/ID 打码）、无行数上限。另：聚合间隔硬编码 10 分钟（非任务书所述"每分钟 worker"），不可配置 | `bg/provider_error_aggregator.go:165`（LEFT 200 入键）、`:193-194`、`:219-225`（ON CONFLICT 键）；`bg/partition_manager.go:125`（硬编码 10min）；TTL 兜底 `bg/partition_manager.go:1136-1172` |
| 5 | P3 | 存量（matrix 已列，仍未迁移） | durable_recovery_worker 仍调用弃用的 `AggregateTaskOutcome`（无 history 循环检测、无 commit-aware 覆盖），恢复 worker 的重试决策与主链路（WithHistory）不一致 | `domains/streaming/durable_recovery_worker.go:431` 对照 `domains/streaming/attempt_outcome.go:431-437`（DEPRECATED 注释） |
| 6 | P3 | 存量 | FailureLogger 落库的 request_id 取自 HTTP header 而非 `params.RequestID`。主链路两者一致（handler.go:420-427 生成后回写 `r.Header`），但生成式 ID 未回写请求头的内部路径（如 embeddings 只写响应头）会落 request_id 为空的失败行，破坏 32cdf5479 要建立的 request_id 追溯链 | `domains/streaming/executors/executor_dispatch.go:511,767,784,967`（`params.R.Header.Get("X-Request-Id")`）；对照 `domains/streaming/embeddings.go:89-93`（只 `w.Header().Set`） |
| 7 | P3 | 存量（32cdf5479 相关文件，行为本身非 24h 引入） | `foldCandidateOutcomes` 给每个 CandidateOutcome 填 `Err: execErr.LastErr`（最后一个候选的错误），per-attempt 无独立 error/status code；AttemptResult 候选集合仅 Kind 可区分。32cdf5479 的 attemptSummary 日志（provider/credential/model/kind + request_id）部分缓解，但结构化对象仍失真（matrix"候选详情字段不完整"的残留） | `domains/streaming/execute_attempt.go:117-123,125-131` |
| 8 | P3 | 剩余边界（与 24h `8b22a2e75`/`5d925d5f5` 相关，非其引入） | **恢复窗口（outage mirror grace）内的失败无"降级路由"标记**。Redis 故障时 `FilterAndScoreOutageFallback` 在 OutageGrace（默认 30m）内以本地镜像路由，可能命中 outage 前已不可用的凭据；产生的失败照常写入 candidate_failure_logs/provider_error_details，但 forwardForDispatch 的 extra context 无 outage-gear 标记 → 恢复窗口内凭据 QoS 统计可能被误读为供应商质量恶化（"恢复窗口内误判"的具体形态）。24h 两提交已把镜像触发收紧到可证明的传输层不可达、canceled 不再算故障、陈旧恢复不能覆盖新事故（open_if_epoch）、gate close 失败释放 debounce claim —— 逻辑边界已闭合，剩下的是呈现层归因问题 | `domains/ursm/v2/manager.go:652-691`；`manager.go:614-630`（isRedisTransportUnavailable，5d925d5f5 移除 context.Canceled）；`domains/ursm/v2/recovery/manager.go:136-147`（cleanup + return false）；对照 `executor_dispatch.go:505-516`（extra 无 outage 标记） |
| 9 | P3 | 存量（已知限制，有度量） | 非流式请求结构性无法收到非中断反馈：bridgeDispatchNotice 对非流式（OnNodeJump nil 或 preStream 未初始化）丢弃 notice 并计入 `DispatchNoticeDroppedTotal(non_streaming/pre_stream_uninit)`；failover 对非流式客户端表现为纯延迟，无任何进度信息。流式 + preStream 未初始化同样丢弃。有指标无告警 | `domains/streaming/executors/executor_dispatch.go:353-378`；`domains/dispatch/notice.go:101-116` |
| 10 | P3 | 存量 | 两处按 byte 截断可劈开 UTF-8 多字节字符（产生无效 UTF-8 落库/落日志）：`errorsx.SanitizeErrorText` 的 `out[:maxBytes]` 与 integrity `TruncateForSample` 的 `s[:maxLen]`。candidate_failure 的 preview 随后经 `truncateUTF8`（executors/safe_truncate.go:16）补救，但 error_message/body 本身不经过 | `errorsx/sanitize.go:40-42`；`domains/streaming/integrity/recorder.go:230-235` |
| 11 | P3 | 存量（随 24-48h 内 `035df5f74`/`db88b7573` 双模式存储引入的边界） | **lite 模式（SQLite/file/memory）下整条错误闭环无数据**：`routingExec.FailureLogger` 仅在 PG dbConn 可用时装配；lite 模式 FailureLogger=nil → candidate_failure_logs 无写入 → provider_error_details 空卷 → 凭据错误详情/质量分接口静默返回空集。运维侧无任何"此模式不支持错误归因"提示 | `cmd/gateway/main.go:1911-1913`（`if dbConn != nil` 才装配）；对照 `db/db.go:1339-1347`（lite 下 pool 为 nil） |

**正面确认（非缺陷）**：preflight 拒绝（fp-slot 饱和 / 熔断打开 / 并发限流 / key 轮询耗尽）均以显式 Kind 记入 candidate_failure_logs_hot（`executor_dispatch.go:549-556,571-585,608-616,641-651`，统一出口 `:943-980`）；HTTP 4xx/5xx/429 错误带 Body（4KB cap）/StatusCode/RetryAfter（`upstream/client.go:45-60,293-317`）；网络错误走 ClassifyError 兜底落库；admin 慢查询保护到位（5s ctx、hours 白名单 1/24/168、LIMIT 10，`vendor_credential_error_handlers.go:87,134-143,195`）。

---

## 二、「需求 → 实现现状」对照

### 线 1：供应商端错误必须记录

| 需求 | 现状 | 结论 |
|------|------|------|
| HTTP 4xx/5xx/429 | `upstream.Error`{Kind,Body≤4KB,StatusCode,RetryAfter}（client.go:293-317）→ forwardForDispatch defer/失败分支 → `candidate_failure_logs_hot`（LogFailure/LogFailureWithKind，含 429=KindRateLimit）→ 10min 聚合 provider_error_details | ✅ 完整 |
| 网络错误（超时/DNS/reset） | ClassifyError 消息兜底 KindNetwork/KindTimeout/KindStreamTimeout，同路径落库；mid-stream 用 LogFailureWithKind 保 Kind 不被压平（executor_dispatch.go:760-780） | ✅ 完整 |
| 解析/转换错误 | KindConversion 等经同一失败分支落库；**例外：XML 工具片段溢出完全静默**（发现 3） | ⚠️ 一个空洞 |
| 字段足以定位凭据 | credential_id/provider_id/raw_model/attempt_index/upstream_status_code/body(1KB sanitize)/preview(320)/latency/retryable/context JSONB（candidate_failure_logger.go:39-56） | ✅ |
| request_id 贯穿到落库 | slog 链路 24h 已补（execute_attempt.go:150-168）；DB 行 request_id 来自 header，主链路一致、边缘路径可能为空（发现 6） | ✅（边缘缺口） |

### 线 2：fallback 以 think / 不影响会话的模式返回、不中断流程

| 需求 | 现状 | 结论 |
|------|------|------|
| think 模式等价物 | 存在：`preStreamKeepalive.writeThinking` 发纯 SSE 注释 `: thinking: <json>`（handler.go:227-233；2026-07-23 调研注释说明为何用 comment 而非 data: —— 规避 Zod 严格解析），不进会话内容、不触发客户端事件 | ✅ 机制存在 |
| 覆盖面 | 三路全部接入同一通道：① dispatch notices（retry/node_switch/model_switch/queued/scheduled，`dispatch/notice.go:16-27` → `bridgeDispatchNotice` → OnNodeJump，failover.go:164-198/282-300）；② goal-retry 重试提示（handler.go:4278-4281「上游请求失败，正在重试(第N/M次…)」）；③ survival RetryNotice（survival_wiring.go:105-112「正在等待可用节点并重试…」）。另有 OnPreStreamKeepalivePause/Resume 探测Hold 协调 | ✅ 流式全覆盖 |
| 不中断流程 | pre-firstbyte failover 对客户端透明（dispatch mover 重排队列）；committed 后中断返回协议错误是防重复输出的预期保护（attempt_outcome.go:352-353，matrix 标注"不应修"） | ✅ |
| 差距 | 非流式无通道（结构性，已计数，发现 9）；流式 preStream 未初始化时丢弃（已计数）。两者均无"降级为响应头/日志"的替代反馈 | ⚠️ 已知限制 |

### 线 3：异常信息呈现在凭据详情下（供应商服务质量评估）

| 需求 | 现状 | 结论 |
|------|------|------|
| 聚合链路 | candidate_failure_logs_hot → ProviderErrorAggregator（10min、advisory lock 幂等、watermark、credential_id 归因 migration 639）→ provider_error_details（TTL 30d + cleanup index） | ✅ 数据面完整 |
| 凭据禁用/恢复一致性 | 凭据详情 API 同时返回 availability_state/circuit_state/consecutive_failures 等元数据（vendorCredentialMeta）与错误集合，可交叉评估；URSM 侧 8b22a2e75/5d925d5f5 已收口 mirror 触发/恢复边界 | ✅（呈现层归因缺口见发现 8） |
| Admin API | `/api/vendors/credentials/{id}/error-detail`（错误分 Kind 汇总 + 最近 10 条 + 7d 质量分）、`/api/candidate-failures/credential/{id}`、`/api/provider-error-stats?credential_id=`（admin/handler.go:1192-1196；provider_credential.go:1113-1180） | ✅ |
| 前端 | `web/src/api/vendor-credential-error.ts`（含测试）+ `web/src/views/provider-detail/ErrorDetailTab.vue` | ✅ |
| 防抖/上限/慢查询 | advisory lock + tx-local visibility + 5min 超时 + 10min tick + TTL；读侧 5s ctx/白名单 hours/LIMIT。**缺**：消息归一化与行数上限（发现 4）、聚合器自检被 Scan 失配致盲（发现 1） | ⚠️ 两处缺口 |

### 附：会话维度记录（任务 4）

- 失败请求落 request_logs（error_kind/upstream_status/response_preview，handler.go:4882）→ `telemetry.AddOnRequestLogPersisted` hook → `internal/sessionv2mirror.PersistHook`（仅 terminal 失败/成功入镜，hook.go:59-66,813-824）→ `session_turns`（success/status_code/**error_kind**/model/provider/credential_id，`domains/session/v2/turn_writer.go:251`、`session_writer_v2.go:310`）。注：schema 中没有任务书所称 `error_details/error_provider/error_model` 列，实际为 `error_kind`/`provider`/`model`；错误消息/响应体不进 session_turns（留在 request_logs 与 candidate_failure_logs，digest meta 亦只含 kind）——「直接记录到会话」以 kind 粒度成立，细节依赖关联查询。

---

## 三、总体结论

三条需求线的**主链路均已实现且近期提交方向正确**：错误记录（含 preflight 拒绝与 mid-stream 中断）→ `candidate_failure_logs_hot` → 聚合 → 凭据详情 API + 前端；流式 fallback 通过 `: thinking:` SSE 注释实现非中断反馈且三路（dispatch/goal-retry/survival）统一接入；session_turns 以 error_kind 粒度承接会话侧记录。24h 的 `32cdf5479` 补齐了候选折叠日志的 request_id 追溯（log 层）；`8b22a2e75`、`5d925d5f5` 有效收口了 outage mirror 的触发与恢复边界（canceled 不再误判为 Redis 故障、open_if_epoch 防陈旧恢复覆盖新事故、debounce claim 失败释放）。

无 P0/P1。最需要优先处理的两项 P2 都不在 24h 三提交内但直接削弱本轴目标：(1) **聚合器 Scan 列数失配**（数据不丢但自检/统计全盲，一行修复）；(2) **request_logs 错误体脱敏缺失**（与 candidate_failure_logs 的纵深防御不对称，存在凭据回显泄漏面）。其次是 XML 工具溢出静默丢弃（记录闭环唯一空洞，matrix 已列但 32cdf5479 只留了注释占位）与 error_message 入聚合键造成的碎片化。恢复窗口/outage grace 的剩余问题是呈现层归因（失败行无"降级路由"标记），非状态机缺陷。
