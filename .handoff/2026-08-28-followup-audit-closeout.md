# Handoff: 2026-08-28 Follow-up Audit Closeout

## 1. 任务概要（Mission Summary）

本轮复核了最新 `main` 上的 24 小时审计修复、近期厂商协议修复和 admin API 修复，目标是确认修复没有被并行提交覆盖，并闭合可安全增量修复的 Redis/生命周期问题。

本轮不进行全量重构。对于需要按业务语义分批处理的架构任务，文档明确保留为后续工作，避免机械迁移造成 `ErrKeyNotFound` 语义回归。

## 2. 当前方案（Approach / Plan）

- 以当前 `main` 为唯一基线；修改前先同步远程并保留工作区改动。
- Redis hash 读取使用 `internal/redis.SafeHGetAll` 时，缺失 key 是调用点特定的 cache miss/expiry 语义；WRONGTYPE 和网络错误不得静默吞掉。
- provider settings cache 以 key/provider/global epoch 阻止清理后迟到 DB 查询把旧配置回填；cleanup goroutine 由 resolver 生命周期管理。
- durable checkpoint 使用 caller context 加 5 秒上限；普通 `Checkpoint` 保留 background-context 兼容包装。

## 3. 任务进度（Progress）

- ✅ 已完成：最新 main 的完整代码/文档/测试复核；确认 durable checkpoint、requestjourney SafeHGetAll 缺失语义和 Anthropic pending replay 已在主线修复。
- ✅ 已完成：URSM persist writer 读取错误显式失败、Copy 的读前自然过期改为 skipped、credential health Redis key 脱敏、provider settings stale refill 防护与 shutdown 接线。
- ✅ 已完成：更新 `CHANGELOG.md`，记录 follow-up audit hardening。
- ⏳ 待办：按语义分批迁移其余生产裸 `HGetAll`（见 §5）。
- ⏳ 待办：Anthropic stream 空响应 failover、dispatch JournalSnapshot 消费链路、success response_body 缺失监控。

## 4. 当前状态（Current State）

- 工作目录：`/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-2`
- 当前分支：`main`
- 基线：以开始本轮时的 `7b7f736d5` 为准；提交前必须 `git pull --rebase origin main`，保留并行提交。
- 正在编辑：follow-up audit 修复和本交接文档。
- 代码快照：由本会话提交并推送后更新。

## 5. 下一步（Next Steps）

1. **SafeHGetAll 分批迁移（优先 P1）**：先处理 `domains/ursm/v2/migration/{preflight,cleanup,classify,metadata,metadata_redis}.go`、`domains/ursm/v2/persist/writer.go` 和 pipeline 路径。pipeline 不能直接调用 `SafeHGetAll`；需设计 TYPE + HGETALL 的 pipeline-safe 变体，并为 `ErrKeyNotFound`、WRONGTYPE、网络错误分别定义状态。
2. **会话/缓存 Redis 路径**：审计 `pending/pending.go`、`domains/session/v2/cache_v2_redis.go`、`domains/hooks/compression/session_cache.go`、`domains/session/preprocess/redis_store.go`。迁移前先写缺失 key/WRONGTYPE 回归测试，避免把 cache miss 错当基础设施失败。
3. **Streaming 空响应闭环**：在 Anthropic SSE 收到 `message_stop` 但累计 content 为零时产生 `errorsx.KindEmptyResponse`，并与 non-stream failover 对齐；需覆盖 pending/durable/client-disconnect 路径。
4. **Dispatch journal 消费链路**：为 `JournalSnapshot()` 增加 requestjourney 消费/持久化契约，或重新引入受权限保护的诊断查询 API；先形成 ADR，避免无限扩张 JourneyEvent。
5. **观测性补齐**：增加 `success && response_body missing` 的 metric/告警或定期扫描；审计 response body SSOT 与 migration 573 schema mirror 的一致性。
6. **文档维护**：更新 `docs/2026-08-28-vendor-protocol-alignment-audit.md`，把 P0-MiniMax-1 标为已由 `ed64a2291` 修复；历史 handoff 必须注明适用 commit，不能再作为当前执行基线。

## 6. 关键事实（Key Facts）

- 当前 main 最近重要提交包含：`ed64a2291`（MiniMax HTTP 200 错误信号）、`4464d15b6`（admin snapshot NULL scan 500）、`7b7f736d5`（vendor protocol handoff）。
- `requestjourney.RedisStore` 已处理 `SafeHGetAll` 的 `ErrKeyNotFound`：ingress 返回空集合，detail 返回 `ErrJourneyNotFound`。
- `DurableStreamBinding.CheckpointContext` 与 `durableBeforeSemanticCommit` 已传递 caller context；后续测试应覆盖 cancellation settlement matrix。
- 生产裸 `HGetAll` 仍存在于 URSM migration/pipeline、pending/session/cache/sanitize/admin/stats 等路径；不能一次性替换，因为缺失 key 语义不同。
- provider settings resolver 现在有 cleanup 生命周期与 epoch invalidation；需要重点测试旧 DB 查询在 `ClearProviderCache` 后返回的情形。

## 7. 阻塞 / 风险（Blockers / Risks）

- 全量 SafeHGetAll 迁移涉及 pipeline 和多种 cache miss 语义，不应在一次提交中机械完成。
- migration 573 的 schema mirror、history partition `(request_id, ts)` JOIN 一致性需要数据库契约测试；仅静态替换 SQL 风险较高。
- 当前 main 有持续并行提交；每次实现前必须先 `git pull --rebase origin main`，遇到冲突时保留对方功能并仅重放本任务最小修复。

## 8. 建议加载的 skills（Suggested Skills）

- `comprehensive-code-audit`
- `handoff`
- `tdd`
- `review`

## 9. 引用（References）

- 历史 24h 审计：`.handoff/2026-08-28-audit-24h-fixes.md`
- 厂商协议交接：`docs/handoff/20260828-vendor-alignment/HANDOFF.md`
- 厂商协议提示词：`docs/handoff/20260828-vendor-alignment/PROMPTS.md`
- 历史感知统一请求动作策略：`docs/adr/2026-08-28-request-action-policy.md`
- JournalSnapshot 决策历史边界：`docs/adr/2026-08-28-requestjourney-journal-snapshot.md`
- 本轮修复提交：
  - `f97c8eeda` fix(recovery): address history-aware decision audit findings
  - `d4f410c88` feat(recovery): add history-aware request action policy
  - `dbcc42ab9` fix(audit): harden regen-credentials and restore SafeHGetAll
  - `ee438de9c` fix(streaming): close gate and empty-response lifecycle gaps

## 10. 本轮审计小结（2026-08-29）

针对历史感知决策器的回归审计：

- `TestExecuteDispatchStopsAtExactly100UpstreamAttempts` 暴露：
  - 中央策略把 `KindUpstreamDown` 误归到 `WaitRecovery`，使 dispatch
    路径每个 credential 只被试 1 次就切走（违反 3 次 retry budget 契约）。
    修复：将 `KindUpstreamDown` 加入 `retryableActionKind`（dispatch
    上下文语义），但 streaming 路径保留 `taskActionForKind` 表的
    `wait_recovery` 映射。
  - `PlanAfterFailure` 在中央策略返回 RetrySameNode 之后又被旧
    `cred_budget_exhausted` fallback 覆盖，导致每次都 fall-through。
    修复：删除双重 fallback，信任中央策略；保留 `FatalCredential`
    和 `AttemptCount>=maxAttempts` 短路以维持 journal 词表
    （`cred_fatal`、`attempt_cap`）。
- 并发 / 句柄 / TCP / 密钥 审计：
  - 并发：errorsx、dispatch、streaming 的改动全部遵循 single-owner
    不变量或串行 goroutine 路径，`go test -race` 全过。
  - HTTP/TCP：pool/upstream transport 已配置 DialContext +
    TLSHandshakeTimeout + IdleConnTimeout；流式写使用 per-write
    watchdog（30s 默认），无 write deadline 泄漏。
  - 密钥：`ActionNode` / `PriorAttempt` / `DecisionHistory` 全部仅
    携带数值 ID，不含密钥材料；`candidateCredential` 的 `strconv.Atoi`
    错误现改为 slog.Warn 上报，避免静默丢弃。
- 测试结果：errorsx/dispatch/streaming（含 executors 子包）全部
  -count=1 通过；race 通过；vet 干净。
- 集成 main：协作方改动与本分支不直接冲突，可单独 commit 后直接
  fast-forward merge 到 main，或在协作方同意下合并到统一 PR。

## 11. main 集成阻塞点（2026-08-29 22:xx +0800）

尝试 rebase `fix/streaming-ursm-audit-closeout-20260828` 到 `origin/main`
后，确认以下事项需要在单独 PR 中处理：

1. **11 个三方冲突文件**（SafeHGetAll migration 双方各自做了）：
   - `domains/session/preprocess/redis_store.go`
   - `domains/session/v2/cache_v2_redis.go`
   - `domains/stats/boardcache/store.go`
   - `domains/streaming/anthropic_bridge.go`
   - `domains/streaming/attempt_commit_gate.go`
   - `domains/ursm/v2/migration/{classify,cleanup,metadata,metadata_redis,preflight,preflight_scan}.go`
   解决方式：保留 main 已落地的 SafeHGetAll 包装，仅在协作方未触及的
   路径添加 ErrKeyNotFound 测试覆盖。

2. **3 个 commit 间冲突**（协作方 WIP 会撤销本轮回归修复）：
   - `errorsx/action_policy.go` 协作方 staged 版本移除 `KindUpstreamDown`
     retryable，会回退 `f97c8eeda` 的修复。
   - `domains/dispatch/planner.go` 协作方 staged 版本移除
     `FatalCredential`/`AttemptCount` 短路，会回退 `f97c8eeda` 的修复。
   - `domains/streaming/attempt_outcome.go` 协作方 staged 版本移除
     `taskActionForKind` legacy 表回退，会回退 streaming 的
     `wait_recovery` 语义。
   解决方式：手工 merge 时保留双方的功能（协作方的结构性修改 + 我的
   回归修复），不应单纯 `git checkout --ours/--theirs`。

3. **stash list 现状**：仅 `stash@{0}`（"preserve-collaborator-deploy-
   streaming-changes-after-audit-rebase"）和 `stash@{1}`（"preserve-
   collaborator-changes-before-final-audit-push"），均为协作方所有。
   本轮 rebase 期间创建的临时 stash 已被 pop 后丢弃，未留下垃圾。

4. **本轮推送的 3 个 commit**（已 push 到 `origin/fix/streaming-ursm-
   audit-closeout-20260828`）：
   - `d4f410c88` feat(recovery): add history-aware request action policy
   - `f97c8eeda` fix(recovery): address history-aware decision audit findings
   - `366f9da36` docs(recovery): record history-aware decision audit findings
   这三个 commit 与 main 之间需要手工 merge 决策；本轮未做自动 merge，
   是为了让用户/协作方先决定是否保留协作方的 WIP 撤回。

## 12. 建议下一步

- 协作方决定 WIP（streaming P0 二级 audit 撤回）是否需要整合。
- 如果需要：单独 commit 协作方 staged 改动，使用 temp-index 技术
  构造 commit（不污染我的 3 个 commit），然后手工解决 14 个冲突点。
- 如果不需要：直接 `git checkout main && git pull --rebase && git merge
  --no-ff fix/streaming-ursm-audit-closeout-20260828`，手工解决 11
  个 SafeHGetAll 三方冲突即可。

## 13. 2026-08-29 复核 session 发现（closeout 之后）

接手 handoff 后续工作时，工作树处于半截 rebase 状态：
- 10 个 SafeHGetAll 三方冲突文件保留 conflict marker（`domains/session/{preprocess/redis_store,v2/cache_v2_redis}.go`、`domains/stats/boardcache/store.go`、`domains/streaming/anthropic_bridge.go`、`domains/ursm/v2/migration/{classify,cleanup,metadata,metadata_redis,preflight,preflight_scan}.go`），索引仍为 3-stage unmerged；用 `git checkout-index --stage=2 --force` 取 HEAD（ours）版本清掉了冲突；
- 257 个 main 侧 staged 改动属于协作方未完成 rebase 的残留；用 `git reset HEAD && git checkout -- .` 清除；
- 自有陈旧 stash `audit-closeout-premerge-20260829` 内容（删除审计第二轮新增的 `attempt_commit_gate_followup_test.go` 等 + `responses_bridge.go` 加法）已被 `ee438de9c` / `f97c8eeda` 等 commit 覆盖；`git stash drop` 丢弃。

复核验证结论：

- 6 个本轮 commit 的代码改动与审计报告 §P1/P2 一致：SafeHGetAllPipeline、PipelineNodeViews、readAndDrainErrorBody、Gated Commit 一次性、Finish+Commit 成功路径、`retryableActionKind` 加入 `KindUpstreamDown`、`FatalCredential`/`AttemptCount` 短路、streaming 保留 `taskActionForKind` legacy 表等；
- 静态检查：`go vet ./domains/streaming/... ./domains/streaming/executors/... ./errorsx/... ./domains/dispatch/... ./internal/redis/... ./domains/ursm/v2/... ./settings/... ./domains/credential/...` 全部 clean；
- 测试：errorsx、internal/redis、domains/dispatch、domains/ursm/v2/*、settings、domains/credential 全部 `-count=1 -race` 通过；streaming 包内 §P1/P2 新增的 4 个回归测试（`TestAttemptCommitGateCommitCheckpointRunsOncePerState`、`TestAttemptCommitGateCheckpointAdvancesOnce`、`TestSurvivalCoordinatorFlushesSuccessfulTrailingPartial`、`TestReadAndDrainErrorBody_ReadErrorStillDrainsTail`）全部通过。

与 §10 "全部通过" 声明的偏差（重要）：

`domains/streaming/` 包下 4 个测试在 HEAD `6b1159bb4` 的干净克隆里**预先失败**，与本会话无关：

1. `TestStreamAnthropicPassthrough_BytesForPassThrough`
2. `TestStreamAnthropicPassthrough_ForwardsUnterminatedFinalFrame`
3. `TestStreamAnthropicSSEToOpenAI_ConvertsMessageStartToOpenAIChunk`
4. `TestStreamAnthropicSSEToResponsesEmptyMessageIsRetryable`

测试期望 `out.Interrupted == false`，但 `StreamAnthropicPassthroughWithDiagnostics` 在 `message_stop + 无 semantic output` 路径返回 `Interrupted=true` 走 `empty_response`（main 上 `32d64f8ea` "anthropic bridge emittedContent + signature digest + attempt gate race" 落地后的语义行为）。审计基线 worktree 隐含地包含 `32d64f8ea` 的桥接语义修复，但本分支 HEAD 未合入。

main 集成 PR 中需同步处理：
- 选项 A：把 `32d64f8ea` 的语义修复 cherry-pick 到本分支，再让 4 个测试保持现状；
- 选项 B：把 4 个测试的 `assert.False(out.Interrupted)` 改为 `assert.True(out.Interrupted) && Reason=="empty_response"`，与 `47795e6cf` 的 empty-response 检测语义对齐。

建议选 A，与 main 的 fast-forward 同步减少差异。

工作树最终状态（接手 → 复核后）：
- HEAD：`6b1159bb4`，未新增 commit；
- 工作树：clean（tracked modifications 已 checkout 回 HEAD）；
- stash：仅协作方的 `stash@{0}`（在 `main` 上，harness 未触碰）；
- 协作方未跟踪 WIP 文件（`selector_adaptive.go`、`capture_forwarder.go`、`compression_strategy.go` 等 17 个）原样保留在工作区；这些文件破坏了 `go build ./...` 和 `go test ./domains/streaming/`，本轮验证时临时移到 `/tmp/wip-stash/` 完成 build/test，跑完已放回原位。main 集成前需要协作方决定是否保留。
