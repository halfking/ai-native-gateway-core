# 48 小时改动审计与修正报告 (2026-08-07)

> **审计时间**: 2026-08-07 09:00 (CST)
> **审计范围**: `git log --since="48 hours ago"` — 100 commits · 116 files · +21,700 / −354 lines
> **审计方式**: 三路并行 read-only 子代理（会话管理 API / 会话 V2 压缩 / Web composable + IR），结论逐条回仓库与 schema 验证

## 一、执行摘要

审计发现 **2 个 P1 真实 bug**（已修）+ **1 个 P2**（已修）+ **1 个被误判的 P0**（驳回，见 §三）。全部修正通过 `go build ./...`、`go test`（v2 + compression + ir）、`vitest`（31 文件 / 191 用例全绿）与 `vue-tsc --noEmit`（exit 0）。

| # | 严重度 | 主题 | 文件 | 状态 |
|---|--------|------|------|------|
| 1 | **P1** | V2 `SessionTurnsReader.LoadState` 把 `pgx.ErrNoRows` 当硬错 → 新会话永远回退 V1 | `domains/session/v2/cache_v2.go` | ✅ 已修 + 真库测试 |
| 2 | **P1** | `useLiveStreamFilters.applyAgentFilter` 抽取时丢失 `.toLowerCase()` 归一化 | `web/src/composables/useLiveStreamFilters.ts` | ✅ 已修 + 回归测试 |
| 3 | **P2** | `SessionCacheV2` 无 `Close()`，V2 Redis 连接池无优雅释放入口 | `domains/session/v2/cache_v2.go` | ✅ 已加 `Close()` |
| — | 驳回 | 误判 P0：「`session_summaries` 引用不存在的列，所有会话管理端点 500」 | — | ❌ 驳回（见 §三）|

## 二、已修正问题

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

## 四、验证

```
go build ./...                          # exit 0（仅 vendor cgo 警告）
go test ./domains/session/v2/...        # ok
go test ./domains/hooks/compression/... # ok（含 caveman/lite/summary）
go test ./internal/ir/...               # ok
vitest run                              # 31 files / 191 tests passed
vue-tsc --noEmit                        # exit 0
```
