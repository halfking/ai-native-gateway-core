# Handoff: §5.2 hardening follow-through — 2026-08-29

**交接时间:** 2026-08-29 (下午)
**项目根:** `/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-3`
**当前分支:** `main`
**最新 commit:** `a8324a38e`（已 push 到 origin/main）

---

## §0 任务来源

承接 `.handoff/2026-08-29-followup-h4-hardening.md` §5.2 的"未来 harden 警示"——
原话："5 处 SSE/IO goroutine 中**唯一**没有 recover 的" 是 §4 #1 那处，本轮已修；
剩余 4 处高风险 goroutine 沿用同一 hardening 模式补齐。

承接方案的范围在 plan mode 中与用户确认：
- **范围**: 4 个 goroutine（reader + durable 组合）
- **风格**: 命名返回值 + defer recover（沿用 §4 #1 模板，不引入新 helper）

---

## §1 本轮交付

### 1.1 修复（commit `a8324a38e`）

**生产路径 4 处 panic recover hardening + 4 个对应测试：**

| # | 文件:行 | 模式 | 修复要点 |
|---|---|---|---|
| 1 | `domains/streaming/anthropic_event_reader.go:34-44` | channel-send | 包装 `readAnthropicSSEEventRaw` 在 named-return + defer recover 中，panic → `"anthropic SSE read panic: %v"` error |
| 2 | `domains/streaming/request_meta.go:165-181` | channel-send + ctx 超时分支第二次裸 `<-resultCh`（最坏 120s 阻塞） | 包装 `io.ReadAll` 在 named-return + defer recover 中，panic → `"request body read panic: %v"` error |
| 3 | `domains/streaming/durable_recovery_worker.go:137-166` | 长生命周期 `for{select}` worker 循环 | 抽出 `safeRunOnce` 闭包，外层 `defer recover()` + `slog.Warn` 带 stack，让循环继续 |
| 4 | `domains/streaming/durable_recovery_worker.go:295-321` | `defer close(attemptDone)` + 外层 `attempt, err = w.runner.Run(...)` | goroutine 内嵌套 func + named-return + defer recover，panic → `err = "durable attempt runner panicked: %v"`，被 attemptFinished 的 reschedule 分支捕获 |

### 1.2 测试（4 个新测试，全部镜像 `TestNativeResponsesEventReaderPanicBecomesError` 的 `panickingReader` 模式）

| 测试 | 文件 | 关键断言 |
|---|---|---|
| `TestReadAnthropicSSEEventPanicBecomesError` | `anthropic_event_reader_test.go` | `anthropicPanickingReader{}.Read` panic → 500ms 内返回 error 含 "panic" |
| `TestReadRequestBodyPanicBecomesError` | `request_meta_test.go` | `panickingReadCloser{}.Read` panic → 2s 内返回 error 含 "panic"（不是 120s 默认） |
| `TestDurableRecoveryWorkerSurvivesRunnerPanic` | `durable_recovery_worker_test.go` | `panickingThenSucceedingRunner`：第一次 panic、第二次 success；3s 内 `runner.calls >= 2` 且 `commitCalls >= 1`（证明 worker goroutine 没有在第一次 panic 后死亡） |
| `TestDurableRecoveryAttemptRunnerPanicCapturesMessage` | `durable_recovery_worker_test.go` | `panickingRunnerOnce.Run` panic "synthetic runner panic with sentinel" → `commitCalls == 0` 且 `rescheduleCalls >= 1` |

---

## §2 全量验证

| 命令 | 结果 |
|---|---|
| `go build ./...` | clean |
| `go vet ./...` | clean |
| `go test ./domains/streaming/ -count=1 -timeout 120s` | ok（67s，所有测试通过） |
| `go test ./domains/streaming/executors/... ./domains/streaming/integrity/... ./domains/streaming/state/... ./domains/transformation/... ./provider/... ./cmd/gateway/... -count=1 -timeout 180s` | ok（10 个目标包全绿） |
| `git status` | clean working tree |
| `git log origin/main -1` | `a8324a38e`（本次修复） |
| `git push origin main` | `ca553cf65..a8324a38e main -> main`（已推送） |

origin/main 期间新增了 2 个无关 commit：
- `ca553cf65 fix(sql): synchronize standard model seed mirrors`
- `22ef8f9ba feat(streaming): thread ClientSemanticBytesVisible through native Responses SSE`

均不涉及本轮触碰的 6 个文件，无需 rebase，直接 fast-forward push。

---

## §3 本轮提交历史

```
a8324a38e fix(streaming): extend panic recover to anthropic SSE reader, request body peek, durable recovery worker  ← 本次
ca553cf65 fix(sql): synchronize standard model seed mirrors                                                            ← origin/main
22ef8f9ba feat(streaming): thread ClientSemanticBytesVisible through native Responses SSE                              ← origin/main
6fbd3b4c5 merge: integrate origin/main (2026-08-29) — web request detail UX + §4/§5 handoff docs                       ← origin/main
d5142c7f6 docs(handoff): capture 2026-08-29 §4 hardening + origin/main P0 discoveries                                  ← 上次 handoff
1ac028aec fix(streaming): guard native Responses SSE reader goroutine against panic                                     ← §4 #1
```

---

## §4 §5.2 警示覆盖度

剩余未加固的 goroutine（本轮**明确**未触及）：

| 文件:行 | 模式 | 兜底机制 | 下一轮优先级 |
|---|---|---|---|
| `connection_registry.go:356` | buffered channel-send | 30s `DefaultClientWriteTimeout` timer fallback | 中（已有兜底，但 hang 30s 不优雅） |
| `durable_stream.go:92` | 长循环 lease renewal | 15s safety reaper 兜底 → 任务被回收 | 中（同上） |
| `handler.go:2270` | formatCache.Set fire-and-forget | 无兜底 | 低（fire-and-forget，panic 只丢一次 cache 更新） |
| `handler.go:2415` | session.Touch fire-and-forget | 无兜底（注释："best-effort touch, non-critical"） | 低 |
| `handler.go:5675` | anomalyRecorder.RecordAnomaly fire-and-forget | 无兜底 | 低 |
| `handler_autocombo.go:216` | OmniFree 并行 resolve | 错误路径返回 + `len(entries)` 容错 | 低 |
| `handler_state_init.go:37` | rt.Run 启动 | 无兜底（但 `rt.Run` 内部应有自己的 recover，需要验证） | 中（待审计 rt.Run 内部） |
| `format_cache.go:54` | cache.Set fire-and-forget | 无兜底 | 低 |
| `executors/health_tracker.go:79, 119` | 凭证健康 Append fire-and-forget | 无兜底（仅 slog.Warn） | 低 |
| `executors/executor.go:1871` | fpReleaseQueue 全局 worker（sync.Once init） | 无兜底（worker 死 → fp slot 永久泄漏） | **高**（与 #3/#4 同模式但无外部兜底） |

`executor.go:1871` 的 fp release worker 风险其实比 `durable_stream.go:92` 高——
它是 sync.Once init 的全局 worker，panic 死掉后整个进程的 fp 永久泄漏，
且不像 durable_stream 有 safety reaper。下一轮 §5 hardening 应优先考虑它。

---

## §5 经验教训（未来 harden 警示）

1. **§5.2 警示的有效性验证**：本次 4 处 hardening 中 #1/#2 是 §4 #1 的等价风险
   （reader goroutine + channel send），是必须加固的；#3/#4 是 durable 任务
   长生命周期循环的等价风险。**今后任何新增 reader goroutine / 长生命周期 worker
   必须自带 recover，否则就是隐藏的 §4 #1 等价 bug**。

2. **`executor.go:1871` 是更危险的同类问题**：sync.Once init 全局 worker 死了
   没人重启，比 durable worker（每次 Start 都启）更难恢复。建议下一轮优先。

3. **测试模板值得固化**：本轮 4 个 panic 测试都用 `panickingXxx` 简单类型 +
   紧 timeout + 断言 elapsed < timeout 的模式。建议在 `domains/streaming` 包内
   把这个模式写进 `testutil/`，下次类似 hardening 可以直接复用。

4. **panic recover 是 stream/IO 路径的合同**：本轮全部用命名返回值 + defer recover，
   不引入 helper 是为了保持最小 diff 与可读性。**但 §4 提到的"5 处 SSE/IO
   goroutine 中唯一没有 recover"已经扩展为"4 个 reader + 1 个 worker loop +
   1 个 attempt runner + 1 个全局 fp worker + 多个 fire-and-forget"**——
   一个统一的 `safego.Go(name, fn)` helper 在下一个 batch hardening 时值得考虑。

---

## §6 引用

- 本次修复：commit `a8324a38e` (4 处 panic recover + 4 个测试)
- 上游 commit：`.handoff/2026-08-29-followup-h4-hardening.md` §5.2 警示
- §4 #1 模板：commit `1ac028aec` `native_responses_stream.go:57-75`
- 测试模板：`TestNativeResponsesEventReaderPanicBecomesError`
  `domains/streaming/native_responses_stream_test.go:130-148`

## §7 当前状态（Current State）

- 工作目录：`/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-3`
- 分支：`main`，HEAD `a8324a38e`，与 `origin/main` 一致
- 工作区：clean
- 未推送改动：无
