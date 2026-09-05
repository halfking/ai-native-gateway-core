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

## 2026-08-24 LP5：QueueProjection percentile 窗口增量维护

### 改动（domains/dispatch）

- `waterfallRing.push` 返回被覆盖（淘汰）的条目，供派生读模型对齐
  窗口；Pipeline 既有 `recordWaterfall` 调用点不受影响（返回值可弃）。
- `QueueProjection` 新增 `waitsSorted`（升序 QueueWaitMS 窗口，容量
  以 ring cap 为上界，p.mu 保护）：`QueueRequestCompleted` 观察时
  增量插入新样本 / 移除淘汰样本（binary search + int memmove）。
- `Snapshot` 的 p50/p95 改为对已排序窗口直接 `nearestRank`，删除了
  原先的全环快照（200 × cloneWaterfallRequest）+ `sort.Ints` 路径；
  返回值按次新盒分配，保持 detached 契约。被替代的
  `waitingPercentilesFromRequests` 一并移除（本任务自身产物）。
- 样本资格判定（ArrivedAt/CredDequeuedAt 非空且 QueueWaitMS>0）与
  原实现逐条一致；`ring.snapshot` 的深拷贝所有权红线原样保留。

### 否决的候选：lane 排序结果缓存

- `sourceVersion` 由 Snapshot 读路径自增（`p.sourceVersion.Add(1)`），
  本质是读序号而非数据版本，无法做失效判据。
- 若改用 mutation counter：每次 enqueue/dequeue 都会使缓存失效，
  高负载下命中率趋近 0；且缓存共享 slice 违反 detached snapshot
  契约（现有测试断言外部修改不影响下次快照）。收益/风险比不成立。

### Benchmark（BenchmarkQueueProjectionSnapshot*，24 model + 24 cred lanes + 满环）

| 场景 | 改动前 | 改动后 |
|---|---|---|
| Snapshot | ~138 us / 111521 B / 214 allocs | ~3.2 us / 4000 B / 12 allocs |
| SnapshotWaterfall | ~110 us / 141617 B / 269 allocs | ~10 us / 34096 B / 67 allocs |

SnapshotWaterfall 剩余成本即 50 条样本的 cloneWaterfallRequest 深拷贝
（detached 所有权保证，按红线保留）。

### 验证

- 新增回归测试：percentile 窗口语义（空窗 nil、非资格样本排除、
  100 样本 p50/p95=50/95、淘汰回绕后窗口=101..300 → 200/290、
  重复 wait 值淘汰恰好移除一份、返回盒 detached）。
- `TestPipelineQueueStats_RealPipelineTraffic`（真实管线流量端到端
  断言 percentile 存在性与 p95>=p50）通过。
- `go build ./...`、`go vet`、`go test ./domains/dispatch ./admin`、
  触及路径 `-race`、`bash verify.sh`（pre-commit 4/4 PASS）全部通过。
