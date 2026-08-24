# Session/Rotation/Queue 内存与 Redis 读取优化

- 日期：2026-08-24
- 类型：perf（行为保持型优化）
- 计划文档：`docs/04-implementation/plan/2026-08-24-session-queue-memory-optimization-plan.md`

## 变更内容

### session 读取路径（domains/session）

1. `GetEnrichedSession` 复用单次 `HGetAll`：session 与 stats 原来各读一次
   session hash，现在合并为一次读取；rotation 历史仍独立走 List 读取。
2. `StopSession` 复用入口处已加载的 session hash：
   - 结束凭据轮换（endCredRotation）不再重复 `HGetAll`；
   - 快照持久化不再重新 `Get` + `GetStats`（原来共 3 次冗余 hash 读）。
3. `StartCredRotation` 改为 Redis Lua 原子脚本：在 Redis 端读取
   `total_turns` 并一次性完成 LPUSH + HSET + EXPIRE，消除一次客户端
   往返，同时把原来的非原子 pipeline 升级为原子操作。Redis 数据格式
   完全不变。
4. `EndCredRotation` 的 `LSet` 失败现在显式返回带上下文的错误
   （原来是静默丢弃）；HGetAll 失败时提前返回错误，避免把零值统计
   写入 rotation 历史（原来会写入 Turns=0 的脏数据）。
5. 删除本轮优化后已无调用者的 `getFieldFromRedis` 辅助函数。

### dispatch 队列投影（domains/dispatch）

- 新增 `BenchmarkQueueProjectionSnapshot` 与
  `BenchmarkQueueProjectionSnapshotWaterfall` 基准测试，建立后续优化
  的度量基线：
  - Snapshot: ~42 us/op, 111520 B/op, 214 allocs/op
  - SnapshotWaterfall: ~42 us/op, 141616 B/op, 269 allocs/op
- 未改动快照所有权语义：waterfall 深拷贝（detached snapshot）与 SSE
  快照复制均保留，因缺乏不可变所有权与 race 安全证明。

### 新增测试（domains/session/session_state_memory_test.go）

- 基于 go-redis ProcessHook 的命令计数器，断言：
  - `GetEnrichedSession` 对 session hash 只读 1 次；
  - `StopSession` 全程只读 1 次 hash、0 次 HGet；
  - Lua 版 `StartCredRotation` 保持 `current_cred_start_turn` 正确、
    TTL 正确刷新、rotation JSON 记录与旧格式兼容。

## 行为保持说明

- `sessionFromRedisHash` 完整保留 `ErrSessionExpired` 过期检查语义。
- 快照写入的字段值与原实现一致（stop 三字段在内存中同步注入）。
- Redis key、hash field、rotation List 的 JSON schema 均未变化，
  存量数据无需迁移。

## 验证

- `go test ./domains/session ./domains/dispatch ./eventbus ./ratelimit ./autoroute`
- `go test -race ./domains/session`
- `go vet ./domains/session ./domains/dispatch`
- `go build ./...`、`bash verify.sh`、`go test ./... -count=1` 全部通过。

## 2026-08-24 LP4：QueuedRequest 十阶段时间戳改为内联数组存储

### 改动（domains/dispatch + domains/streaming/executors）

- `QueuedRequest` 的 9 个 `*time.Time` 阶段字段（T1–T9）与 T0 值字段
  合并为 `stages [10]time.Time` + `stageSet uint16` 位掩码：
  - 每阶段一次的堆分配（per-attempt 最多 5 次）归零；
  - failover 重试不再产生额外时间戳分配。
- 新增导出 API：
  - `ReqStage` 枚举 + `ReqStageTime` / `SetReqStageTime`（读 / 显式
    种子，服务 executors 测试与 journey 事件时间写入）；
  - `StageTimestamps()`：单次盒分配（`*[10]time.Time` 拷贝）返回十个
    detached 指针，未记录阶段为 nil。替代 executors 原先对请求内部
    指针的借用——failover 重写不再可能污染已提取的值，也不会因
    interior pointer 把整个 QueuedRequest 钉在内存里。
- journey.go 的 T7/T8 直写改走 `setStage`（仍在 attemptMu 临界区内）。
- `stageSeconds`/`durationMS` 转值语义（`IsZero` 哨兵，等价于原
  nil 检查，setter 全部来自 `time.Now()`，不存在 set-but-zero）；
  删除 `stageSecondsFrom` 与无调用方的 `formatTSPtr`。
- legacy `EnqueuedAt`/`CredEnqueuedAt`/`DequeuedAt` 按计划保留
  （第一轮不删），`SetT5`/`SetT6` 双写行为不变。

### Benchmark（BenchmarkRequestStageTimestamps，M4 Max）

| 场景 | 改动前 | 改动后 | Δ allocs |
|---|---|---|---|
| attempts=1 | 1159 ns / 1487 B / 25 allocs | ~987 ns / 1743 B / 16 allocs | −36% |
| attempts=3 | 1608 ns / 1727 B / 35 allocs | ~1270 ns / 1743 B / 16 allocs | −54% |

分配数不再随 failover 次数增长；B/op 持平（结构体内联 +146B 与
单盒拷贝 240B 抵消原 9×24B 散落分配）。

### 验证

- 字段访问零遗漏：删除导出指针字段后由编译器驱动全部 12 处消费点
  迁移，最终 `rg 'qr\.T[0-9]_'` 零残留。
- `go build ./...`、`go vet`、`go test ./domains/dispatch -count=1`、
  `go test -race ./domains/streaming/executors`、触及路径定向
  `-race` 全部通过；`bash verify.sh` pre-commit 4/4 PASS
  （govulncheck 的 go1.26.6 stdlib 通报为仓库既有状态，非本次引入）。
