# telemetry request_logs 失败兜底：内存 ring buffer + 在线 dump/replay

**Date**: 2026-07-20
**Priority**: P1
**Type**: Feature (observability / incident response)

## Summary

`telemetry` worker 写 DB 失败时，原架构只输出 `slog.Warn("telemetry request db persist failed; fallback written", ...)`，
请求体（包括 prompt/response 元数据 + context）就丢了。本次新增**内存 ring buffer**作为"快速访问层"，
让运维可以在线 dump 最近 N 条失败条目、按需 replay 回 DB，
不再需要 grep 几百 MB 的日志文件找 trace。

## 背景

- 2026-07-20 修 `routing_attempts` 22P02 时发现：从 02:32 到 04:59 期间，多个 probe + 普通请求的
  INSERT 全失败了，但 `fallback written` 仅写一行日志，**完全无法恢复**。
- 当前架构：`Client.fallback` 是 `dbdegradation.BackupWriter` 接口，
  唯一实现是磁盘 `FileWriter`（写 gzip JSONL spool）。
- FileWriter 适合"重启恢复"（运维手动 replay 到 DB），但**不适合在线排查**（需要读 gzip 文件、解码 JSONL、grep）。

## 设计决策（已与老板对齐）

| 决策点 | 选择 | 理由 |
|---|---|---|
| 与 FileWriter 关系 | **叠加双写** | FileWriter 写磁盘（重启恢复），ring buffer 是内存快速访问层 |
| 失败 replay 处理 | **只记日志，不重入 buffer** | 防死循环；调用方按 `failed` 计数决定是否重试 |
| 容量 | 默认 10000 条（env `TELEMETRY_FALLBACK_BUFFER_CAP`） | 50-200MB 内存，单进程可接受 |
| 并发模型 | `sync.Mutex` | dump/replay 非热路径，简单即可 |
| 鉴权 | admin token（复用 `middleware.NewAdminTokenMiddleware`） | 与 `/healthz/full`、`/metrics` 一致 |

## 改动清单

### 新增文件

| 文件 | 类型 | 说明 |
|---|---|---|
| `domains/dbdegradation/ring_buffer.go` | 新建 | `RingBuffer` 结构（FIFO + cap 覆盖）；实现 `BackupWriter` 接口（`WriteRequestLog`/`WriteRequestWAL`） |
| `domains/dbdegradation/ring_buffer_test.go` | 新建 | 10 个单元测试（FIFO / cap 覆盖 / BackupWriter 接口 / 并发 / Clear / Replay success / Replay failure / Replay limit / Replay empty / 0 cap 兜底） |
| `domains/dbdegradation/multi_writer.go` | 新建 | `MultiBackupWriter` fanout 多个 `BackupWriter`（用于同时写 FileWriter + ring buffer），失败聚合不阻断 |
| `domains/dbdegradation/multi_writer_test.go` | 新建 | 4 个单元测试（fanout / 部分失败聚合 / nil 过滤 / 空 multi） |
| `cmd/gateway/telemetry_fallback_buffer_handler.go` | 新建 | HTTP handler：4 个端点（stats / dump / clear / replay） |
| `cmd/gateway/telemetry_fallback_buffer_handler_test.go` | 新建 | 9 个单元测试（每个端点的 happy + edge） |

### 修改文件

| 文件 | 改动 |
|---|---|
| `cmd/gateway/main.go` | 加 `positiveIntEnv` helper；main body 顶层 `var ringBuffer *RingBuffer`；dbConn 块内 `MultiBackupWriter(fileWriter, ringBuffer)` 替换单写；挂 4 个 HTTP 路由 |

### 文件总行数

- 新增：~1100 行（其中 ~600 行测试）
- 修改：~50 行

## 接口契约

### 数据结构（`domains/dbdegradation/ring_buffer.go`）

```go
type RingBuffer struct {
    mu sync.Mutex
    cap int
    buf []BackupRecord        // 预分配
    head int                   // 下一个写入位置
    size int                   // 有效条目数（≤ cap）
    writes uint64
    dropped uint64
}

func NewRingBuffer(capacity int) *RingBuffer
func (rb *RingBuffer) WriteGeneric(ctx, recordType, key, payload) error
func (rb *RingBuffer) WriteRequestLog(ctx, key, payload) error   // BackupWriter 接口
func (rb *RingBuffer) WriteRequestWAL(ctx, key, payload) error    // BackupWriter 接口
func (rb *RingBuffer) Dump() []BackupRecord                       // 复制当前 buffer（FIFO 顺序）
func (rb *RingBuffer) Stats() RingBufferStats                     // cap/size/writes/dropped
func (rb *RingBuffer) Clear()                                      // 清空（dropped 计数器保留供监控）
func (rb *RingBuffer) Replay(ctx, limit int, fn ReplayFn) (replayed, failed int, err error)
```

### HTTP 端点（admin token 鉴权）

| Path | Method | 用途 | 返回 |
|---|---|---|---|
| `/internal/telemetry/fallback-buffer/stats` | GET | 容量 / 大小 / dropped / writes | `{"capacity":N,"size":N,"writes":N,"dropped":N}` |
| `/internal/telemetry/fallback-buffer/dump` | GET | dump 全部条目 | `{"count":N,"records":[...]}` |
| `/internal/telemetry/fallback-buffer/clear` | POST `{"confirm":true}` | 清空 | `{"cleared":N}` |
| `/internal/telemetry/fallback-buffer/replay` | POST `{"limit":N}` (可选) | replay 到 DB | `{"replayed":N,"failed":N}` |

错误码：
- 401：缺 / 错 admin token
- 400：clear 缺 `confirm=true`
- 405：方法不允许
- 404：未知子路径
- 503：replay 未接线（telemetry client 未配置，db 关闭模式）

## 验证

### 单元测试（`-race` 全部通过）

- ✅ `RingBuffer` × 10 个用例（FIFO、cap 覆盖、并发安全 race、Replay 三种场景）
- ✅ `MultiBackupWriter` × 4 个用例（fanout、部分失败聚合、nil 过滤、空）
- ✅ HTTP handler × 9 个用例（每个端点 + method 错误 + 未知路径 + 错误码）

总计 23 个测试，race detector 全过。

### 集成验证（部署后跑）

1. `curl /healthz` → 200
2. `curl -H "Authorization: Bearer $ADMIN_TOKEN" http://154/internal/telemetry/fallback-buffer/stats` → JSON
3. 制造 INSERT 失败（临时改 DB URL 错配 + 重启 gateway），制造 1 条 fallback
4. `curl .../dump` → 应能拉出该 BackupRecord
5. 恢复 DB URL，`curl -X POST .../replay` → `{"replayed":1,"failed":0}`
6. `curl .../stats` → `size=0`

### L1-L4 分层验证（rule 03 §6）

- L1 `/healthz` 200
- L2 DB + Redis 连通（admin ping endpoints）
- L3 正常流量 → `request_logs` 写入；fallback buffer 仍为 0
- L4 业务 API 正常；admin endpoints 返回正确 JSON

## 风险与缓解

- **风险 1**：ring buffer 满（10000 条）后覆盖最老 → 监控 `dropped` 指标，超阈值告警
- **风险 2**：dump 输出过大（10K × 20KB ≈ 200MB）→ dump 不限但 `replay` 支持 `limit` 限速
- **风险 3**：replay 与正常写入并发冲突（同 request_id 两次 INSERT）→ 复用现有 `ReplayFallback`（INSERT ON CONFLICT upsert 语义），并发安全
- **风险 4**：payload 含敏感信息（prompt / response）→ 当前 4 端点都是 admin token 鉴权，外网无法访问
- **风险 5**：db 关闭模式（ringBuffer == nil）→ 路由自动 fail-closed，admin 接口返回 404

## 部署

- 双写无破坏性：FileWriter 路径不变（仍然写磁盘），ring buffer 是新增层
- 回滚：单 commit revert 即可，ring buffer 不影响 FileWriter 兜底
- 容量调整：`TELEMETRY_FALLBACK_BUFFER_CAP=5000`（减小）或 `=50000`（增大）

## 后续 / 不在本期范围

- ring buffer 周期 dump 到本地 JSONL（用户决策"叠加 + 失败只记日志"已否决）
- 自动 replay（带 backoff 的 goroutine），目前仅手动触发
- 多副本实例之间的 buffer 同步（gateway 单进程足够，副本之间不共享）
- payload PII 脱敏（prompt / response 内容 dump 时是否需要 mask）
