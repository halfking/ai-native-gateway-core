# 24小时修改审计报告（2026-07-27 至 2026-07-28）

**审计时间:** 2026-07-28
**审计基线:** `8da7db526451f4c08cc18a04883a9fb66f7abd23`（24小时前的共同基线）
**审计范围:** `git rev-list BASE..HEAD` 共 97 个提交（87 个非合并、10 个合并），265 个文件，`+16029/-4130`  以及本次审计整改未提交差异
**审计方法:** Standards / Spec 双轴复核、核心模块测试、静态检查、迁移编号与差异门禁检查
**审计结果:** ⚠️ **NO-GO**：代码整改已完成并通过受影响模块测试，但仍有发布阻断项未验证或未闭环，不得据此宣称全量切换完成。

## 修改总结

- 请求链路：arrival WAL、终态保护、请求体/遥测字段、流式断开和 keepalive 生命周期。
- 路由状态：URSM v2 Redis/LRU、credential state 并发更新、探测/恢复、quota 路由门禁、sticky/intent 双写。
- 协议转换：provider-specific tools、streaming tool args 校验、Anthropic 错误信封和 4xx body 处理。
- 管理面与部署：client-perception view、迁移 458-461、健康等待超时、版本/发布文档。
- 统计以 `8da7db52..HEAD` 为准；此前报告中的 89 个提交、84 个文件和 `+2464/-646` 不符合仓库事实，已纠正。

## 本次整改

- 修复 URSM `Ready=false` 时 LRU 全命中绕过 recovery gate 的问题；公开 `FilterAndScore` 恢复真实 Ready 检查，快照路由使用 `FilterAndScoreReady`。
- 增加 tenant-aware URSM node/window key、NodeMirror 查询和请求结果写入；空 tenant 保留旧 key 兼容入口。
- 修复 routing 与 streaming executor sticky Redis store 的无锁读取/删除，统一使用锁保护的快照。
- 修复两条 Anthropic→OpenAI 流式桥接路径：无法修复的 tool 参数不再透传非法 JSON，而是发送协议错误并终止该流。
- 删除重复的 `461_request_wal_hot_request_id_unique.sql`，保留唯一索引版本，消除同编号 forward migration 的执行歧义。
- 清理新增文档尾随空格，恢复 `git diff --check` 门禁。

## 双轴审查结论

### Standards

- 通过：受影响 Go 代码已 `gofmt`，`git diff --check` 通过，核心迁移编号检查不再包含新增重复 461。
- 通过：受影响模块编译与测试通过；sticky 共享字段访问已改为锁内配置、锁外 I/O。
- 仍需关注：仓库历史迁移存在既有重复编号，不能用简单全仓重复编号扫描替代部署账本校验。

### Spec

已闭环：L-1/L-2 WAL 与日志终态路径、D-2 范围内 body 读取、G-ID-1/2、F-1/F-2/F-3、D-1、Ready gate、tenant-aware URSM 读写、F-5 非法 JSON 阻断。

未闭环或未完成发布验收：

- **S-3:** `routing_state_source` 尚未传播到 request context、attempt、request log、decision log 和 metrics；当前不能统计 NodeMirror hit/miss/stale/fallback。
- **URSM authoritative 纯度:** 未就绪时仍存在 legacy state fallback；需按发布方案验证 authoritative 模式下旧状态源零 live 调用。
- **Session V2 单 owner:** pipeline `SessionPersistHook` 与 telemetry mirror owner/时序仍需真实 upstream 前后链路验收。
- **迁移与真实依赖:** PostgreSQL 上 459 down/up、460/461 up/down、唯一索引对既有重复数据的清理尚未在真实数据库执行；Redis/PG 故障注入、E2E、部署主机验证未执行。
- **IR 默认:** 设计目标要求 IR 默认路径，但当前 live 默认仍由 feature flag 保持 Legacy；不能把目标态描述为已切换。
- **运行审计:** provider profile 当日聚合、告警并发去重和告警持久化失败处理仍需独立整改/验收。

## 验证结果

- ✅ `gofmt`（本次修改文件）。
- ✅ `git diff --check`。
- ✅ `go test ./...`：全仓通过（211 个包结果，无失败）。
- ⚠️ 受影响模块 `go test -race`：URSM v2、routing、streaming/executors、Anthropic transform 通过；`domains/streaming/TestRedisFormatCache_TTL` 的既有 TTL 过期断言失败（不在本次整改文件范围）。
- ⚠️ 未执行真实 PostgreSQL/Redis 迁移回滚、故障注入、部署主机和完整协议 E2E。
- ⚠️ 严格 secrets 扫描受仓库既有 `.env*`/示例文件命中影响；未发现本次整改新增秘密。

## 结论与后续

当前状态是“代码修复完成、核心回归通过、发布验收未完成”。在 S-3、authoritative 纯度、Session V2 owner、真实迁移/依赖/E2E 验证完成前，不应 merge/push 或宣称 GO。若业务明确允许带风险提交，需单独记录延期项、责任人和发布阻断豁免；本审计不自动豁免。

**审计人:** ZCode
**审计日期:** 2026-07-28

---

## 增量审计：154 最近 6 小时日志异常 + 模型质量探测补齐 (2026-07-28)

> **范围:** 仅针对 6h 内"请求中断 / 上游 LLM 响应不合理"的归因，以及"模型注水 / 假替代"探测能力的补齐。上一节（24h audit）的结论未变，本节是**追加**。

### 1. 6h 日志观察到的"中断/异常"形态（按现有探针归类）

| 形态 | 现象 | 直接成因 | 根因归属 | 现有探针 |
|---|---|---|---|---|
| `stream_interrupted=true` | `failure_detail_code ∈ {stream_chunk_timeout, eof_without_done, client_cancel, read_error}` | 上游连接 / 网关超时 / 客户端断开 | 上游或客户端 | `request_log_pipeline.go:441-449` + `streamErrorKindForDetailCode` (`handler.go:5149`) |
| `empty_response` + `upstream_empty_response` | 200 OK + 0 token + ≤3 chunk + `failure_detail_code=zero_tokens_few_chunks` | 200 + 0 内容（NIM pattern） | 上游/模型质量 | `detectEmptyStreamResponse` (`handler.go:5778`) |
| `failure_detail_code=eof_without_done` 但有内容 | 上游未发 [DONE] | 上游协议/模型 | 上游/模型 | 已判定 benign (`classifyStreamInterruption` `handler.go:5125`) |
| `finish_reason=content_filter` / 拒答 | Anthropic `stop_reason=refusal` → OpenAI `content_filter` | 模型策略/上游 | 翻译中已识别（`anthropic_to_chat.go:194`），**未标记为完整性事件** |
| `finish_reason=length` / 截断 | 上游达到 token 上限 | 上游/模型 | 翻译中已通过字段传出（`fields.go:51 truncation`），**未独立标记** |
| 上游返回 `model` ≠ client 请求 `model` | 模型替换 / 注水 | **代码问题** | `CheckSoftMismatch` 已实现但 OpenAI 路径未被调用（`executor_chat.go:128`）；Anthropic SSE 侧已实现（`anthropic_passthrough_stream.go:195`） |
| `tools_restore_failed` / `request_body_truncated` / `body_decode_failed` / `metadata_dropped` | 网关 IR 转换丢字段 | **代码问题** | `data_loss_anomaly.go:8-37` + recorder → `response_format_anomalies` |
| `tool_calls_missing` (IR) | IR 序列化丢 `tool_calls` | **代码问题** | `lockfree_anomaly_reporter.go:136-184` |
| `persistence_failed` / `json_marshal_failed` | request_logs 写入失败 | **代码问题** | `request_log_pipeline.go:316-427` |

**结论：**

- 大多数"中断"是上游/客户端/超时/空响应，**网关已有探针**。
- **真正缺的是模型质量探测**：模型身份不一致（OpenAI 非流 + 上游 SSE 都缺）、`finish_reason=refusal/length` 完整性标记、token 算术一致性、同一 (cred, model) 的指纹漂移。
- 现有 `response_format_anomalies` 只覆盖**格式**类异常；`finish_reason` 拒答/截断和"假替代"还没有独立表。

### 2. 本次提交的修复（feat/model-integrity-20260728）

#### 2.1 新增表 `model_integrity_events`（独立于 `response_format_anomalies`）

迁移：
- `sql/migrations/startup/462_model_integrity_events.sql` + `.down.sql`
- `deploy/sql/objects/tables/model_integrity_events.sql`（deploy 目录镜像）
- `db/db.go::ensureModelIntegrityEventsSchema`（增量 IF NOT EXISTS）

字段：`ts, request_id, tenant_id, application_id, api_key_id, provider_id, provider_code, credential_id, client_model, outbound_model, raw_model_name, anomaly_type, severity, expected_value, actual_value, sample, context, resolved, resolved_at, resolution_notes`。

`anomaly_type` 枚举（**追加不改**）：
- `model_mismatch` — 上游模型 ≠ 客户端请求
- `finish_refusal` — `finish_reason ∈ {refusal, content_filter}`
- `finish_truncation` — `finish_reason ∈ {length, max_tokens}`
- `token_arith_fail` — `prompt+completion ≠ total` 且 body 非空
- `empty_response` — 流 200 OK + 0 token + ≤3 chunk
- `repeated_content` — 256B 块在响应中重复 ≥ 2 次
- `fingerprint_drift` — `(cred, model)` 7d 内 `system_fingerprint` 主导值变化

索引：`ts DESC`, `(credential_id, raw_model_name, anomaly_type, ts DESC)`, `(provider_id, anomaly_type, ts DESC)`, `request_id`, 未解决 partial。RLS 策略与 `response_format_anomalies` 镜像。

#### 2.2 新增子包 `domains/streaming/integrity`

```
domains/streaming/integrity/
  signals.go          // AnomalyType / Severity / Event
  recorder.go         // PoolRecorder: 异步落表 + 采样 + nil-safe
  detector.go         // 8 个 signal 一次性扫描
  executor_adapter.go // 把 executors.IntegrityCandidate 适配到 integrity.Candidate
  recorder_test.go
  detector_test.go
```

设计要点：
- **采样**：`LLM_GATEWAY_INTEGRITY_SAMPLE_RATIO=0.1`，critical 必落。
- **零拷贝**：`sample` 字段只放 provider_response_id / system_fingerprint / finish_reason / chunk_count / usage_source，**绝不**放用户 prompt / 模型正文。
- **context.WithoutCancel + 3s 预算**（沿用 `data_loss_anomaly.go:41` 的 `anomalyRecordTimeout` 模式），保证客户端断开不会让记录半途而废。

#### 2.3 信号采集（全部走 async 落表）

| 信号 | 来源 | 触发点 |
|---|---|---|
| `model_mismatch` (OpenAI 非流) | 上游响应 JSON `.model` 字段 | `executors/executor_chat.go::executeOpenAI` 在 `W.Write(respBody)` 之前调用 `IntegrityDetector.Observe` |
| `model_mismatch` (OpenAI 流) | 首块 SSE `chat.completion.chunk.model` | `domains/streaming/stream.go` 在 3 处 `ir.ParseOpenAIStreamChunk` 后调用 `capture.SetRespModelIfEmpty`（新增 `audit.StreamCapture.RespModel` 字段，mutex 保护） |
| `model_mismatch` (Anthropic 流) | `message_start.message.model` | **已存在**（`anthropic_passthrough_stream.go:195`） |
| `finish_refusal` / `finish_truncation` | `finish_reason` | `domains/streaming/handler.go::emitTelemetry` 末尾 `integrityDetector.Observe` |
| `token_arith_fail` | `prompt+completion ≠ total` | 同上 |
| `empty_response` | `detectEmptyStreamResponse` 已判定 | 同上 |
| `repeated_content` | 256B 块 ≥ 2 次 | 同上 |
| `fingerprint_drift` | 7d 滚动分布 | `bg/integrity_fingerprint_drift.go`（新增，1h tick） |

#### 2.4 后台巡检 — `bg/integrity_fingerprint_drift.go`

每 1 小时跑一次：
- 时间窗：过去 7 天（env 可覆盖）
- 最小样本：20（env 可覆盖）
- 主导比例：< 80% 视为漂移（env 可覆盖）
- 命中后写一行 `anomaly_type='fingerprint_drift', severity='high'`，含 `window_days / total_samples / dominant_fingerprint` 上下文。

#### 2.5 Admin API

`/api/admin/model-integrity/{summary,events,fingerprint-drift,events/{id}/resolve}` — 全部 `superAdmin` 鉴权链（与 `format-anomalies` 镜像）。新文件 `admin/model_integrity.go` + `admin/model_integrity_test.go`。

#### 2.6 Web — 1 行 UI 芯片

`web/src/components/IntegrityChip.vue` + `web/src/api/integrity.ts`：自包含、零 store 依赖、按 color 切换（绿/橙/红/灰），点击 emit `open` 事件。**未**新建独立页面 — 由前端把 `<IntegrityChip />` 嵌入到现有模型路由/异常 dashboard 顶部即可。修改面 ≤ 1 行。

#### 2.7 main.go 装载

`cmd/gateway/main.go` 在 `anomalyHarvester.Start()` 之后挂：
```go
integrityRecorder := integrity.NewPoolRecorder(dbConn.Pool(), 0.1)
integrityDetector := integrity.NewDetector(integrityRecorder)
integrityAdapter  := integrity.NewExecutorAdapter(integrityDetector)
routingExec.IntegrityDetector = integrityAdapter
chatHandler.SetIntegrityDetector(integrityAdapter)
integrityDriftWorker = bg.NewIntegrityFingerprintDrift(dbConn.Pool())
integrityDriftWorker.Start(context.Background())
```
shutdown 链路：和 `anomalyHarvester.Stop()` 并列 `integrityDriftWorker.Stop()`。

### 3. 验收

- ✅ `go build ./...`（含 `cmd/gateway`）— 0 error
- ✅ `go test -short ./domains/streaming/...` — 3 个子包全绿
- ✅ `go test -short ./bg/...` — 全绿
- ✅ `go test -short ./admin/...` — 全绿（含新加的 6 个 model_integrity 路由测试）
- ✅ 关键文件 `gofmt` 通过
- ⚠️ `make test` 全量未跑（按 deploy-154 规则部署后才在 154 上跑）
- ⚠️ 真实 PG 迁移 / 真实 deploy / smoke test / model-smoke-test 留作 PR 描述里的 follow-up runbook

### 4. 不在本任务内（按 plan 锁定的边界）

- 不动 `_to-be-deprecated/`、`pms-go-*`、任何旧 relay 路径。
- 不重写 `request_logs` schema。
- 不修改 `quality-service` 二进制。
- 154 上的 deploy、smoke、model-smoke 留作 PR 合并后的 follow-up。
