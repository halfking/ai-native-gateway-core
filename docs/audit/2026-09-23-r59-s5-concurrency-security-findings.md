# R59 S5 组审计报告 — 多队列并发 D04 + 场景安全 D14（2026-09-23）

- 审计基点：main @ 9f7b0ea3f（与 origin/main 对齐）。只读审计（可跑测试）。
- 必审 commits：14713d1e7（ctxpool）/ 132a60fec（error_probe 门）/ 2e6d55c11（ProbeSync 闸）/ 83567d0ca（KindRateLimit 剔除）。
- 必跑结果：`go build ./...` ✓；`go vet ./domains/... ./internal/...` 零输出 ✓；-race 四包全绿（见 §三）。

## 一、必审项逐项实证

### 1. 14713d1e7 ctxpool 竞态加固 + 未接线 STATUS 钉注

**加固方式验证（✅ 实证有效）**——`internal/ctxpool/ctxpool.go`：
- wrapper 竞态：`acquireWithTimeout` 写 `pc.wrapper` 入 `mu`（ctxpool.go:312-314），`Err`（:130-132）/`Deadline`（:152-154）改为锁内拷贝引用再解引用——读写两侧都收敛到 mu，数据竞争消除。
- Release 双 Put：`released.CompareAndSwap(false, true)`（:209-211）替代原 check-then-act，两个并发 Release 只有一个能 `pool.Put`，另一个 panic。CAS 语义正确。
- `internal/ctxpool` -race 全绿（1.7s）。

**"未接线 STATUS"钉注（ctxpool.go:54-57）与零消费口径**：
- 零消费实锤：全仓 `grep -rn ctxpool --include=*.go` 除包自身（ctxpool.go + ctxpool_test.go）外 **零引用**——比 R58 被保留的 WeightedRouter 更严格（WeightedRouter 尚有 tests/local/gateway/main.go 在用，R58 §二据此保留；ctxpool 连测试基建都不用）。
- 与 R58 口径比对：R58 删了 chunk_buffer.go（前提证伪+语义缺陷）与 error_detector_ring.go（包内零外部消费实锤）；保留 WeightedRouter 的唯一理由是 tests/local 在用。**ctxpool 两头都不占**：零消费像前两者，但它是 Handoff-B #4 的挂账优化件（CAPACITY_HANDOVER.md:63 `未跟踪/未接线 | 未完成`），有明确"待接线"意图，不像 R58 两个被删件那样"接入形态不存在"。
- **裁决建议（P3）**：钉注是合理折中，但必须给 Handoff-B #4 一个 Owner/期限。若 R60/R61 仍零接线，按 R58 口径删除（341 行 + 测试的维护成本含每轮 -race；git 历史可找回，接线时复活）。"加固了一个零生产流量路径"的投入本身也印证了零消费口径——加固只有防患价值，无当期收益。

**遗留残余（P3，并入上条）**：`Value()`（ctxpool.go:167-172）**无锁读** `c.parent`，而 `reinit`（:330-340）在 mu 内写 `parent`、在 mu 外写 `createdAt/generation/owner`。与本次加固的 wrapper 竞态同类（读侧并发于 reset 周期即触发），当前靠"接线后单 goroutine 拥有"约定兜底。接线前应一并补齐，否则 STATUS 钉注声称的"加固完成"不完整。

### 2. 132a60fec error_probe submitter 接线 `!useNewProbeMode()` 门

**✅ 实证无双提交**：
- 门位置 cmd/gateway/main.go:4230 `if activeProbe != nil && !useNewProbeMode()`；
- 新模式接线在 main.go:4356 `stateManager.SetActiveProbeSubmitter(nodeProbeWorker.Submit, 2)`，位于 `shouldStartNewProbeWorkers(selfCheckAPIKey)`（main_helpers.go:298 = `useNewProbeMode() && canStartGatewayDependentNewProbes`）块内。
- 两调用点互斥：4236 要求 `!useNewProbeMode()`，4356 要求 `useNewProbeMode()`。`useNewProbeMode()`（main_helpers.go:276-283）读 env，进程内静态（无 `os.Setenv` 写该变量，仅测试有），**不存在"模式切换瞬间"窗口**——env 在进程生命周期内不变。
- `SetActiveProbeSubmitter`（domains/credentialstate/manager.go:195）为字段覆盖写，启动期单线程调用（stateManager.Start 之前），无竞态。

**零提交路径盘点**：
1. legacy 模式 + activeProbe==nil：activeProbe 在 main.go:4138 无条件构造，不成立。
2. **新模式 + 系统 API key 不可用**：`shouldStartNewProbeWorkers` 整组跳过（main.go:4399 WARN "new probe workers skipped"），4356 随块跳过 → consecutive_fails 只累计、无故障触发探测。属**既有设计降级**（整组共享生命周期，部分启动被有意避免），非本 commit 引入，但建议在 P3 清单留档：该降级下 B5 键与 legacy 一样失去故障触发探测能力，仅日志一行可见。
3. credProbeV2 的 `ProbeNowAsync`（4211 fast-reprobe 提交器、4395/4541 recovery immediate）在新模式不悬空：main.go:3987 新模式下仍 `StartFastProbeConsumer`（注释 :3982 明确），fastReprobeQueue 有消费者。✅

**bg -race（ProbeSync/ProbeConfirm/Confirm/Starv 等 31 用例）全绿（19.4s）**。

### 3. 2e6d55c11 ProbeSync 双层信号量顺序对调 + ProbeConfirm 纳入 per-cred ≤2 闸

**顺序对调验证（✅）**：bg/node_probe.go:1600-1604 获取序 `credSem <- ` → `sem <- `；defer 注册序使释放序为 LIFO：先 `<-sem`（:1604）后 `<-credSem`（:1602），与获取严格逆序。

**全仓死锁扫描（✅ 无反序调用点）**：
- `w.credSemaphore` 全仓仅两个获取点：ProbeSync（node_probe.go:1600）与 `acquireCredSlotForConfirm`（:1850）。`sem` 是 ProbeSync 调用内的**局部** channel（:1585，每调用重建），不跨函数共享——跨函数锁序死锁结构性不可能。
- 唯一双层持有者就是 ProbeSync goroutine（credSem→sem），完成路径（probeDirect/probeGateway/state 写）零资源依赖，与 commit 声明一致。
- 锁序 `w.mu → syncWaitersMu` 不变：两处（:1474-1477、:1562-1564）都是先 w.mu 后 syncWaitersMu，无反序点；信号量获取均不持 w.mu。

**per-cred ≤2 闸释放路径（✅ 主路径无泄漏；一处 panic 路径例外 → 发现 F3）**：
- ProbeSync：`defer func() { <-credSem }()`（:1602）panic-safe。
- ProbeConfirm：`release()`/`release2()` 在两次 `round()` 后**内联**调用（:1898/:1916 附近）以让睡眠窗口不占槽——但 **panic 不经 defer**，`round()` panic 则槽永久丢失。`credSyncSem` 条目"created on first use and never evicted"（:1833 注释）→ 该凭据容量永久降为 ≤1 直至重启。唯一生产调用方 scheduleFlashBlipConfirm（executors/executor_nodehealth.go:328-334）有 recover（:331），进程不死但槽已漏。概率低（probeDirect 为 HTTP+JSON），列 P3。
- 饥饿/取消路径：`acquireCredSlotForConfirm`（:1849-1860）三路 select，未获得即无槽可漏；饥饿 fail-open + `llmgw_node_probe_confirm_slot_starved_total`，ctx 取消 fail-closed——与"确认才降级"教义自洽。钉桩测试 ×2 在 bg/node_probe_confirm_test.go，本轮 -race 复跑绿。

### 4. 83567d0ca KindRateLimit 剔除断路器失败计数

**✅ 实证**（domains/credential/breaker.go）：
- `RecordFailure` 在 client-bug 早退后、计数前对 `KindRateLimit` 早退（:383-388）：不计 `consecutive`/`failCount`、不写 `lastErrorKind`、不触发 OPEN/QUARANTINE。
- HALF_OPEN 探针撞 429：`b.State()==StateHalfOpen → ReleaseProbe()`（:384-386），归还探针槽保持半开，不会把断路器卡在探针被占的半开态（claimProbe 的超时强收机制 :264 也在兜底）。`ReleaseProbe`（:302-312）mu 内操作，`State()` 原子读，无锁问题。
- **其他 kind 累计正确**：早退点在 `b.mu` 临界区之前，Timeout/Network/Transient/UpstreamDown 等照常走 :390 起的原路径，`transientFamily` 同族计数（:350-355、:409-419）与 R34 coolingCycle 重置逻辑均不受影响；`defaultPolicies`/`freeTierPolicies` 的 RateLimit 项已删（:112-114、:170 注释）。
- **绑定级降权保留**：writer.go:167/:348/:596-616 绑定级 `coolingDuration`（Retry-After 优先、maxCoolingDuration 封顶、默认 3min）路径未动；executor_nodehealth.go:412-413 429 → nodehealth.ErrorKindRateLimit 单独映射，不进 `Circuit.RecordFailure` 计数。
- 语义注记（非缺陷）：429 早退**不重置** consecutive 链——"限流对断路器不存在"语义自洽（429 前后的真实失败连续性得以延续）。
- `TestRateLimitExcludedFromBreaker` -race 绿（domains/credential 2.1s）；ADR docs/adr/2026-09-22-rate-limit-breaker-exclusion.md 在位。

### 5. 队列架构现状盘点（D04）

实际架构 = **请求路径无中央待处理队列**（每请求即席规划）+ **探测域 PG 持久队列** + **执行域多层信号量**：

| 层 | 实现 | 证据 |
|---|---|---|
| 分层选路 L1/L2/L3 | `partitionBySelectionLayer`：L1 优先层（flag+quota ok+并发余量）/ L2 常规层 / L3 兜底层（饱和优先节点），层内保序 | executors/router.go:1700-1731 |
| Tier 桶 | `[1,2,3,9]` 顺序 + `policy.TierFallbackMax` 截断 + 全局 maxTotal=12 | router.go:27、:1078-1133 |
| 权重来源 | `c.Weight`（DB credentials）为首跳抽签基数；`firstHopLotteryWeights` 以 sticky .15/recency .05/balance .05/planQuota .10 折算惩罚（负值钳 0、floor 1，R46 F8④），只影响首跳，failover 顺序保持健康序 | router_scoring.go:707-772、router.go:1104-1126 |
| 层内均衡 | P2C（`p2cOrder`）+ `weightCounterSoftCap`=100k map 轮换（R28 #22b） | router.go:1372、:1137-1163 |
| 限流×选路互动 | 规划期 `priorityNodeSaturated`：容量两读位面（LiveLoad→DB concurrency_limit 快照；legacy→Limiter 热更 Capacity），used 取 LiveLoad→Limiter.Used；饱和优先节点沉 L3 尾部。派发期 `ApplySoftPenalty` 同契约第二读。fail-open（容量未知=不饱和） | router.go:1661-1698、router_scoring.go:405-437 |
| channel 池 | Limiter 五层信号量 Global/Pool/Credential/Identity/Key，`AcquireAll` 全层获取 | domains/credential/limiter.go:200-683 |
| 探测解耦 | PG 持久队列 `FOR UPDATE SKIP LOCKED` + lease + dedup key（双入队 no-op），ProbeService 拥有执行；`:36` 兜底 pump | bg/probe_queue.go:419/:618/:644 |
| 观测解耦 | URSMS shadow worker：有界 chan 128、满即弃（返回 bool）、stopAndWait 收口 | router.go:29/:85/:102 |
| 错误封装 | errorsx.ErrorKind → TaskAction（KindRateLimit→WaitRecovery）+ ExecuteError 信封 LastKind/retryable；48h 内 9f3e89cae/0cc311abe/44823d798 三连修闭环 | attempt_outcome.go:181、executor_dispatch.go:462-469 |

WeightedRouter 生产零持有为 R58 实锤，本轮不重复评估其内部。**结论：D04 清单的"总待处理队列/分维队列"以上述形态存在，无未收口的结构缺口。**

### 6. race 实测（每包 ≤110s timeout，全部 -count=1）

| 包 | 范围 | 结果 |
|---|---|---|
| internal/ctxpool | 全包 | ok 1.7s |
| credentialhealth | 全包（建议项） | ok 1.55s |
| bg | `-run 'ProbeSync|ProbeConfirm|Confirm|CredSemaphore|Starv'`（31 用例） | ok 19.4s |
| domains/credential | TestRateLimitExcludedFromBreaker | ok 2.1s |
| security/sanitize（D14 复核附带） | 全包 | ok 3.9s |

### 7. 泄漏/溢出扫描（48h 新增代码为主）

48h 非 test 新增中 `go func`/`time.NewTicker` 仅 3 处生产点位：
- bg/ledger_reconciliation.go：ticker 有 `defer Stop`、ctx/stopCh 双退出——但 goroutine 无 recover → F2。
- executors/executor_nodehealth.go flash-blip：recover + in-flight 清理 defer 齐全 ✓。
- 资源 Close：48h 新增 `defer f.Close()/resp.Body.Close()/rows.Close()` 全部就位；无未 Close 句柄。新增 `make(chan` 均为有界或 stopCh 模式，无无界 buffer。
- ProbeConfirm 内联 release 的 panic 例外 → F3。

## 二、发现清单

| # | 级别 | 发现 | 证据 | 建议 |
|---|---|---|---|---|
| F1 | P3 | ctxpool 全仓零消费（连测试基建都不用），保留口径需与 R58 对齐；且 `Value()` 无锁读 parent、`reinit` 锁外写 createdAt/generation/owner，加固不完整 | internal/ctxpool/ctxpool.go:54-57、:167-172、:330-340；全仓 grep 零引用 | 给 Handoff-B #4 定 Owner/期限；接线前补齐 Value/reinit 的锁覆盖；逾期未接线按 R58 口径删除（git 可找回） |
| F2 | P3 | bg/ledger_reconciliation.go 新增常驻 goroutine 无 recover：RunOnce panic → 对账循环静默永停（running 标志复位但无日志、无重启） | bg/ledger_reconciliation.go:93-124（go func 体） | 按 bg base_worker 的 recover+backoff 重启模式包装，或至少 panic 时 slog.Error |
| F3 | P3 | ProbeConfirm 每次直连 ping 的 per-cred 槽释放为内联调用，panic 路径漏槽：credSyncSem 条目永不重建，该凭据容量永久降为 ≤1（调用方有 recover 不崩进程） | bg/node_probe.go:1888-1916（release/release2 内联）、:1833（never evicted）；调用方 executors/executor_nodehealth.go:331 | release 改 panic-safe（defer + slot-held 标志，或 acquire 后 defer 内按标志释放），保住"睡眠窗口不占槽"语义 |
| F4 | P3（留档） | 新探测模式 + 系统 API key 不可用 → 整组 worker 跳过 → SetActiveProbeSubmitter 零接线：故障触发探测静默失效，仅一行 WARN | cmd/gateway/main.go:4356、:4399；main_helpers.go:276-298 | 非本次引入、属有意降级；建议该 WARN 挂告警规则，或在降级时回退接线 legacy submitter（需裁决 legacy Start 与整组门的一致性） |

无 P0/P1/P2 发现。四项必审 commit 的核心声明全部实证成立（见 §一各节 ✅ 标记）。

## 三、D14 场景安全复核（81e4ab932）

- `security/sanitize` smart_sani_guard.go：restoreToolCallsArgs（:984）/restoreJSONRecursive（:1030）覆盖 chat 完成式 + 流式 delta + Anthropic tool_use；`PlaceholderPattern` 预检、arguments 非法 JSON 回退整串还原、marshal 失败保原值。
- 租户隔离：`sanitizeMapKey` 无租户才落 legacy key，有租户强制 tenant-scoped（:1080 附近注释），防跨租户占位符还原。
- 时序契约：restore 拦截器 append 到既有 chain 末尾，保证 output_compliance 先于 sanitize_restore（cmd/gateway/goal_control.go:588-597 注释与实现一致）。
- -race 全绿（3.9s）；tests/48h-audit/D14-security 四类套件入库。

## 四、下一轮入口

1. F1：Handoff-B #4 裁决（Owner/期限 or 删除）——建议随 R59 收口方案文档定版。
2. F3：ProbeConfirm release panic-safe 化属 5 行级修复，可随下轮修复批带走。
3. F2：ledger_reconciliation recover 包装同上。
4. F4：降级告警接线留给 S7/D09 语境。
