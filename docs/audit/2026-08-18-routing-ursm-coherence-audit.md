# Routing & URSM Coherence Audit — Agent D (Read-Only)

- Date: 2026-08-18
- Branch: `main` @ `1eac33fac`
- Mission: 只读审计 URSM v2 source-priority、`/api/routing/resolve` 与真实 Router 的 `runtime_routable` 一致性、URSM tenant key coverage；不修改生产代码
- Related handoff: `docs/handoff/2026-08-18-global-routing-audit-handoff.md`

---

## 0. 关键事实速览（Findings TL;DR）

1. **`record_request.lua` 与 `apply_probe.lua` 在 source-priority 上是不对称的**：
   - `apply_probe.lua` 写 `source_priority = priority` (默认 20 / Recover=30, 来自 `ARGV[7]`)，并且在 `cool` 恢复分支会清掉 `available/disabled/cool_until_ms/fail_streak/last_err`（`apply_probe.lua:42-66`）。
   - `record_request.lua` 默认写 `source_priority = 10`（普通请求），但**当 `current_priority > 10` 时直接 `EXPIRE` 早返回**，并不会尝试覆盖 probe 留下的 `available/disabled/cool_until_ms`（`record_request.lua:186-190`）。
   - 普通请求若 `in_cool == false`（即 `disabled=0` 或 `cool_until_ms` 已过期），**总是把 `available=1` 写回**，并**重置 `fail_streak=0`**（成功路径）或**累加 `fail_streak` 并在 ≥ limit 时再次 `disabled=1, available=0, cool_until_ms = now + cool_seconds*1000`**（`record_request.lua:232-260`）。这意味着一个"刚刚过去的 cool_until"对普通成功请求没有任何保护。
   - **`manual_hold` 短路优先级最高**（`record_request.lua:60-63`、`apply_probe.lua:15-27`）。
2. **Resolve 与真实 Router 在 URSM miss 时的 `runtime_routable` 结论可能不同**：
   - Resolve handler（`admin/routing.go:355-396`）在 URSM v2 Ready 时调用 `FilterAndScore`；缺失 key 走 `nodeViewFromHash` 的 `len(raw)==0` 分支返回零值 `NodeView{Available: false, ...}`（`store/pipeline.go:111-115`），`applyURSMOverlay` 用 `viewByKey` 命中这个零值 view 并把 `Routable=false, BlockReason="node_unavailable_by_ursm_v2"`（`admin/routing.go:4459-4470`）。
   - 真实 Router 在 URSM v2 `ModeAuthoritative` + `Ready=true` 时也调用 `FilterAndScoreReadyWithSource`，行为一致：T4 contract 下 miss 候选被过滤掉（`router.go:298-353`）。
   - 真实 Router 在 `ModeShadow` / `ModeCanary` / `ModeOff` 时**不会**走 v2 过滤（`router.go:251-259` 阴影路径、`router.go:348-354` 守门返回 nil 仅 Authoritative 非 ready 情况），legacy filter 只看 `c.IsAvailable()`（`state_backend.go:97-118`），而**Resolve handler 仍然走 URSM 覆写**——这是 P2 同构风险。
3. **Tenant key coverage 不透明**：
   - K2 语法明确禁止空 tenant（`keys_k2.go:102-113`）；legacy `NodeKeyForTenant("")` 走 `NodeKey(prefix, cid, raw)`（`keys.go:15-27`），即 `ursm:v2:node:36:minimax-m3`。
   - resolve handler 写入 seed 时**始终用 `TenantID: "default"`**（`admin/routing.go:376`），即 `ursm:v2:node:default:36:minimax-m3`（`keys.go:18-22`），是 `t:default:36:minimax-m3` 形式；这是**非 k2、non-empty tenant、legacy-compatible 形式**。
   - `record_request.go` / `apply_probe.go` 的 Go 侧 `NodeKeySetForTenant` 在 production tenant 实际值（`COALESCE(c.tenant_id, 'default')`，见 `cmd/gateway/main.go:4818`）下写出来的也是带 `t:` 标记的 legacy 兼容键，**不是 k2**。
   - 没有任何 dashboard 端点（`/api/routing/health` / `/api/routing/probe`）会把"legacy/无 TTL/缺 tenant key"作为独立维度呈现（`admin/routing.go:2457+`、`admin/probe_dashboard.go`），运维无法直接区分。

---

## 1. 审计 `record_request.lua` / `apply_probe.lua` 的 source-priority

### 1.1 Source-priority 规则表

| 调用方 | 默认 source_priority | 入参 / 改写点 | file:line |
| --- | --- | --- | --- |
| `apply_probe.lua` Probe | 20 | `ARGV[7]` 可传 `SourcePriorityRecover=30`（Recover 路径），否则 `priority <= 0 → 20` | `apply_probe.lua:7-13`, `apply_probe.lua:29-32` |
| `apply_probe.lua` Recover | 30 | `apply_admin.lua` 写 `manual_hold=1` 后此处直接 `ignored_manual_hold` | `apply_probe.lua:15-27` |
| `record_request.lua` 普通请求 | 10 | `source_priority=10` 显式 HSET | `record_request.lua:208`, `record_request.lua:239` |
| `record_request.lua` 已有 `>10` 优先级 | n/a | 早返回（不写 available/disabled/cool_until_ms，只 `EXPIRE`） | `record_request.lua:186-190` |
| `record_request.lua` 不可覆盖项 | 10 | `manual_hold=1` → `ignored_manual_hold` | `record_request.lua:60-63` |

### 1.2 已观察到的覆盖关系

1. **Probe 写 `priority=20`，普通请求写 `priority=10`**。当 `current_priority > 10`（即 probe 已经写过一次）时，**普通请求只会刷新 TTL、不会写 `available/disabled/cool_until_ms`**（`record_request.lua:186-190` 早返回）。这就是 P1 提到的"普通成功不会覆盖仍有效的失败 probe 证据"——单测未覆盖，可视为 good。
2. **Probe 在 `in_cool && success` 分支会清掉 cool 证据**（`apply_probe.lua:43-56`）。普通请求想要恢复同一个 in_cool 节点：在 `record_request.lua:198-212` 也是支持的（`disabled=0, available=1, cool_until_ms=0, fail_streak=0, last_err=""`），并把 `source_priority=10`。
   - 风险：当 probe 留下的 `source_priority=20`（probe priority 高于普通 10），普通成功不能直接覆盖（被 `current_priority > 10` 早返回挡住），但**普通失败仍然能写 `cool_until_ms` 和 `fail_streak`**——因为 250-258 行不在 `current_priority > 10` 守门之后，而是 fall through 到 fail_streak 累计。
3. **`cool_until_ms` 过期后的普通成功请求会 `HSET ... available=1, source_priority=10, last_err=...`**（`record_request.lua:236-260`），而 `last_err=err_kind` 仍可能是失败的 `err_kind`（因为 success=1 才走 244-246 的 success 分支）。**只有 success=1 时才会把 `last_err=""` 写回**。
4. **Source-priority 反向覆盖缺位**：`record_request.lua` 没有任何"probe 留下的 source_priority 不能被普通请求降级到 10" 的反向保护，**只是单向守门（probe>10 阻挡普通 success / 普通 success 不写入）**。也就是说：
   - 普通失败 → `current_priority > 10` 时 EXPIRE 走人，**不会**累加 `fail_streak`、不会写 `cool_until_ms`、不会 `disabled=1`。这与 handoff §3"glm-5.2 first round fix"的设计一致。
   - 普通成功 → 同样只 EXPIRE。等于说 probe priority>10 的节点，**任何普通请求都既不能 unblock 它也不能 escalate**——只能等下一次 probe（Recover priority=30 才会再次覆盖）。
   - **风险**：当 probe 队列停摆（参见 handoff §P0: queue dead），probe 优先级 20 的节点会**永久卡在 probe 留下的 cool 状态**，即使 real 流量大量 success，也无法被 release。`record_request.lua:186-190` 的早返回就是这一类"恢复链条断裂"。

### 1.3 file:line 证据

- `domains/ursm/v2/store/record_request.lua:60-63` — `manual_hold` 短路
- `domains/ursm/v2/store/record_request.lua:186-190` — `current_priority > 10` 早返回
- `domains/ursm/v2/store/record_request.lua:197-212` — `in_cool && success` 解除 cool 并写 `source_priority=10`
- `domains/ursm/v2/store/record_request.lua:217-229` — `in_cool && fail` 指数退避，写 `source_priority=10`
- `domains/ursm/v2/store/record_request.lua:232-260` — 非 in_cool 路径：success→available=1 / fail→fail_streak 累加 + 命中 limit 时再 disable
- `domains/ursm/v2/store/apply_probe.lua:29-32` — `priority` 默认 20 / Recover=30
- `domains/ursm/v2/store/apply_probe.lua:42-66` — Probe 写路径，cool 恢复分支清 `available/disabled/cool_until_ms/fail_streak`

### 1.4 潜在竞态场景

1. **R1 — Probe 队列停摆下的"半永久卡死"**：
   - t0: probe enqueue → ApplyProbe 写 `source_priority=20, disabled=1, cool_until_ms=t0+30min`
   - t0+30min: 真实流量 success 抵达 → `current_priority=20 > 10` → 只 EXPIRE，**不写 available=1**
   - t0+30min+1s: queue 仍然停摆（已下轨），`cool_until_ms` 过期，但**没有 probe 写一次 trigger**，`apply_probe.lua:144-165`（nodeViewFromHash 半开逻辑）也救不了，因为 FilterAndScore 在 v2 Authoritative 模式下依旧不路由（cool 已过期，节点半开，但 probe 仍占 source_priority>10）。
   - 结果：节点**永远半开但无法被新流量路由**——和 handoff §6.1 描述的 glm-5.2 复检同构。
2. **R2 — Probe 写 + 普通 success race**：
   - t0: ApplyProbe 写 `source_priority=20, available=1`（非 in_cool 分支，line 58-65）
   - t0+ε: 普通 success 抵达 → `current_priority=20 > 10` → EXPIRE 走人，不会 reset `fail_streak` 也不会写 `source_priority=10`。
   - 行为正确：probe 不会因普通成功而被降级。
3. **R3 — ApplyProbe 与 ApplyAdmin race**：
   - t0: ApplyAdmin 写 `manual_hold=1, available=0`
   - t0+ε: ApplyProbe 抵达 → `manual_hold=1` → `ignored_manual_hold`（`apply_probe.lua:15-27`）
   - 行为正确：admin hold 主导。
4. **R4 — `cool_until_ms` 过期后 record_request + 半开读 race**：
   - 2026-08-18 已经在 `nodeViewFromHash` 修了半开（`store/pipeline.go:144-165`），但 `record_request.lua:236-260` 仍然依赖 `disabled` 的当前 bit。如果上一次 `record_request` 写 `disabled=1` 但没在 cool_until 写之前 auto-clear，`nodeViewFromHash` 仍判定为半开并把 `available=true` 返回——这点和 `record_request` 的 `disabled==1` 路径（line 232-234）有"写 disabled=0 但保留 disabled=1" 的潜在不一致，已被 line 232-234 的"非 in_cool 时 disabled→0" 救回，前提是**任何事件都要来**。
   - 与 R1 互锁：若 R1 中没有事件抵达，则 record_request 也不会写。

### 1.5 缺口与建议（仅描述，不改代码）

- 当前 source-priority 守门是**单方向**（probe>10 → 普通事件无写入），缺乏**多档语义**：admin hold（最高）、probe（20）、recover（30）、请求（10）、legacy（?）。建议至少在 lua 注释和 migration plan 文档里固化"probe 不能被普通 success 覆盖、cool_until 过期后必须经 probe 半开"的契约，并补一个"probe 死后由 ApplyAdmin 显式 ClearState 才能解除 priority=20 残影"的运维手册条目。
- `record_request.lua:236-260` 的 `last_err` 在 success 分支才会清空（line 245-246），但 line 237-241 已经先 `HSET last_err=err_kind`——这意味着任何非 success 失败请求会立即把 `last_err` 覆盖，无论之前 `err_kind` 是什么。属于单调信息损失（不致命）。

---

## 2. 审计 `/api/routing/resolve` 与真实 Router 的 `runtime_routable` 一致性

### 2.1 两条代码路径

| 路径 | 入口 | URSM v2 过滤逻辑 | file:line |
| --- | --- | --- | --- |
| **Resolve handler** | `GET /api/routing/resolve?model=...` | `ursmManager.FilterAndScore(ctx, seeds)` → `applyURSMOverlay(candidates, views, now)` | `admin/routing.go:355-396`, `admin/routing.go:4410-4483` |
| **真实 Router (Authoritative)** | `Router.PlanCandidatesWithContext` | `r.URSMv2.FilterAndScoreReadyWithSource(ctx, seeds, *readySnapshot)` 二次过滤 | `domains/streaming/executors/router.go:298-353` |
| **真实 Router (Shadow/Canary)** | `Router.planCandidates` | 仅 `enqueueURSMv2Shadow` 异步观察；候选列表用 `selectStateBackendWithReady(...).FilterAvailable(...)`，走 `LegacyStateBackend` / `DBOnlyBackend`，**不调 FilterAndScore** | `router.go:251-259`, `state_backend.go:97-118`, `state_backend.go:134-144` |
| **真实 Router (Off)** | 同上 | `URSMv2 == nil` / `Mode() == off`，直接走 `DBOnlyBackend.FilterAvailable` → `c.IsAvailable()` | `router.go:967-973`, `state_backend.go:132-144` |

**关键差异**：
- Resolve handler 不区分 URSM v2 模式，只要 `ursmManager.Ready(ctx)` 为 true 就调 `FilterAndScore` 并 apply 覆写（`admin/routing.go:355-396`）。
- 真实 Router 在 Shadow/Canary/Off 模式**不会**调 FilterAndScore 路由请求；只在 Authoritative + Ready 时才把 URSM 当作唯一权威源（`router.go:298-353`）。

### 2.2 四种状态下的结论对比矩阵

> 测试条件：生产 URSM v2 模式 = `URSM_V2_MODE=off`（参见 `cmd/gateway/main.go:1032-1033` 注释 "URSM_V2_MODE=off 时 Manager.Mode() == off，PlanCandidates 里的 v2 分支不会进入"、以及 `router_authoritative_test.go:11` "URSM_V2_MODE=off leaves v2 un-wired"），resolve handler 通过 `applyURSMOverlay` 模拟行为，真实 Router 按 `PlanCandidates` 路径走。

| 场景 | URSM v2 Redis 状态 | Resolve handler `runtime_routable` | 真实 Router (Authoritative + Ready) | 真实 Router (Shadow/Canary/Off) | 一致？ |
| --- | --- | --- | --- | --- | --- |
| **URSM miss** | key 不存在 | view `Available=false` → `Routable=false, BlockReason="node_unavailable_by_ursm_v2"`（`routing.go:4459-4470`）；`pipeline_test.go:41-44` 锁定 Available=false | `allow` map 不命中 → 候选被剔除（`router.go:332-343`） | **不调 v2**，候选由 `DBOnlyBackend.FilterAvailable` 决定；若 SQL view `is_routable=true` → 通过 | **不一致** |
| **URSM ok** | `available=1, fail_streak=0, no cool` | `Routable=true, Available=true`（`routing.go:4439-4481` 默认 success 分支） | `allow` 命中 → 候选保留 | DB view 决定，与 v2 无关 | 视 DB 一致则一致 |
| **URSM cool** | `available=0, disabled=1, cool_until_ms > now` | view `Available=false, CoolUntil > now` → `Routable=false, BlockReason="node_in_cool_until"`（`routing.go:4472-4480`） | `allow` 不命中 → 剔除 | DB view + legacy backend `IsAvailable` 决定；若 DB `is_routable=true` → 通过 | **不一致** |
| **URSM disabled (admin hold)** | `available=0, manual_hold=1, no cool` | `applyURSMOverlay` 仅看 `Available`/`CoolUntil`，**不检查 `manual_hold` 字段**（`routing.go:4439-4481`）→ view `Available=false` → 走 `node_unavailable_by_ursm_v2` 分支 | 同 Authoritative | DB view 决定 | **一致**（因为 v2 view 的 Available=false 已经否决）；但 **block_reason 错误**——admin hold 应被标为 `node_disabled`/`manual_hold`，不是 `node_unavailable_by_ursm_v2` |

### 2.3 不一致的根本原因

1. **生产模式是 `URSM_V2_MODE=off`**（`cmd/gateway/main.go:1032` 注释、`router_authoritative_test.go:11` 测试 pin "URSM_V2_MODE=off leaves v2 un-wired"）。
2. resolve handler 仍然尝试 `FilterAndScore`（`admin/routing.go:349-355`），前提是 `h.ursmV2 != nil` 且 `Mode() != ModeOff`（`admin/routing.go:355-360` 的 `switch`）。**生产 off 模式下走 `applyResolveDefaults` 路径**（`admin/routing.go:357`），全部 `Available=true`——这与"DB view is_routable=true 但 URSM miss 的真实灰度"是乐观的。
3. 真实 Router 在 off 模式下用 `c.IsAvailable()`（来自 SQL view）过滤——`provider.Candidate.IsAvailable()` 内部看 `cmb.available`（SQL 列 `credential_model_bindings.available`），而 Resolve handler 也基于 `v.is_routable`（`admin/routing.go:317`）。两者来源**不完全相同**：v.is_routable 综合了 lifecycle / availability / quota / plan_type（参见 `v_routable_credential_models`），`cmb.available` 只是 cmb 列。`admin/routing.go:202-205` 的注释承认了"运行时状态在 URSM v2 里才是真相源"——但生产 off 模式时 URSM v2 不写入。

**结论**：
- **P0 风险**：生产 URSM v2 off 模式下，resolve handler 显示 `runtime_routable=true`（用 `applyResolveDefaults` 灌默认值），但实际请求可能因 `cmb.available=false` / `node_probe_state.consecutive_failures >= N` / `fpslot NodeState.Disabled=true` 被多个独立 gating 拦截。Resolve 显示的"乐观 green"和"真实 routing 拒"不一致。
- **P1 风险**：URSM v2 在 canary 模式下，resolve handler 已经走 `FilterAndScore` 拿真实 view，而真实 Router 走 legacy backend——运维如果根据 resolve 页"4 个 candidate 都不可用"以为全网宕了，但实际 chat 流量仍在跑（legacy backend 路径）。这种"resolve 误报"已经记载在 `bg/credential_recovery_test.go:19`："where /api/routing/resolve showed routable nodes while real chat"。

### 2.4 file:line 证据

- `admin/routing.go:355-396` — Resolve handler URSM 覆写分支
- `admin/routing.go:4396-4408` — `resolveRuntimeDefaults` 把 Redis miss / 管线失败灌成 `available=true`
- `admin/routing.go:4410-4483` — `applyURSMOverlay`：viewByKey 命中后才覆盖 `Routable`/`Available`
- `domains/streaming/executors/router.go:298-353` — 真实 Router Authoritative 路径
- `domains/streaming/executors/router.go:251-259` — Shadow/Canary 异步 enqueue，不调 FilterAndScore
- `domains/streaming/executors/router.go:963-973` — `filterAvailableWithStateManager`，注释明确"URSM_V2_MODE=off leaves v2 un-wired"
- `domains/streaming/executors/state_backend.go:97-118` — `LegacyStateBackend` 走 `IsAvailable`（StateManager 内存缓存）+ `c.IsAvailable()`
- `domains/streaming/executors/state_backend.go:134-144` — `DBOnlyBackend` 仅 `c.IsAvailable()`
- `domains/ursm/v2/store/pipeline.go:111-115` — `nodeViewFromHash` 在 `len(raw)==0` 时返回零值 view（Available=false）
- `domains/ursm/v2/store/pipeline_test.go:28-44` — `TestPipelineBatchReadMissing` pin 缺失 = unavailable
- `admin/routing_resolve_overlay_test.go:62-89` — `TestApplyURSMOverlayWritesBackMutations` pin Redis-miss 走 T4 defaults（仅适用 resolve handler 自身；不约束真实 Router）

---

## 3. Legacy / default tenant / K2 key coverage 审计

### 3.1 写入路径

| 路径 | 写入的 key 形式 | tenant 取值 | TTL | file:line |
| --- | --- | --- | --- | --- |
| `record_request.go` Go 侧 `BuildKeySet` | 调 `NodeKeySetForTenant(prefix, tenant, cid, raw)` → legacy exact-byte 形式（**非 k2**） | `COALESCE(c.tenant_id, 'default')`（SQL），production tenant 实际是 `"default"` 或 5-7 位数字 id | `node_ttl` 来自 `ARGV[6]`，默认 3600s | `domains/ursm/v2/store/record_request.go`（见 handoff §98）、`keys.go:15-27` |
| `apply_probe.go` Go 侧 | 同上 | 同上 | `node_ttl` 来自 `ARGV[6]` | `domains/ursm/v2/store/apply_probe.go` Go wrapper（参见 `keys.go:53-65`） |
| `apply_admin.go` Go 侧 | 同上 | 同上 | 视 action 而定 | `domains/ursm/v2/store/apply_admin.lua` 注释（`apply_admin.lua` 文件） |
| Resolve handler 写入 seed | 不写 URSM，仅 **读** via `FilterAndScore` | seed `TenantID: "default"` 写死 | n/a | `admin/routing.go:371-383` |
| Migration copy_hash / metadata_advance | k2 形式 | 从 SQL 解析 | k2 TTL | `domains/ursm/v2/migration/copy_hash.lua`、`domains/ursm/v2/migration/metadata_advance.lua` |

### 3.2 关键发现

1. **`admin/routing.go:376` 写死 `TenantID: "default"`**：resolve handler 把所有 seed 都标 `TenantID: "default"`，但生产 `cmd/gateway/main.go:4818` 用 `COALESCE(c.tenant_id, 'default')` 拉真实 tenant id。当生产 tenant id 是数字字符串（5-7 位），`keys.go:21-22` 的 `isNumericTenant` 会走 `node:t:<numeric>:<cid>:<raw>`；resolve handler 走 `node:default:<cid>:<raw>`，**两套 key 不命中**。
   - 这是一个**潜在的 silent miss 放大器**：真实流量写的是 `node:t:1234567:36:minimax-m3`，resolve handler 读 `node:default:36:minimax-m3`，永远拿不到真实 telemetry。
2. **TTL 风险**：`record_request.lua:188` `EXPIRE(node_key, node_ttl)` 每次普通请求都刷新 TTL；`apply_probe.lua:55, 64` 也刷新。**但当所有事件消失**（node 长时间无流量），TTL 到期后整个 node key 消失，**任何 resolve 请求拿到的都是 zero view（Available=false）**——尽管真实历史状态可能是可路由的。这与 handoff §6.2 提到的"glm-5.1/gpt-5.5/kimi-k2.6/doubao 长期 URSM miss"同构。
3. **K2 / legacy 共存**：`store/pipeline.go:44-65` 在 `KeySchemaModeDual` 下读 k2 → fallback legacy；在 `KeySchemaModeCanonical` 下只读 k2。**resolve handler 不知道 schema mode 是什么**——它调 `FilterAndScore` → 内部走 `PipelineNodeViews` → 内部走 `s.schemaMode`（`store/pipeline.go:44`）。如果生产在 dual mode 且 k2 没覆盖某节点，resolve 会回退 legacy key（命中），**但 Go 侧写路径的 legacy key 的 `tenant_key_part` 跟 k2 解析出来的 tenant 不一致**——k2 grammar 用 base64url 编码 tenant 段，legacy grammar 不编码。当某节点的 `tenant_id` 含 `:` 或 base64url 不合法字符时，k2 与 legacy key 不可互转。
4. **无 TTL 显式 metric**：`admin/probe_dashboard.go`（grep 结果无 `coverage` 命中）没有任何端点把"URSM tenant key 数量"作为 dashboard 维度；`/api/routing/health`（`routing.go:2457+`）和 `/api/routing/probe`（`routing.go:2578+`）只列模型级状态，不区分 tenant schema / 是否有 key / TTL 剩余。

### 3.3 覆盖率可观察性差距

- `coverage_key` (`keys.go:183-185`) 在 canonical startup 时被 authoritative 启动流程读取，**但其内容是 migration 写入的"预期覆盖 manifest"**，运行时不会增删；运维端没有端点把"实际存在的 node key 数 / 期望覆盖数 / TTL 分布"展示出来。
- `cmd/gateway/main.go:4818` 的 `SELECT DISTINCT cmb.credential_id, COALESCE(c.tenant_id, 'default')` 是某处 SQL 块，未在此 handoff 引用；需要补充审计（**建议但未实施**——grep 显示这是 `cmd/gateway/main.go:4818`，但未读完整上下文；与本审计边界不冲突，仅指出）。

### 3.4 Dashboard 能否区分

- 答：**不能**。
  - 现有 `/api/routing/health` / `/api/routing/probe` / `/api/routing/resolve` 三个端点（`admin/routing.go:2457`, `2578`, `161`）都**不返回**：
    - 该节点的 URSM key 是否存在
    - 写入的 schema 形式（legacy / k2 / dual）
    - 写入时使用的 tenant 值（`default` vs `COALESCE(c.tenant_id)`）
    - TTL 剩余秒数
  - 因此，运维只能通过"resolve 页 available 状态反推 URSM"——但 production off 模式下 `applyResolveDefaults` 永远返回 `available=true`，**反推也无效**。

### 3.5 file:line 证据

- `domains/ursm/v2/store/keys.go:15-27` — `NodeKeyForTenant` 三种形式（空 / non-numeric tenant / numeric tenant）
- `domains/ursm/v2/store/keys_k2.go:59-113` — K2 grammar 与校验
- `domains/ursm/v2/store/keys_k2.go:102-105` — K2 拒绝空 tenant
- `domains/ursm/v2/store/pipeline.go:44-65` — `PipelineNodeViews` 根据 schema mode 选 key
- `admin/routing.go:371-383` — Resolve handler seed 写死 `TenantID: "default"`
- `admin/routing.go:4396-4408` — `resolveRuntimeDefaults` 灌 `Available=true` defaults
- `domains/ursm/v2/store/record_request.lua:188` — `EXPIRE(node_key, node_ttl)` 每次普通请求刷新 TTL
- `domains/ursm/v2/store/record_request.lua:243` — 同上（普通 success/fail 路径结尾）
- `domains/ursm/v2/store/apply_probe.lua:55,64` — probe 路径刷新 TTL
- `admin/routing.go:2457+` — `handleRoutingHealth`
- `admin/routing.go:2578+` — `handleRoutingProbe`
- `admin/probe_dashboard.go` — 全文未命中 `coverage` / `tenant_key`（grep 已确认）

---

## 4. 集成测试建议（仅描述，不写代码；标注 "建议但未实施"）

> 实施位置建议：`domains/ursm/v2/integration/` 或 `tests/integration/routing_ursm_coherence_test.go`。每个场景都需要 Redis fixture、URSM v2 schema 切换能力、与生产 deployment 同版本的 `URSM_V2_MODE` env 覆盖。

### 4.1 场景 A — Failed probe 不会被普通成功覆盖

- **触发条件**：
  1. 在 Redis 中预置 `ursm:v2:node:default:36:minimax-m3`：`available=0, disabled=1, cool_until_ms=now+10min, fail_streak=3, source_priority=20, last_err=provider_500`。
  2. 调 `applyProbe` 写同 key（success=0）→ `last_err=provider_500`、source_priority=20。
  3. 调 `recordRequest` 写同 key（success=1）→ 期望**不覆盖** available/disabled/cool_until_ms/source_priority，只 EXPIRE 刷新 TTL。
- **期望 URSM 状态**：
  - `available=0`（不变）
  - `disabled=1`（不变）
  - `cool_until_ms` 仍为 `now+10min`（不变）
  - `source_priority=20`（不变，被守门保护）
  - `last_err=provider_500`（不变）
  - `node_ttl` 被刷新（被 EXPIRE 拉长）
- **期望 routing resolve 输出**（`/api/routing/resolve?model=minimax-m3`，URSM v2 ready=true）：
  - candidate `cred_id=36, raw_model=minimax-m3`：
    - `Routable=false`
    - `RuntimeRoutable=false`
    - `BlockReason="node_in_cool_until"`（或继承 view 提供的 `in_cool_until`）
    - `Available=false`
- **期望真实 Router 输出**（URSM v2 Authoritative + ready）：
  - `PlanCandidates` 返回不包含 `cred_id=36, raw_model=minimax-m3`（被 `allow` map 过滤）
  - 如唯一候选即此节点，则 `len(result)==0`，调用方走"无可路由候选"路径
- **覆盖的目标 file:line**：
  - `record_request.lua:186-190`（current_priority > 10 守门）
  - `applyURSMOverlay:4439-4481`（overlay 行为）
  - `router.go:298-353`（authoritative 过滤）

### 4.2 场景 B — URSM miss 在 URSM v2 off / Shadow 模式下 resolve 与真实 Router 都"乐观"

- **触发条件**：
  1. SQL `v_routable_credential_models` 中 `cred_id=22, raw_model=glm-5.2` 满足 is_routable=true（`lifecycle_status=active, availability_state=ready, quota_state=ok, plan_type` 兼容、`cmb.available=true`、`node_probe_state` 通过 `next_retry_at<=now`）。
  2. URSM Redis 中**不预置** `ursm:v2:node:default:22:glm-5.2` key。
  3. `URSM_V2_MODE=off` env。
- **期望 URSM 状态**：key 不存在。
- **期望 routing resolve 输出**（production 配置）：
  - `h.ursmV2.Mode() == ModeOff` → 走 `applyResolveDefaults`（`routing.go:355-357`）→ `Available=true, CircuitState=closed, SuccessRate=0.9, P95LatencyMs=9999`。
  - candidate 仍 `Routable=true, RuntimeRoutable=true`。
- **期望真实 Router 输出**（off 模式）：
  - 走 `DBOnlyBackend.FilterAvailable`（`state_backend.go:134-144`）→ 调 `c.IsAvailable()`（依赖 cmb + provider state）→ **若 cmb.available=true 则通过**。
  - 实际可能因 `node_probe_state.consecutive_failures>=N` 已被 SQL view 排除（`is_routable=false`），但**这是 SQL view 的事，与 URSM miss 无关**。
- **断言关键点**（仅描述）：
  - 若 SQL view 说可路由，则 resolve 和真实 Router **都**显示可路由——一致（这种"一致乐观"是 production 当前的"安全"状态）。
  - 若 SQL view 说不可路由（e.g. node_probe_state 失败），则**两者都应该不可路由**——但 resolve handler 的 `applyResolveDefaults` 不读 `node_probe_state`，仅靠 SQL view（`is_routable`），**不二次读** node_probe_state。所以两者应该一致。
  - **真正的不一致点**是：当真实 Router 在 Shadow/Canary 模式，URSM v2 数据**已经被读**（shadow worker 异步），但**真实 Router 不消费**——而 resolve 仍走 URSM 覆写，会显示 v2 看到的 available 状态。建议在 Shadow/Canary 模式下让 resolve 走 `applyResolveDefaults` 跳过 URSM 覆写（**建议但未实施**——需要修改 `admin/routing.go:355-360` 的 switch 条件）。

### 4.3 场景 C — Source-priority 20 probe 留下后，普通失败不能升级到 disabled

- **触发条件**：
  1. Redis 预置 `ursm:v2:node:default:42:gpt-5.5`：`available=1, source_priority=20, fail_streak=0, last_err=""`。
  2. 模拟"probe 之后无新 probe"的场景（probe queue 停摆）。
  3. 调 `recordRequest` 失败 4 次（success=0, err_kind=provider_500），每次都用同一个 request_id 之外的不同 id 绕开 dedup。
- **期望 URSM 状态**：
  - `current_priority=20 > 10` → `record_request.lua:186-190` 早返回，**不写 disabled、cool_until_ms、fail_streak**。
  - `fail_streak` 保持 0（不累加）。
  - `source_priority=20`（不降级）。
  - `last_err` 不变（line 237-241 在 fail_streak 之前没写 last_err，但 EXPIRE 走人也跳过了 line 248-258 的 HSET fail_streak/disable 路径）。
- **期望 routing resolve 输出**：
  - 调 `FilterAndScore` 返回 view：`Available=true, FailStreak=0, CoolUntil=zero`。
  - `applyURSMOverlay` → `Routable=true, RuntimeRoutable=true, Available=true`。
- **期望真实 Router 输出**：
  - Authoritative 模式：v2 view 命中 `allow=true` → 候选保留。
  - Off/Shadow/Canary 模式：DB view 决定。
- **断言关键点**：
  - **生产 off 模式**下，resolve 显示可路由、真实 Router 也可路由，**不一致于 B**：B 是 URSM miss 场景，本场景 C 是 probe priority 卡住后无法升级的**语义 bug**——运维在 dashboard 看不到任何异常（resolve 显示 green），但实际节点可能在 20 分钟后过期。
  - 建议在测试中加**时间跳变** fixture：把 system clock 推进 25 分钟（> node_ttl 默认 3600s 不会过期，但推进 1h+ 触发 TTL 过期），再次 resolve 应得到 view 全零（key 已 EXPIRE 完）。本测试仅做时间观察，不做实际注入。
- **覆盖的目标 file:line**：
  - `record_request.lua:186-190`（守门）
  - `admin/routing.go:4396-4408`（resolve defaults）
  - 真实 Router 的"按 mode 走不同 filter"分支（`router.go:251-353`）

### 4.4 场景 D（建议但未实施）— Resolve 应返回 `tenant_key_coverage` 维度

- **触发条件**：
  1. 对一组 `cred_id × raw_model` 组合（5+ 节点），其中 3 个有 URSM key，2 个没有。
  2. resolve handler 当前不返回 coverage 维度。
- **期望**：resolve 输出应包含一个 `ursm_v2_key_state` 字段，枚举 `{present, missing, expired}`，让运维不需要看 Redis 也能区分。
- **目标 file:line**：`admin/routing.go:460-468` 的 writeJSON 块。
- **状态**：建议，不在本次修复范围。

### 4.5 场景 E（建议但未实施）— Tenant `default` 与真实 `tenant_id` 不命中

- **触发条件**：
  1. 真实 `COALESCE(c.tenant_id, 'default')` 返回 `"1234567"`（数字）。
  2. 通过真实请求路径（调 `recordRequest` 的 Go wrapper）写 key → 实际 key 是 `ursm:v2:node:t:1234567:36:minimax-m3`。
  3. 通过 resolve handler 读 → seed `TenantID: "default"` → 读 `ursm:v2:node:default:36:minimax-m3`。
  4. 两次 key 不同，resolve 永远 miss。
- **期望**：resolve 拿到零值 view → `Available=false` → `BlockReason="node_unavailable_by_ursm_v2"`。
- **期望真实 Router 输出**：真实请求写过的 key 在 `FilterAndScore` 仍命中（如果 seed 也用真 tenant id），候选保留。
- **当前 production 行为**：
  - resolve handler line 376 写死 `"default"`——**与真实 tenant 不一致**。
  - 真实 Router 调用栈（`router.go:298-353`）：seed 的 `TenantID` 来自调用方（executor 的 `task.tenant`，参见 `router.go:240, 308, 463, 640`）—— production executor 是否传 `""` 或 `"default"` 还是真实 `tenant_id`？需要查 `domains/streaming/executors/executor.go` 入口（**建议但未实施**，本审计未深入 executor）。
- **断言关键点**：
  - 若 executor 传 `""`，则真实 Router 也走空 tenant（`keys.go:16-18` 的 legacy 形式），与 resolve 的 `default` 不一致——resolve 显示更乐观（永远命中不存在的 legacy key 因为 key 不存在返回 zero view = unavailable），真实 Router 显示真实 telemetry。
  - 若 executor 传 `"default"`，则与 resolve 写死一致——但仍可能与真实写入的 `t:1234567:...` 不一致。

---

## 5. 总结（Summary for parent agent）

### 5.1 已确认的同构风险

1. **P0-1 缺口（resolve vs 真实 Router）**：当生产 `URSM_V2_MODE=off`，resolve handler 走 `applyResolveDefaults` 永远 `Available=true`；真实 Router 走 `DBOnlyBackend` 依赖 `c.IsAvailable()` 与 SQL view。两者都乐观，但**真相源不同**——resolve 不读 `node_probe_state`、不读 `fpslot NodeState`、不读 `credentialstate` 内存缓存。建议在生产 off 模式下让 resolve 显式标注"URSM v2 离线，本页 runtime_routable 不代表真实可路由"。

2. **P1 缺口（source-priority 单向）**：`record_request.lua:186-190` 单向守门 `current_priority > 10` 是 glm-5.2 第一轮修复的设计，但**未覆盖普通失败路径的反向需求**——当 probe 队列停摆，probe 留下的 `source_priority=20` 节点永远无法被普通流量 unblock（既不能 success 也不能 fail 累加），只能等下一次 probe 或 admin `ClearState`。建议运维手册补充"probe 队列停摆时需手动 ClearState"。

3. **P2 缺口（tenant key coverage 不可观察）**：dashboard 完全不显示 URSM tenant key 数量、schema 形式、TTL 剩余。`coverage_key` 是 startup 用的，**不反映运行时**。

4. **P2 缺口（resolve seed tenant 写死 `"default"`）**：`admin/routing.go:376` 把所有 seed 都标 `"default"`，与 `cmd/gateway/main.go:4818` 的 `COALESCE(c.tenant_id, 'default')` 可能不一致（当真实 tenant 是数字字符串时）。需要在 resolve handler 拉 seed 时按 credential_id 查 `credentials.tenant_id`（**建议但未实施**）。

### 5.2 不在本审计范围（明确说明）

- bg/credential_recovery.go:443-485 修复（Agent A 范围）
- node_probe_runs.trigger_kind CHECK 修复（Agent B 范围）
- credential_selfcheck.go pin 修复（Agent C 范围）
- dashboard 数据源重写（Agent C 范围）

### 5.3 引用（References）

- 关键源码：
  - `domains/ursm/v2/store/record_request.lua`（source-priority 守门、in_cool 处理、TTL 刷新）
  - `domains/ursm/v2/store/apply_probe.lua`（Probe priority 默认 20、Recover=30、cool 恢复）
  - `domains/ursm/v2/store/pipeline.go:111-180`（nodeViewFromHash：miss=zero view / expired cool=half-open / manual_hold wins）
  - `domains/ursm/v2/manager.go:417-558`（filterAndScore：mirror-first + Redis verify、PipelineNodeViews 失败 = 拒绝）
  - `domains/ursm/v2/store/keys.go:15-65`（legacy exact-byte key 三种形式）
  - `domains/ursm/v2/store/keys_k2.go:59-113`（K2 grammar：拒绝空 tenant）
  - `admin/routing.go:161-468`（resolve handler）
  - `admin/routing.go:4410-4483`（applyURSMOverlay：viewByKey 命中后才覆写）
  - `admin/routing.go:4396-4408`（resolveRuntimeDefaults：T4 Redis miss = Available=true）
  - `domains/streaming/executors/router.go:181-353`（PlanCandidates 三种 URSM 模式分支）
  - `domains/streaming/executors/state_backend.go:97-144`（Legacy / DBOnly backend 行为）
- 关键测试 pin：
  - `domains/ursm/v2/store/pipeline_test.go:28-44`（`TestPipelineBatchReadMissing`）
  - `domains/ursm/v2/store/pipeline_test.go:46-101`（expired cool / future cool 互锁）
  - `admin/routing_resolve_overlay_test.go:17-55`（`TestApplyURSMOverlayPairsViewsByKey`）
  - `admin/routing_resolve_overlay_test.go:62-89`（`TestApplyURSMOverlayWritesBackMutations`：T4 defaults）
- 关键 SQL：
  - `cmd/gateway/main.go:4818`（`COALESCE(c.tenant_id, 'default')`）
  - `cmd/gateway/main.go:1032-1033`（`URSM_V2_MODE=off` 注释）

---

## 6. 实施优先级（供后续 Agent 决策）

| 优先级 | 任务 | 修复点 |
| --- | --- | --- |
| **P0** | 修复 resolve handler seed tenant 写死 `"default"` | `admin/routing.go:371-383` 用 `JOIN credentials` 拉 `c.tenant_id`，确保与真实写路径一致 |
| **P0** | 让 resolve 在 `ModeOff` 模式下显式标注 "URSM v2 离线，runtime_routable 不代表真实可路由" | `admin/routing.go:355-360` 扩 switch case |
| **P1** | 补 `TestSourcePriorityProbeAfterRegularRequest` 集成测试 | `domains/ursm/v2/integration/` 新增 |
| **P1** | 补 `TestResolveMatchesRealRouterInAuthoritative` 集成测试 | `tests/integration/routing_resolve_vs_router_test.go` 新增 |
| **P2** | dashboard 加 `ursm_v2_key_state` 维度 | `admin/routing.go:460-468` writeJSON + `admin/probe_dashboard.go` |
| **P2** | 修复 `record_request.lua:186-190` 的"probe 死后无法 unblock" 单向守门：要么让普通 success 在 `cool_until_ms < now` 时允许 unblock，要么补运维 ClearState 手册 | `record_request.lua:186-190` 或文档 |

---

> 本审计报告由 Agent D 在 2026-08-18 完成；所有结论均基于 `1eac33fac @ main` 代码快照；未修改任何业务代码；集成测试建议标注 "建议但未实施"；后续 Agent（A/B/C/E）可基于本报告交叉验证。
