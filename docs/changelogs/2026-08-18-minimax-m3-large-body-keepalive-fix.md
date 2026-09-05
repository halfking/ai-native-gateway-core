# 2026-08-18 — minimax-m3 / 大 body 客户端断连修复

> B1-PR1: 154 上 minimax-m3 大 body（800+ messages、~1.5MB）请求被 opencode SDK
> 默认 idle 超时（~10s）切断 → r.Context() canceled → 重试循环在 attempt 0
> 之前 abort → 返回 provider_error 115 字节。直接连 api.minimaxi.com 时 vendor
> 端点会发 SSE keep-alive，gateway 之前的实现把 pre-stream keepalive 推迟到
> session compressor Prepare 之后才发 200，长达 5-40 秒里客户端一个字都收不到。

## 1. 改动

| 文件 | 改动 |
|------|------|
| `domains/streaming/handler.go` | 把 `startPreStreamKeepalive` 上移到 `isStream` 判定之后、candidate resolution 之前；HTTP 200 + 第一帧 `: keep-alive\n\n` 立即发出；保留原位 fallback 防御性兜底 |
| `domains/streaming/executors/executor_chat.go` | 上游 HTTP call 入口加 `upstream_call_starting` 日志（upstream_url / rctx_err / timeout_remaining_ms） |
| `domains/streaming/handler.go` | session compressor Prepare 外层包计时 `session_compressor_prepare_done`；candidate resolution 包计时 `candidates_resolved` |
| `internal/logging/raw_data_logger.go` | 单文件上限 200MB→100MB；保留数 5→10；总上限 ≈1000MB（rule 11 §3） |
| `cmd/gateway/main.go` | 同步 raw_data_logger 默认 100MB |

## 2. 验证（生产 154 build 1601）

| 时间 | request_id | body | Prepare 耗时 | 结果 |
|------|-----------|------|-------------|------|
| 12:09:36 | 31ad4e07... | 924 KB | 41164 ms | success=true, stream_chunks=3, ttfb=43237ms |
| 12:13:03 | ec5f23cb... | 513 KB | 21412 ms | success=true, stream_chunks=70, ttfb=29568ms |

修复前同样大小的请求会在 attempt 0 之前被 context canceled，返回 115 字节
`provider_error` SSE envelope。修复后客户端立即收到 200 + 第一帧
`: keep-alive`，整个 session compressor / sanitize / IR validate 流程不再
打断客户端。

## 3. 遗留与风险

- session compressor 单线程 Prepare 对 ≥500 KB body 仍能跑到 20-40 秒，
  远超任何客户端默认 idle timeout；fix 是 keepalive 把客户端留在连接上，
  但若上游 MiniMax 也降级到 30+ 秒才回 first byte，P99 仍可能拖到分钟级。
  后续考虑把 compressor 改成 streaming chunk-by-chunk 或预分配 worker pool。
- new logs 默认 INFO 级别；如果磁盘压力，把 `candidates_resolved` /
  `session_compressor_prepare_done` / `upstream_call_starting` 切到 DEBUG
  （每个请求 3 行，单天 100K 请求级别约 ~10MB/日 JSON）。