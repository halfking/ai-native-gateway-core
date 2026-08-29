# 2026-08-29 接手 main-integration 后续 — 复测与死锁修复

## 1. 任务概要

接手 `.handoff/2026-08-28-followup-audit-closeout.md` §13 的 streaming 测试
对齐决策，并把 §5 的 P1 跟进项推到第一波。本轮实际完成了 **复测 + 两个
真实 P0 缺陷修复**：

1. `AttemptCommitGate.Discard()` 锁序倒置导致的死锁（生产路径）
2. `StreamAnthropicSSEToOpenAI` empty-response 检测漏掉 `pc != nil`
   短路，导致 `TestStreamAnthropicSSEToOpenAI_DisconnectsKeepsCapturer`
   与所有 pc-equipped empty fixture 误判为空响应、capturer 拿不到 `[DONE]`

§5 剩余的 P1/P2 留作下一会话。

## 2. §13 决策的复测结果（已自然闭合）

§13 列出 4 个预先失败的 streaming 测试：

1. `TestStreamAnthropicPassthrough_BytesForPassThrough`
2. `TestStreamAnthropicPassthrough_ForwardsUnterminatedFinalFrame`
3. `TestStreamAnthropicSSEToOpenAI_ConvertsMessageStartToOpenAIChunk`
4. `TestStreamAnthropicSSEToResponsesEmptyMessageIsRetryable`

复测结果：**4 个全部通过**（HEAD `7715ca1d4`，`-race -count=1`）。

原因：`32d64f8ea`（"Anthropic bridge emittedContent + signature digest +
attempt gate race"）已经是 HEAD 的祖先（merge commit `bf1ae3c0a` 把它
从 origin/main 带入），§13 推荐的 **选项 A（cherry-pick `32d64f8ea`）**
已自然落地。所以：

- **§13 选项 A vs B 决策不再是开放项**。
- 旧的 §13 段落下次会话可以删除/合并，仅作为历史背景。

## 3. 复测中发现 P0 缺陷（两个，均已修复）

### 3.1 缺陷 A：`Discard()` 与 `Commit()` 锁序倒置导致的死锁

#### 现象

`go test ./domains/streaming/ -count=1 -timeout 30s` 在
`TestAttemptCommitGateCommitReturnsDiscardedWhenDiscardWinsDuringHook`
卡住 30 秒，被 timeout 强杀。该用例在 §13 复测中**没有**运行过（§13
只跑了 §P1/§P2 的 4 个用例与该用例同名的家族不同成员）。

#### 根因

`ee438de9c`（"fix(streaming): close gate and empty-response lifecycle
gaps"，2026-08-28）在 `AttemptCommitGate.Discard()` 加了
`g.writeMu.Lock() / Unlock()`。但同文件 line 804–824 的 lock-scope
注释（P3 doc，由 `049760198` 添加）明确说：

> Lock scope (audit-24h-20260828-r4 P3 doc): Discard acquires g.mu only,
> NOT writeMu. ... not 'fix' the asymmetry by adding writeMu — that
> would deadlock any goroutine currently in the write side ...

被 `ee438de9c` 反向修复。死锁场景：

- 测试 goroutine：`go func() { commitErr <- g.Commit() }()` →
  `Commit()` 取 `writeMu`，再取 `mu`，进入 hook 前释放 `mu`，
  hook 在 `releaseHook` 上阻塞。
- 主测试 goroutine：`<-hookEntered` 之后调用 `g.Discard()` →
  `Discard()` 试图取 `writeMu.Lock()`，与 `Commit()` 持有的
  `writeMu` 互斥 → **永久阻塞**。
- 同一文件下 `Commit` / `WriteFrame` / `FinishAttempt` / `FlushHoldback`
  都取 `writeMu`。任一处于 hook 窗口或 wire-IO 时，`Discard()` 都会
  死锁。**这是生产代码路径上的真实死锁，不止测试用例**。

`git blame` 锁定引入点：`ee438de9c` (lines 826–827)。

#### 修复

[`domains/streaming/attempt_commit_gate.go:825`](domains/streaming/attempt_commit_gate.go) 删除 `Discard()` 内的 `writeMu` 两行，
把 lock-scope 注释内联到函数头强化：

```go
func (g *AttemptCommitGate) Discard() error {
    // mu-only acquisition: see lock-scope comment above. writeMu is omitted
    // because (a) Discard performs no wire write, only resets in-memory state,
    // and (b) Commit/WriteFrame/FinishAttempt/FlushHoldback all hold writeMu
    // around their hook or wire-IO windows — adding writeMu here would deadlock
    // any concurrent caller that is mid-hook (regression caught by
    // TestAttemptCommitGateCommitReturnsDiscardedWhenDiscardWinsDuringHook).
    g.mu.Lock()
    defer g.mu.Unlock()
    ...
}
```

正确性论证（与文档一致）：

- `Discard` 不做 wire I/O，仅修改 `g.buffer / g.discarded / g.state /
  g.checkpointBlocked / g.checkpointErr / g.firstMetaAt`。
- 并发的 `WriteFrame` 取 `writeMu` + `mu`；它在 `mu` 内查 `g.discarded`
  (line 324)。`Discard` 在 `mu` 内写 `g.discarded = true`。
  两者在 `mu` 上互斥，无 race。
- 没有 `writeMu` 的 `Discard` 不会与任何 in-flight 写入发生竞争——
  它只是更早地让 `WriteFrame` 看见 `g.discarded=true`。
- 没有 `writeMu` 的 `Discard` 不会破坏序列化保证——`g.writer`（被
  `writeMu` 串行化）从未被 `Discard` 触碰。

### 3.2 缺陷 B：`StreamAnthropicSSEToOpenAI` empty-response 检测漏 `pc != nil`

#### 现象

`TestStreamAnthropicSSEToOpenAI_DisconnectsKeepsCapturer` 失败：
captured body 缺 `data: [DONE]`，pending 状态 `failed` 而非
`completed`。同家族还有
`TestStreamOpenAIToResponsesSSE_DisconnectsKeepsCapturer` 与
`TestStreamOpenAIToAnthropicSSE_DisconnectsKeepsCapturer`——后两者
仍通过，差异在 Anthropic→OpenAI 路径多了 message_stop → done 的
terminator chunk 链路。

#### 根因

`987653e98`（"fix(streaming): align empty-response detection with
documented contract"，2026-08-29 06:30）显式声明要把 `pc != nil`
短路进 empty-response 决策：

> hasPendingReplay (pc != nil) short-circuits the interrupt: the caller
> wired a pending replay buffer for client-disconnect recovery and MUST
> see a completed body even on empty streams. ... Without this guard
> every pc-equipped empty fixture was wrongly marked as empty_response
> and the [DONE] chunk was never written to the capturer.

它正确地在 helper
[`domains/transformation/anthropic/stream_support.go:105`](domains/transformation/anthropic/stream_support.go)
里加了 `hasPendingReplay` 短路，并且 `responses_bridge.go` 的三处
call site 都正确传了 `pc != nil`。**但 `anthropic_bridge.go:1185` 的
`case ir.ChunkTypeDone:` 分支没有改——它沿用了旧的行内检查
`if !emittedContent { ... return ... }`**，把
`anthropic_empty_response` 当成无条件中断返回，跳过了 `[DONE]`
chunk 写出与 `pc.finalize(outcome)` 之后的 `completed` 路径。

注意代码块缩进也是错的（注释从 case 里蹦出来单独成段，再嵌 if），
但 Go 编译器对 case 块内缩进不敏感，CI 没拦住。

#### 修复

[`domains/streaming/anthropic_bridge.go:1205`](domains/streaming/anthropic_bridge.go) 行内检查替换为 helper 调用，并修齐缩进：

```go
if anthropictransform.IsAnthropicStreamEmpty(emittedContent, inputTokens, outputTokens, pc != nil) {
    if capture != nil {
        capture.MarkInterruptedWithReason("anthropic_empty_response")
    }
    return StreamOutcome{Interrupted: true, Reason: "anthropic_empty_response", Kind: errorsx.KindEmptyResponse, Resumable: true, ChunkCount: chunkCount}
}
```

`pc != nil` 经由 helper 内部第一行短路返回 false，与
`responses_bridge.go` 三处 call site 完全对齐。

§13 的三个 empty-response fixture（`...EmptyMessageIsRetryable` 家族）
在 `pc == nil` 路径上仍命中 true 分支，行为不变。

新会话接手时建议：

- 读 `domains/streaming/pending_disconnect_extra_test.go:96–139`
  确认当前期望与 `pc.Snapshot()` 实际行为；
- 检查 `StreamAnthropicSSEToOpenAIWithDiagnostics` 的 outcome 分类
  与 `pc` 写入路径，看 `client_write_failed` 是否在 disconnect 路径
  上被错误优先于 `client_disconnected`；
- 决定方向：（a）把测试改为接受新行为，（b）恢复旧行为并复测所有
  failover 边界。这属于 §5 缺失 key/WRONGTYPE 之外的另一类 **观测
  性问题**，单独 PR 处理。

## 5. 完整验证（两个修复同时落地后）

- `go test ./domains/streaming/ -run TestAttemptCommitGateCommitReturnsDiscardedWhenDiscardWinsDuringHook -count=1 -race -timeout 30s`：PASS（0.00s）
- `go test ./domains/streaming/ -run TestStreamAnthropicSSEToOpenAI_DisconnectsKeepsCapturer -count=1 -race -timeout 30s`：PASS
- §13 fixture 三件套（`TestStreamAnthropicSSEToOpenAIEmptyMessageIsRetryable` /
  `TestStreamAnthropicSSEToResponsesEmptyMessageIsRetryable` /
  `TestStreamAnthropicPassthroughEmptyMessageIsRetryable`）：PASS
- 全部 pending-disconnect 测试（`TestStreamChatWithPendingCapture*`、
  `TestStreamOpenAIToResponsesSSE_DisconnectsKeepsCapturer`、
  `TestStreamOpenAIToAnthropicSSE_DisconnectsKeepsCapturer`、
  `TestStreamAnthropicPassthroughContinuesAfterClientDisconnect` 等）：PASS
- `go test ./domains/streaming/... -count=1 -race -timeout 180s`：69s，**所有子包 ok**
- `go test ./errorsx/... ./domains/transformation/anthropic/... ./domains/dispatch/... -count=1 -race`：clean
- `go vet ./domains/streaming/... ./domains/transformation/anthropic/...`：clean
- `go build ./...`：clean（仅 vendored `shoenig/go-m1cpu` 两个 -Wgnu-folding-constant 警告，与本修复无关）

## 6. origin/main 进展（须先 git pull --ff）

复测开始时拉取 `origin/main = f2f527511`。本会话完成后再次拉取，发现
origin/main 已前进到 `6ef7e8455`，期间 4 个新 commit 由 `zcode` 推送：

- `8778b3d6c` fix(dispatch): accept stream-only native Responses capability at gate
- `7684783b0` fix(audit): post-merge closeout for 2026-08-29 batches
- `9dd65c7ae` docs(handoff): capture 2026-08-29 audit findings + dispatch gate fix
- `6ef7e8455` fix(migrations): 重编号 611→612, 612→613 避免迁移编号冲突

`git log origin/main -4 --name-only | grep -E "attempt_commit_gate|anthropic_bridge"`：
**空集**——这些 commit 不触碰本会话修改的两个文件。`git pull --ff`
快进到 `6ef7e8455`，本会话两个修复以独立 commit 落到新 tip 上。

## 7. 当前状态（Current State）

- 工作目录：`/Users/xutaohuang/workspace/ai-native-tools/syncfield/
  llm-gateway-go-2`
- 分支：`main`，HEAD `6ef7e8455`，与 `origin/main` 一致
- 未提交：本会话修改（修复 + handoff）已 commit + push（见 §8）
- 未推送改动在 `/tmp/wip-stash/`：**空**（§13 closeout 提到的 17 个
  协作方未跟踪 WIP 在 main 集成时已由协作方自行处理）

## 8. 本轮提交记录

- `fix(streaming): AttemptCommitGate.Discard() must take g.mu only, not
  writeMu` — 修复 §3.1 锁序倒置死锁
- `fix(streaming): thread pc != nil into StreamAnthropicSSEToOpenAI
  empty-response check` — 修复 §3.2 pc-equipped 空响应被误判
- `docs(handoff): 2026-08-29 接手 main-integration 后续 — 复测与死锁修复`
  — 本文件

## 9. 本轮未做（留给下一会话）

- §5.1 SafeHGetAll 批量迁移：`domains/ursm/v2/migration/{preflight,
  cleanup, classify, metadata, metadata_redis}.go`、
  `domains/ursm/v2/persist/writer.go`、pipeline 路径
- §5.2 缺失 key / WRONGTYPE 回归测试（pending、session/v2、
  hooks/compression、session/preprocess）
- §5.3 streaming 空响应闭环（pending/durable/client-disconnect 三条
  路径全覆盖）
- §5.4 dispatch JournalSnapshot ADR
- §5.5 `success && response_body missing` 观测性
- §5.6 docs/2026-08-28-vendor-protocol-alignment-audit.md P0-MiniMax-1
  状态更新

## 10. 阻塞 / 风险

无新增。`go test ./domains/streaming/... -race` 全绿。

## 11. 引用

- 上游 lock-scope 注释：`domains/streaming/attempt_commit_gate.go:804–824`
- 引入冲突的提交：`ee438de9c` "fix(streaming): close gate and
  empty-response lifecycle gaps"
- 测试入口：`domains/streaming/attempt_commit_gate_test.go:89`
- helper 定义：`domains/transformation/anthropic/stream_support.go:105`
- 修复处：
  `domains/streaming/attempt_commit_gate.go:825`（缺陷 A）
  `domains/streaming/anthropic_bridge.go:1205`（缺陷 B）
- 历史背景：`.handoff/2026-08-28-followup-audit-closeout.md` §10–§13
- 关联 commit：`987653e98`（声明 pc != nil 短路但 anthropic_bridge.go 漏改）
