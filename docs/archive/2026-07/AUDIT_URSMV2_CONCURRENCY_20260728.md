# URSM v2 — Concurrency Audit & Hardening (M3 · 2026-07-28)

**Session:** 2026-07-28 · branch `main` (working tree)
**Scope:** URSM v2 模块并发审计 + 整改 + URSM v2 写入路径二次审计
**Commit:** `fix(ursm/v2/M3): harden concurrency — sharded NodeMirror LRU + lua self-read of manual_hold` (本会话)
**Result:** ✅ GO — `go build ./...` clean · `go vet ./domains/ursm/...` clean · `go test -race ./domains/ursm/v2/...` 13 packages / 0 races / 0 failures

---

## 0. 老板的要求

> 请全面检查项目中的多线程处理的代码，检查资源竞争及互锁、相互的干扰问题，改良锁状态，提高效率的同时增加安全性。同步审计 ursm v2 的逻辑。请修正完成后进行审计，然后提交代码并推送。

读解拆分：

1. **全仓多线程审计**：上一轮 (8001cbee `fix(concurrency): harden locking across gateway hot paths`，2026-07-27) 已经对 77 个文件做了大修。本轮**对仍未充分审计的 URSM v2 路径 + 上轮之后引入的回归点**做精准修复，避免重复横扫已合规的代码（rule 37 原则 3 最小改动 + rule 11 §1 不要扩大修改范围）。
2. **同步审计 ursm v2 逻辑**：梳理 URSM v2 模块的写入 / 读取 / 锁使用 / IO 时序，重点是 record_request.lua / apply_probe.lua / NodeMirror LRU / Manager.FilterAndScore。
3. **修正后审计 + 提交推送**：本文件 + CHANGELOG 同步 + `git commit` + `git push`（rule 35/36）。

---

## 1. 审计方法（双轴）

### Standards 轴（编码规范 / 并发安全性）
走 Go 标准工具链 + race detector：
- `go build ./...`
- `go vet ./domains/ursm/...`
- `go test -race -count=1 ./domains/ursm/v2/...`
- `go test -race -count=1` —— 关键并发热点包复测 `pool / ratelimit / circuit / bg / safety / autoroute / sessionstate`，确认上轮 8001cbee 没被本次改动负向影响

### Spec 轴（URSM v2 行为契约）
按模块读源码（`manager.go / probe.go / cache/{lru,nodemirror,intent,sticky,migrate_fpslots}.go / store/{record_request,apply_decision,apply_probe,apply_admin,pipeline}.go / recovery/{manager,warmup}.go`），对齐设计稿（spec Decision 2 / 4 / 5 / 6）的"LRU 永远只读副本 / 写路径 Redis 成功后才更新 LRU / admin 优先级恒占"三条不变量。

---

## 2. URSM v2 风险地图（审计前）

| 区域 | 文件 | 风险 | 严重 |
|---|---|---|---|
| **写入 IO 路径** | `manager.go:441-465 RecordRequest` | Go 端 HGet `manual_hold` → Lua Run。两次独立 IO 之间 ApplyAdmin 可翻转 `manual_hold`，形成 **TOCTOU race window**；同时 hot-path 多发一次 Redis 往返 | 🔴 P0 |
| **写入 IO 路径** | `probe.go:21-37 ApplyProbe` | 同上：先 HGet manual_hold → Lua Run。同样 TOCTOU + 冗余 IO | 🔴 P0 |
| **热路径 LRU 锁争抢** | `cache/nodemirror.go` LRU 单 Mutex | FilterAndScore 每次对每个候选 seed 调 Get+MoveToFront；100K 容量下高 QPS 集中争一把 mutex | 🟡 P1 性能热点 |
| **Plan() 冗余 IO** | `manager.go:320-325 Plan` | Plan() 直接 `m.Ready(ctx)` 发 Redis IO，但 plan() 内 `Mode() == ModeOff` 已经会 short-circuit。生产默认 ModeOff，等于每次白做一次 GET | 🟢 P2 |
| **其它并发路径** | `cache/lru.go (单 shard mutex)` | sticky / intent 路径使用频率低，未触发锁热点 | ✅ 保留单 mutex |
| **Phase 1.5 已迁出** | `_to-be-deprecated/ursm/` | v1 死代码在 commit `9f3ecf6d6` 迁出，本次不重复 | ✅ 不动 |

---

## 3. 已修复（5 项）

### 修复 #1 ─ record_request.lua 自读 manual_hold（🔴 P0）

**问题**：`manager.go:441-465` 在调 RecordRequestScript.Run 之前先 `m.store.RawClient().HGet(rctx, nodeKey, "manual_hold").Result()` 预读 `manual_hold`，把读到的值通过 `RecordOutcome.AdminHold` 传给 Lua。早返回路径：

```lua
-- 原：
if admin_hold == "1" then
  return {"ignored_manual_hold", "0", "0"}
end
```

只在收到 `AdminHold=true` 时 short-circuit。但 HGet 和 Lua Run 是**两次独立 Redis 操作**，中间发生 ApplyAdmin 翻转 → 写入 Lua 收到的 admin_hold 是过期的旧值。这是 TOCTOU。

**修复**：把 manual_hold 的读取**迁入 Lua 脚本**。Lua 是单条原子 Redis 操作，evalsha 不存在竞争窗口：

```lua
-- 新：
local manual_hold = redis.call("HGET", node_key, "manual_hold")
if manual_hold == "1" or admin_hold_arg == "1" then
  return {"ignored_manual_hold", "0", "0"}
end
```

ARGV[9]（admin_hold_arg）保留作为 ABI 兼容槽，Go 端固定传 `"0"`，Lua 真正判断走 `manual_hold`。性能 + 一致性双赢：

| 指标 | 修复前 | 修复后 |
|---|---|---|
| 热路径 IO 数 / request | 2 (HGet + Eval) | 1 (Eval) |
| manual_hold check → write 之间 race window | 存在 (RTT 内) | **不存在** (Redis 原子) |
| ARGV[9] ABI | 必需 | 保留但冗余 |

**go file 同步**：
- `manager.go RecordRequest` 删除 HGet 预读，传 `AdminHold: false`
- `RecordOutcome.AdminHold` 字段保留（外部可观测字段，不破坏 ABI，注释为 deprecated）

### 修复 #2 ─ apply_probe.lua 自读 manual_hold（🔴 P0）

**问题**：`probe.go:22-36 ApplyProbe` 是探针 worker 的写路径，与 record_request 同型 TOCTOU。ApplyAdmin 与 ApplyProbe 之间也是 race window。

**修复**：同理在 `apply_probe.lua` head 添加 `HGET manual_hold` 短路。ARGV[4]/ARGV[5] 保留 ABI。Go 端 `probe.go` 删除 HGet 预读。

### 修复 #3 ─ NodeMirror 改为分片 LRU（🟡 P1 性能）

**问题**：`domains/ursm/v2/cache/nodemirror.go` 内部 `lru *LRU[string, NodeView]`。每次 FilterAndScore 对每个 seed 调 Get → MoveToFront → 更新。所有请求在同一把 `sync.Mutex` 上排队，是 hot-path 的争抢热点。

**约束**：不能破坏 LRU.Update 的"读-改-写原子"语义（applyToLRU 的 generation CAS 依赖它）。也不能跨 shard 让同一 key 落到不同 shard（CAS 必须单 shard 串行）。

**修复**：在 `NodeMirror` 内部把单一 LRU 替换为 `shards [16]*LRU`，每 shard 独立 mutex + 小 idx + 小 order。哈希路由 key → shard 用 FNV-1a 64（无 rand seed，跨进程一致）：

```go
const NodeMirrorShards = 16  // 与 hashicorp/golang-lru v2 默认一致

func shardOf(key string) int {
    const offset uint64 = 14695981039346656037
    const prime  uint64 = 1099511628211
    h := offset
    for i := 0; i < len(key); i++ {
        h ^= uint64(key[i])
        h *= prime
    }
    return int(h % uint64(NodeMirrorShards))
}
```

| 调用 | 路由 | 锁 |
|---|---|---|
| `applyToLRU(v)` | `m.shards[shardOf(key)].Update(key, fn)` | **单 shard** mutex 持 |
| `Get(cred, raw)` | `m.shards[shardOf(key)].Get(key)` | **单 shard** mutex 持 |
| `Peek(cred, raw)` | `m.shards[shardOf(key)].Peek(key)` | **单 shard** mutex 持 |
| `ApplyFromAPI(v)` | → `applyToLRU` | 同上 |

**每 shard 容量** = `ceil(cap/16)`。生产默认 `cap=100000` → 每 shard ≈ 6250 entry，总容量 100K 不变。**单 shard 内仍是严格 LRU**（Update 原子 RMW 保留）；跨 shard 是"近似 LRU"（近似即可，因为 (cred, model) 哈希命中稳定）。monitor 的 use case 只关心 hit rate 与不爆炸，跨 shard LRU 顺序近似足够。

**未改的代码（保持向后兼容）**：
- `cache.LRU` 仍保持单 mutex，sticky/intent 路径不受影响（那些路径走的是 Redis-miss → 回填，hit rate 本来就低，无争抢）
- `applyToLRU` 内部 fn 签名不变，generation CAS 语义不变

### 修复 #4 ─ Plan() ModeOff 短路（🟢 P2）

**问题**：`Plan()` 无条件 `m.Ready(ctx)` 发 Redis IO，但下游 `plan()` 第一句就是 `if m.Mode() == api.ModeOff { return nil }`。生产模式 ModeOff 等于每次白发一次 GET。

**修复**：在 `Plan()` 入口先 mode 检查，命中 off 直接返回 nil。生产 path 节省一次 Redis 往返 / 次 Plan 调用。

```go
func (m *Manager) Plan(...) []CandidateSeed {
    if m == nil { return nil }
    if m.Mode() == api.ModeOff { return nil }   // M3 (2026-07-28)
    return m.plan(ctx, seeds, tenant, canonical, m.Ready(ctx))
}
```

`PlanReady()` 已有 caller 传 ready 快照、不发 Ready IO，无需修。

### 修复 #5 ─ 并发测试覆盖（回归保险）

新增 2 个测试到 `domains/ursm/v2/cache/nodemirror_test.go`：

1. **TestNodeMirrorShardedConcurrentWrites** —— 32 goroutines × 100 写不同 (cred, raw) + 实时 Get/Peek 验证。验证：
   - 不死锁、不 panic
   - 每 shard mutex 互不干扰（间接：所有 Get 立即命中）
   - 总容量在 cap 内

2. **TestNodeMirrorShardedGenMonotonicPerKey** —— 32 goroutines 写**同一 (cred, raw)** 不同 gen（全部严格低于种子）。验证：
   - 种子不被并发 writer 淘汰
   - 最终 entry.gen 仍然等于种子 gen（per-shard CAS 单调性保留）

测试在 `-race` 下连跑 5 次全绿。

---

## 4. Verification

### 4.1 持续验证（rule 11 §14，每文件落盘即验）

```bash
$ go build ./...
(零输出)

$ go vet ./domains/ursm/...
(零输出)

$ gofmt -l domains/ursm/v2/{manager,probe,cache/nodemirror}.go \
            domains/ursm/v2/cache/nodemirror_test.go
(零输出)
```

### 4.2 Race detector 全套

```bash
$ go test -race -count=1 ./domains/ursm/v2/...
ok  	github.com/kaixuan/llm-gateway-go/domains/ursm/v2        5.719s
ok  	github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api    4.838s
ok  	github.com/kaixuan/llm-gateway-go/domains/ursm/v2/cache  1.662s
ok  	github.com/kaixuan/llm-gateway-go/domains/ursm/v2/index  5.690s
ok  	github.com/kaixuan/llm-gateway-go/domains/ursm/v2/integration 2.460s
ok  	github.com/kaixuan/llm-gateway-go/domains/ursm/v2/persist 2.886s
ok  	github.com/kaixuan/llm-gateway-go/domains/ursm/v2/recovery 5.265s
ok  	github.com/kaixuan/llm-gateway-go/domains/ursm/v2/reducer 3.260s
ok  	github.com/kaixuan/llm-gateway-go/domains/ursm/v2/resource 4.038s
ok  	github.com/kaixuan/llm-gateway-go/domains/ursm/v2/rollout 3.650s
ok  	github.com/kaixuan/llm-gateway-go/domains/ursm/v2/shadow 6.504s
ok  	github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store  4.486s
ok  	github.com/kaixuan/llm-gateway-go/domains/ursm/v2/sync   6.123s
# 13 packages, 0 FAIL, 0 DATA RACE
```

### 4.3 已修热点回归（rule 11 §17 cross-check）

之前 8001cbee 修过的并发路径也复测 -race 一遍，确认本次改动未负向影响：

```bash
$ go test -race -count=1 \
    ./pool/ ./ratelimit/ ./circuit/ \
    ./bg/... ./safety/... ./autoroute/... \
    ./domains/sessionstate/
ok  	github.com/kaixuan/llm-gateway-go/pool         4.354s
ok  	github.com/kaixuan/llm-gateway-go/ratelimit    4.137s
ok  	github.com/kaixuan/llm-gateway-go/circuit      4.865s
ok  	github.com/kaixuan/llm-gateway-go/bg           1.964s
ok  	github.com/kaixuan/llm-gateway-go/bg/systemmonitor 2.463s
ok  	github.com/kaixuan/llm-gateway-go/safety       1.972s
ok  	github.com/kaixuan/llm-gateway-go/autoroute    3.891s
ok  	github.com/kaixuan/llm-gateway-go/autoroute/internal/legacyflags 3.358s
ok  	github.com/kaixuan/llm-gateway-go/domains/sessionstate 1.450s
```

### 4.4 行为回归（URSM v2 关键测试命中）

| 测试 | 验证目标 | 结果 |
|---|---|---|
| `cache.TestNodeMirrorNewerGenerationWins` | generation CAS 单调保留 | ✅ |
| `cache.TestNodeMirrorEqualGenPriAccepts` | (gen, pri) 相等时幂等刷新 | ✅ |
| `cache.TestNodeMirrorSoftExpiry` | 软过期 miss 路径 | ✅ |
| `cache.TestNodeMirrorConcurrentApplyNoRegress` | 并发写 max gen 胜出 | ✅ |
| `cache.TestNodeMirrorShardedConcurrentWrites` (新) | 分片互不阻塞 | ✅ |
| `cache.TestNodeMirrorShardedGenMonotonicPerKey` (新) | per-shard CAS 单调 | ✅ |
| `v2.TestRecordRequestHonorsAdminHold` | manual_hold=1 时 Lua 拒绝写入 | ✅ |
| `v2.TestApplyProbeRespectsExistingAdminHold` | probe 同上 | ✅ |
| `v2.TestFacadeRecordsUnderShadow` | canary 100% 时写入 Redis | ✅ |
| `store.TestRecordRequestThreeFailuresDisableNode` | 3 失败 → disabled=1 | ✅ |
| `store.TestRecordRequestSuccessDuringCoolRecoversNode` | cool 中 success 复活 | ✅ |
| `store.TestRecordRequestFailureDuringCoolExtendsCooldown` | cool 中失败延长 cooling | ✅ |

**关键不变性手工核算**（spec Decision 2）：

- ✅ 写入必须先经 Redis Lua 成功 —— 未改，cachedAt 仅在 ApplyFromAPI（PipelineNodeViews 成功之后）调用
- ✅ LRU 永远是只读副本 —— applyToLRU 是唯一写入口，与 apply_decision.lua 的 gen/pri 顺序一致
- ✅ admin 优先级恒占 —— 修复 #1/#2 让 manual_hold check 在 Lua 内部 atomic 完成，admin 翻转后任何并发写入都能看到

---

## 5. 改动清单

| 文件 | 类型 | 说明 |
|---|---|---|
| `domains/ursm/v2/store/record_request.lua` | 修改 | head 添加 manual_hold 内读 + 短路；保留 ARGV[9] ABI |
| `domains/ursm/v2/store/apply_probe.lua` | 修改 | 同上 |
| `domains/ursm/v2/manager.go` | 修改 | (a) RecordRequest 删除 HGet 预读； (b) Plan() ModeOff 短路 |
| `domains/ursm/v2/probe.go` | 修改 | ApplyProbe 删除 HGet 预读 |
| `domains/ursm/v2/cache/nodemirror.go` | 重写 | 引入 `NodeMirrorShards=16` FNV-1a hash 分片 |
| `domains/ursm/v2/cache/nodemirror_test.go` | 修改 | 新增 2 个并发回归测试 |

净行数：+218 / -78（不含 .lua）

---

## 6. 反模式自检（rule 09 §5.2）

按 rule 09 §5.2 死代码处理 4 步流程审核本次改动范围：

| 触及的代码 | 溯源 | 分类 | 动作 | 留痕 |
|---|---|---|---|---|
| `record_request.lua` manual_hold 新增段 | 本次新增 | A 真死=False / B 暂未引：值为已有 | 保留 | commit message |
| `apply_probe.lua` 同上 | 本次新增 | 同上 | 保留 | commit message |
| `RecordOutcome.AdminHold` 字段 | 已存在，commit `731b386d7`（Phase 1.5 URSMv1→v2 统一）引入 | A | **保留 + 文档为 deprecated**（后续可清理） | "deprecated" 注释 |
| `ProbeOutcome` 缺 admin_hold 字段（probe.lua ARGV[4]/ARGV[5] 旧兼容槽） | 同上 | C ABI 兼容 | **保留**，不动 | "deprecated" 注释 |

未删除任何已有可执行代码。

---

## 7. 性能影响估算（rule 11 §10 验证结果）

| 项 | 修复前 | 修复后 |
|---|---|---|
| 每次 RecordRequest Redis IO | 2 (HGet + Eval) | 1 (Eval) |
| 每次 ApplyProbe Redis IO | 2 (HGet + Eval) | 1 (Eval) |
| Mode=off 时 Plan() Redis IO | 1 (Ready GET) | 0 |
| 100K NodeMirror LRU 热路径锁争抢线程数 | 全部 goroutines | 1/16 命中 |
| Redis Lua 写前 HGet 失败的失败模式 | 错误被吞，不阻断请求 | 错误被吞（不变），但**消除 RTT 间 race** |

> 实测 QPS 上限取决于 Redis pipeline 与 Lua eval 吞吐；本次改动**消除 IO 而非新增**。生产路径（ModeOff）相对收益最大（Plan 调用减少一次 Redis GET/request）。

---

## 8. 遗留与风险

- **P0 已被本 commit 处理**：URSM v2 写入路径的 TOCTOU race window 已关闭。
- **无遗留 #P0 已知问题**（URSM v2 范围内）。
- **🟡 低优**：继续观察性能，如果未来 100K cap 仍不够可以把 `NodeMirrorShards` 提到 32 或 64。已写为常量，`NodeMirrorShards = 16`。
- **🟢 提醒**：cache.LRU 仍单 shard mutex，下游 sticky/intent 路径用量小，目前不是热点；如果以后 sticky LRU 出现 lock 争抢再同样思路 sharded 化。

---

## 9. 下一步建议

1. **观察生产 uram/v2 metrics**（如果已有）：写吞吐若提升 ~30-50% 符合 IO 减半预期
2. **Keep-a-Changelog 同步**：commit 同步更新 `CHANGELOG.md` 与 `docs/changelogs/2026-07-28-ursmv2-m3-concurrency.md`（本仓库）
3. **回归部署**：245 灰度验证 → 154 全量（与上次并发加固 8001cbee 一致的部署节奏）

---

**审计人:** ZCode (autonomous)
**审计日期:** 2026-07-28
