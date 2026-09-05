---
archived_from: (legacy) docs/archive/2026-08/AUDIT_CROSSCUTTING_CONCURRENCY_20260813.md
archived_at: 2026-08-17
archived_by: docs-archive remediate v1.0
backup_ts: 20260817-190917
status: archived
note: legacy archive, frontmatter retroactively added
---

# Cross-Cutting Concurrency & Reliability Audit — Integration Report (2026-08-13)

**Scope:** 集成本轮 5 个子领域审计的最终结论。基线 commit `944346ab`。
**Sub-areas:**
1. 请求队列 / 调度流水线 (`domains/dispatch/`)
2. 凭据并发限流 (`domains/credential/{limiter,rpm_redis,rpm_memory,breaker}.go`)
3. 凭据指纹槽系统 (`credentialfpslot/{slot,node_state,reclaim}.go`)
4. 凭据状态管理器 + 流重试生命周期 (`domains/credentialstate/` + `internal/streamretry/`)
5. 速率限制器 (`ratelimit/{sliding,redis_sliding}.go`)

**Verdict:** 🔴 **有阻塞项**。`domains/dispatch/` 包当前 `-race` **不通过**（`qr.CredEnqueuedAt` data race），需先修才能上线。其余包 `-race` 全绿，但 **多类遗留缺陷**（shutdown 不 drain / 槽抢占破坏指纹隔离）仍未修，需排期。
方法：每个发现都用 `grep`/源码核对当前代码状态，**并实际跑了 `go test -race` 复现**，不信任前序乐观结论（前序有把未提交标成已完成的先例，且未对 dispatch 包跑 race 测试）。

---

## 0. 阅读指南

每条发现给出 **状态**：
- ✅ **FIXED** — 当前代码已修复
- 🔴 **STILL-OPEN** — 当前代码仍是缺陷，建议修
- ⚪ **NOT-LIVE** — 缺陷在代码里但没有生产调用方（死代码）
- 🟦 **BY-DESIGN** — 经核实现为有意行为

---

## 1. 跨领域主题（模式归纳）

本轮最重要的不是单个 bug，而是 **两个反复出现的反模式**：

### 主题 A：`Stop()` 不 drain in-flight / queued 工作（请求丢失 + goroutine 泄漏）

`domains/dispatch/` 的三层（Tier-1 model 队列 / Tier-2 cred 队列 / failover mover）里，**只有 Tier-1 修了这个 bug 类**（`runModelDrainer:254-260` + `TestStopNoDrainLoss`）。Tier-2 和 failover handoff 都漏了，是同一个概念缺陷的三处表现，应作为一个连贯的 shutdown-drain 改动一并修。详见 §2-D1/D2/D3。

### 主题 B：长生命周期资源的"活跃度"判断靠墙钟 TTL，没有心跳

`credentialfpslot` 的指纹槽用 `idle = slotTTL - remaining` 判活跃，没有任何 in-flight 心跳/续约。活跃流超过 5 分钟就被 LRU 抢占，破坏指纹隔离不变量（系统存在的全部意义）。详见 §3-S1。`streamretry` 的 keepalive goroutine 也有类似"无生命周期管理"的味道，已被 `40ecc76a` 移除（见 §4-SR3）。

---

## 2. 子领域 1 — 请求队列 / 调度流水线 (`domains/dispatch/`)

**全部 🔴 STILL-OPEN**，同一根因（Stop 不完成在途/排队请求）。代码现状已逐一核对 `pipeline.go` / `forwarder.go`。

| ID | 严重 | 位置 | 问题 | 状态 |
|----|------|------|------|------|
| **D-RACE** | **CRITICAL** | `pipeline.go:301` (写) ↔ `forwarder.go:107-108` (读) | **真实 data race，`-race` 必报**（已用 `go test -race` 复现，`TestPacingTimeoutSkipsSameCredRetry` FAIL）。`tryEnqueueCred` 在 `cf.queue <- qr` **之后**才写 `qr.CredEnqueuedAt = time.Now()`；forwarder loop 在 `acquire()` 里读 `qr.CredEnqueuedAt` 算 queue-wait metric。channel hand-off 不同步"发送后的写"，两个 goroutine 裸读写同一字段。这是 commit `16290035`(wire Tier-2 queue-wait metric) 引入的。QueuedRequest 的"单 owner"不变量在此被破坏。 | 🔴 |
| D1 | **HIGH** | `forwarder.go:69-71` | Tier-2 `loop()` 在 `<-cf.ctx.Done()` 直接 return，**不 drain `cf.queue`**，队列里的 `qr` 永不 complete → `Submit` caller 永久阻塞。Tier-1 (`runModelDrainer:254-260`) 已修此 bug 类，Tier-2 漏修。 | 🔴 |
| D2 | HIGH | `pipeline.go:287-288` | `routeFailover` 的 `case <-p.stopCh` 分支 return 但**不调 `p.complete(qr,...)`**。对照 `runModelDrainer:258` 正确调了。故障爆发 + Stop 时 in-flight forward 的 qr 被丢弃。 | 🔴 |
| D3 | HIGH | `forwarder.go:22,35,67`; `pipeline.go:135-141` | forwarder `loop()` 与 per-request `attempt()` 由 **`cf.wg`** 跟踪，**不在 `p.wg`**。`Stop()` 调 `cf.cancel()` 后 `p.wg.Wait()` 立即返回，in-flight forward 仍在跑（可能跨上游超时数秒）。 | 🔴 |
| D4 | HIGH→MED | `pipeline.go:311-324` | `getOrCreateForwarder` **无 `shutdown` 守卫**（对照 `getOrCreateModelQueue:220` 有）。Stop 后一个延迟的 `Submit`→`tryEnqueueCred` 可 spawn 一个永不被 cancel 的 forwarder，永久泄漏。 | 🔴 |
| D5 | MED | `pipeline.go:220-222` | shutdown 时 `getOrCreateModelQueue` 返回**无 drainer 的 throwaway cap-1 队列**，`enqueueModel` 假成功 → `tryModelChange` 以为 re-enqueue 成功，qr 永久滞留。 | 🔴 |
| D6 | MED | `pipeline.go:278-280` | `complete()` 的 `default` 分支静默吞结果。当前 CAS 保证不可达，但若未来加第二个发送方会静默丢。 | 🔴(脆弱) |

**修复方向（一个连贯改动）：**
1. `forwarder.loop()` 的 `<-cf.ctx.Done()` 分支加 `for { select { case qr := <-cf.queue: p.complete(qr, ErrShutdown); default: } }` drain 循环（镜像 `runModelDrainer:254-260`）。
2. `routeFailover` 的 `case <-p.stopCh:` 加 `p.complete(qr, ForwardOutcome{Err: ErrShutdown})`。
3. forwarder goroutine 改用 `p.wg`（或 `Stop()` 收集所有 `cf` 后 `cf.wg.Wait()`）；`getOrCreateForwarder` 加 `shutdown.Load()` 守卫，命中时返回 nil/让 `tryEnqueueCred` 返回 false（fail admission 而非假成功）。

---

## 3. 子领域 2/3 — 凭据槽系统 (`credentialfpslot/`)

**主要缺陷 🔴 STILL-OPEN。** S1 是最有安全影响的一条（指纹隔离被破坏）。

| ID | 严重 | 位置 | 问题 | 状态 |
|----|------|------|------|------|
| **S1** | **HIGH** | `slot.go:77,776-778` (idle 逻辑) ; `:706` (`BuildEgressIdentity(slot)`) | **无心跳**：活跃度纯靠 `idle = slotTTL - remaining`（wall-clock TTL 衰减）。活跃流超过 `DefaultActiveGateSeconds=300` 后 `idle >= gate` 即被 LRU 抢占。`BuildEgressIdentity(cred, slot)` 是 slot index 的确定性函数 → 抢占者与原活跃流**呈现同一指纹**给上游，正是槽系统要避免的冲突。小池（1-3）+ Claude/o1 长推理流是现实触发场景。 | 🔴 |
| S2 | MED | `slot.go:327-331,683-684` | `Acquire` 把 Redis/Lua **错误**（NOSCRIPT/网络/脚本错）和真正的"无空闲槽"都归为 `recordAcquireSaturated()`。`recordAcquireRedisError()` 只在 `client==nil` 分支调。Redis 抖动时运维把基础设施问题误读成凭据饱和。 | 🔴 |
| S3 | MED | `metrics.go:146`; `slot.go:699` | `recordPreempt()` + `slotPreemptEvents` 是**死代码**（唯一调用在 coverage test）。LRU 抢占只 `slog.Info`，从不 metricate → 抢占率永远读 0，正好掩盖 S1 的症状。 | 🔴 |
| S4 | MED | `reclaim.go:207-224` | reclaim sweep 每 key 一次 `EVAL`（SCAN + 逐 key `reclaimSlotScript.Run`），无 pipeline；首个 key 出错即 `return ... fmt.Errorf` **中止整轮**，剩余 key 留到下一 tick。 | 🔴 |
| S5 | LOW | `slot.go:184-187` vs `:140` | `DefaultLimit()` fallback 返回 5，与 `DefaultDefaultLimit=20` 矛盾。 | 🔴 |
| S6 | LOW | `slot.go:461-465` | `Release` Lua 注释说"keep alive 24 hours"，实际 `EXPIRE slotTTL`（30 min）。pin 的 24h 是另一处单独设置。注释误导。 | 🔴 |
| S7 | LOW | `node_state.go:312-333` vs `:366-374` | cooldown-extend 失败分支**漏更新** `last_disabled_at` / `disable_count`，削弱"动态冷却调整"信号。 | 🔴 |
| S8 | LOW/INFO | `node_state.go:62-74,199-226` | `IsUsable`/`ConsecutiveFailureStreak` **会 mutate receiver**（prune SlideWindow / 翻 Disabled）。当前唯一生产调用方同步用、不缓存，今天无 race，但若未来被缓存/并发读会踩雷。 | 🔴(footgun) |

**S1 修复方向：** 给持有的 lease 加心跳 —— 流式循环里周期性 `EXPIRE` 刷新 TTL（如每 60s），`Release` 时停。或把活跃度判断从 TTL 衰减改成 holder 写的 "last seen" 时间戳。

---

## 4. 子领域 4 — 凭据状态 + 流重试 (`domains/credentialstate/` + `internal/streamretry/`)

| ID | 严重 | 位置 | 问题 | 状态 |
|----|------|------|------|------|
| CS1 | CRITICAL→✅ | `domains/credentialstate/batch_writer.go:58-97` | BatchWriter 关闭丢数据。**已修**：`Stop()` cancel + `<-bw.done`，`run()` 在 `ctx.Done()` 先 `flush()`（final drain）再 close(done)。 | ✅ FIXED |
| CS2 | HIGH→✅ | `manager.go:501-588` | `UpdateFromProbe` nil-deref。**已修/不存在**：`State` 无 slice/map 字段，`m`/`state` 顶部 nil 守卫，每个 `oldState.*` 在 `if oldState != nil` 下，每个 `*time.Time` 有短路。 | ✅ FIXED |
| SR1 | MED | `internal/streamretry/retry.go:228` | `CalculateRetryDelay`: `delayMs := baseDelayMs * (1 << attempt)`。`attempt` 未 clamp，`attempt>=30`（32 位）溢出为负/0。**cap 在溢出之后**，`if delayMs < 0` 只兜全负。当前调用方 `MaxRetries` 默认 3，路径安全；但函数导出、可配置，重启用即雷。 | 🔴 STILL-OPEN |
| SR2 | — | `internal/streamretry/wrapper.go:319-362` | `errorRecorder.Write` 无条件置 `committed=true`。前序曾标为 bug（5xx-with-body 不可重试）。**核对 commit `40ecc76a` 后确认是 BY-DESIGN**：字节已上线，重试会产 corrupt 响应（双 status/双 body）。注释 `:319-345` 明确。 | 🟦 BY-DESIGN |
| SR3 | — | `wrapper.go` KeepaliveWriter | 前序担心后台 keepalive goroutine 永不启动/泄漏。**已由 `40ecc76a` 处理**：移除后台 goroutine，keepalive 改为 `RetryContext.Sleep` 内同步 tick（避免与 handler 写竞争）。 | ✅ FIXED |
| CS3 | LOW | `domains/credentialstate/manager.go:169-181` | `Manager.Stop()` 顺序：先 `batchWriter.Stop()` 再 `callbacks.Wait()`。若 in-flight timer 回调还在跑且其下游调 `batchWriter.Add`，会写进已停止的 writer（被静默吞，叠加 CS1 历史问题；CS1 已防丢但顺序仍脆）。建议反序：先 set stopped + drain callbacks，再 stop writer。 | 🔴(顺序脆弱) |

---

## 5. 子领域 5 — 速率限制器 (`ratelimit/`)

| ID | 严重 | 位置 | 问题 | 状态 |
|----|------|------|------|------|
| RL1 | P1(潜伏) | `ratelimit/redis_sliding.go:85` (`tpmLua`) | ZSET member = `now .. ':' .. tokens`。同毫秒 + 同 token 估算（`defaultTokenEstimate=800` 很常见）→ member 相同 → `ZADD` 覆盖而非新增 → **TPM 少计**。对照 `rpmLua:55` 用 `count`（单调）安全。当前 `CheckTPM` 无生产调用方（`RPMLimiter` 接口只暴露 `CheckRPM`/`RPMStatus`），重启用 TPM 即触发。 | 🔴 潜伏 |
| RL2 | P2 | `redis_sliding.go:43,55`; `domains/credential/rpm_redis.go:20` | 两个 Redis 脚本用**客户端时钟**做 score/evict，非 `redis.call('TIME')`。跨实例时钟漂移会少计/多计。60s 窗口 + NTP 下风险低。 | 🔴 低危 |

**修复方向：** RL1：member 改 `now .. ':' .. tokens .. ':' .. count`（复用 rpm 模式）或加 `redis.call('TIME')` 后缀。重启用 TPM 前必修。RL2：score 改用 `redis.call('TIME')[1]`（服务端秒）消除跨实例漂移。

---

## 6. 子领域 2 — 凭据并发限流 (`domains/credential/`)

前序 brief 把断路器/RPM/滑动窗列为高危，**核对后多为 ✅ 或 ⚪**：

| 声称问题 | 核对结论 | 状态 |
|---------|---------|------|
| 断路器 `ProbeCheck` 多探针 race | `Manager.ProbeCheck` (`breaker.go:596-607`) 确有 TOCTOU，但**零生产调用方**（grep `.ProbeCheck(` 全仓仅 test）。生产探针走 `Allow()`→`claimProbe()`（`:223-237`，持 `b.mu` 跨 state-check + 计数自增，原子）。 | ⚪ NOT-LIVE |
| RPM 计数器在 reject 路径泄漏 | RPM 预留（`CheckAndReserve`）设计上**不可取消**，靠 60s 滑动窗自动过期。信号量层在 reject 时全部 release（`limiter.go:414-419`），调用方再 release fp lease + probe（`executor.go:2513-2528`）。无泄漏。 | ✅ |
| Redis 滑动窗正确性 | 单 `EVAL` 原子（check+record 一次往返），`ZREMRANGEBYSCORE -inf cutoff` 正确淘汰，`PEXPIRE` 每次刷新。无 off-by-one。 | ✅ |

---

## 7. 验证（`-race` 现状）

本轮实际跑了 `go test -race`：

| 包 | 结果 |
|----|------|
| `internal/streamretry/` | `ok 2.296s`，0 race（含并发回归测试） |
| `ratelimit/` | `ok`，0 race |
| `domains/credential/` | `ok`，0 race |
| `credentialfpslot/` | `ok`，0 race |
| `domains/credentialstate/` | `ok 2.975s`，0 race |
| **`domains/dispatch/`** | **FAIL — DATA RACE**（`TestPacingTimeoutSkipsSameCredRetry`，见 D-RACE） |

⚠️ **修正前序"5 主题全 GO"的乐观结论**：dispatch 包当前 `-race` **不通过**。前序报告（round-1/round-2）未覆盖 dispatch 包的 race 测试，故未发现。这是本整合报告最重要的新发现。

注：D1-D6（dispatch Stop 不 drain）和 S1（槽抢占）在 `-race` 下不可见——它们是请求丢失/goroutine 泄漏/不变量破坏，需要 shutdown 时序的功能回归测试（`TestStopNoDrainLoss` 已为 Tier-1 存在，Tier-2 需补），而非 race detector。

---

## 8. 优先级排序（建议排期）

**P0（请求丢失 / 安全不变量破坏 / data race，建议尽快修）：**
1. **D-RACE**（`qr.CredEnqueuedAt` data race，`-race` FAIL）—— 把 `qr.CredEnqueuedAt = time.Now()` 挪到 `cf.queue <- qr` **之前**（发送前赋值，channel hand-off 建立 happens-before），1 行修复。当前阻塞 dispatch 包 `-race` 门禁。
2. **D1+D2+D3**（dispatch Stop 不 drain Tier-2 + routeFailover + forwarder goroutine 未 join）—— 一个连贯的 shutdown-drain PR，镜像已存在的 `runModelDrainer` 模式。补 Tier-2 `TestStopNoDrainLoss`。
3. **S1**（槽无心跳 → 活跃流超 5min 被抢占 → 指纹冲突）—— 加 lease 心跳（流式循环周期 EXPIRE）。

**P1（可观测性 / 运维误导）：**
3. **S2+S3**（槽 Redis 错误误报饱和 + 抢占率 metric 死代码）—— 一起修，因为 S3 的指标正是发现 S1 的手段。
4. **RL1**（tpmLua member 冲突）—— 重启用 TPM 前必修；修法 1 行。

**P2（健壮性 / 边界）：**
5. **SR1**（`CalculateRetryDelay` overflow，clamp `attempt`）。
6. **D4+D5**（getOrCreateForwarder 缺 shutdown 守卫 + throwaway 队列假成功）—— 与 P0#1 一并修。
7. **S4**（reclaim N+1 + abort-on-first-error → pipeline / continue-on-error）。

**P3（清理 / 一致性）：**
8. S5/S6/S7/S8、RL2、CS3、D6。

---

## 9. 与前序报告的衔接

本报告整合下列已提交报告的结论，并对 brief 中标"已完成"但实际未提交的项做了**以代码为唯一事实源**的复核：

| 报告 | 覆盖 | 本报告增补 |
|------|------|-----------|
| `AUDIT_STREAMRETRY_CONCURRENCY_20260812.md` | streamretry P0 metrics race（已修）+ 5 主题 GO | 复核：SR2 确为 by-design（`40ecc76a`）；新增 SR1（overflow，前序未覆盖） |
| `AUDIT_ROUND2_SESSION_URSM_PROM_20260812.md` | session queue / v2 mirror / URSM v2 / prometheus 全 GO | 维持 GO，无增补 |
| `AUDIT_PROBE_STREAM_LIFECYCLE_20260812.md` | probe SSE loopback/stale-tile/task-id（first+second pass 已修） | 维持，无增补 |
| `docs/修订0811/15,16-凭据节点状态与路由审计` | outbox 事务化 / override ORDER BY / writer nil-tx / node_state 注释 | 已合并入 commit（`0e575e01` 等）；§5-D/E/F 评分口径未修，本报告不重复 |
| **本报告新增** | dispatch shutdown-drain（D1-D6）、credentialfpslot（S1-S8）、streamretry overflow（SR1）、ratelimit（RL1-RL2） | 这些是 brief 列出但前序报告未最终落地的发现 |

---

**审计人:** ZCode
**审计日期:** 2026-08-13
**基线 commit:** `944346ab`
