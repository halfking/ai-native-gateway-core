# 流式计数器实现状态分析

## 发现

经过深入代码审查，我发现**流式块计数功能已经完全实现**！

## 现有实现

### 1. 数据流

```
stream.go (chunkCount 变量)
    ↓ 递增于每次 safeWriteSSE() 调用后
StreamCapture.chunkCount
    ↓ 通过 ObserveChunk() 递增
StreamCapture.SummaryAsMap()
    ↓ 导出为 "stream_chunk_count"
handler.emitTelemetry()
    ↓ 从 map 提取并设置
RequestLogEntry.StreamChunkCount
    ↓ 写入数据库
request_logs.stream_chunk_count (数据库列)
```

### 2. 关键代码位置

#### stream.go - 块计数跟踪
```go
// Line 160
chunkCount := 0 // Track number of chunks sent

// Line 265 - 第一个块
chunkCount++ // Count first chunk

// Line 424 - 主循环
safeWriteSSE(w, line)
safeFlush(flusher)
lastSend = time.Now()
chunkCount++ // Track chunks sent
```

#### audit.go - StreamCapture
```go
// Line 113
chunkCount int

// Line 183 & 300 - ObserveChunk 中递增
sc.chunkCount++

// Line 465 - SummaryAsMap 导出
"stream_chunk_count": sc.chunkCount,
```

#### handler.go - emitTelemetry
```go
// Line 2243-2244
if v, ok := m["stream_chunk_count"].(int); ok {
    reqLog.StreamChunkCount = &v
}
```

#### telemetry/client.go - RequestLogEntry
```go
// Line 108
StreamChunkCount *int `json:"stream_chunk_count,omitempty"`

// Line 667 & 919 - INSERT 语句中
entry.StreamChunkCount,
```

## 我们添加的 StreamChunksSent 字段

在 Migration 320 中，我们添加了一个**新的** `stream_chunks_sent` 字段，但实际上：

1. **已有的 `stream_chunk_count`** 字段已经准确跟踪成功发送的块数
2. **新的 `stream_chunks_sent`** 字段是多余的，因为功能已经存在

### 两者的区别

| 字段 | 来源 | 用途 |
|------|------|------|
| `stream_chunk_count` | StreamCapture（从流中自动提取） | 已存在，正在使用 |
| `stream_chunks_sent` | RequestLogContext（我们新添加） | 冗余，未被使用 |

## 建议

### 选项 1: 移除冗余字段（推荐）

从 Migration 320 中移除 `stream_chunks_sent` 和 `stream_chunk_errors`，因为：
- `stream_chunk_count` 已经跟踪成功的块
- 错误可以通过 `stream_interrupted` + `failure_detail_code` 推断

### 选项 2: 保留但重新定位用途

如果保留 `stream_chunks_sent`，可以用于**非流式错误路径**的计数（当 StreamCapture 不可用时），但这种情况很少见。

## 流式错误统计

关于 `stream_chunk_errors`，当前实现已经有：

1. **stream_interrupted** (boolean) - 标记流是否中断
2. **failure_detail_code** - 详细的中断原因
3. **stream_done_received** - 是否收到 [DONE]

可以通过这些字段组合查询流错误：

```sql
SELECT 
    COUNT(*) as interrupted_streams,
    failure_detail_code,
    AVG(stream_chunk_count) as avg_chunks_before_failure
FROM request_logs
WHERE stream_interrupted = true
    AND ts >= NOW() - INTERVAL '24 hours'
GROUP BY failure_detail_code
ORDER BY interrupted_streams DESC;
```

## 结论

**流式计数器功能已经完全实现并正常工作**，我们不需要添加额外的字段。

### 测试验证

可以通过以下查询验证：

```sql
-- 查看最近的流式请求及其块计数
SELECT 
    request_id,
    stream_chunk_count,
    stream_interrupted,
    failure_detail_code,
    latency_ms
FROM request_logs
WHERE request_mode = 'stream'
    AND ts >= NOW() - INTERVAL '1 hour'
ORDER BY ts DESC
LIMIT 10;
```

如果 `stream_chunk_count` 有值，说明功能正常工作。
