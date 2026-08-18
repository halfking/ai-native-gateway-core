# docs/agents/issue-tracker.md — llm-gateway-go task tracking

> 本仓库使用本地 Markdown 跟踪协议。所有 ticket 状态 / 归属 / 收口记录在此维护。

## §1 跟踪位置

仓库使用 `.scratch/<feature>/<NN>-<slug>.md` 单文件 ticket 形式，
与 acc-toolkit 元仓库（`vibe-coding/docs/agents/issue-tracker.md`）
的协议对齐。`.scratch/` 不入仓（`.gitignore` 已配置）。

跨 SP 的 stream-request lifecycle state machine 进展记录在
`docs/changelogs/2026-08-19-stream-state-machine.md` 与
`docs/design/2026-08-19-stream-state-machine.md`；本文件只维护
ticket 关闭状态与当前 active 计划。

## §2 ticket 状态

### e0a849a3f29abff690818a812fa44c34 — pre-stream keepalive 缺位

- **状态**：✅ **closed**（2026-08-18）
- **根因**：154 上 minimax-m3 大 body 请求（800+ messages / ~1.5MB）经
  session compressor `Prepare` 阶段 5-40 秒，期间没有任何 keepalive
  帧发到客户端，opencode SDK 默认 idle 超时（~10s）切断 → `r.Context()` 
  canceled → 重试循环在 attempt 0 之前 abort → 返回 provider_error。
- **修复**：`c52769e4a` 把 `startPreStreamKeepalive` 上移到
  candidate resolution 之前；HTTP 200 + 第一帧 `: keep-alive\n\n` 
  立即发出。详见 `docs/changelogs/2026-08-18-minimax-m3-large-body-keepalive-fix.md`。
- **关联**：本次 stream state machine 计划 C 的设计文档
  `docs/design/2026-08-19-stream-state-machine.md` §4 把
  pre-stream ticker 的 cancel 顺序列为"第一观察者"。

### plan C — stream-request lifecycle state machine refactor

- **状态**：🚧 **active**（2026-08-19 起）
- **范围**：把散落在 handler / executor / compressor / streamretry 中
  的 `slog.Info` 生命周期标记合并为显式状态机（`domains/streaming/state/`）。
  5 个子工作 SP-01..05 按顺序合入 main，245 pre-prod 24h 观察门禁
  见 `docs/changelogs/2026-08-19-stream-state-machine.md` §3。
- **基线**：HEAD `7bd6783b0`（`stream-state-machine-sp01` 分支），
  SP-02..04 在各自 worktree 上有未提交改动。
- **设计文档**：`docs/design/2026-08-19-stream-state-machine.md`
  （4 个 ASCII 状态图 + 事件表 + RequestContext 字段表 + 取消传播
  时间线 + slog→状态机映射表 + 并发危险/守卫三件套）。
- **回滚命令**：`docs/changelogs/2026-08-19-stream-state-machine.md` §4。

## §3 SP-01..04 期间的 follow-up 风险

- **pre-stream keepalive 与 session_compressor 串行顺序**：
  当前 keepalive 在 candidate resolution 之后、session compressor 之前
  启动（`c52769e4a`），但若 compressor 端 `tryLLMSummaryWithFallback` 
  在 `ctx.Err()` 检查时收到已 canceled 的 ctx，会立刻跳过 LLM 
  summarizer — 这是 plan C 的预期行为，但如果客户端在 compressor
  之前就断开且 ticker 已启动，会有 1-2 帧空 keepalive 帧发出。245
  观察期内重点看这种"先 keepalive 后 cancel"的占比与延迟分布。

- **状态机 cancel 与重试退避的耦合点**：
  `internal/streamretry/wrapper.go` 的 `bindStateCancel` 派生 ctx
  在 state-machine cancel channel 关闭时立刻 cancel。极端情况下
  如果 retry 已经在 attempt 3 接近完成，可能丢失"已耗时的
  retry 进度" — 245 观察期需关注重试成功率是否回归。

- **状态机 `EventFailed` 与 `EventCancelled` 的优先级**：
  handler 在 executor 返回 error 时用
  `!errors.Is(r.Context().Err(), context.Canceled)` 守卫避免重复
  发 `EventFailed`，但 r.Context() 与 parent context 之间的
  race window 极小 — 245 观察期关注日志里
  `EventFailed` 后立即 `EventCancelled` 的双发情况。

- **compressor 的 ctx 提前退出时机**：
  `tryLLMSummaryWithFallback` 在 `tryLLMSummary` 完成后再次
  检查 `ctx.Err()`，如果 cancel 发生在 LLM summarizer 已返回
  但尚未写入缓存的窗口内，summary 被丢弃。这是预期行为，但
  若 client retry，会重新触发 compressor，重复一次 LLM 摘要
  — 245 观察期关注 compression_quality metric 的 retry 后分布。

- **handler_state_init.go 的 goroutine 泄漏**：
  `initRequestStateMachine` 启动一个 event-loop goroutine，
  依赖 `defer cancelRequestStateMachine(rt, nil)` 触发
  `rt.Cancel(...)` 把它推到 terminal。如果 handler 在某些
  极端 panic 路径下未走到 defer（已被上层 recover 截胡），
  goroutine 会泄漏到下一个 request。SP-02 的
  `TestInitRequestStateMachine_NilSafe` 覆盖了 nil 路径，
  但 panic 路径在 245 观察期需要重点看 `goleak` 类指标。

## §4 关联

- 设计文档：`docs/design/2026-08-19-stream-state-machine.md`
- 变更日志：`docs/changelogs/2026-08-19-stream-state-machine.md`
- keepalive 修复变更日志：`docs/changelogs/2026-08-18-minimax-m3-large-body-keepalive-fix.md`
- acc-toolkit 元协议：`~/workspace/ai-native-tools/vibe-coding/docs/agents/issue-tracker.md`
- 提交纪律：`rules/35-task-commit-discipline.md`（future 合并时遵守）
