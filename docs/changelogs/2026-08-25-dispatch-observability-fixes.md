# 2026-08-25 dispatch/streaming 并发可观测性修复（minimax-m3 高并发审计产出）

## 背景

245 minimax-m3 高并发"变慢 + 供应商泳道请求量偏少"排查的代码侧收尾。泳道缺数的根因
（`update_session_summary()` DECIMAL(10,6) token 比例溢出回滚整个 telemetry 事务）已由迁移
571/572（commit `5689df7cc`）修复并在 252 验证 applied。本 changelog 只覆盖并发审计确认的
四个 Go 侧缺陷。

## 修复

### 1. LatencyTracker 滑动窗口失效（domains/routing/latency_tracker.go）

`evictStale` 原为空实现（无样本时间戳、循环体直接 break），60s 窗口从不淘汰。weighted
router 的 LatencyPenalty 因此被永久陈旧的均值污染。现在每条样本记录 UnixMilli 时间戳，
`Avg/P50/P90/P99/Count` 读路径在写锁内做真正的前缀淘汰；maxSize 裁剪与时间淘汰共用同一
样本集。新增注入时钟的回归测试覆盖"窗口外样本被所有读取剔除"与"size trim + 窗口共存"。

### 2. CapacityRetryCount 污染错误重试（domains/dispatch/dispatcher.go）

容量重试成功入队后计数从不清零，而 `onRetryDue` 只要 `CapacityRetryCount > 0` 就走全量
重路由。后果：一次队列拥塞后，后续同凭据 5xx/timeout 的"同凭据重试"被错误放大为重新
路由/换节点，放大延迟并破坏 RetryPerCredential 语义。现在 `dispatch()` 成功入队分支清零
该计数；容量再次满时 `scheduleCapacityRetry` 会重新递增，语义不变。

### 3. GateWriter 未闭合 SSE 帧无上限（domains/streaming/gate_writer.go）

`Write` 对无 `\n\n` 边界的字节无限追加 `pending`；attempt gate 的
`MaxMetadataBufferBytes` 只约束进入 gate 的完整帧，不覆盖该缓冲。新增
`gateWriterMaxPendingBytes`（默认 4 MiB，var 便于测试降档）：越界丢弃 pending 并返回
`ErrGateWriterFrameOverflow` 中止当前 attempt，gate 保持可用。

### 4. RetryScheduler 关闭丢弃 parked 请求（domains/dispatch/retry_schedule.go + pipeline.go）

`Close()` 原只停 picker，堆内未到期请求永远等不到 `ResultCh`，优雅停机时 Submit 调用方
悬挂。新增 `NewHeapRetrySchedulerWithCloseHandler`：Close 排空堆并把剩余项交给回调；
`Pipeline.NewDefaultRetryScheduler` 接线为 `complete(qr, ErrShutdown)`。原
`NewHeapRetryScheduler`（nil 回调，保持丢弃行为）签名不变，生产接线（cmd/gateway/main.go
经 NewDefaultRetryScheduler）自动获得完成语义。

### 5. TotalQueueCapacity 注释对齐实现（domains/dispatch/config.go）

注释原声称"从 Submit 持有到 complete"，实现实为 Tier-0 等候室（进入 model lane 即释放）。
只改注释、不动准入行为；全生命周期持有的语义变更加单独压测评估（见 handoff）。

## 验证

- `go test ./domains/routing ./domains/dispatch ./domains/streaming -count=1` ✅
- `go test -race ./domains/routing ./domains/dispatch ./domains/streaming` ✅
- `go vet` / `gofmt` / `go build ./cmd/gateway` ✅

## 明确不做（另行跟踪）

- TotalQueueCapacity 改为全生命周期持有的行为变更（显著影响 429 率，需压测）
- 245/154 部署与 A/B 验证（走常规 deploy-245 流程）
- 请求体存储优化（573 迁移 .skip）与遥测侧信道硬化（已有独立 plan）
