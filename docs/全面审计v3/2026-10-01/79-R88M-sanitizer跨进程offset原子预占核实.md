# 79 号报告：R88-m sanitizer 跨进程 offset 原子预占 —— 核实通过，无缺陷

- 日期：2026-10-01
- 轮次：R88-m
- 变更面：**纯文档**，零代码改动
- 来源：objective 明确点名「sanitizer 跨进程 offset 原子预占」，
  且正落在用户自述专长（并发/多线程）上，本轮专项核实

---

## 0. 结论先说

**实现正确，且测试覆盖到位。本轮未发现缺陷。**

objective 里的三条表述里，这一条是**唯一经逐段核实为「已正确实现」**的。
本报告的价值是**留证**：把机制和它为什么正确写清楚，
使后续审计不必重新追一遍，也使将来改它的人知道不能碰哪几行。

---

## 1. 机制：三段式，每段都有真实的原子性保证

| 段 | 位置 | 手段 | 为什么是真原子 |
|---|---|---|---|
| **① 预占** | `smart_sani_guard.go:343-367` | `SetNX(key, tok, leaseTTL)`，`tok` 是 **8 字节随机数**的 hex（`:343-347`），TTL 15s（`:50` `defaultOffsetLeaseTTL`） | Redis `SET NX` 单条命令原子 ⇒ **只有一个 worker 能拿到 lease** |
| **② 续租** | `smart_sani_guard.go:366` | `go m.renewOffsetsLease(ctx, lease, leaseTTL)` 后台 goroutine | 防止慢 detector 跑到一半租约过期被别人接管 |
| **③ 提交** | `smart_sani_guard.go:470-512` `commitSanitizeMapScript`（**Redis Lua**） | 脚本**第一行**就是 `if redis.call('GET', KEYS[1]) ~= ARGV[1] then return redis.error_reply('sanitize offsets lease lost') end` | **租约校验与写入在同一次 Lua 执行内** ⇒ 不存在「先校验后被抢」的 TOCTOU |

### ③ 脚本还额外挡住三类破坏

- **键类型守卫**（`:474-481`）：map/offsets 键必须是 `none` 或 `hash`，否则报错
  —— 防止有人把同名键写成 string/list 后脚本静默写坏；
- **占位符不可覆盖**（`:484-491`）：`HEXISTS` 命中即报错
  ⇒ **一个占位符值永不被后来者覆写**（脱敏映射的完整性）；
- **map 与 offsets 同生共死**（`:493-506`）：两个 hash 在同一脚本里写、同一条 `PEXPIRE`
  ⇒ **不会出现「map 写了但 offset 没写」的不一致中间态**。

---

## 2. 最容易做错、而这里做对的一点

朴素实现会是：Go 侧 `GET` 租约 → 检查 → Go 侧 `HSET` 写数据 → `DEL` 租约。
**这在租约过期 + 新 owner 接管时会写坏别人的数据。**

本实现把**检查和写入都放进 Lua**，让 Redis 单线程执行整个脚本，
从根上消除了这个窗口。`smart_sani_guard.go:276-277` 的注释自陈了这个意图：
「The Redis lock spans load, allocation, and an atomic, lease-checked map+offset commit」。

---

## 3. 测试覆盖（`offset_lock_test.go`，6 道）

| 用例 | 守住什么 |
|---|---|
| `TestSanitizeOffsetsAcrossMiddlewareInstances` | 跨 middleware 实例的 offset 不撞号 |
| `TestSanitizeOffsetsBusyOrRedisFaultStopsDispatch` | 抢不到租约或 Redis 故障 ⇒ **停止转发**，不放行未脱敏请求 |
| `TestSanitizeOffsetsSlowDetectorRenewsLease` | 慢 detector 不会丢租约 |
| `TestSanitizeOffsetsExpiredLeaseCannotCommit` | 租约过期后提交被拒 |
| `TestSanitizeOffsetsExpiredWorkerCannotOverwriteNewOwner` | **过期 worker 覆盖不了新 owner 的 map**（正是第 2 节那个窗口） |
| `TestSanitizeOffsetsNoRedisKeepsRequestLocalMapping` | 无 Redis 时降级为请求内映射，不静默丢数据 |

关键设计：测试用 **miniredis** 起真 Redis，`offsetLeaseTTL` **可缩短**
（结构体注释 `:83-85` 明写「Tests can shorten it」）⇒ 租约过期路径**可测**，
不是靠注释声称。

---

## 4. 实跑结果

```
go test ./security/sanitize/ -count=1              → ok  3.678s
go test -race ./security/sanitize/ -count=1        → ok  7.134s
```

**`-race` 也干净** —— 这对一个**并发**组件是必要的第二道证据
（`smart_sani_guard.go` 单文件 63KB、`offset_lock_test.go` 有 `cross_tenant_race_test.go`
配套的跨租户竞态测试）。

---

## 5. 本轮未做

- 未审 `smart_sani_guard.go` 其余 63KB 逻辑（占位符生成、restore 侧、
  SSE 分片续传等）—— 本轮**只审 offset 预占这一条链路**，不做整包审计。
- 未核实 objective 同一句里的另两项（`SanitizedMessageRefs` /
  `AlignmentMap` 接入 V2 metadata），**登记为后续项**。
- 未做真库/多进程实证（Lua 原子性依赖 Redis 单线程执行模型，
  本轮是**结构性核实**不是运行期多进程实测）。
