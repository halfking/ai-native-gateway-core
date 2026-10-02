# 本地 mock 压测审计 Handoff（2026-09-22 第二轮 + §11.6 生产修补）

> 上一轮关注对象：`tests/stress/gateway` + mock 上游。
> 本轮关注对象：**`cmd/gateway` 的 OpenAI chat 流式执行器**（`domains/streaming/stream.go`
> 的 `streamReadEOF` 分支），与测试网关那套「缓冲再写」是两套不同代码。

## 本轮收口（2026-09-22 12:30–15:48）

### 上一轮留口
- 上一轮（02:01，commit `a78fdfb27`）修了 timeout 空跑 / s7 alpha 回流 / 26k prompt /
  s14 pool，但 **s11 仍先写 HTTP 200 再截断**：80 条全是 200，8 条缺 `[DONE]`，
  靠客户端 `done_rate` 抓漏。方案 §11.6 未满足。
- 本轮把测试网关 SSE 改成 **缓冲到 `[DONE]`/EOF 再写客户端**。缺 `[DONE]` 不
  committed，可 failover。s11 断言改为 `require_done` + 禁止 `mock-alpha` + 要求 `mock-beta`。
- 2026-09-22 11:47:59–11:49:18 全量 **16/16 PASS**（`tests/stress/results/report.json`）。
- 文档：测试方案头部改为 v1.2；§11.7 写明缓冲只在测试网关；`REPORT.md` 按 11:47 数字重写。

### §11.6 生产修补（本次收口）
测试网关的「缓冲再写」只在 mock 上能用，**生产 `cmd/gateway` 不允许整包缓冲后再写**
（会破坏 TTFB、流式体验、长流内存上限）。所以必须把「committed 截断」改成
「结构化错误」，而不是「silent 200」。

**改动**：把 2026-09-01 commit `05c79fbe9`（"treat eof_without_done after commit as
benign upstream non-compliance"）的 benign 路径**反过来**：

| 维度 | 旧（05c79fbe9） | 新（本次 §11.6） |
|---|---|---|
| `outcome.Interrupted` | `false`（成功） | **`true`**（失败） |
| `outcome.Reason` | `"eof_without_done_after_commit"` | **`"eof_without_done"`**（在 `audit.isInterruptionCode` 白名单里） |
| `outcome.Kind` | `KindEmptyResponse`（非失败） | **`KindUpstreamDown`**（失败） |
| `outcome.Resumable` | `false` | `false`（不变，committed 字节不能重试） |
| 客户端 wire | 只发合成 `[DONE]` | **结构化错误帧 + 合成 `[DONE]`**（顺序：error 在前） |
| audit `stream_interrupted` | `false`（success=true） | **`true`**（success=false） |
| audit `failure_detail_code` | 空 | **`"eof_without_done"`**（SQL filter 可命中） |
| `RecordStreamSynthesizedDone` 指标 | 递增 | **递增**（合成 [DONE] 仍在线上，运维信号保留） |
| 电路熔断 / 凭据健康度 | 不计入失败 | **计入失败**（凭据慢性截断会被降权） |

**wire 形状**（客户端 SDK 实际收到的字节）：
```
data: {"id":"chunk-1","choices":[{"delta":{"content":"partial answer"}}]}

data: {"id":"chunk-2","choices":[{"delta":{},"finish_reason":"stop"}]}

data: {"error":{"type":"upstream_incomplete","message":"upstream closed the stream without sending [DONE]","code":"eof_without_done"}}

data: [DONE]
```
HTTP 状态保持 200（流已 started，不能中途改状态码）。结构化错误帧 + 合成 [DONE] 就是 §11.6
要求的「结构化错误」。

**为什么 `Reason` 不新造字面量**：必须落在 `audit.isInterruptionCode` 白名单里，
否则 `failure_detail_code` 列会保持空，运维就抓不到这些 committed 截断。
运维用 `chunk_count > 0 AND failure_detail_code = 'eof_without_done'` 区分
committed vs 未 committed 截断。

## 改动文件

| 文件 | 性质 |
|---|---|
| `domains/streaming/stream.go` | 翻转 EOF 分支：committed 截断走结构化错误路径 |
| `domains/streaming/stream_eof_test.go` | 改 3 个测试 + 加 1 个 §11.6 全链路 guard |
| `docs/05-testing/02-test-plans/testing/comprehensive-test-plan.md` | §11.6 第三行展开，含可验收断言 |
| `docs/handoff/2026-09-22-stress-audit-handoff.md` | 本文件（更新本轮收口） |

未触动：`vendor/`、`go.sum`、`cmd/gateway` 编译入口之外的 wiring、
ring/minheap/ctxpool/prompt_compress/chunk_buffer 接线（这些是 §11.7 Handoff-B 的
七项优化，按任务约束先接线再谈吞吐）。

## 测试命令与结果

```bash
go build ./domains/streaming/...              # clean
go test ./domains/streaming/ -count=1 -timeout 180s        # PASS 70.950s
go test ./domains/streaming/executors/ -count=1 -timeout 180s  # PASS 33.519s
go test ./domains/hooks/audit/ ./domains/dispatch/ -count=1  # PASS
go test ./cmd/gateway/ -count=1 -timeout 180s               # PASS 1.346s
```

新增 / 修改的测试（全部 PASS）：

| 测试 | 锚点 |
|---|---|
| `TestStreamChatWithPendingCapture_EOFWithoutDoneAfterCommitIsStructuredError` | committed 截断 → Interrupted=true、Kind=KindUpstreamDown、wire 含 error envelope + [DONE] |
| `TestStreamChatWithPendingCapture_SynthesizedDoneIncrementsMetric` | `RecordStreamSynthesizedDone` 仍递增，synth 计数=1 |
| `TestStreamChatWithPendingCapture_EOFWithoutDoneAfterCommitIsStructuredErrorCapture` | audit capture `stream_interrupted=true` + `failure_detail_code="eof_without_done"` |
| `TestStreamChatWithPendingCapture_Section11_6_PseudoSuccessGuard` | §11.6 全链路：wire 顺序（error 在 [DONE] 前）+ audit + executor 分类 + 没有 silent 200 |
| `TestStreamChatWithPendingCapture_EOFWithoutDoneZeroChunks`（不变） | chunk_count=0 + EOF 仍走原失败路径（Resumable=true） |
| `TestStreamChatWithPendingCapture_EOFWithoutDoneZeroChunksRemainsFailure`（不变） | capture 失败路径不变 |

## 结论 / 根因

- §11.6 禁止的伪成功：production `cmd/gateway` chat 流在 2026-09-01 MiniMax 修补里
  把「committed 截断」静默标 success，与 §11.6「HTTP 200 SSE 无 `[DONE]` 不得当作成功」
  冲突。
- 修补方式（不能用测试网关的「整包缓冲」）：committed 字节已落到客户端连接，**不可**
  透明 failover（会重复发送），所以 §11.6 路径只剩「返回结构化错误」一种。
- 实际行为：客户端收到 committed 内容 + 一个 SSE 错误帧 + 合成的 `[DONE]`，状态仍是
  200（不能中途改状态码）；audit 记 success=false，failure_detail_code=`eof_without_done`，
  电路熔断与凭据健康度正确计入失败。

## 明确没有完成的事 / 遗留风险

1. **§11.6 第三行的真实供应商验收仍没做**——只是改了 `cmd/gateway` 的代码契约 +
   单元测试，没有在 154/245 上跑「真实供应商省略 [DONE]」的故障注入。Mockito 上跑
   也不算数（mock 不模拟此协议违例）。
2. 154/245、真实模型、30 min RSS、cgroup 容量、TPM 折算：都没做。
3. Handoff-B 七项优化（ring / minheap / ctxpool / prompt_compress / chunk_buffer
   等）：**未接线**。按任务约束未接线先不评吞吐。本地 mock 16/16 不能当作生产吞吐
   已 OK。
5. 测试网关 `tests/stress/gateway` 仍保留「先缓冲再写」的旧行为（适用于短流 mock），
   不要把测试网关的缓冲逻辑搬到生产路径。
6. 如果上游（如 MiniMax）真的稳定不发 `[DONE]` 且发送完整 finish_reason，可能频繁
   触发本失败路径 → 凭据慢性降权。Mitigation：上游打补丁前，运维可以在凭据上临时
   配 `quality_fix_mode=on` 之类的适配层（如果有），或者在 §11.3 真实供应商门禁里
   决定是否给这种上游打「忽略 EOF」白名单——但 §11.6 字面是禁止「silent 200」，
   所以这是有意识保留的「失败计入」设计取舍，不是 bug。

## 复跑 §11.6 单元测试

```bash
go test ./domains/streaming/ -run 'Section11_6|EOFWithoutDone' -v -count=1
```

## 下一轮入口（按优先级）

1. **§11.6 真实供应商门禁**：154 低并发 → 245 低并发 → 故障注入脚本
   「上游不发 [DONE] + EOF」 → 断言 `request_logs.success=false`、流体内含
   `upstream_incomplete` 错误帧、错误帧位于合成 [DONE] 之前。**本地 mock 不能替代此验收**。
2. Handoff-B 七项优化（ring / minheap / ctxpool / prompt_compress / chunk_buffer）：
   先接线到 `cmd/gateway` 真实请求路径，再谈 s8 / s13 吞吐。合成 in-process 测试不算。
3. Linux cgroup 容量：154/245 跑 `capacity_matrix.sh`。macOS GOMAXPROCS 与
   ×0.033 TPM 折算都未验证。
4. 是否给「稳定不发 [DONE] 但发完整 finish_reason」的上游加白名单（需在
   §11.6 字面与供应商兼容性之间做权衡，本轮默认不破 §11.6 字面）。

---

## 上一轮留口（保留备查）

- `tests/stress/results/report.json` 16/16 PASS @ 11:47。
- 测试网关只对 mock 缓冲；不要照搬到 `cmd/gateway`。
- `Proxy: nil` / `reset` tag / `mock-stress-large` 只绑 delta 不变。
- `tests/stress/handoff_b_s8s13_test.go` 未提交（Handoff-B 7 项未接线）。