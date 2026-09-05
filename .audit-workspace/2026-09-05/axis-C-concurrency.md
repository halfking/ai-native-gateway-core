# 审计报告 · 轴 C：多层队列 / 并发控制 / 出口限流 / 权重负载均衡 / 竞态与资源安全

- 仓库：`/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-5`（只读审计，未修改任何源码）
- 日期：2026-09-05
- 范围：`domains/dispatch/`、`domains/streaming/executors/executor_dispatch.go`、`domains/streaming/executors/router.go`、`cmd/gateway/main_dispatch*.go`
- 方法：全仓 grep 字段直读扫描 + 逐文件人工审读 + 定向 `go test -race`（dispatch 包取消/关停用例通过，但未覆盖下述 P1 窗口）

## 一、发现清单

| # | 级别 | 位置 | 问题 | 可达性 |
|---|------|------|------|--------|
| 1 | **P1** | `domains/dispatch/pipeline.go:1057-1066`、`domains/dispatch/journal.go:105-145`、`domains/dispatch/forwarder.go:491`、`domains/dispatch/failover.go:50` | **取消路径打破 single-owner 不变式 → journal 字段数据竞争**。`Submit` 的 `ctx.Done` 分支在**调用方 goroutine** 里执行 `abandoned.Store(true)` + `p.complete(qr,…)`（L1063-1064），complete 的 CAS 胜出后无条件执行 `qr.recordDecision`（pipeline.go:1431），写 `journalSeq/AttemptJournal/Counts/LastFailover`（无锁）。而此刻仍存活的执行链 `attempt → routeFailover（forwarder.go:491，无 completed/abandoned 检查）→ move（failover.go:50，入口同样无检查）→ PlanAfterFailure→decisionHistoryOf（读 JournalSnapshot）→ recordDecision` 会在 mover goroutine 上并发读写同一组字段。客户端断连恰好发生在 pre-firstbyte 失败/重试期间即触发；`emitJournalSnapshot` 的 sink I/O 拉长了窗口。race detector 可标记；最坏情况并发 append+ring copy 可损坏 journal/LastFailover 投影。修复方向：`routeFailover`/`move` 入口检查 `qr.completed.Load()` 短路丢弃，或把取消侧 journal 写移交给当前 owner 链/加锁。 | **并发可达（生产主路径：client abort × failover）** |
| 2 | **P2** | `domains/dispatch/dispatcher.go:25-27`、`domains/dispatch/failover.go:29-31`、`domains/dispatch/pipeline.go:1102-1106` | **shutdown 漏排空（幽灵请求）**。`runDispatcher`/`runFailover` 在 `stopCh` 触发时直接 `return`，`dispatchIn`/`failoverCh` 缓冲区（各 `workers×4`）中残留的 qr 永不被 complete；`runTotalDrainer` 的 `shutdown.Load()` 分支只 complete 当前一条就 `return`（L1102-1106），`totalQueue.ch`（容量 1000）中其余条目无人接管——对照 `stopCh` 分支（L1131-1142）和 `runModelDrainer`/`drainAndComplete` 的正确做法。被困请求的 Submit 调用方只能靠 ctx 解锁：survival 流的 ctx 是 `WithoutCancel+2h` 定时器（`executor_chat.go:2102`），graceful restart 期间可挂住大量 handler goroutine 与内存长达 2h。 | 并发可达（每次 Stop/重启） |
| 3 | **P2** | `domains/dispatch/dispatcher.go:16-29`、`domains/dispatch/failover.go:20-33`、`domains/dispatch/pipeline.go:1094-1145` | **三类 worker goroutine 无 panic recover**。`runTotalDrainer`/`runDispatcher`/`runFailover` 直接调用 `p.dispatch`/`p.move`，其中 `routeFunc`（executor 的 `dispatchRoute`→`Router.PlanCandidatesPinned`）、`modelRecommendFunc`、planner 均为跨包回调；任一 panic 会击穿 goroutine 杀死整个进程。对照：`forwarder.attempt` 有 recover（forwarder.go:434-444），notice/observation/journal/queue-observe 各 sink 也均有 recover——唯独三个调度 worker 入口裸奔。 | 并发可达（路由回调 panic 即触发，历史上 Router 侧有复杂排序逻辑） |
| 4 | **P2** | `domains/dispatch/pipeline.go:1529-1570`、`cmd/gateway/main_dispatch_observation.go:155-262`、`cmd/gateway/main.go:948` | **complete() 同步执行 JournalSink，进程级串行点**。生产接线为 `newDispatchJourneyJournalAdapterWithReceipt`（main.go:948）：`emitJournalSnapshot` 在 dispatcher/mover/forwarder goroutine 上同步进入 `ApplyJournalSnapshot`，其中 `a.mu`（main_dispatch_observation.go:182-183）**串行化全进程所有请求的 journal 投递**，且 `ClaimWithProjectionBase`/`Complete` 是无超时 DB 调用（ctx=`context.WithoutCancel`）。DB 抖动时完成路径被拖住，mover/dispatcher/forwarder 吞吐整体塌陷（head-of-line blocking）。`recorder.Apply` 本身是异步有界队列，阻塞点在 receipt 的两次 DB 往返 + 全局互斥。建议：sink 调用改异步/带超时，或去掉进程级串行。 | 并发可达（DB 慢时全量触发） |
| 5 | **P3** | `domains/dispatch/forwarder.go:43,240-274,280-305` | **pendingOld 单槽覆盖理论竞态**。`replaceDepth` 把被换下的 channel 存入单槽 `pendingOld`；若两次 grow 连续发生（两次 ApplyPolicy 连续改大 max_queue_depth）且 loop 尚未 reclaim 第一次的旧 channel，单槽被第二次覆盖；此时一个已加载旧 channel 指针、被调度暂停的 producer（`tryEnqueueCred` 的 non-blocking send）最终落入旧 channel，请求永久滞留（Submit 挂到 ctx）。窗口极窄。建议：pendingOld 改为 slice 队列或在 replaceDepth 内同步 drain 旧 channel（当前生产仅启动时深度固定，实际触发概率接近 0）。 | 低概率可达（需连续 grow + producer 停顿） |
| 6 | **P3** | `domains/streaming/executors/router.go:1114-1180`、`provider/client.go:1747-1760` | **加权抽签 totalWeight int 溢出退化**。`promoteWeightedCandidate` 累加 `c.Weight` 无溢出保护：capacity 派生权重已 clamp 到 [1,1000]，但**手工 weight 不设上限**（applyCapacityWeightedLB 尊重 ≠100 的任意手工值）。多凭据配置超大权重使 `totalWeight` 变负后，`uint64(totalWeight)` 变巨数、`position*stride % totalWeight` 得非正值，`position < c.Weight` 恒真 → 抽签恒定选字典序第一凭据，LB 静默失效（不崩溃）。建议：累加用 int64 + 上限保护，或对 weight 做 [1,10000] clamp。 | 可达（需运维配置极端权重） |
| 7 | **P3** | `domains/dispatch/pipeline.go:1188-1194` | **enqueueModelFromTotal 忙等**。Tier-1 lane 满时对每个请求以 `time.After(1ms)` 循环重试：~1000 次/秒/请求的 timer 分配与唤醒。建议 `time.NewTimer` 复用或加大退避。 | 并发可达（Tier-1 背压期间） |
| 8 | **P3** | `domains/dispatch/governor.go:82-86`、`domains/dispatch/redis_backend.go:645-652` | **concurrency governor 2ms 轮询**。饱和时每个 Tier-2 等待者以 `time.After(2ms)` 空转（Tier-2 队列深 300 时约 150 wake/s/请求）。CPU/调度噪声，非正确性问题。建议信号量化（channel 信号）或指数退避。 | 并发可达（凭据饱和期间） |
| 9 | **P3** | `domains/streaming/executors/executor_dispatch.go:207-234`、`domains/dispatch/priority_affinity.go:17-26` | **PriorityCluster 从未被赋值**。`candidateToRef` 不填 `PriorityCluster`（恒 0），`sortPriorityClusters` 永远是稳定空转排序——"优先聚簇先选"在 dispatch 路径是死代码（当前顺序由 Router tier/权重保证，功能影响低，但属于承诺了却未接线的语义）。 | 静态（非竞态） |
| 10 | **P3** | `domains/dispatch/failover.go:139-203` | **abandoned 请求仍完整走 mover 阶梯**。与 #1 同根因的另一半：move 无 completed/abandoned 短路，取消后仍可能执行一次 `tryEnqueueCred` + governor `Acquire` + 一次注定失败的 forward，白耗一个并发 slot 与 attempt 预算后才在终态被 CAS 拦截。补上 #1 的入口检查即同时修复。 | 并发可达（client abort × failover） |
| 11 | **P3** | `domains/dispatch/governor.go:180,247` | **rpm/tpm 等待用 `time.After(wait)`**，wait 可达 MaxQueueWaitMS 预算（数十秒）；大量等待请求时未触发 timer 堆积、无法提前 GC。改 `NewTimer+Stop` 可省。 | 并发可达（限流窗口等待期） |

## 二、accessor 迁移覆盖度结论

**生产代码迁移完整，覆盖度 100%。**

1. **字段直读扫描（排除 vendor、排除 `domains/dispatch` 内 accessor 本体、排除 `*_test.go`）**：
   - `qr.ResolvedModel` / `qr.SelectedCred` / `qr.vendor` 的直读直写在生产代码中为 **0 处**；仅存于 `queued_request.go:262-307` 的 accessor 内部。
   - 跨包使用方 `domains/streaming/executors/executor_dispatch.go` 已全部改用 `ResolvedModelSnapshot()`（L91、L126、L187）。
   - `admin/session_online.go:220`、`domains/hooks/observability/telemetry/client.go:391` 对 `QueuedRequest` 仅为**注释引用**，无字段访问；`cmd/gateway/main_dispatch_observation.go:55`、`domains/requestjourney/*`、`errorsx/action_policy.go` 的 `ResolvedModel` 均属其他结构体（Observation/JourneyEvent/DecisionContext），与 qr 无关。
   - grep 其余接收者名（`q./req./queued./jr./item./task.`）无补充命中；`ResolvedVendor` 字段不存在。
   - 测试 fixture 直写保留在 9 个 dispatch 包 `_test.go`（concurrency_wait/dimension_journal/dispatch_loop/dispatch/failover_exhaustion/journey/minute_stats_pipeline/planner*/priority_affinity/registry/waterfall 测试），与基线报告描述一致——**仅测试路径，非并发可达**。

2. **锁配对与拷贝语义（queued_request.go）**：全部正确。
   - `resolvedModel()/setResolvedModel()`（modelMu）、`selectedCredential()/setSelectedCredential()`（selectedCredMu）、`selectedVendor()/setVendor()`（vendorMu）读写锁均成对释放，无锁内回调、无锁升级。
   - 返回值均为**值拷贝**：`CredentialRef` 是纯值结构体（无 slice/map/pointer 字段，queued_request.go:21-34），字符串不可变——没有共享引用逃逸出锁，深拷贝需求天然满足。
   - `StageTimestamps()`（L388-427）在锁内 `box := qr.stages` **复制整个数组**后再取指针，返回值与后续 failover 重写完全隔离；`JournalSnapshot()`（journal.go:165-172）、`waterfallAttempts()/exhaustionAttempts()`（journey.go:217-256，attemptMu 内拷贝）同样是 detached copy。
   - 遗留字段 `EnqueuedAt/CredEnqueuedAt/DequeuedAt` 无锁，但写入点均严格先于 channel send（pipeline.go:1675-1681 注释、forwarder.go:403 读取），靠 channel happens-before 保证——当前正确，属脆弱但可接受的设计。

3. **残余风险不在字段直读，而在不变式本身**：`QueuedRequest` 文档声明的 single-owner 不变式（queued_request.go:70-73）被 `Submit` 取消路径打破（发现 #1）——`complete()` 从调用方 goroutine 写 journal/AttemptCount 等单主字段，与仍存活的 owner 链并发。这是当前唯一需要修的竞态。

## 三、总体结论

24h 内的三个提交（034c94ee7、1eb00d411、cb1b4c6ff）**有效且干净地**解决了 `ResolvedModel`/`SelectedCred`/`vendor` 的跨 goroutine 直读竞态：accessor 锁配对无误、返回值拷贝语义正确、生产代码迁移零遗漏，质量良好。

但同族风险在更深一层仍然存在：**P1（取消路径 journal 数据竞争）** 表明 single-owner 不变式缺少机械保障（依赖约定而非类型/锁），建议以 `move()`/`routeFailover()` 入口的 `qr.completed` 短路作为最小修复，并以 mutex 保护 `recordDecision` 作为长期修复。另有两类工程缺口值得尽快跟进：**worker 级 shutdown 漏排空（P2#2）** 与 **worker 无 panic recover（P2#3）**——二者在重启与上游回调异常场景分别造成请求悬挂与进程崩溃；**complete() 上的同步 journal sink（P2#4）** 则是慢 DB 下的全局吞吐放大器。多层队列的准入/释放对称性（totalQueueDone、cluster admission take-once）、三层队列背压、governor 限流与凭据队列的衔接总体实现严谨，未发现死锁（锁序 attemptMu→journeyMu→stageMu 单向）与 channel 误关（observationCh 的 closed-flag+互斥、QueueMirror 的 closed-guard 均正确）。
