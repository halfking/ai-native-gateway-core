# 轴C（round2）：多层队列/并发/限流/负载均衡/运行安全

仓库 `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go`（只读）。基线：docs/audit-2026-09-05-24h-comprehensive.md §二/§四 + .audit-workspace/2026-09-05/axis-C-concurrency.md。本轮全部路径人工审读，未重复报告已修项。

**发现计数：P0=0，P1=0，P2=3，P3=7（合计 10）**

## 一、发现列表（C-#12 起续编）

### C-#12 [P2] dispatch()/parkScheduled 路径缺 completed/abandoned 终态守卫 —— C-#1 single-owner 修复的 TOCTOU 残余
- **证据**：`domains/dispatch/dispatcher.go:74`（`dispatch(qr)` 入口无守卫，对照 `failover.go:96` move() 的守卫）；取消路径 `pipeline.go:1110-1118`（qr 停在 totalQueue.ch/mq.ch/dispatchIn 任一缓冲时 Submit 即可 CAS 完成）。此后 dispatcher/mover 仍会在无锁字段上写：`dispatcher.go:225`（tryModelChangeOutcome→recordDecision）、`dispatcher.go:375`（scheduleCapacityRetry→recordDecision）、`failover.go:179`（move() 中段 recordDecision——入口检查经 `failover.go:152` routeFunc 长回调后失效）、`pipeline.go:1314`（runTotalDrainer→parkScheduledRequest→recordDecision）。竞争字段组与已修 C-#1 相同（journalSeq/AttemptJournal/Counts/LastFailover，`journal.go:105-145`）。
- **影响**：客户端取消恰逢排队/路由回调期间 → 与取消胜者的 journal 写并发（-race 可标记）；同时已取消请求仍完整跑一次 routeFunc（Router.PlanCandidatesPinned，含 Redis 读）+ 入队，白耗调度预算直到 governor Acquire 因 ctx 取消快速失败。
- **最小修复**：`dispatch()` 入口加 `if qr.completed.Load() || qr.abandoned.Load() { return }`（镜像 move()）；`parkScheduledRequest` 同样前置短路；move() 在 routeFunc 返回后、`failover.go:179` recordDecision 前二次检查。长期：以互斥保护 recordDecision（round-1 已建议）。工作量 M。

### C-#13 [P2] Redis-enforce governor 饱和等待 = 每 2ms 重放一次 Redis Lua，无退避（限流重试风暴）
- **证据**：`domains/dispatch/redis_backend.go:471-485`（redisRateGovernor：wait 恒 2ms，每循环跑一次 rate Lua；本地版 `governor.go:166-167` 会算精确 next-token wait，Redis 版没有）；`redis_backend.go:523-565 + 644-652`（redisEnforceGovernor：code=0 → waitOrGiveUp 2ms → 重放 acquire Lua）。并发模式 + `config.go:115` MaxQueueWaitMS 默认 0（zero-wait 语义）时 giveUp 为零值 → **无预算上限，只有 ctx 能终止等待**。
- **影响**：redis_enforce 后端 + 凭据饱和时，Tier-2 队列深 300 → 单凭据可达 ~15 万 EVALSHA/s，把限流等待放大成 Redis 事故（与 C-#8 本地 CPU 噪声不同级，故升 P2）。
- **最小修复**：由 Lua 返回 remaining 计算等待时长（对齐本地 rpmGovernor），或 2ms→100ms 指数退避封顶。S-M。

### C-#14 [P2] 三类调度 worker goroutine 仍无 panic recover（round-1 C-#3 遗留核实：未修、且未进 §四 未修清单）
- **证据**：`domains/dispatch/dispatcher.go:22-44`（runDispatcher）、`failover.go:26-47`（runFailover）、`pipeline.go:1147-1195`（runTotalDrainer）均直接调用 `p.dispatch`/`p.move`，其中 `routeFunc`（→Router.PlanCandidatesPinned 复杂排序）、`modelResolveFunc`、`modelRecommendFunc` 为跨包回调，在这些 goroutine 上裸奔执行。对照已有防护：`forwarder.go:442-452`（attempt）、`executor_dispatch.go:496-499`（forwardForDispatch）、`pipeline.go:312/966/1778`（observation/journal/sink）。第二轮修复 #16 的 "shutdown 短路不执行 executor 回调"只覆盖关停，不覆盖 panic。
- **影响**：路由回调任一 panic 击穿 worker 杀死整个进程（历史 Router 侧排序逻辑复杂，非纯函数链）。
- **最小修复**：三个 worker 循环体包 `defer recover()` + 打点（与 forwarder.attempt 同式）。S。

### C-#15 [P3] complete() 终态路径仍串行叠加多次 Redis 往返（C-#4 修复的邻接残余）
- **证据**：`pipeline.go:1580-1612`：`queueBackend.ClearDue`（`queue_backend_redis.go:367-372` 无显式超时，仅靠 go-redis 默认 3s）、`releaseAllClusterAdmissions` → 最多 3×Release（各 3s，`queue_backend_redis.go:309-323`）、`recordMinuteStats`（`priority_affinity.go:70`，100ms）。队列后端默认 `auto`→redis（`cmd/gateway/main_dispatch_backend.go:185-190`），生产常开。
- **影响**：Redis 挂起（非快速失败）时单次完成最长 ~9-12s，占用 dispatcher/mover/forwarder worker。建议合并 sweep/异步化或统一 100ms 预算。工作量 S。

### C-#16 [P3] Router.weightCounters 以"当前可用候选子集"为 sync.Map key，永不清理（慢泄漏）
- **证据**：`domains/streaming/executors/router.go:135-136、1103-1112`；key 来自健康过滤后的 `sorted`（`router.go:1039-1056`），节点 flapping 时子集组合膨胀，条目只增不减。
- **修复**：改 (ProviderID,CredentialID,RawModel) 粒度计数器或 LRU。S。

### C-#17 [P3] Pipeline.Stop() 的 wg.Wait 无预算，可越过 5s stopCtx
- **证据**：`pipeline.go:978-1051`（`p.wg.Wait()` 无超时）；`cmd/gateway/main.go:6634-6659` 在 5s stopCtx goroutine 内调 pipeline.Stop()。一个 survival 流（2h 上限，`executor_dispatch.go:247/executor_chat.go:2112`）的 in-flight attempt 会阻塞 wg.Wait 至 systemd SIGKILL，Stop 之后的 journey recorder Close 等清理全部跳过（与 G-#4 同族）。修复：wg.Wait 包 stopCtx/超时。M。

### C-#18 [P3] upstream Transport 未设 MaxConnsPerHost、未启用 HTTP/2 —— 与 identity 池配置不一致
- **证据**：`upstream/client.go:137-149`：keepalive 30s/IdleConnTimeout 90s/MaxIdleConnsPerHost=32 均有，但 `MaxConnsPerHost=0`（ModeDisabled 凭据可无限活跃连接），自定义 Transport 未设 `ForceAttemptHTTP2` → 全部上游走 HTTP/1.1（每流一连接，32-idle 池外反复重建）。对照 `pool/pool.go:109-127`（identity 池有 MaxConnsPerHost=64）。修复：补两行配置。S。

### C-#19 [P3] 遗留 C-#6 核实：加权抽签溢出未修
- **证据**：`router.go:1130-1141`（totalWeight int 累加无上界；`weightStride` router.go:1171-1177 的 `totalWeight*618` 与 `position*stride` 在 totalWeight>~4e9 时 int64 溢出 → 恒选字典序第一凭据）；手工权重无 clamp（`provider/client.go:1753-1758` 仅把 ≤0 归一化 100，其余原样尊重）。结论：仍未修，维持 P3（需极端配置才触发）。

### C-#20 [P3] 遗留 C-#9 核实：PriorityCluster 仍未接线（死代码确认）
- **证据**：`executor_dispatch.go:207-234` candidateToRef 不填 PriorityCluster；全仓生产代码无赋值点；`priority_affinity.go:17-26` sortPriorityClusters 恒为空转稳定排序（`dispatcher.go:119` 每请求白做一次 O(n log n)）。维持 P3。

### C-#21 [P3] 遗留 C-#7/#8/#11 核实：忙等/轮询/timer 未修
- **证据**：C-#7 `pipeline.go:1268-1274`（enqueueModelFromTotal 仍 `time.After(1ms)` 忙等）；C-#8 `governor.go:82-86`（本地 2ms 轮询，另见 C-#13 Redis 放大版）；C-#11 `governor.go:180/247`（rpm/tpm 仍 `time.After(wait)`）。维持 P3。

## 二、已确认闭环（本轮复核通过，不再跟踪）

1. **C-#1 取消路径 single-owner 竞态**：move() 入口守卫（failover.go:96）、routeFailover 守卫（pipeline.go:1855）、onRetryDue 守卫（failover.go:356）到位；残余 TOCTOU 收窄为 C-#12。
2. **C-#2 shutdown 漏排空**：dispatcher（dispatchIn 排空 + drainerWg/modelMu barrier 保证生产者集终结）、failover（forwarderWg/credMu barrier）、Tier-0 total 残留（drainTotalQueueResidue）、Tier-1 lane 残留（drainModelQueueOnShutdown，mq.mu 内关停门）、Tier-2（forwarder drainAndComplete + reclaimPendingOld）五层全部闭合；enqueue 双路径在 mq.mu/handoffMu 内做 shutdown 检查防 send-after-drain。
3. **C-#4 complete() 同步 JournalSink**：快照同步构造（生命周期安全）→ 有界 256 队列 → 单 worker 异步投递；journalChMu 防 send-on-closed；Stop 有界排空 5s（pipeline.go:1673-1785）。
4. **worker panic guard**（本轮修的是 bg 域 4 个常驻 goroutine）确认在位；dispatch 域 worker 的对应缺口独立记为 C-#14。
5. **准入对称性**：totalQueueDone/clusterTotal/clusterModel/clusterCred 全 take-once；Submit 拒绝路径补偿释放（pipeline.go:1097-1098）；HeapRetryScheduler Close 带 close handler 完成 parked 请求。
6. **锁与共享结构**：qr 各 accessor 锁成对、值拷贝返回；锁序 journeyMu→mq.mu/handoffMu、credMu→govMu 全仓单向；runForEachCredSnapshot 两段式避免 credMu 内缓存写；Registry/DimensionIndex/snapshotAge/credStateCache 均有锁；-race 盲区未发现新直读点。
7. **泄漏扫描**：范围内 8 处 NewTicker 全部 defer Stop；ctx 传递完整（executeViaDispatch defer cancelDispatch；fire-and-forget goroutine 均 detached+bounded timeout，health_tracker/context_summarize/fpRelease）；resp.Body 错误路径均关闭（upstream captureErrorBody、executor_chat 4 处、pool.probe）；sticky/proxy/pool sweeper 均有 stop 通道。
8. **selector.go sync.Pool**：survivors/scored 归还无引用泄漏（Alternatives 为新分配拷贝，Scorer 契约切片不入池）；n 上限 MaxUseRecords=100，Seq int32 无越界——本轮优化正确。
9. **溢出扫描**（重点 execute_attempt.go / routing_tracker.go / selector.go / attempt_budget.go）：无整型溢出/越界；AttemptBudget 上限 601 clamp；routing Seq=len+1 单调；compactForStorage 防占位条目污染 settle retry_count 推导。
10. **网络/可用性**：redisQueueBackend fail-open（degraded 打点+心跳自愈+3s 超时）；governor Redis 冷启动 fail-open 回退本地或 fail-closed unavailableGovernor（forwarder.go:80-99）；ratelimit RedisLimiter 熔断+单探活 goroutine 回退内存（redis_sliding.go:253-332，recovering 标志防 goroutine 繁殖）；凭据轮换 ResetKeyRotatorForCredential/Key 有失效入口、瞬时 401 不永久弹出。
11. **密钥不落日志**：范围内 slog 调用 grep APIKey/Authorization/Bearer/secret 零命中；upstream.Error Body 4KB 上限且为 vendor 错误体（E-#2 request_logs preview 脱敏缺口属轴 E 遗留，不重复计）。

## 三、冗余/待清理代码

- `priority_affinity.go sortPriorityClusters` + `CredentialRef.PriorityCluster`：恒定空转排序 + 死字段（C-#20），接线或删除。
- `executors/router.go planLegacy`（994-1007）：deprecated 占位死函数，确认无引用后删除。
- `ratelimit/redis_sliding.go rpmSHAFallback`：`nolint:unused` 死函数。
- `pipeline.go itoa`：手写 strconv.Itoa 替代品，建议直接用标准库。
- `dispatch.CredentialRef` 与 `governor_spec.GovernorSpec` 的 mode/limit 双轨镜像（pipeline.go:648-676 specToCredentialRef 的 max 合并逻辑）长期应收敛为单一来源。
