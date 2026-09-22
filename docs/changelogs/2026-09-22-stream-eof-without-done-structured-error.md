# 2026-09-22 §11.6 生产流式伪成功修复——OpenAI chat EOF-without-DONE 走结构化错误

## 概要

P0 修正 `cmd/gateway` 的 OpenAI chat 流式执行器在「HTTP 200 + 已 committed 语义帧 +
EOF 但无 `data: [DONE]`」路径下静默标 success 的伪成功行为。该行为来自 2026-09-01
commit `05c79fbe9`（"treat eof_without_done after commit as benign upstream
non-compliance"），直接违反综合测试方案 §11.6 第三行「HTTP 200 空响应或 SSE 无
`[DONE]` | 不向客户端返回伪成功，切换或返回结构化错误」。

测试网关（`tests/stress/gateway`）的「先缓冲再写客户端」不能搬到生产路径（会破坏
TTFB、流式体验、长流内存上限），所以唯一合规路径是「committed 截断时返回结构化错误」
而不是 transparent failover 或 silent 200。

## 改动

1. **EOF 分支翻转**（`domains/streaming/stream.go:1148-1220`）

   committed 截断（旧：benign success → 新：structured-error failure）：

   | 维度 | 旧（05c79fbe9） | 新（§11.6） |
   |---|---|---|
   | `outcome.Interrupted` | `false` | **`true`** |
   | `outcome.Reason` | `"eof_without_done_after_commit"` | **`"eof_without_done"`**（在 `audit.isInterruptionCode` 白名单里） |
   | `outcome.Kind` | `KindEmptyResponse` | **`KindUpstreamDown`** |
   | `outcome.Resumable` | `false` | `false`（committed 字节不能重试） |
   | 客户端 wire | 仅合成 `[DONE]` | **结构化错误帧 + 合成 `[DONE]`**（顺序：error 在前） |
   | `audit stream_interrupted` | `false` | **`true`**（`request_logs.success=false`） |
   | `audit failure_detail_code` | 空 | **`"eof_without_done"`** |
   | 电路熔断 / 凭据健康度 | 不计入失败 | **计入失败**（慢性截断会被降权） |
   | `RecordStreamSynthesizedDone` | 递增 | **递增**（合成 [DONE] 仍在线上，运维信号保留） |

   chunk_count=0 的 EOF 分支（uncommitted）保持原样（`Interrupted=true,
   Resumable=true`，透明 failover 到下一个候选）。

2. **wire 形状**（客户端 SDK 实际收到的字节，error envelope 必须先于合成 [DONE]）：

   ```
   data: {"id":"chunk-1","choices":[{"delta":{"content":"partial answer"}}]}\n
   \n
   data: {"id":"chunk-2","choices":[{"delta":{},"finish_reason":"stop"}]}\n\n
   data: {"error":{"type":"upstream_incomplete","message":"upstream closed the stream without sending [DONE]","code":"eof_without_done"}}\n\n
   data: [DONE]\n\n
   ```

   HTTP 状态保持 200（流已 started，不能中途改）。Composition Client 打开 SDK 错误帧
   视为流错误（与 `stream_timeout` / `json_error_in_stream` 同形态）。

3. **测试更新**（`domains/streaming/stream_eof_test.go`）：

   - 改名 + 翻转断言：
     - `EOFWithoutDoneAfterCommitIsCompletedAndNotRetryable` →
       `EOFWithoutDoneAfterCommitIsStructuredError`（outcome 翻 true；wire 含 error envelope）
     - `EOFWithoutDoneAfterCommitIsSuccess` →
       `EOFWithoutDoneAfterCommitIsStructuredErrorCapture`（capture 翻 interrupted；failure_detail_code 填充）
     - `SynthesizedDoneIncrementsMetric` → 断言 `Interrupted=true` 但 `synth` 计数=1
       仍递增（合成 [DONE] 落线）
   - 新增 §11.6 全链路 guard：`TestStreamChatWithPendingCapture_Section11_6_PseudoSuccessGuard`
     一次钉 wire 顺序（error envelope 先于合成 [DONE]）+ HTTP 200 不被改写 + audit
     capture 失败 + executor Kind=KindUpstreamDown + 没有 silent 200。

## 改动文件

```
domains/streaming/stream.go          | +60 -30 (EOF 分支翻转)
domains/streaming/stream_eof_test.go | +200 -80 (3 改 1 增)
docs/05-testing/02-test-plans/testing/comprehensive-test-plan.md | +18 -1
docs/handoff/2026-09-22-stress-audit-handoff.md | 重写
```

未触动：`vendor/`、`go.sum`、`cmd/gateway/main.go` 编译入口以外的 wiring、
`tests/stress/gateway`（测试网关仍保留旧「先缓冲再写」行为，专供短流 mock）。
未触动 ring / minheap / ctxpool / prompt_compress / chunk_buffer 接线（Handoff-B
未接线之前不评吞吐）。

## 测试

```bash
go build ./domains/streaming/...                                       # clean
go test ./domains/streaming/ -count=1 -timeout 180s                   # PASS 70.950s
go test ./domains/streaming/executors/ -count=1 -timeout 180s         # PASS 33.519s
go test ./domains/hooks/audit/ ./domains/dispatch/ -count=1            # PASS
go test ./cmd/gateway/ -count=1 -timeout 180s                          # PASS 1.346s
```

新增 / 修改的测试（全部 PASS）：

| 测试 | 钉住的不变量 |
|---|---|
| `TestStreamChatWithPendingCapture_EOFWithoutDoneAfterCommitIsStructuredError` | outcome 翻转 + wire 含 error envelope + 顺序 |
| `TestStreamChatWithPendingCapture_SynthesizedDoneIncrementsMetric` | synth 计数=1（合成 [DONE] 仍落线） |
| `TestStreamChatWithPendingCapture_EOFWithoutDoneAfterCommitIsStructuredErrorCapture` | audit capture stream_interrupted + failure_detail_code |
| `TestStreamChatWithPendingCapture_Section11_6_PseudoSuccessGuard` | §11.6 全链路 |
| `TestStreamChatWithPendingCapture_EOFWithoutDoneZeroChunks`（不变） | uncommitted EOF 走原失败路径（Resumable=true） |
| `TestStreamChatWithPendingCapture_EOFWithoutDoneZeroChunksRemainsFailure`（不变） | capture 失败路径不变 |

## 关联背景

- 触发场景：本任务接续 `docs/handoff/2026-09-22-stress-audit-handoff.md` 的「生产流式
  伪成功（§11.6）」条目；明确「不要抄测试网关整包缓冲」。
- 历史背景：2026-09-01 commit `05c79fbe9` 修补了 minimax ~13% 不发 [DONE] 的产品
  bug，但走的是「benign completion」路径（success=true），与 §11.6 字面冲突。本
  commit 是它的「正确性补丁」：保持合成 [DONE] 给客户端（OpenAI SDK 仍能收尾），
  同时改 audit / executor / 电路熔断 / 凭据健康度走真实失败路径。
- 运维识别：committed 截断 vs uncommitted 截断用 SQL filter
  `chunk_count > 0 AND failure_detail_code = 'eof_without_done'` 区分；
  `llm_gateway_stream_synthesized_done_total` 指标继续观测供应商省略 [DONE] 行为。

## 回滚预案

```bash
git revert <this-commit>
git push origin main
```

回滚影响：把 committed 截断路径退回到 `05c79fbe9` 的 benign success。**注意**：这会
恢复 §11.6 禁止的伪成功，仅作紧急 hotfix 使用，正式版必须重新应用本 commit。

## 关联故事

任务入口：「继续会话：docs/handoff/2026-09-22-stress-audit-handoff.md」，约束：
- 被测对象改为 `cmd/gateway` 的 stream executor，不是 `tests/stress/gateway`；
- HTTP 200 SSE 无 `[DONE]` 不得当作成功；
- 已 committed 的截断流要有结构化错误，不能静默 200；
- 真实供应商长流禁止整包缓冲后再写客户端；
- 禁止真实供应商进 `tests/stress`；本地 mock 16/16 不能过 §11。

本 commit 满足前 4 条；第 5 条靠 `tests/stress/gateway` 的 mock-only 边界维持（已在
handoff 文档与综合测试方案 §11.7 钉住）。

## 部署跟进 / 真实供应商验收

本地单测全绿仅证明 `cmd/gateway` 代码契约变更正确。**§11.6 第三行的真实供应商验收
仍需在 154/245 上跑**「上游不发 [DONE] + EOF」故障注入脚本，断言：

1. `request_logs.success = false`；
2. 流式响应体内含 `upstream_incomplete` 错误帧；
3. 错误帧位于合成 `[DONE]` 之前；
4. `failure_detail_code = "eof_without_done"`；
5. 凭据健康度 / 电路熔断正确计入失败。

本地 mock 16/16 不替代此验收（mock 不模拟此协议违例）。