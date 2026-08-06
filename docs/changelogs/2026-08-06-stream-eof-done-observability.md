# P1 hot-patch — synthesized-[DONE] observability (2026-08-06)

- **commit**: `0818a07c`
- **影响**: minimax 上游 (provider_id=14 / cred 21) 在 ~13% 流不发 `data: [DONE]\n\n`；当前
  代码已经在 `domains/streaming/stream.go:759-760` 主动合成 [DONE] 给客户端，归类为
  `isBenignEOF` (stream.go EOF 分支 + executor_chat.go:975 + handler.go:5609 双重识别)，
  `success=true` 判定正确，但运维 dashboard 无法与"真中断"区分。
- **优先级**: P1 (observability gap only; zero behaviour change)
- **部署状态**: 154 生产 (PID 7898) 已运行 `2.4.9-0818a07c-20260805-1450`，08:40:19 启动；
  counter `llm_gateway_stream_synthesized_done_total` 已暴露，等待流量触发。

## 改动文件

```
domains/streaming/stream.go          | +18 -0  (1 hot branch insert)
domains/streaming/stream_eof_test.go | +135 -0 (countingRecorder + 2 tests)
metrics/interface.go                 | +14 -0  (Recorder method + Noop)
metrics/prometheus.go                | +32 -4  (counter field + impl)
metrics/metrics_test.go              | +34 -0  (2 tests)
```

总计 233 行新增、4 行删除，**纯增量观测、不删既有行为**。

## 新增 Prometheus 指标

```
# HELP llm_gateway_stream_synthesized_done_total Streams where the gateway had to inject a trailing 'data: [DONE]\n\n' because the upstream closed without one (MiniMax API ~13% rate as of 2026-07-28).
# TYPE llm_gateway_stream_synthesized_done_total counter
```

新增 slog 字段：

```
stream synthesized [DONE] terminator
  client_model=<minimax-m3>
  chunk_count=<N>
  had_capture=<bool>
```

## 行为不变性

| 维度 | 状态 |
|---|---|
| `outcome.Interrupted` | `!upstreamDoneReceived` (不变) |
| `outcome.Reason` | `"eof_without_done"` (不变) |
| `outcome.Kind` | `errorsx.KindUpstreamDown` (不变) |
| `outcome.ChunkCount` | 不变 |
| `isBenignEOF` (executor+handler) | 不变 |
| `audit: request completed success` | 不变 |
| `safeWriteSSE` + `safeFlush` 顺序 | 不变 |
| `streamErrorKindForDetailCode` 映射 | 不变 |

## 测试

| 测试 | 状态 |
|---|---|
| `TestStreamChatWithPendingCapture_EOFWithoutDoneAppendsDone` (原有) | ✅ PASS |
| `TestStreamChatWithPendingCapture_EOFWithoutDoneZeroChunks` (原有) | ✅ PASS |
| `TestStreamChatWithPendingCapture_SynthesizedDoneIncrementsMetric` (新) | ✅ PASS |
| `TestStreamChatWithPendingCapture_UpstreamDoneNoSynthMetric` (新 inverse) | ✅ PASS |
| `TestNoopRecorder_StreamSynthesizedDoneNoCrash` (新) | ✅ PASS |
| `TestPrometheusRecorder_StreamSynthesizedDoneCounter` (新) | ✅ PASS |
| `go build ./...` | ✅ |
| `go vet ./domains/streaming/... ./metrics/...` | ✅ |
| `go test ./domains/streaming/... ./metrics/...` | ✅ |

## 部署观察 (24h post-deploy)

期望：

1. `journalctl _SYSTEMD_UNIT=llm-gateway-go.service | grep "stream synthesized [DONE]"` — 应该出现 ~13% × minimax 流式 QPS。
2. Prom counter `llm_gateway_stream_synthesized_done_total` 应当按比例增长。
3. `audit: request completed success=true` 总数 与 `upstream EOF without [DONE]` 比例应当接近 1:1。

如果 counter 在 24h 后仍为 0、或者与 minimax 流式 QPS 不成比例，说明有其它症状 — 需重新诊断。

## 回滚预案

```bash
git revert 0818a07c
git push origin main
```

回滚影响：删除一个 slog.Warn + 一个 Prometheus Counter；零行为变更风险。

## 关联故事

切合生产诊断请求：用户报告 minimax 上游偶尔不发 DONE 导致客户端"会话直接停止"，怀疑是网关中信息截断或格式异常。本 hot-patch 给出可观测性而非路径修正 — 因为当前代码已经在 stream.go:759-760 主动合成 [DONE]，**真问题在 minimax API 行为**。Counter 让运维识别 minimax 已知模式 != 真实中断。

诊断过程完整记录在 `.handoff/2026-08-06-stream-eof-without-done-observability.md`
（本地目录，gitignored）。
