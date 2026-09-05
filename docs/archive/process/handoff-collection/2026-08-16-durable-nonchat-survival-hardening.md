# Durable 非 Chat 流式接线后续加固交接（2026-08-16）

## 当前状态

- 分支：`main`
- 代码提交：`5a62fb89 fix(streaming): wire durable survival for nonchat streams`
- 基线：该提交建立在 `origin/main` 的 `ac16be37` 之上。
- 工作树：交接文档落盘前代码工作树干净。

## 已完成

`/v1/messages` 与 `/v1/responses` 的流式 durable 请求不再固定返回 `501 streaming_durable_unsupported`，而是复用 Chat 已有的前台 durable / `SurvivalCoordinator` 语义。

1. 两端点在既有 durable snapshot cut point 调用 `maybeStartDurable(..., foreground=true)` 并保留 `DurableStreamBinding`。
2. 两端点将现有 `executors.ExecParams` 构造改为 writer 工厂；durable binding 存在或 tenant 开启 request survival 时，统一通过 `runSurvivalCoordinator` 执行。
3. 两端点复用 Chat 的 response interceptor stream writer 生命周期：将 coordinator 的 base writer 包装为 `interceptingStreamWriter`，并在流结束时调用 `finish()`。
4. coordinator 负责的 native SSE terminal 已写出后，handler 不再追加 endpoint 的 JSON 错误体。
5. 新增 `releaseDurableBeforeSurvival`：候选解析错误、无候选、Responses 的 invalid-model 分支发生在任务已 create-and-claim、但 coordinator 尚未接管时，立即使用 task 的 owner/fencing token `Reschedule` 给 RecoveryWorker，避免任务一直保持 claimed/running 直至前台 lease 超时。
6. 新增 `TestReleaseDurableBeforeSurvivalHandsTaskToWorker`，验证该提前移交带正确 fencing 字段、原因且不伪造 terminal 结果。

## 审计结论

本次审计发现并已修复两个 P1：

- 非 Chat 前台 durable 的候选解析早退会遗留 claimed task。
- 非 Chat survival 路径未接入 response interceptor/session-capture writer 生命周期。

以下已确认但未在本提交扩展处理，需作为独立 durable 核心加固任务：

- `settleDurableStream` 中 `Complete`、`ReleaseToWorker` 或 `FailTerminal` 的 store 写入失败目前只记录日志后停止续租。该行为依赖后续 safety reaper；需要设计可靠重试或补偿机制，但不得破坏 **post-content disconnect 必须进入 `resume_safety_blocked`、不可重放** 的现有安全语义。
- 需要补端点级（而非仅 coordinator/component 级）故障注入覆盖：Messages/Responses 的 checkpoint failure、lease loss、deadline、client disconnect，以及 native terminal SSE 恰好一次且不追加 JSON body。
- `docs/修订0811/32-M4-S30-S35故障注入统一验证报告-2026-08-15.md` 第 54 行仍写着 Messages/Responses 流式 durable 为 501；该状态已经被本提交替代，应在完成端点级验证后更新报告，不要仅凭当前 component tests 宣称完整 S30-S35 覆盖。

## 验证证据

在最终 rebase 到 `ac16be37` 后：

```text
go test ./domains/streaming -run 'Test(ReleaseDurableBeforeSurvival|Durable|Survival)' -count=1  # pass
go test -race ./domains/streaming -count=1                                                       # pass
go test ./... -count=1                                                                            # pass
go vet ./...                                                                                      # pass
```

## 下一会话提示词

```text
继续 llm-gateway-go main 上 durable 前台流式的后续加固工作。先读取：
- docs/handoff/2026-08-16-durable-nonchat-survival-hardening.md
- 提交 5a62fb89
- docs/修订0811/32-M4-S30-S35故障注入统一验证报告-2026-08-15.md §5

背景：5a62fb89 已让 /v1/messages 和 /v1/responses 的 streaming durable 进入 SurvivalCoordinator，并修复候选解析早退时将 claimed task 立即 ReleaseToWorker 与 response interceptor lifecycle 对齐。不要回退这部分。

本次目标：设计并实现 durable settlement store 写失败的可靠补偿/重试方案，并补齐 Messages/Responses 的端点级故障注入测试。必须保持：
1) semantic bytes 发送前 checkpoint 写前持久化；
2) client disconnect pre-content 可安全交给 worker；
3) post-content/tool-call disconnect 必须保持 resume_safety_blocked、绝不能重放；
4) Anthropic 使用 event:error，Responses 使用 response.failed，且 native SSE terminal 后不追加 JSON；
5) 不丢弃或覆盖其他人的工作树改动。

先进入 plan mode，审计 durable settlement/reaper/store contract 与当前 endpoint fixture 可测试性，提出最小设计并获批后再改代码。完成后运行 focused、-race、go test ./...、go vet，并更新 doc 32 §5 的状态与验证边界。
```
