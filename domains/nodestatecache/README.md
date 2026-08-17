# nodestatecache — 节点状态缓存包与选择闭包（FR-10 / T10）

## 定位声明（R10.1）

本包是**进程内派生读缓存**：全节点位图状态 + 每节点紧凑统计/资源仪表 +
节点选择闭包 + 需自检列表。**权威是 URSM v2 Redis（Lua 状态机）；本包禁止
双写——永不写 URSM/DB。** 包不 import `dispatch` / `executors`（避免环，
参照 dispatch 的 `CredentialRef` 解耦先例），也不依赖 redis。

### 喂入三条路径（只进不出）

1. **RecordRequest 效应链尾部旁路更新**：`Update(nodeID, success, errKind, latencyMS)`
   （挂接点 `executors/executor_nodehealth.go` 效应链尾部，以及探测结果回写）。
2. **pub/sub 失效广播置脏**：`Invalidate(ref)` 清 Avail/Probe、State=unknown，
   并回调 `InvalidationListener`（集成者订阅 NodeMirror 失效频道后调用；
   `domains/ursm/v2/cache/manager.go:779-814` 的订阅复用点）。
3. **低频对账循环**（默认 30s，可配）：`Reconcile(ctx, snapshot)` 以注入的
   `AuthoritativeSnapshot`（URSM/Governor/fpslot 聚合视图）覆盖式收敛漂移。

另有资源事件流喂入：dispatch Governor 的 acquire/release 与
credentialfpslot 的 acquire/reclaim → `TryAcquire` / `Release`（派生镜像值，
**权威准入仍在 Governor 与 fpslot**；上限源=credentials 表与 fpslot 配额，
本包不另建上限配置）。

## 结构与文件

| 文件 | 内容 |
|---|---|
| `nodestatecache.go` | `Options`（全默认值、clock 可注入）、`New`、三闭包（`Selector()`/`Updater()`/`NeedProbe`）+ 资源仪表组 + ticker + `Close` |
| `registry.go` | `NodeRef{TenantID,CredentialID,RawModel} ↔ int32` dense id 双射；容量有界（默认 65536），满时 LRU 回收未活跃（不持有资源占用）id；`InvalidationListener` 接口 |
| `bitmap.go` | `NodeStateBitmap{Avail,Probe,Full []uint64}` 按 dense id 位寻址；单位操作无锁 atomic，复合迁移 16 分片锁（对齐 NodeMirror） |
| `stats.go` | `NodeStatsSlot`（16B：LastUsedUnixSec/Succ1h/Fail1h 饱和/FailStreak/State/WindowStart 1h 翻转）；`RecordOutcome` 语义 |
| `resources.go` | `NodeResourceSlot`（16B：Conc/FP/RPM 各 Used+Limit、WindowID、Flags.bit0 满载联动 Full 位图）；CAS 无锁；`ResetWindow` 分钟窗+1h 窗 |
| `selector.go` | `SelectQuery/SelectResult/NodeSelector`（R10.2 签名照抄）、`NodeUseRecord`（R10.3 16B 位压缩 + Pack/Unpack）、`Scorer/StickyLookup/ModelFallback` 接口 |
| `updater.go` | `Update`：成功恢复 Avail、失败达阈值（默认 3=URSM FailStreakLimit）转待探测；429→RPM 满载镜像（分钟窗翻转回归）；errKind↔journey ErrorKind 映射 |
| `reconcile.go` | `AuthoritativeSnapshot` / `AuthorityRecord` / `Reconcile`（覆盖式收敛、幂等） |
| `probe_candidates.go` | `NeedProbe(now)` = Probe 位图 ∩ 36h 命中（`RecentSuccessLookup`）∩ 退避到期，三条件缺一不产出 |

三个闭包（Selector/Updater/NeedProbe）**单一责任、互不调用**。

## 集成接线点（五个注入接口）

| 接口 | 建议实现位置 |
|---|---|
| `Scorer` | 包装 URSM `FilterAndScoreReadyWithSource`（`domains/ursm/v2/cache/manager.go:356`）：幸存集（dense id→NodeRef 经 `Cache.NodeRef`）评分排序（0.4·price+0.4·latency+0.2·失败率），并完成模型归属过滤 |
| `StickyLookup` | dispatch 会话亲和（AffinityKey→绑定节点），流程 4 的 sticky 复用 |
| `ModelFallback` | `domains/modelquality` 品质档位映射：任务类型→同档位候选模型序列 |
| `RecentSuccessLookup` | `bg/credential_recovery.go`（R4.4）的 36h 成功回看（request_logs[_hot]） |
| `AuthoritativeSnapshot` | URSM Redis + dispatch Governor + credentialfpslot 的聚合快照（对账循环与 pub/sub 重拉共用） |

可选：以本包资源仪表实现 `domains/ursm/v2/resource/pools.go` 接口，
激活 URSM `NodeView` 的 Conc/FP/RPM 仪表（现为死脚手架恒 0）。

## 性能（R10.6）

- 读路径无锁 atomic；写路径 16 分片；资源增减 CAS O(1)；所有操作
  O(1) 或 O(幸存集)。
- 单次选择 P99 < 1ms 门禁由 `TestSelectP99Under1ms`（10k 节点宇宙，
  4096 采样）与 `benchmark_test.go` 验证；实测见测试输出
  （Apple M4 Max：Select ≈ 84µs/op，sticky 74ns，Updater 15ns，
  TryAcquire/Release 12ns；race 下 perf 门禁自动跳过）。

## 测试

`docs/会话优化v4测试/客户端会话保持-测试方案与用例.md` G4（UT-NS-01..12）
全覆盖；`go test -race ./domains/nodestatecache/...` 通过。
