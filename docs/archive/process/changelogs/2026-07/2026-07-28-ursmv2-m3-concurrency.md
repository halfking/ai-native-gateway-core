# URSM v2 M3 并发加固（2026-07-28）

**Session:** 2026-07-28 · branch `main`
**Title:** `fix(ursm/v2/M3): harden concurrency — sharded NodeMirror LRU + lua self-read of manual_hold`
**Result:** ✅ GO — 5 改动文件 + 1 改动测试；`go test -race ./domains/ursm/v2/...` 13 包 / 0 race / 0 failure

## 改动背景

老板要求"全面检查项目多线程代码 + 同步审计 ursm v2"。上一轮 (commit `8001cbee9`，2026-07-27) 已经对 77 个文件做了大范围加固，本轮聚焦 URSM v2 模块的写入路径与 NodeMirror 锁热点，避免重复横扫已合规的代码（rule 37 原则 3 + rule 11 §1）。

## 5 个修复点

### 1. `record_request.lua` 自读 manual_hold（🔴 P0）

**Bug**: 原 `manager.go:441-465 RecordRequest` 在 `RecordRequestScript.Run` 之前先 `HGet manual_hold`，再把读到的值通过 `RecordOutcome.AdminHold` 传给 Lua。HGet + Lua Run 是两次独立 Redis 操作，**ApplyAdmin 在这两步之间翻转 `manual_hold` 会让 Lua 收到过期的旧值**，继续写。这正是 spec Decision 2 的 TOCTOU race window。

**Fix**: Lua 脚本内部 `HGET manual_hold` 并短路。Lua 是单条原子 Redis 操作，不存在 race window。同时节省一次 hot-path RTT。

| 指标 | 修复前 | 修复后 |
|---|---|---|
| 热路径 IO 数 / request | 2 (HGet + Eval) | 1 (Eval) |
| `manual_hold` check → write race window | 存在 (RTT 内) | **不存在** (Redis 原子) |

`manager.go RecordRequest` 同步删除预读 HGet，传 `AdminHold: false`；`RecordOutcome.AdminHold` 字段保留为 deprecated（ABI 兼容）。

### 2. `apply_probe.lua` 自读 manual_hold（🔴 P0）

**Bug**: 与修复 #1 同型。`probe.go:21-37 ApplyProbe` 在 `ApplyProbeScript.Run` 前 HGet `manual_hold`，再传 ARGV[5] `current_admin_hold`。ApplyAdmin 与 ApplyProbe 之间也存在 race window。

**Fix**: 同理，Lua 内 `HGET manual_hold` 短路。`probe.go` 删除预读，ARGV[4]/ARGV[5] 保留为 deprecated。

测试 `v2.TestApplyProbeRespectsExistingAdminHold` 仍然通过（预设 manual_hold=1，新 Lua 立即短路返回 `{"ignored_manual_hold"}`）。

### 3. NodeMirror 改为分片 LRU（🟡 P1 性能）

**问题**: `domains/ursm/v2/cache/nodemirror.go` 内部 `lru *LRU[string, NodeView]` 单 Mutex。FilterAndScore 每次对每个候选 seed 调 Get+MoveToFront。所有请求在同一把 mutex 上排队，是 URSM v2 的锁争抢热点。

**Fix**: NodeMirror 内部把单一 LRU 替换为 `shards [16]*LRU`，每 shard 独立 mutex + 小 idx + 小 order。哈希路由 key → shard 用 **FNV-1a 64**（无 rand seed，跨进程一致）。

```go
const NodeMirrorShards = 16

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

**每 shard 容量** = `ceil(cap/16)`。生产默认 `cap=100000` → 每 shard ≈ 6250 entry，总容量 100K 不变。单 shard 内仍是严格 LRU（Update 原子 RMW 保留）；跨 shard 是"近似 LRU"，对 (cred, model) 哈希命中稳定的 NodeMirror use case 完全足够。

**未改动**:
- `cache.LRU` 单 mutex 保留（sticky/intent 路径用量小、争抢低，不热）
- `applyToLRU` 内部 fn 签名不变；generation CAS 语义在单 shard 内不变

### 4. `Plan()` ModeOff 短路（🟢 P2）

**问题**: `manager.go:320-325 Plan` 无条件 `m.Ready(ctx)` 发 Redis IO，但下游 `plan()` 第一句就是 `if m.Mode() == api.ModeOff { return nil }`。生产默认 `ModeOff` 等于每次白做一次 GET。

**Fix**: Plan() 入口先 mode 检查，命中 off 直接返回。`PlanReady()` 已有 caller 传 ready 快照、不发 Ready IO，无需修。

```go
func (m *Manager) Plan(...) []CandidateSeed {
    if m == nil { return nil }
    if m.Mode() == api.ModeOff { return nil }   // M3 (2026-07-28)
    return m.plan(ctx, seeds, tenant, canonical, m.Ready(ctx))
}
```

### 5. 新增并发回归测试

`domains/ursm/v2/cache/nodemirror_test.go` 新增：

- `TestNodeMirrorShardedConcurrentWrites` —— 32 goroutines × 100 写不同 (cred, raw) + 实时 Get/Peek，验证分片互不阻塞
- `TestNodeMirrorShardedGenMonotonicPerKey` —— 32 goroutines 写同一 (cred, raw) 不同 gen，全部低于种子，验证 per-shard CAS 单调性保留

## 文件清单

| 文件 | 类型 | 改动 |
|---|---|---|
| `domains/ursm/v2/store/record_request.lua` | 修改 | head 添加 manual_hold 内读 + 短路；保留 ARGV[9] |
| `domains/ursm/v2/store/apply_probe.lua` | 修改 | 同上 |
| `domains/ursm/v2/manager.go` | 修改 | RecordRequest 删 HGet；Plan() ModeOff 短路 |
| `domains/ursm/v2/probe.go` | 修改 | ApplyProbe 删 HGet |
| `domains/ursm/v2/cache/nodemirror.go` | 重写 | 16 shard FNV-1a hash |
| `domains/ursm/v2/cache/nodemirror_test.go` | 修改 | +2 并发测试 |

净行数：+218 / -78（不含 .lua）

## 验证

```bash
go build ./...                                     # 0 errors
go vet ./domains/ursm/...                          # 0 issues
go test -race -count=1 ./domains/ursm/v2/...       # 13 packages, 0 FAIL, 0 DATA RACE
go test -race -count=1 ./pool ./ratelimit ./circuit ./bg/... ./safety ./autoroute/... ./domains/sessionstate  # 复测上轮已修热点，无回归
```

## 不变性手工核算（spec Decision 2 / 4 / 5 / 6）

- ✅ **写入必须先经 Redis Lua 成功**：未改。`applyToLRU` 仍只在 `m.PipelineNodeViews` / `RecordRequest` / `ApplyProbe` 等 Redis-success 之后的路径被调
- ✅ **LRU 永远是只读副本**：`applyToLRU` 仍为唯一写入入口
- ✅ **generation 单调**：`applyToLRU` 内部仍 `l.Update(fn)` RMW；per-shard mutex 保证单 key CAS 原子
- ✅ **admin 优先级恒占**：manual_hold 现在在 Redis 原子上下文读，永远反映"调用瞬间"的最新 admin 状态

## 部署影响

- `URSM v2 manager` wiring 与 wiring 配置不变（`cmd/gateway/main.go` 无需调整）
- 本次无 API 变更、无 schema 变更、无迁移脚本
- 与 commit `8001cbee9` (上一轮 77 文件加固) 走同一部署节奏：245 灰度 → 154 全量

## References

- AUDIT 报告：`AUDIT_URSMV2_CONCURRENCY_20260728.md`
- 上一轮全仓加固 commit：`8001cbee9 fix(concurrency): harden locking across gateway hot paths`
- URSM v2 设计稿：repo 内 `docs/superpowers/specs/2026-07-27-model-format-conversion-audit-design.md` §5.7 / §5.11
- rules 18 / 19 / 38 / 39 / 44 / 47（环境一致性、SQL 真相、敏感信息、文件写入、envs 注册）

---

**维护**: ZCode (autonomous)
**日期**: 2026-07-28
