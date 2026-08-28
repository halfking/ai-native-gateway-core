# 2026-08-28 Streaming P0 审计后续修复报告

## 审计范围与基线

- **审计时间**：2026-08-28
- **基线提交**：`fca45d614` (origin/main)
- **目标提交**：`4317624c1` (fix/p0-streaming-body-gate-20260827)
- **审计方法**：代码路径追踪、单元测试覆盖验证、并发与边界条件分析

## 审计结论摘要

上一轮 P0 修复（`4317624c1`）已通过同 patch 副本 `72e735d5b` 进入主线；其功能在当前 `origin/main` 中存在。但审计发现该修复仍有遗漏：

1. **Anthropic 错误响应 body drain 不完整**
   - 只读取一次 4096 字节，短读时不继续消费
   - `PreStreamPrepared`、model-not-found、context-length、retryable/heuristic 等 4xx 早返回路径缺少统一 drain
   - 上游返回 nil Body 时未防御

2. **SerializedStreamWriter/AttemptCommitGate 短写与容量边界**
   - Writer 对 `(n < len(p), err == nil)` 短写不转为 `io.ErrShortWrite`
   - Gate `appendBufferedLocked` 在 append 后才检查容量，允许 `bufferLen` 短暂超过 `maxMetadata`

## 修复内容

### 1. Anthropic body 生命周期修复

**文件**：`domains/streaming/executors/executor_anthropic.go`

**新增 helper**：
```go
func readAndDrainErrorBody(body io.Reader) ([]byte, error)
```
- 通过 `io.LimitReader` 捕获最多 `maxPassthroughErrorBody` (64 KiB) 用于分类/透传
- 继续消费剩余 body 至 EOF，保证 HTTP 连接复用
- 处理短读、nil Body

**修改路径**：
- 所有 `resp.StatusCode >= 400` 分支统一调用 `readAndDrainErrorBody`
- 删除旧的单次 `Read(4096)` 和条件 drain 逻辑
- 保持现有错误分类、脱敏与 retry 语义不变

**新增测试**：`domains/streaming/executors/executor_anthropic_body_drain_test.go`
- `TestReadAndDrainErrorBody_ShortReads`：验证 1 KiB 分块读取 8 KiB body
- `TestReadAndDrainErrorBody_ExceedsCap`：验证 128 KiB body 只保留 64 KiB prefix，剩余已 drain
- `TestReadAndDrainErrorBody_NilBody`、`_EmptyBody`：边界条件

### 2. SerializedStreamWriter 短写修复

**文件**：`domains/streaming/serialized_stream_writer.go`

**修改**：`write` 方法
```go
n, err := s.w.Write(p)
if err != nil {
    s.detached = true
    s.detachErr = err
    return n, err
}
// 2026-08-28 audit fix: treat short writes as errors
if n < len(p) {
    s.detached = true
    s.detachErr = io.ErrShortWrite
    return n, io.ErrShortWrite
}
return n, nil
```

**新增测试**：`domains/streaming/serialized_stream_writer_short_write_test.go`
- `TestSerializedStreamWriter_ShortWriteDetached`：验证 `(5, nil)` 短写导致 detach
- `TestSerializedStreamWriter_ShortWriteWithError`：验证 `(3, err)` 短写保留原错误

### 3. AttemptCommitGate 容量前置检查

**文件**：`domains/streaming/attempt_commit_gate.go`

**修改**：`appendBufferedLocked`
```go
// Check capacity BEFORE appending
if g.bufferLen+len(frame) > g.maxMetadata {
    metrics.SurvivalAttemptGateMetadataOverflowTotal.WithLabelValues(...).Inc()
    return ErrAttemptMetadataBufferExceeded
}
g.buffer = append(g.buffer, frame...)
g.bufferLen += len(frame)
```

**新增测试**：`domains/streaming/attempt_commit_gate_capacity_test.go`
- `TestAttemptCommitGate_CapacityCheckBeforeAppend`：验证超限帧被拒绝且 `bufferLen` 未超过 cap

## 测试结果

### 新增测试
```bash
# Anthropic drain tests
PASS: TestReadAndDrainErrorBody_ShortReads
PASS: TestReadAndDrainErrorBody_ExceedsCap
PASS: TestReadAndDrainErrorBody_NilBody
PASS: TestReadAndDrainErrorBody_EmptyBody

# Writer short-write tests
PASS: TestSerializedStreamWriter_ShortWriteDetached
PASS: TestSerializedStreamWriter_ShortWriteWithError

# Gate capacity tests
PASS: TestAttemptCommitGate_CapacityCheckBeforeAppend
```

### 回归测试
```bash
go test ./domains/streaming/... ./domains/streaming/executors/...
ok  	github.com/kaixuan/llm-gateway-go/domains/streaming	66.821s
ok  	github.com/kaixuan/llm-gateway-go/domains/streaming/executors	(cached)
ok  	github.com/kaixuan/llm-gateway-go/domains/streaming/executors/webcookie	(cached)
ok  	github.com/kaixuan/llm-gateway-go/domains/streaming/integrity	(cached)
ok  	github.com/kaixuan/llm-gateway-go/domains/streaming/state	(cached)
```

所有 streaming 包测试通过，无新增回归。

## 未修复风险

1. **Gate checkpoint 与 Discard 并发**：计划审计中识别了 `Commit`/`WriteFrame` metadata hook 解锁期间与 `Discard` 的竞态窗口，但现有测试未能稳定复现该行为，因此暂未修改实现；保留为未来 P2 审计项。

2. **Durable checkpoint context 接线**：当前审计分支工作树包含未提交的 `CheckpointContext` API，但生产 wiring 尚未完整接入；本次修复不改变该状态。

## 审计依据

- 目标提交 `4317624c1` 与 `72e735d5b` 的 patch-id 相同（`0521a551be70...`），确认为重复提交
- 合并提交 `61dc4985d` 的第一父 `031241f17` 已包含等价 P0 修复
- Anthropic executor 所有 4xx 早返回路径通过静态代码审查验证未调用 drain
- SerializedWriter 的 `write` 方法对 short-write 仅返回 `n`，未转为错误
- Gate `appendBufferedLocked` 在 L489 append、L493 才检查容量

## 相关提交

- 基线：`fca45d614` (origin/main, 2026-08-28)
- 目标：`4317624c1` (fix/p0-streaming-body-gate-20260827)
- 重复：`72e735d5b` (同 patch，先进入主线)
- 本次修复：`fix/streaming-p0-audit-20260828` 分支

## 后续建议

1. 对 checkpoint/Discard 并发补充 barrier-based deterministic test，明确 fail-closed 或 happen-before 契约
2. 验证 durable `CheckpointContext` 的完整 wiring，确保 streaming 生产路径实际调用带 context 的 hook
3. 补充端到端 Anthropic 4xx/5xx 与 connection-reuse 集成测试（需稳定 upstream mock）
