# 2026-08-29 §5 自然闭合复核 + 剩余项盘点

## 1. 任务概要

承接 `.handoff/2026-08-29-followup-streaming-recheck.md` §9「本轮未做
（留给下一会话）」，对照当日合并的 `cd84808cb`（"merge: integrate
history-aware recovery action policy into main"）再次落点 §5 全部 6 项，
发现 3 项已被前置批次自然闭合、2 项处于半开、1 项仍待设计。本文件是
复核结论与下一会话的实际行动项，**没有新增 commit**。

## 2. §5 状态矩阵

| §  | 项目 | 状态 | 证据 |
|---|---|---|---|
| 5.1 | SafeHGetAll URSM migration 迁移 | **自然闭合** | 见 §3.1 |
| 5.2 | 缺失 key / WRONGTYPE 回归测试 + 4 路径迁移 | **自然闭合** | 见 §3.2 |
| 5.3 | Streaming 空响应闭环（pending/durable/disconnect） | **半开** | 见 §3.3 |
| 5.4 | dispatch JournalSnapshot ADR + 受限消费者 | **半开**（ADR 在，contract tests 缺） | 见 §3.4 |
| 5.5 | `success && response_body missing` 观测性 | **仍待设计** | 见 §3.5 |
| 5.6 | P0-MiniMax-1 状态更新到 vendor-protocol doc | **自然闭合** | 见 §3.6 |

## 3. 各项证据

### 3.1 §5.1 — SafeHGetAll URSM migration 已闭合

`domains/ursm/v2/migration/` 全部生产 call sites 已切到
`redissafe.SafeHGetAll`：

- `classify.go:77`
- `cleanup.go:347`, `cleanup.go:469`
- `copy.go:155`, `copy.go:184`, `copy.go:321`
- `metadata.go:285`
- `metadata_redis.go:96`
- `preflight.go:235`, `preflight.go:311`
- `preflight_scan.go:133`

`domains/ursm/v2/persist/writer.go:66, 100` 已用 `SafeHGetAll`。

Pipeline-safe 变体已存在并被调用：
- `internal/redis/safe_pipeline.go:25` `SafeHGetAllPipeline(ctx, pipe, keys)`
  一次 TYPE pipeline 验证 keys 类型，再只 HGETALL 哈希键；三态分类
  `ErrKeyNotFound` / WRONGTYPE / 网络错误。
- `domains/ursm/v2/store/pipeline.go:67, 95` 已用 `SafeHGetAllPipeline`。

仅存的 raw `HGetAll` 全部在 `*_test.go` 里（`canonical_cleanup_regression_test.go:71`、
`classify_test.go:273`、`cleanup_test.go:220/234`、`copy_test.go:83/221`、
`g4_isolated_redis_evidence_test.go:389/405/478`、`metadata_test.go:115`），
这些是有意为之——测试需要直接 Redis 才能构造 WRONGTYPE fixture。

验证：

```
$ go test ./domains/ursm/v2/... -count=1 -race -timeout 180s
ok  	github.com/kaixuan/llm-gateway-go/domains/ursm/v2	7.613s
ok  	github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api	3.622s
...（15 个子包全部 ok）
```

### 3.2 §5.2 — 缺失 key / WRONGTYPE 回归测试与 4 路径迁移已闭合

四条 Redis 路径全部切到 `SafeHGetAll`：

- `pending/pending.go:202, 349`
- `domains/session/v2/cache_v2_redis.go:112`
- `domains/hooks/compression/session_cache.go:570`（间接：经由接口
  `HGetAll`，需要查实现端是否走 SafeHGetAll —— 下一会话核查点 #1）
- `domains/session/preprocess/redis_store.go:341`

回归测试覆盖（`audit-24h-20260828-r4 P2` 系列）：

- `internal/redis/safe_pipeline_test.go` `TestSafeHGetAllPipelineClassifiesMixedKeys`
- `internal/redis/safe_operations_test.go` 对 `ErrKeyNotFound` / WRONGTYPE
  / network error 分类均有 case

核查点 #1：`domains/hooks/compression/session_cache.go:240` 的
`HGetAll(ctx, key) (map[string]string, error)` 是接口方法，line 570
调的是 `c.redis.HGetAll(ctx, ...)`。需要确认实现侧（Redis client
wrapper）是否已经把 `HGetAll` 内部切到 `SafeHGetAll`，或者调用点应
改成直接调 `redissafe.SafeHGetAll`。**这属于 §5.2 真正剩下的唯一
空隙**，但因为 handoff §5.2 把 hooks/compression 列在四条路径里，
应优先确认。下一会话行动项见 §4.1。

### 3.3 §5.3 — Streaming 空响应闭环仍半开

`StreamAnthropicSSEToOpenAI empty-response` 已在 `b4e4449d3` 修齐
`pc != nil` 短路，`anthropic_bridge.go:1205` 走 helper
`IsAnthropicStreamEmpty`。

但上一会话 §3.2 末尾留的开放问题尚未解决：

> `StreamAnthropicSSEToOpenAIWithDiagnostics` 的 outcome 分类与 `pc`
> 写入路径，看 `client_write_failed` 是否在 disconnect 路径上被错误
> 优先于 `client_disconnected`。

仍未定方向：（a）接受新行为，测试改写；（b）恢复旧行为，复测所有
failover 边界。**单独 PR 处理**，下一会话行动项见 §4.2。

### 3.4 §5.4 — JournalSnapshot ADR 已有，contract tests 缺

`docs/adr/2026-08-28-requestjourney-journal-snapshot.md` 66 行，Status:
**Proposed**（不是 Accepted）。决策要点：

1. 受限消费者接口；不直接扫 Redis
2. requestjourney store 仍是 read-model SSOT；不复制 body
3. bounded by retention + max event count/size；truncation 显式
4. tenant + auth context 必传；未授权返回 not-found-shaped 结果
5. 持久化仅 summary + integrity metadata；失败必须可观测但不影响 settlement
6. `(tenant_id, request_id, snapshot_version)` 幂等

实现现状：`domains/dispatch/observation.go:262 JournalSnapshot`、
`:275 ApplyJournalSnapshot` 已存在；`cmd/gateway/main_dispatch_observation.go:96`
已用 `dispatchJourneyJournalAdapter` 把 snapshot 喂到 requestjourney。

**缺口**：ADR §Consequences 列的 contract tests（authorization、
bounded/truncated、duplicate retry、persistence failure isolation）
均未落地。下一会话行动项见 §4.3。

### 3.5 §5.5 — `success && response_body missing` 仍待设计

当前状态：

- `errorsx.KindEmptyResponse`（streaming 路径已分类）
- 流式中断下 `StreamCapture.TextContentSnapshot()` / `PreviewSnapshot()`
  已把 partial 输出回写到 `request_logs.response_body`（`audit.go:520`）
- 非流式：没有 `success && response_body missing` 的检测 / metric / 告警
  路径；唯一关联是 `domains/hooks/observability/telemetry/client.go:1481`
  把 `has_response_body` 推到 Prometheus（仅 telemetry 字段，未单独
  设告警阈值）
- `cmd/tools/validate_sessions_v2/loader.go:131` 用 `COALESCE(response, '{}'::jsonb)`
  把 NULL response 兜底为 `{}`，但这只是离线校验工具，没有运行时检测

下一会话行动项见 §4.4。

### 3.6 §5.6 — P0-MiniMax-1 状态更新已闭合

`docs/2026-08-28-vendor-protocol-alignment-audit.md` 现状：

- line 18 表格：「MiniMax | base_resp.status_code 错误信号（已由
  `ed64a2291` 修复）| **P0** | 流式+非流式现已正确识别 HTTP 200 包装错误」
- line 120：「✅ **P0-MiniMax-1 已修复（`ed64a2291`）**」
- line 125：「**缺陷 P0-MiniMax-1（已修复，`ed64a2291`）**」
- line 258：「#### P0-MiniMax-1: base_resp.status_code 错误信号丢失（已修复）」

文档已经标"已修复"，且明确指向 `ed64a2291`。handoff §5.6 的"标为已
由 `ed64a2291` 修复"已完成，无新动作。**注意 §5.6 也提到"历史 handoff
必须注明适用 commit"**——本文件抬头已写 `cd84808cb`，可作为下一会话
的现基线引用。

## 4. 下一会话行动项（按优先级）

### 4.1（P1，1 小时）核查 `hooks/compression` 的 `HGetAll` 是否真走 SafeHGetAll

- 读 `domains/hooks/compression/session_cache.go:230–250` 接口上下文
  和 `:560–580` 调用点
- 读实现侧（mock / Redis client），确认 `HGetAll` 方法是否在内部调
  `redissafe.SafeHGetAll`
- 如果不是：把 `session_cache.go:570` 改成直接调 `SafeHGetAll`
- 加 WRONGTYPE 回归测试（同 §5.2 其它路径风格）

### 4.2（P1，半天）§5.3 outcome 分类方向决定 + 实现

- 读 `domains/streaming/anthropic_bridge.go:1185` `case ir.ChunkTypeDone:`
  分支上下文
- 读 `domains/streaming/pending_disconnect_extra_test.go:96–139`
  当前期望与 `pc.Snapshot()` 实际行为
- 读 `StreamAnthropicSSEToOpenAIWithDiagnostics` outcome 分类代码，
  比较 `client_write_failed` vs `client_disconnected` 优先级
- 决定方向：
  - (a) 测试接受新行为 — 修改 `assert` 与 fixtures
  - (b) 恢复旧行为 — 修 `outcome` 写入顺序
- **禁止两边都改**——必须 commit 一次性方向变更

### 4.3（P2，半天）§5.4 JournalSnapshot contract tests

按 ADR §Consequences 写 4 类测试：

1. **authorization**：tenant 不匹配 → not-found-shaped；未授权
   （无 caller context）→ 同上
2. **bounded/truncated**：注入超 max event count / 超 max serialized
   size 的 journal，断言 metadata.truncated == true 且 result.events
   数 = max
3. **duplicate retry**：同一 `(tenant_id, request_id, snapshot_version)`
   调两次，断言持久化 summary 只写一次
4. **persistence failure isolation**：让持久化故意失败（mock store
   返回 error），断言 settlement 仍 completed + 失败被记录到 metric

测试放 `domains/dispatch/journal_snapshot_contract_test.go`。

### 4.4（P2，1 天）§5.5 `success && response_body missing` 观测性

设计方案（待 review 后落地）：

1. 同步路径 `domains/streaming/handler.go` 末尾 / 非流式响应路径：
   `success==true && response_body==nil/""` 时：
   - slog.Warn 含 request_id / tenant_id / model
   - 推到 Prometheus counter `success_response_body_missing_total`
   - 不改变 settlement（按 §5.4 同样原则）
2. 异步扫描：定期扫 `request_logs` 表
   `WHERE success=true AND (response IS NULL OR response = '{}')`
   写日聚合 metric，避免漏掉长尾
3. ADR：单写 `docs/adr/2026-08-29-success-empty-response-body.md`
   说明失败模式 + metric 阈值 + alert 路由

注：当前 `telemetry/client.go:1481 has_response_body` 是布尔 label
不是 counter，需要单独 metric。

## 5. 当前状态（Current State）

- 工作目录：`/Users/xutaohuang/workspace/ai-native-tools/syncfield/
  llm-gateway-go-2`
- 分支：`main`，HEAD `cd84808cb`，与 `origin/main` 一致
- 工作树：clean（本会话只读，未修改代码）
- 未提交：无
- 未推送：无
- 新增测试：`go test ./domains/ursm/v2/... -count=1 -race` 全绿

## 6. 本轮提交记录

无 commit；纯复核与文档。

## 7. 阻塞 / 风险

- §4.2（streaming outcome 方向决定）属于观测性问题，需要业务方
  确认「测试接受新行为」还是「恢复旧行为」——两个方向都可行但
  决定前不能盲目落地。提议在 PR description 里列出两个方向的实际
  failover 边界证据，让 reviewer 选择。
- §4.4（success-empty-response 观测）涉及 metric schema，需要先
  与 telemetry/alert 路由对齐，避免新 counter 没有 dashboard 消费。

## 8. 引用

- 上游 handoff：`.handoff/2026-08-29-followup-streaming-recheck.md` §9
- §5 来源：`.handoff/2026-08-28-followup-audit-closeout.md` §5
- §5.1 验证：`internal/redis/safe_pipeline.go:25`、
  `internal/redis/safe_operations.go:119`
- §5.3 上游：`.handoff/2026-08-29-followup-streaming-recheck.md` §3.2
- §5.4 ADR：`docs/adr/2026-08-28-requestjourney-journal-snapshot.md`
- §5.4 实现：`domains/dispatch/observation.go:262`、
  `cmd/gateway/main_dispatch_observation.go:96`
- §5.5 telemetry 字段：`domains/hooks/observability/telemetry/client.go:1481`
- §5.5 兜底：`cmd/tools/validate_sessions_v2/loader.go:131`
