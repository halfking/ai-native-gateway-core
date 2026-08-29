# 2026-08-29 §4.1-§4.4 落地汇总

承接 `.handoff/2026-08-29-section5-recheck.md` 的 4 个行动项，本轮
全部完成：

- **§4.1** 核查 + 新增回归测试
- **§4.2** 调研 + 两方向证据整理（需业务方拍板）
- **§4.3** 接口扩展 + 5 个 contract tests + ADR Accepted
- **§4.4** 新 metric + slog + ADR Accepted

工作目录：`/Users/xutaohuang/workspace/ai-native-tools/syncfield/
llm-gateway-go-2`
分支：`main`，HEAD `cd84808cb`，未提交（变更在工作树）

## §4.1 — hooks/compression HGetAll 核查 + 回归测试（P1）

**结论**：handoff §3.2 标记的核查点 #1 实际已经自然闭合。
`cmd/gateway/main_v3_wiring.go:57-70` 的 `redisBackendAdapter.HGetAll`
早已在 P1-14 (2026-08-28) 切到 `redissafe.SafeHGetAll`，并把
`ErrKeyNotFound` 翻译成空 map + nil（保持 go-redis HGETALL 契约），
`WRONGTYPE` 错误原样透传。

**新增**：`cmd/gateway/main_v3_wiring_test.go`（3 个 test）：

- `TestRedisBackendAdapter_HGetAll_KeyNotFoundCollapsesToEmptyMap`
- `TestRedisBackendAdapter_HGetAll_HappyPathReturnsFields`
- `TestRedisBackendAdapter_HGetAll_WrongTypePropagates`

通过 miniredis 注入 `*session.RedisClient`，断言三层行为（missing /
happy / WRONGTYPE）。

## §4.2 — §5.3 outcome 方向证据（P1，需业务方确认）

**结论**：「`client_write_failed` 错被优先于 `client_disconnected`」并
不存在。`anthropic_bridge.go:78-90` 的 `applyClientDisconnectOutcome`
是按「upstreamCompleted」分流：

- `upstreamCompleted=true`（走完 SSE 流 + 中途 chunk write 失败）→
  `client_disconnected`
- `upstreamCompleted=false`（中途非中断返回 + write 失败）→
  `client_write_failed`

测试 `TestStreamAnthropicSSEToOpenAI_DisconnectsKeepsCapturer` 当前
PASS；`domains/streaming -race -count=1` 全包绿（68.4s）。

**新增**（不动代码）：
`.handoff/2026-08-29-section5-outcome-decision.md` — 两方向 A/B 的
failover 边界证据 + 推荐 A（保持当前行为），等业务方 PR review。

## §4.3 — JournalSnapshot contract tests（P2）

**改动**：

- `domains/dispatch/observation.go`：扩展 `JournalSnapshot` 结构体，
  新增 `SnapshotMetadata` 类型。新增字段：`SnapshotVersion int64`、
  `CallerTenantID string`、`CallerAuthorized bool`、`Metadata
  SnapshotMetadata`。原 3 字段保留，向后兼容。
- `domains/dispatch/pipeline.go:1523`：`emitJournalSnapshot` 填新字
  段。`SnapshotVersion = int64(qr.journalSeq)`；`Metadata.TotalEntries`
  从 `qr.journalSeq` 算（而不是 `len(qr.AttemptJournal)`，因为 ring
  overflow 已经截断过）。
- `cmd/gateway/main_dispatch_observation.go`：sink 实现
  authorization check（`CallerAuthorized` 与 tenant mismatch）与
  idempotency 短路（`MaxSeq >= SnapshotVersion` 直接 return）。
- `domains/dispatch/journal_snapshot_contract_test.go`：5 个 test：
  - `Authorization_TenantMismatchContract`（3 个 subtest）
  - `Authorization_PipelineStampsTrustedCaller`
  - `BoundedTruncated_OverflowMarksMetadata`
  - `BoundedTruncated_NonOverflowClearsMetadata`
  - `Idempotency_SnapshotVersionEqualsJournalSeq`
  - `Idempotency_DuplicateRetryRecomputesSameVersion`
  - `Idempotency_ProjectionDedupsByteEqualReplay`
  - `PersistenceFailure_IsolatesSettlement`
- `docs/adr/2026-08-28-requestjourney-journal-snapshot.md`：
  Status `Proposed → Accepted`，§Consequences 列 4 类 contract tests
  实现证据。

**回归**：`go test ./domains/dispatch/ -count=1 -race` 全绿（27.1s）；
`./domains/requestjourney/` 全绿（3.9s）；`./cmd/gateway/` 全绿
（1.9s）。

## §4.4 — success && response_body missing 观测（P2）

**改动**：

- `domains/hooks/observability/telemetry/empty_response_metrics.go`
  （新文件）：
  - `successResponseBodyMissingTotal` CounterVec，labels = `{protocol,
    stream}`，pre-init 6 个 cell
  - `recordEmptyResponseBody(entry)` 通过 onPersisted hook 在
    `persistRequestLog` 成功后检测 `Success && (ResponseBody nil /
    all-whitespace) && (StreamChunkCount nil || == 0)`，命中则
    `.Inc()` + `slog.Warn`
  - `RegisterEmptyResponseGate(*Client)` 显式 opt-in API
  - `hasMeaningfulResponseBody` 分类器把 `""` / 全空白视为 missing
- `domains/hooks/observability/telemetry/empty_response_metrics_test.go`
  （新文件）：9 个 subtest，覆盖：register / nil-safe / 成功且空 body
  +1 / 失败不增 / 非空不增 / 全白 / 流式分流 / gate 禁用 / boot label
  surface
- `cmd/gateway/main.go:1947`：`telemetryClient := telemetry.NewClient()`
  之后立刻 `telemetry.RegisterEmptyResponseGate(telemetryClient)`
- `docs/adr/2026-08-29-success-empty-response-body.md`（新文件）：
  Status **Accepted**，定义 alert surface = `(rate(non_stream) > 0
  over 5m)`

**回归**：`./domains/hooks/observability/...` 全绿（4.3s）；
`./cmd/gateway/` 全绿（0.85s）。

## 累计改动统计

```
新增文件：
  cmd/gateway/main_v3_wiring_test.go
  domains/dispatch/journal_snapshot_contract_test.go
  domains/hooks/observability/telemetry/empty_response_metrics.go
  domains/hooks/observability/telemetry/empty_response_metrics_test.go
  docs/adr/2026-08-29-success-empty-response-body.md
  .handoff/2026-08-29-section5-outcome-decision.md

修改文件：
  domains/dispatch/observation.go            (+43 行，struct 扩展)
  domains/dispatch/pipeline.go               (+33 行，emitJournalSnapshot 改造)
  cmd/gateway/main_dispatch_observation.go   (+25 行，auth + idempotency check)
  cmd/gateway/main.go                        (+5 行，gate 注册)
  docs/adr/2026-08-28-requestjourney-journal-snapshot.md  (Status Accepted)
```

未提交、未推送。

## 阻塞 / 风险

- **§4.2**：业务方需在 PR review 时勾选方向 A 或 B，本会话未擅自落地
  任何 streaming 代码改动。文件
  `.handoff/2026-08-29-section5-outcome-decision.md` 是评审入口。
- **§4.3 新 metric 兼容性**：扩展 `JournalSnapshot` 是 public struct
  扩展，向后兼容。但下游若有第三方消费者依赖 3-字段形态，会自动获得
  zero-valued 新字段——这是预期行为。grep 仓库内构造点全部已 compile
  通过。
- **§4.4 dashboard 消费**：`success_response_body_missing_total` 是新
  counter，需要 ops 端建立对应 dashboard 与 alert 路由。ADR 已写明
  「非零即 page」。

## 引用

- `.handoff/2026-08-29-section5-recheck.md` §4.1-§4.4（输入）
- `.handoff/2026-08-29-section5-outcome-decision.md`（§4.2 输出）
- `docs/adr/2026-08-28-requestjourney-journal-snapshot.md`（§4.3 ADR）
- `docs/adr/2026-08-29-success-empty-response-body.md`（§4.4 ADR）
