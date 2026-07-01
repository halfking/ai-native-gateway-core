# Unknown 错误根因分析与优化方案

**报告日期**: 2026-07-01
**分析对象**: llm-gateway-go (`main` 分支)
**请求ID样本**: `b02dcd1e25ef5d7a88f3b24e75779eec`
**错误现象**: `request_logs.error_kind = 'unknown'`
**协作分支**: `remotes/github/main` 及 `fix/routing-error-transparency`
                  ⚠️ **本仓库的 `github` 分支是独立的并列分支, 不与 `main` 合并**。
                  下文借鉴的是 GitHub 分支已经落地、经过生产验证的修复思路,
                  本次在 `main` 分支上独立实现, 不直接 cherry-pick。

---

## 一、问题摘要 (TL;DR)

| 项 | 结论 |
|---|---|
| **根本原因** | 路由层 / 数据访问层多处**伪装/吞掉错误**, 运维只能看到无上下文的 `unknown` |
| **主要路径** | auto-route decider 失败 / RouteNodeStore Redis 错误 / DBProfileStore 查询错误 / session preference 错误 全部被静默降级 |
| **误判风险** | DB 抖动 / Redis 抖动 / 配置缺失 与 正常的 fallback 行为无法区分, 监控告警失效 |
| **借鉴来源** | GitHub 分支 `fix/routing-error-transparency` (commit `6bc47238` + `9f438d70`) + `fix/disguising-as-no_candidate` (`ec4f8c52`) |
| **main 已修** | ✅ `ec4f8c52` (DB 错误不再伪装为 `no_candidate`) + `errorKindOrFallback` (handler.go:3475) |
| **main 未修** | ❌ auto-route decider 失败伪装 / Redis 错误吞掉 / SessionPreference 错误吞掉 / DBProfileStore 错误级别低 |

---

## 二、"unknown" 错误的所有可能路径 (代码证据)

### 路径 A: `ExecuteError.LastKind == ""` 时的兜底返回

**位置**: `domains/streaming/handler.go:3456-3467`

```go
func mapExecuteErrorToKind(err *executors.ExecuteError) string {
    if err == nil { return "" }
    if err.LastKind != "" { return string(err.LastKind) }  // 1. 有真实原因 → 用真实原因
    if err.Tried == 0 { return "no_candidates" }            // 2. 没尝试过 → 无候选
    return "unknown"                                        // 3. ⚠️ 兜底: 尝试了但没拿到 kind
}
```

**触发场景**: 所有候选都被尝试了 (`Tried > 0`), 但 executor 在循环里没有给 `lastKind` 赋值
(`executor.go:1171`)。

**已被缓解** (main 分支已包含): `errorKindOrFallback` (handler.go:3475-3480) 把 `"unknown"` 替换成
`"model_not_found"` 再写入 DB。所以**理论上** main 分支不应再产生
`error_kind='unknown'` 的新行。

```go
func errorKindOrFallback(kind string) string {
    if kind == "" || kind == "unknown" {
        return "model_not_found"
    }
    return kind
}
```

⚠️ **但是这只是治标**。它把一个真实的"无法分类"的问题掩盖成了"model_not_found",
运维依然拿不到真实原因。"unknown" 在 DB 中残留的两种可能:

1. **历史数据**: 该修复之前的版本写入的 (例如未升级的生产库)
2. **绕过 `errorKindOrFallback` 的旁路**: 在 `_to-be-deprecated/relay/handler.go`、
   老 `messages.go`/`responses.go` 路径、`candidate_failure_logs` 等

### 路径 B: 候选可用性评估时, 错误被静默吞掉

**位置**: `domains/streaming/executors/executor.go:689-694` (UnifiedProbe 前置检查)

```go
for _, c := range params.Candidates {
    reason := c.UnavailableReason()
    if reason == "" {
        reason = "unknown"  // ⚠️ 这里写入的是 UnavailableReason, 不是 error_kind
    }
    reasonCounts[reason]++
}
```

`reason="unknown"` 写入的是路由层的 `UnavailableReason`, 与 `error_kind` 不同列, **不会**
直接污染 `request_logs.error_kind`, 但会出现在 `decision_trace.failure_reason`,
运维看不到候选为什么被过滤。

### 路径 C: auto-route decider 失败时静默降级 — **main 分支现存的关键 bug**

**位置**: `domains/streaming/auto_route.go:308-319`

```go
decision, err := h.decider.DecideWithFeatureFlags(r.Context(), sigs, apiKeyID, headerProfile, taskHint, sessionID)
if err != nil {
    slog.Warn("auto-route: decider failed, falling back",  // ⚠️ 仅 Warn 级别
        "error", err,
        "task_hint", string(taskHint),
        "profile_header", headerProfile,
    )
    // Fall back to a default chat model rather than 502 — clients
    // should not be punished for the gateway's transient issues.
    reqBody.Model = autoFallbackModel()
    return rewriteBodyWithModel(rawBody, autoFallbackModel()), nil, false  // ⚠️ 静默降级
}
```

**实际影响**:
- `DBProfileStore` (查用户画像)、`Index.Refresh` (查凭证模型索引)、Redis (查偏好) 任意一个
  出问题, 用户请求都被改写成 fallback 模型 (`autoFallbackModel()`)
- 运维在日志中看到的是**正常的"用户请求了 fallback 模型"**, 监控/告警看不到 DB/Redis 抖动
- 用户体感: "我的高级模型被替换了" — 但没有任何日志说明原因

### 路径 D: RouteNodeStore Redis 错误被吞掉 — **main 分支现存**

**位置**: `routing/route_node_store.go` (全文件)

```go
// 当前 main 分支代码 (Issue)
val, err := s.client.Get(ctx, key).Result()
if err == redis.Nil {
    return nil, false, nil
}
if err != nil {
    return nil, false, err  // ⚠️ 错误被返回但未记录日志
}
// IsUsable:
state, found, err := s.Get(ctx, credID, model)
if err != nil || !found {
    return true  // ⚠️ Redis 故障被当作"节点可用"返回
}
```

**实际影响**: Redis 抖动时, `IsUsable` 错误地认为所有节点都可用, 故障路由不会被冷却,
请求会持续打到故障节点上, 错误率上升但告警沉默。

### 路径 E: SessionPreference 错误被吞掉 — **main 分支现存**

**位置**: `routing/session_preference.go:GetCredentialID`

```go
func (s *SessionPreferenceStore) GetCredentialID(ctx context.Context, sessionID string) (int, bool) {
    entry, found, _ := s.Get(ctx, sessionID)  // ⚠️ 用 _ 丢弃错误
    if !found { return 0, false }
    return entry.CredentialID, true
}
```

**实际影响**: Redis 故障时, 会话偏好被静默丢弃, 用户每次请求都重新走完整路由 (慢 + 资源浪费),
但运维不知道这是 Redis 的问题。

### 路径 F: DBProfileStore 查询错误级别不够 — **main 分支现存**

**位置**: `autoroute/decision.go:DBProfileStore.Get`

```go
if err != nil {
    if !errors.Is(err, pgxNoRows()) {
        slog.Warn("DBProfileStore.Get query failed", "error", err, "api_key_id", apiKeyID)  // ⚠️ Warn
    }
    return "", false
}
```

**实际影响**: DB 抖动被记为 Warn, 不触发 ERROR 告警, 但下游路由决策会因此走 fallback。

### 路径 G: autoroute Index.Refresh 数据库错误级别低 — **main 分支现存**

**位置**: `autoroute/index.go:Refresh`

```go
if err != nil {
    return fmt.Errorf("query credential_model_index: %w", err)  // ⚠️ 无 ERROR 日志
}
```

**实际影响**: 后台索引刷新失败被静默吞掉, 路由数据陈旧, 但运维看不到。

---

## 三、GitHub 分支已落地的修复 (借鉴清单)

> GitHub 分支 (`remotes/github/main`) 是独立的并列分支, 不与 main 合并。
> 以下 commit 是 GitHub 分支已经过生产验证的修复, 我们借鉴其**思路**和**API 契约**,
> 在 main 分支上独立实现。

### 借鉴清单

| 借鉴思路 | GitHub commit | 修复点 | main 分支当前状态 |
|---|---|---|---|
| **DB 错误不再伪装为 `no_candidate`** | `ec4f8c52` | `classifyRoutingError()` 区分 `routing_not_configured` / `routing_connection_error` / `routing_schema_error` / `routing_database_error` | ✅ **已修复** (main 分支有 `domains/streaming/routing_errors.go`, 且已增强了精确匹配) |
| **auto-route decider 失败不再静默降级** | `6bc47238` (`relay/auto_route.go`) | 改为 `shouldFail=true`, 调用方返回 502 | ❌ **未修复** — `domains/streaming/auto_route.go:308-319` 仍静默降级 |
| **Redis 错误记录 ERROR 日志** | `6bc47238` (`routing/route_node_store.go`) | 区分"未找到"与"Redis 错误", 后者记 ERROR | ❌ **未修复** |
| **RouteNodeStore 错误时不再假定节点可用** | `6bc47238` (`routing/route_node_store.go`) | `IsUsable` 失败时仍返回 true 但已记 ERROR | ❌ **未修复** |
| **filterByRouteNodeHealth 宽容模式 + 错误日志** | `6bc47238` + `9f438d70` (`routing/router.go`) | 当所有节点被过滤时, 用宽容模式重试, 且记录错误 | ❌ **未修复** |
| **SessionPreference.GetCredentialID 不再静默丢弃** | `9f438d70` (`routing/session_preference.go`) | 错误改为 `slog.Debug` 记录, 不再 `_` | ❌ **未修复** |
| **DBProfileStore.Get 数据库错误级别 → ERROR** | `6bc47238` (`autoroute/decision.go`) | 区分"未找到"与"DB 错误" | ❌ **未修复** |
| **autoroute.Index.Refresh 数据库错误级别 → ERROR** | `6bc47238` (`autoroute/index.go`) | 错误前增加 `slog.Error` | ❌ **未修复** |

---

## 四、main 分支现状对比

### 4.1 main 分支已包含的好改动

✅ **DB 错误透明化 (`ec4f8c52`)**:
- `domains/streaming/handler.go:1335-1342` - GetCandidates 失败时调用 `classifyRoutingError`
- `domains/streaming/messages.go:392-403` - 同上
- `domains/streaming/responses.go:335-346` - 同上
- `domains/streaming/routing_errors.go` - 新增分类函数

✅ **`errorKindOrFallback` 把 `unknown`/`""` 替换成 `model_not_found`**:
- `domains/streaming/handler.go:3475-3480`

✅ **路由决策 trace 记录 candidate filter reasons** (含 "unknown"):
- `domains/streaming/executors/executor.go:689-694`

### 4.2 main 分支仍存在的问题 (优先级排序)

| 优先级 | 问题 | 文件:行 | 借鉴思路 |
|---|---|---|---|
| **P0** | auto-route decider 失败静默降级 | `domains/streaming/auto_route.go:308-319` | GitHub `6bc47238` |
| **P0** | RouteNodeStore Redis 错误吞掉 | `routing/route_node_store.go:93-100, 208-214` | GitHub `6bc47238` |
| **P1** | SessionPreference 错误吞掉 | `routing/session_preference.go:148-156` | GitHub `9f438d70` |
| **P1** | DBProfileStore 错误级别低 | `autoroute/decision.go:442-450` | GitHub `6bc47238` |
| **P1** | autoroute Index.Refresh 无 ERROR 日志 | `autoroute/index.go:210-217, 228-234` | GitHub `6bc47238` |
| **P2** | filterByRouteNodeHealth 缺少宽容模式 | `routing/router.go:100-150` | GitHub `6bc47238` + `9f438d70` |

---

## 五、优化方案设计

### 设计原则

1. **借鉴不照搬**: GitHub 分支的修复思路可以借鉴, 但 API 契约 (函数名、字段、错误码) 要与
   main 分支现有约定保持一致
2. **分层记录**: ERROR 日志用于监控告警, DEBUG 日志用于详细诊断
3. **不回归**: 已通过的测试不能破坏, 新增的测试要明确覆盖修复点
4. **可回滚**: 每个修复是独立 commit, 出问题可单独 revert

### 5.1 方案结构 (3 个独立 commit)

#### Commit 1: `fix(routing): 透明化 Redis 错误 (借鉴 github 分支)`

**目的**: 让 Redis 故障不再被吞掉

**修改文件**:
1. `routing/route_node_store.go`:
   - `Get()`: Redis 错误前加 `slog.Error`
   - `IsUsable()`: 区分 "未找到" 与 "Redis 错误", 后者记 ERROR 但仍降级返回 true
2. `routing/session_preference.go`:
   - `GetCredentialID()`: 不再用 `_` 丢弃错误, 改为 `slog.Debug` 记录

**借鉴自**: GitHub `6bc47238` + `9f438d70`

**回归测试**:
- `routing/route_node_store_test.go`: 新增 `TestIsUsable_RedisError` 验证 Redis 错误时
  返回 true 且日志记录
- `routing/session_preference_test.go`: 新增 `TestGetCredentialID_RedisError`

#### Commit 2: `fix(autoroute): 透明化数据库错误 (借鉴 github 分支)`

**目的**: 让 DB 抖动不再被伪装

**修改文件**:
1. `autoroute/decision.go`:
   - `DBProfileStore.Get()`: 区分 `pgxNoRows` (正常) 与 DB 错误, 后者升级为 `slog.Error`
2. `autoroute/index.go`:
   - `Refresh()`: DB 查询/迭代错误前加 `slog.Error`

**借鉴自**: GitHub `6bc47238`

**回归测试**:
- `autoroute/decision_test.go`: 新增 `TestDBProfileStore_DBError` 验证错误级别

#### Commit 3: `fix(auto-route): 不再静默降级, 透明化 decider 失败`

**目的**: auto-route decider 失败时返回 502, 让用户和运维知道真实原因

**修改文件**:
1. `domains/streaming/auto_route.go`:
   - `maybeResolveAuto()`: decider 失败时改为 `slog.Error` + 返回 `shouldFail=true`
   - 调用方 (`domains/streaming/handler.go:1152`) 处理 `shouldFail=true` 路径,
     返回 502 + 透明错误码

**借鉴自**: GitHub `6bc47238` (`relay/auto_route.go` 的修复模式)

**回归测试**:
- `domains/streaming/auto_route_test.go`: 新增 `TestMaybeResolveAuto_DeciderFails` 验证
  返回 `(nil, nil, true)` 而非降级

### 5.2 配套的可观测性增强 (可选, 单独 commit)

#### Commit 4: `feat(observability): 暴露 routing_layer_error 指标`

**目的**: 让运维能在 dashboard 上看到 "decider/Routing/Redis 错误次数"

**修改文件**:
- 新增 `routing/metrics.go`: 计数器 `routing_layer_errors_total{component="decider|route_node|session_pref|db_profile"}`
- 在 Commit 1/2/3 的修复点调用 `metrics.Inc(...)`

---

## 六、验证计划

### 6.1 单元测试

```bash
go test ./routing/... ./autoroute/... ./domains/streaming/... -count=1
```

### 6.2 集成测试 (本地)

```bash
# 模拟 DB 抖动场景
go test ./domains/streaming/... -run TestRoutingError_DisconnectedDB -count=1

# 模拟 Redis 抖动场景
go test ./routing/... -run TestRouteNodeStore_RedisDown -count=1
```

### 6.3 Lint

```bash
golangci-lint run --config .golangci.yml ./routing/... ./autoroute/... ./domains/streaming/...
```

### 6.4 部署验证 (生产)

部署到 SERVER 184 (测试) → 模拟 Redis 抖动 → 验证:
- 日志中能找到 `slog.Error("RouteNodeStore.Get: Redis access error", ...)`
- 请求被路由到正确的 fallback, 而非静默改写
- Dashboard 出现 `routing_layer_errors_total` 指标

---

## 七、风险评估

| 风险 | 缓解措施 |
|---|---|
| 修复 3 (auto-route 失败返回 502) 可能影响自动路由用户的体验 | 仅 decider 失败时返回 502, 正常路径不受影响; 客户端可重试 |
| 错误日志变多可能淹没告警 | 用结构化标签 (`component=decider` 等) 便于过滤 |
| `_to-be-deprecated` 路径可能仍存在同样的 bug | 已标记为废弃, 后续 cutover 时一并修复 |

---

## 八、问题追踪

- **报告 ID**: 2026-07-01-unknown-error
- **关联 issue**: 无
- **关联 commit**:
  - 借鉴: `6bc47238` (GitHub, 不合并), `9f438d70` (GitHub, 不合并), `ec4f8c52` (GitHub, 已合并入 main)
  - 即将新增: 3 个 commit (按本方案 5.1 节)
- **验证负责人**: TBD
- **目标部署**: SERVER 71 (生产), SERVER 184 (测试)