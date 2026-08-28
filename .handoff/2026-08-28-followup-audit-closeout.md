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
- 待协作方处理：
  - 当前 staging 的 streaming/deploy/streaming 测试改动保留在工作树
    （用户先前明确禁止丢弃），需要在单独的 commit 中整合到 main。
  - 两个 stash（`stash@{0}`、`stash@{1}`）未触碰。
- 测试结果：errorsx/dispatch/streaming（含 executors 子包）全部
  -count=1 通过；race 通过；vet 干净。
- 集成 main：协作方改动与本分支不直接冲突，可单独 commit 后直接
  fast-forward merge 到 main，或在协作方同意下合并到统一 PR。
