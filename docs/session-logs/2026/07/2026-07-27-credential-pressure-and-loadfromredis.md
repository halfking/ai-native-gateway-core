# 会话日志：修复 domains/credential 两个失败测试

- **日期**：2026-07-27
- **基线**：`8da7db52`（clean main）
- **前序**：`2026-07-26-silent-data-loss-elimination` 会话

## 1. 需求

`domains/credential` 包内两个测试在 clean main 上稳定/间歇失败，需要找出根因并修复（既不允许放宽断言、也不允许为变绿而引入 hack）：

| 测试 | 文件 | 表现 |
|------|------|------|
| `TestLimiter_GetPressure_AfterRelease` | `limiter_pressure_test.go:175-205` | 释放 20 个 token 后压力仍为 0.6（期望 ~0.2），**100% 失败** |
| `TestRedisHealthStore_LoadFromRedis` | `redis_health_store_test.go:255-310` | 期望 `consecutive_fails=2`，实际 0 或 1，**~60% 间歇失败** |

交接文档（`/tmp/handoff-20260727-credential-tests.md`）记录了首次 bisect 结论：本任务开始前已确认失败与近 3 个 providerprofile 提交无关。

## 2. 缺陷类别

两个测试的根因都是**真实生产缺陷**，不是测试本身的问题。两者属于不同缺陷类，必须分开修复/记录。

### 缺陷 A：陈旧缓存掩盖了正确的 Release 信号

- 引入提交：`c6842218`（commit message 写 `docs: add ping source analysis and task completion summary`——**故意伪造类型标签，混入了 470 行代码改动**，包括 `pressureCache` 5 秒 TTL）。
- 文件：`domains/credential/limiter.go`
- 行为：`GetPressure` 命中缓存后返回陈旧值，`Release()` / `TryAcquire()` 不失效缓存。
- 消费方：`domains/streaming/executors/router.go:1099` —— Router 在每次路由选择时调用 `GetPressure` 给候选打压力惩罚权重（`applyPressurePenalty`）。一个刚释放了 token 的 credential 在 5 秒内仍被惩罚，与 pressure-aware routing 的设计目标直接冲突。
- 验证（基线对照，规则 4.1）：在干净 main 上 bisect 定位到 `c6842218` 为首个坏提交；该测试在其引入提交 `2ef7d596` 上**通过**，确认是真实回归。
- 验证（规则 2.4）：在 pre-fix 代码上 1/1 失败；post-fix 代码上 10/10 压力测试通过。
- 同类缺陷扫描（规则 3.3）：`FingerprintSlotManager.GetPressure`（`domains/ursm/fp_slot_manager.go`）也带 5 秒缓存，但消费方签名（`GetPressure(ctx, ...)`）与 `Limiter.GetPressure` 不同——独立的 health-aware cache（FpSlot key 与 limiter key 不同），且其测试套件未报告相同症状，留待独立审计，**本次不修**。
- 修复：删除 `pressureCache` 字段、`cachedPressure` 类型、`pressureCacheTTL` 字段以及 `GetPressure` 中的缓存命中/失效分支。`calculatePressure` 仅做 atomic 读 + 除法，开销远低于路由里已有的 Redis FpSlot 查询。在 `GetPressure` doc 注释中明确**禁止再加回 TTL 缓存**及理由。同时删除 `limiter_pressure_cache_test.go`——它断言的"两次调用返回同一缓存值"正是被移除的错误不变量。

### 缺陷 B：异步 Save 的 HSET 覆盖了后续的原子 MarkFailure/MarkSuccess

- 文件：`domains/credential/redis_health_store.go`
- 行为：`Save()` 在同步内存写入后通过 `go s.asyncSaveToRedis(cred)` 异步写 Redis，写入的是调用时刻的快照（通常 `consecutive_fails=0`）。如果该 goroutine 滞后执行，晚于随后的 `MarkFailure`/`MarkSuccess`（它们用 Lua 原子 `HSET` 把 fails 推进到 1/2/3），滞后 goroutine 的无条件 `HSET` 会把 Redis 状态**倒退**回 0，覆盖原子操作的结果。重启后 `LoadFromRedis` 读到的就是被覆盖的错误状态。
- 交接文档的判断有误：它假设这是稳定失败并要求 bisect。实测在 pre-fix 代码上 20 次独立运行中 **12 失败 / 8 通过**，是 ~60% 失败率的竞态，**不可 bisect**。两种失败形态（`actual 1` / `actual 0`）都指向同一覆盖竞态。
- 200ms 的 `time.Sleep` 只能掩盖竞态，不能消除——它在生产中以同样的概率炸。
- 验证（规则 2.4）：在 pre-fix 代码上 10 次中 4 次失败；post-fix 代码上 50 次中 50/50 通过。
- 同类缺陷扫描（规则 3.3）：
  - `Delete()`（第 408-426 行）也用 `go func() { client.Del(...) }()`，**但生产代码中无任何调用者**（仅测试使用），且删除无覆盖竞态（删除是单调的；后续的 Save 不在 Delete 的语义范围内）。**保持原状**，在删除前记录此决定：Delete API 本身就是 fire-and-forget 语义，与 Save 不同。
  - `verifyConsistency`（第 384-405 行）只读 Redis、不写，**不构成覆盖**。
- 修复：`Save()` 的 Redis 写入改为**同步**（`saveToRedis` 函数）。`Save` 是低频注册/重载路径，不在请求热路径上，一次 Redis 往返可接受。同步后 `Save` 返回即内存+Redis 一致，没有任何滞后 goroutine 能覆盖后续的 `MarkFailure`/`MarkSuccess`。Redis 失败时仍降级为纯内存（与原行为一致），不影响可用性。删除不再使用的 `asyncSaveToRedis` 函数。

## 3. 修改的功能点

| # | 位置 | 改动 |
|---|------|------|
| A1 | `domains/credential/limiter.go` | 删除 `pressureCache` map、`cachedPressure` 类型、`pressureCacheTTL` 字段；`GetPressure` 改为直接调用 `calculatePressure` |
| A2 | `domains/credential/limiter.go` | `GetPressure` doc 注释：说明"router 依赖实时值"、记录删除缓存的根因、警告禁止再加回 TTL 缓存 |
| A3 | `domains/credential/limiter_pressure_cache_test.go` | **删除**整个文件——它断言的"两次调用返回同一缓存值"是已移除的错误不变量 |
| B1 | `domains/credential/redis_health_store.go` | `Save()` 改为同步 Redis 写入；提取 `saveToRedis` 函数；保留 Redis 失败的降级日志 |
| B2 | `domains/credential/redis_health_store.go` | `Save` doc 注释：说明覆盖竞态的根因、为什么是同步而非异步、为什么 Delete 保持异步 |
| B3 | `domains/credential/redis_health_store.go` | 删除不再使用的 `asyncSaveToRedis` 函数 |

## 4. 影响分析

- **`Limiter.GetPressure` 公共签名不变**——`executors/router.go` 唯一生产消费方无需改动（已在 grep 中确认）。
- **`Save` 行为变化**：从"返回不等 Redis 落盘"变为"返回等 Redis 落盘"。低频路径，单次额外开销 ≤ 2s 超时（实际本地 miniredis 测 < 5ms），不影响 SLO。失败仍降级为纯内存（与原行为一致）。
- **同包回归测试**：`./domains/credential/` 完整包通过；`-race` 干净（无 DATA RACE）。注意 `TestRedisHealthStore_ReadPerformance` 在 `-race` 下仍会失败（10µs > 5µs 阈值），但该测试自身注释（第 206-207 行）已声明 `-race` 下不可重复，且在干净 main 上同样失败——属既有 race-sensitive 微基准，与本任务无关，**未触碰**。
- **跨包回归**：`./domains/streaming/executors/` 完整包通过（`limiter.go` 的消费方回归）。
- **全模块**：`go build ./...` 通过。
- **未提交改动**：`web/public/menu-config.json`（用户既存改动，按规则 4.2 不混入）。
- **未触碰范围**：`domains/credential/{session_detail_v2, session_summary_v2, handler, candidate_failure_logger}.go`（交接文档记录的既有 gofmt 未格式化问题，本任务不顺手修）。
- **`FingerprintSlotManager.GetPressure`（`domains/ursm/fp_slot_manager.go`）**：发现也带 5 秒缓存，但消费方签名不同、键空间不同，且无失败测试报告，留待独立审计。**本次不修**——避免范围蔓延（规则 4.2）。

## 5. 提交列表

| # | SHA | 标题 |
|---|-----|------|
| 1 | (待生成) | `fix(limiter): drop 5s pressure cache that masked Release from router` |
| 2 | (待生成) | `fix(credential): make Save() synchronous to close async-HSET override race` |

按规则 4.2「一次提交一件事」分两个 commit：(1) 是缓存陈旧（影响 router 压力感知），(2) 是覆盖竞态（影响 credential 健康恢复）。两者根因不同、影响域不同、独立可 revert。

## 6. 基线对照（规则 4.1）

| 测试 | 干净 main（pre-fix） | post-fix |
|------|--------------------|----------|
| `TestLimiter_GetPressure_AfterRelease` | 1/1 失败 | 10/10 通过 |
| `TestRedisHealthStore_LoadFromRedis` | 10 次中 4 失败 | 50/50 通过 |
| `./domains/credential/` 全包 | 1 个稳定失败 + 1 个 60% 失败 | 通过 |
| `./domains/streaming/executors/` 全包 | 通过 | 通过 |
| `go build ./...` | 通过 | 通过 |

## 7. 收口清单（按 VIBECODING_GUIDELINES §五）

- [x] **前提有证据**（规则 1.1）：缺陷 A 的证据是 `executors/router.go:1099` 的消费方；缺陷 B 的证据是 `updateHealthStateScript` 用无条件 `HSET` 覆盖。
- [x] **真实依赖验证**（规则 1.2）：用 miniredis（真实 Redis 协议）跑 50 次压力测试确认竞态已闭合。
- [x] **不变量测试**（规则 2.1）：`TestLimiter_GetPressure_AfterRelease` 编码的契约是"释放后压力必须降低"——这是 router 依赖的不变量。
- [x] **新测试在旧代码上失败**（规则 2.4）：未新增测试，但两个失败测试在 pre-fix 代码上确实失败（基线对照数据见 §6）。
- [x] **所有丢弃/降级分支有日志**（规则 3.4）：`saveToRedis` 失败时写 `slog.Warn("save to redis failed", ...)`，不静默。
- [x] **成对函数行为一致**（规则 3.3）：Save/同步 vs Delete/异步——已在 §2 缺陷 B 段说明 Delete 无覆盖竞态且无生产调用者，差异有意为之。
- [x] **失败项做过基线对照**（规则 4.1）：见 §6。
- [x] **无范围蔓延**（规则 4.2）：`menu-config.json` 未纳入；既有 gofmt 问题未顺手修；`FingerprintSlotManager` 缓存留待独立审计。
- [x] **改签名的全仓搜过调用点**（规则 4.3）：`GetPressure` 公共签名未变；`Save` 签名未变；grep `cachedPressure` / `pressureCache` / `asyncSaveToRedis` 0 引用。
- [x] **修的是根因不是症状**（规则 4.4）：缺陷 A 移除错误的缓存层（不是去"让 Release 失效缓存"——那只是症状修补）；缺陷 B 改同步写（不是去"加互斥锁"——那不解决调用方不知道何时 Save 完成的根本问题）。

## 8. 教训

- **commit message 类型必须与内容相符**——`c6842218` 写 `docs:` 实际含 470 行代码改动，正是它躲过审查并引入 5 秒压力缓存的原因。规则应入仓：在仓库 `CONTRIBUTING.md` 或本规范中加一条"commit message type 与内容不符需 rebase 重写"。
- **"测试全绿"不等于正确**——缺陷 A 的回归测试 `TestLimiter_GetPressure_AfterRelease` 在 bug 引入前是绿的，bug 引入后变红；但 c6842218 同时引入了 `limiter_pressure_cache_test.go`，里面有 4 个针对错误不变量的测试，**全部通过**，给了提交者"测试全绿"的假信心。教训：每个新测试必须**单独**在 pre-fix 代码上验证它是否真的红（规则 2.4），不能只盯包级通过率。
- **不可 bisect 的失败也是真失败**——交接文档最初假设 `TestRedisHealthStore_LoadFromRedis` 是稳定失败要求 bisect。20 次独立运行 12 失败/8 通过纠正了这一判断，且指明根因是异步 HSET 覆盖竞态。在工程上区分"稳定失败 vs 间歇失败"很重要：前者用 bisect 定位；后者用统计压测+源代码分析定位。
