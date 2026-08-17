# 48 小时改动审计与修正报告 (2026-08-07)

> **审计时间**: 2026-08-07 09:00 - 11:30 (CST)  
> **审计范围**: `git log --since="48 hours ago"` — 100 commits · 116 files · +21,700 / −354 lines  
> **审计方式**: 初轮三路并行 read-only 子代理（会话管理 API / 会话 V2 压缩 / Web composable + IR），第二轮三路并行（熔断器+流式 / model-quality worker / auto-title/summary/end-user），结论逐条回仓库与 schema 验证

## 一、执行摘要

审计分两轮：**初轮**发现 **2 个 P1 真实 bug**（已修）+ **1 个 P2**（已修）+ **1 个被误判的 P0**（驳回，见 §三）；**第二轮**深入生产热路径，发现 **3 个 P1**（已修）+ **4 个 P2**（已修）。全部修正通过 `go build ./...`、相关包测试全绿（executors 5.5s / modelquality 346s / bg cached）与 `vitest`（31 文件 / 191 用例全绿）+ `vue-tsc --noEmit`（exit 0）。

### 初轮（§二）

| # | 严重度 | 主题 | 文件 | 状态 |
|---|--------|------|------|------|
| 1 | **P1** | V2 `SessionTurnsReader.LoadState` 把 `pgx.ErrNoRows` 当硬错 → 新会话永远回退 V1 | `domains/session/v2/cache_v2.go` | ✅ 已修 + 真库测试 |
| 2 | **P1** | `useLiveStreamFilters.applyAgentFilter` 抽取时丢失 `.toLowerCase()` 归一化 | `web/src/composables/useLiveStreamFilters.ts` | ✅ 已修 + 回归测试 |
| 3 | **P2** | `SessionCacheV2` 无 `Close()`，V2 Redis 连接池无优雅释放入口 | `domains/session/v2/cache_v2.go` | ✅ 已加 `Close()` |
| — | 驳回 | 误判 P0：「`session_summaries` 引用不存在的列，所有会话管理端点 500」 | — | ❌ 驳回（见 §三）|

### 第二轮（§四）

| # | 严重度 | 主题 | 文件 | 状态 |
|---|--------|------|------|------|
| 4 | **P1** | 压缩上游 4xx（除 429）探针槽位泄漏 — 正是 70fe6d81 应该修但遗漏的 bug 类 | `domains/streaming/executors/context_summarize.go:500-504` | ✅ 已修 |
| 5 | **P1** | `ScheduleInterval == 0` 时 `time.NewTicker` panic | `domains/modelquality/monitor.go:122` | ✅ 已修（floor 到 24h）|
| 6 | **P1** | `Stop()` 无法中断运行中的基准测试；首次运行总是执行完 | `bg/model_quality_worker.go` + `monitor.go` | ✅ 已修（WithCancel ctx）|
| 7 | **P2** | `NewRequestWithContext` 失败后探针泄漏（触发罕见） | `context_summarize.go:472-474` | ✅ 已修 |
| 8 | **P2** | synthesized [DONE] 可观测性欠计数（仅 OpenAI 路径计数，Anthropic/Gemini 未计数）| `stream.go:768` + `metrics` | 📝 已记录 |
| 9 | **P2** | `GetScoreHistory` 总是加载整个 JSONL 历史文件 | `modelquality/storage.go:110-151` | 📝 已记录 |
| 10 | **P2** | `loadLastScores` 写入 `m.lastScores` 时未持有 `m.mu`（当前安全，但脆弱）| `monitor.go:278-289` | 📝 已记录 |

## 二、初轮已修正问题

### 2.1 [P1] V2 LoadState 把新会话当硬错，新会话永远走 V1

- **文件**: `domains/session/v2/cache_v2.go:349`（修复前）
- **根因**: `LoadState` 对 `QueryRow(...).Scan(...)` 返回的任何错误（含 `pgx.ErrNoRows`）统一 `fmt.Errorf("load session state: %w", err)` 返回 `(nil, err)`。调用链 `HasState → Get → LoadState` 因此对新会话返回 `(false, err)`，而非契约规定的 `(false, nil)`。
- **影响**: `session_compressor.go:741` `tryLoadV2State` 命中 `err != nil` 分支返回 `ok=false`，**每个新会话都回退到 V1**，第 747 行「新会话 → `ok=true`」分支永远不可达。V2 对新会话形同未启用。兄弟读取器 `TurnReader.LoadLatestOutbound`（`turn_reader.go:46`）正确处理了 `ErrNoRows`，此处遗漏。
- **修复**: `LoadState` 在包装前先判 `errors.Is(err, pgx.ErrNoRows) { return nil, nil }`，并补 `errors` / `pgx` import。
- **测试**: `domains/session/v2/outbound_builder_integration_test.go` 新增 `TestSessionTurnsReader_LoadState_RealDB_NoRows`，用真库断言无行时 `(nil, nil)`。

### 2.2 [P1] applyAgentFilter 抽取丢失大小写归一化

- **文件**: `web/src/composables/useLiveStreamFilters.ts:71`（修复前）
- **根因**: 抽取 composable 时，`applyAgentFilter` 写成 `agentFilter.value = new Set(selected)`，丢失了原组件（`HEAD~1` LiveRequestStreamV2.vue:463）的 `selected.map(s => s.toLowerCase())`。
- **影响**: `filteredLanes` 仍对 `r.agent_name` 小写化，所以**过滤本身仍命中**（这也是测试没抓到的原因）。但 `agentFilterSelected` 回显原始大小写，与弹窗里恒小写的 `availableAgents` 通过 `draft.has(opt)` 比对时**勾选状态错位**——一旦调用方传入非全小写值即触发，违反 composable 自身「客户端过滤器全小写匹配」契约。
- **修复**: 恢复 `new Set(selected.map(s => s.toLowerCase()))`。
- **测试**: `useLiveStreamFilters.test.ts` 新增 `applyAgentFilter normalizes selections to lowercase`，传 `['ZCODE','OpenCode']` 断言存储为 `['zcode','opencode']`。

### 2.3 [P2] SessionCacheV2 缺 Close()，V2 Redis 连接池无释放入口

- **文件**: `domains/session/v2/cache_v2.go`
- **根因**: `NewSessionCacheV2` 内部 `NewRedisGovernanceCache` 创建独立 `*redis.Client`，但 `SessionCacheV2` 无 `Close()`，`RedisGovernanceCache.Close()` 无人调用（死代码）。进程退出时由 OS 回收连接池。
- **修复**: 新增 `SessionCacheV2.Close()`（nil-safe，转发到 `l2.Close()`），为未来优雅关闭提供入口。
- **范围说明**: 本次**未**接入 `cmd/gateway/main.go` 关停序列——`sessionCacheV2` 声明在 `if telemetryClient != nil {...}` 内层块（line 1684），关停 goroutine（line 4659）在外层 main body 作用域不可见，强行抬升声明作用域风险大于收益。且与既有模式一致：`redisClientForCache`（line 494）同样未显式 Close，沿用进程退出回收。`Close()` 作为 API 入口已就位，留待后续重构关停序列时接入。

## 三、驳回的误判（P0）

子代理报告「`session_summaries` 引用 5 个不存在的列（`gw_project_id`/`gw_task_id`/`user_tags`/`session_status`/`search_vector`），所有会话管理端点 500」。**经回仓库验证，此结论不成立**：

- 这 5 列由迁移 `deploy/sql/migrations/V353__session_summaries_project_task_tags.sql` 添加（commit `591207699` 引入，`ADD COLUMN IF NOT EXISTS` 全部覆盖），代码与迁移一致。
- 子代理搜索的是 `sql/migrations/startup/` 目录（未命中），而 V353 实际位于 `deploy/sql/migrations/`（迁移存在两套目录）。
- `sql/schema/01-schema.sql`（2026-08-04 从 252 库生成）确实不含这些列——因为 V353 在快照之后才上库，快照本身滞后，并非代码缺陷。

**附带发现（非本次任务范围，已记录）**: 迁移双目录（`deploy/sql/migrations/` 与 `sql/migrations/startup/`）+ schema SSOT 滞后是真实的可维护性隐患，但属既有问题，且非本次 48h 改动引入，本审计不擅自改动，留独立任务。

## 四、第二轮已修正问题（生产热路径深入审计）

继用户「请继续」指令，扩大覆盖面至**生产关键热路径**：熔断器半开探针槽位泄漏修复（2026-07-03 事故根因）、stream synthesized-[DONE] 可观测性、model-quality worker 加固（13 文件大改）、auto-title/summary/end-user 集群（请求隔离/租户安全/链式自触发）。

### 4.1 [P1] 压缩上游 4xx 探针槽位泄漏 — 70fe6d81 修复遗漏

- **文件**: `domains/streaming/executors/context_summarize.go:500-504`（修复前）
- **根因**: `doCompactionUpstream` 的熔断器状态处理只覆盖 `>= 500 || == 429`（RecordFailure/ReleaseProbe）和 `< 400`（RecordSuccess），但 **4xx（除 429）既不匹配 `>= 500 || == 429`，也不匹配 `< 400`**，导致两个分支都跳过，函数在第 504 行 `return resp, nil` 时**探针槽位仍持有**。
- **影响**: 压缩 4xx（400/401/403/404/etc.）响应后，熔断器在 HALF_OPEN 状态下楔住，直到 5 分钟 `halfOpenProbeTimeout` 安全网回收。压缩 4xx（auth/quota/bad-request）是可能的生产场景。这正是 commit 70fe6d81 本应修复但**遗漏**的 bug 类别。
- **修复**: 添加 `else` 分支（`resp.StatusCode >= 400`）调用 `ReleaseProbe`（4xx 对压缩而言是凭据健康或模糊信号，不记录为 failure，但必须释放探针）。
- **验证**: `go test ./domains/streaming/executors/... ok 5.5s`

### 4.2 [P1] ScheduleInterval==0 时 time.NewTicker panic

- **文件**: `domains/modelquality/monitor.go:122`（修复前）
- **根因**: `time.NewTicker(m.config.ScheduleInterval)` 在 duration 为 0 时 panic。main.go 生产路径在第 2803 行将 `mqIntervalHours` floor 到 >=1，所以**今天不可达**。但 `NewQualityMonitor` 接受任意 config，且 worker 自身的 `UpdateConfig`（monitor.go:239）或未来调用方构造 `MonitorConfig{ScheduleInterval: 0, EnableScheduled: true}` 会在 goroutine 中触发未恢复的 panic，导致**进程崩溃**。
- **修复**: 在 `scheduledCheckLoop` 中守卫：`interval := m.config.ScheduleInterval; if interval <= 0 { interval = 24 * time.Hour }`。
- **验证**: `go test ./domains/modelquality/... ok 346s`

### 4.3 [P1] Stop() 无法中断运行中的基准测试

- **文件**: `domains/modelquality/monitor.go:107-118` + `:99` / `bg/model_quality_worker.go`
- **根因**: `scheduledCheckLoop` 在进入 select 循环**之前**同步调用 `m.runScheduledCheck(ctx)`（第 126 行），而 `runScheduledCheck`/`testModel`/`Execute` 只在问题之间检查 `ctx.Done()`（executor.go:58）。main.go 传入的 ctx 是 `context.Background()`（main.go:2842，永不取消）。因此，如果 worker 在**启动基准测试**（同步、多分钟）期间被 `Stop()`，`monitor.Stop()` 关闭 `stopChan` 但运行中的基准测试继续完成。`worker.Stop()` 在 `doneCh` 返回后返回（`doneCh` 只等待简单的 `loop` goroutine，而非基准测试），所以 monitor goroutine **活过了 `Stop()`**。这导致 stop/restart 可能有两个基准测试 goroutine 并发运行（旧的未退出 + 新 worker 的），都写入同一 JSONL score 文件（`FileStorage` 有 mutex，所以无损坏，但有重复写入）。
- **修复**: 
  1. `worker.Start`: 用 `context.WithCancel(ctx)` 创建 `monitorCtx`，存储 `cancelFunc`，传 `monitorCtx` 给 `monitor.Start`
  2. `worker.Stop`: 调用 `cancel()` 以中断运行中的基准测试
  3. `monitor.runScheduledCheck` / `testModel`: 在开始时检查 `ctx.Done()`，使 `Stop()` 能在基准测试启动前中断
- **验证**: `go test ./bg/... ok (cached)`

### 4.4 [P2] NewRequestWithContext 失败后探针泄漏

- **文件**: `domains/streaming/executors/context_summarize.go:472-474`（修复前）
- **根因**: 如果 `http.NewRequestWithContext` 在 `Allow()` 消耗探针（第 444 行）后失败，早期 `return nil, err` 既不释放探针也不记录任何东西。同样的 5 分钟缓解适用。触发罕见（仅在 URL/header 构造失败时）。
- **修复**: 在返回前添加 `if e.Circuit != nil { e.Circuit.ReleaseProbe(...) }`，匹配第 455-457 行的 limiter-reject 路径。

### 4.5 [P2] synthesized [DONE] 可观测性欠计数

- **文件**: `domains/streaming/stream.go:768-778` + `metrics/prometheus.go`
- **根因**: 新的 `llm_gateway_stream_synthesized_done_total` 计数器仅在 `StreamChatWithPendingCaptureAndDiagnostics`（OpenAI/MiniMax 路径）中递增。其他 [DONE] 合成站点（Anthropic bridge EOF 路径、Gemini 路径：`anthropic_bridge.go`、`handler_gemini.go:88`）对相同语义（"上游未发 [DONE]，网关注入"）不递增它。commit 明确将更改范围限定为 OpenAI 路径并声明"无行为更改"，所以这是**有意的范围限定**，但将计数器名称读为 total 的操作员会**欠计数**跨协议的合成终止符。如果需要完整覆盖，将 `metrics.Global().RecordStreamSynthesizedDone()` 连接到其他合成站点。
- **状态**: 📝 已记录（不是功能 bug，是可观测性覆盖范围问题）

### 4.6 [P2] GetScoreHistory 全文件加载

- **文件**: `domains/modelquality/storage.go:110-151`
- **根因**: `GetScoreHistory` 在每次调用时加载整个 JSONL 历史文件，然后排序，即使 `limit == 1`（如 `GetLatestScore` 使用的，每个目标模型在每次 monitor 启动时调用一次）。历史文件是仅追加的，无限增长（每天每个模型一条记录，永久）。随着长期生产部署，此读取+排序成本线性增长。commit 消息声称通过切换到 `bufio.Scanner` "修复"了全文件加载，但 scanner 在排序/截断前仍将整个文件具体化到 `scores` — 流式更改实际上不减少 `GetLatestScore` 的内存。
- **建议**: 对于 `limit == 1`，扫描文件一次仅跟踪 max-Timestamp 记录而不是追加所有到切片；或轮换/截断历史文件。
- **状态**: 📝 已记录（P2 资源关注，非紧急）

### 4.7 [P2] loadLastScores 未持有 mutex 写入

- **文件**: `domains/modelquality/monitor.go:278-289`
- **根因**: `loadLastScores` 写入 `m.lastScores` 时未持有 `m.mu`。**今天安全**仅因为它在 `Start()` 中同步运行，在 `scheduledCheckLoop` goroutine 启动（第 99 行）且 `Start` 返回前 — 所以尚不存在并发读取器。但这是脆弱的：任何未来在另一个 goroutine 持有引用时调用 `Start` 并调用 `GetCurrentScores`/`TriggerAnomalyCheck` 的调用者都会竞争。锁定规则与 `testModel`（在第 234 行锁定）和 `GetCurrentScores`（在第 293 行锁定）不一致。
- **建议**: 将 `m.lastScores[key] = score` 赋值包装在 `m.mu.Lock()`/`Unlock()` 中以保持一致性。
- **状态**: 📝 已记录（不是现场 bug，但脆弱）

### 4.8 已验证正确（auto-title/summary/end-user 集群，无新问题）

手动检查了以下修复的当前状态，确认它们**已正确应用**且无回归：
- **链式自触发防护**（1cf1448a）: `shouldSkipAutoTitleGeneration` / `shouldSkipAutoSummaryGeneration` 检查 `logCtx.IsAutoRequest`（从 `X-Gw-Is-Auto` header 设置），在 `handler.go:4445` / summary 触发器中调用。防护到位。
- **GwSessionID 赋值**（e6223e9d）: 标题生成现在通过 `X-Gw-Session-Id` header 接收会话 ID，不再是"永不赋值"。
- **UPSERT 竞态**（15b74407）: `saveSessionTitle` 使用 `INSERT ... ON CONFLICT (task_id, scoped_session_id) DO NOTHING`（第 546 行）— 第一个写入者获胜，无数据丢失。正确。
- **end_user_id 填充**（badf6868/b84b39cf）: `buildEntry` 在第 806 行通过 `resolveEndUser("", c.Request, c.Body)` 填充 `EndUserID`（失败路径）；`resolveEndUser` 是非破坏性的（第 5558-5586 行：优先级链，无 r.Body 破坏性读取）。成功和失败路径的奇偶性保持。
- **租户安全**: auto-title session lookup 查询在第 566 行有租户过滤：`WHERE rl.gw_session_id = $1 AND rl.tenant_id = $2`。无跨租户泄漏。

## 五、验证

**初轮**:
```
go build ./...                          # exit 0（仅 vendor cgo 警告）
go test ./domains/session/v2/...        # ok
go test ./domains/hooks/compression/... # ok（含 caveman/lite/summary）
go test ./internal/ir/...               # ok
vitest run                              # 31 files / 191 tests passed
vue-tsc --noEmit                        # exit 0
```

**第二轮**:
```
go build ./...                                  # exit 0
go test ./domains/streaming/executors/...      # ok 5.480s
go test ./domains/modelquality/...             # ok 346.729s
go test ./bg/... ./bg/systemmonitor/...        # ok (cached)
```

## 六、提交记录

- **初轮提交**: `f8b10499` "fix(audit): 48h review — V2 LoadState ErrNoRows, agent filter lowercase, V2 cache Close"
- **第二轮提交**: 待推送（3 P1 + 1 P2 修复：熔断器探针泄漏补完、monitor ticker panic 守卫、worker Stop 可中断基准测试、NewRequestWithContext 失败路径探针释放）

