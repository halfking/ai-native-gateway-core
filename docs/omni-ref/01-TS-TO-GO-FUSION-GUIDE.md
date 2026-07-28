# TypeScript → Go 翻译与融合技能指南（OmniRoute → llm-gateway-go）

> **目的**：为 `docs/omniroute-ref/{phase1,phase2,phase3}` 的 7 个特性方案提供"可参考代码清单 + 翻译方法论 + 融合技能"。
> **源**：OmniRoute v3.8.49（TypeScript / Next.js / SQLite）
> **目标**：llm-gateway-go（Go 1.25 / net/http + Gin / PostgreSQL + Redis）
> **前置阅读**：`00-AUDIT-EXISTING-DOCS.md`（确认方案事实基础）

---

## 0. 核心原则：不是"端口移植"，是"能力嫁接"

OmniRoute 是 **TypeScript 单体**（Next.js + better-sqlite3 + 同步 IPC）。llm-gateway-go 是 **Go 分布式网关**（PostgreSQL + Redis + 并发原生）。两者的运行时、类型系统、并发模型、数据持久化完全不同。

**铁律**：

1. **不照搬 Node.js 运行时结构**。不要建 `src/lib/db/`、不要用 `getDbInstance()`、不要把 SSE 当 IPC。
2. **翻译"规则数据"和"纯算法"，重写"框架胶水"**。正则规则、JSON DSL、评分公式、状态机迁移表这类**声明式/纯函数**可直接翻译；transport、DB、auth、middleware 这类**命令式胶水**要用 Go 原生重写并接入现有装配。
3. **先复用 Go 现有能力，再补 OmniRoute 缺口**。llm-gateway-go 已有比 OmniRoute 更成熟的 Candidate/Router/Executor/telemetry/failover。OmniRoute 的价值在**目录广度、压缩引擎、协议工具**，不在路由状态机。
4. **每个翻译都要有对照测试**。TS 侧的 golden fixture 直接复用为 Go table-driven test 的输入，保证行为对齐。

---

## 1. 通用翻译方法论

### 1.1 类型与数据结构

| TypeScript | Go | 备注 |
|---|---|---|
| `interface X { a?: number }` | `type X struct { A *float64 }` | 可选 → 指针；**nil 表示"未知"，禁止当 0**（见 Candidate 价格字段） |
| `Record<string, T>` | `map[string]T` | 或 struct 若 key 固定 |
| `Promise<T>` | `func() (T, error)` / `<-chan T` | async/await → 显式 error 返回或 channel |
| `class Foo` 单例 | `type Foo struct` + 构造注入 | Go 不用类继承，用**接口 + 组合** |
| `import { z } from "zod"` | 手写校验 或 `github.com/go-playground/validator` | Go 无 Zod 等价物；**输入校验在 handler 层手写或用 validator tag** |
| `crypto.randomUUID()` | `github.com/google/uuid` | session-manager 已用此库 |
| `setInterval(fn, ms)` | `time.Ticker` + goroutine | 必须 `defer ticker.Stop()`；长生命周期 ticker 注意 context cancel |
| `Map` + cache | `sync.Map` 或 `map` + `sync.RWMutex` | OmniRoute 的 LRU cache → Go 用 `golang-lru` 或自建 |
| RegExp 字面量 `/x/gi` | `regexp.MustCompile` | **预编译放包级 `var`**，不要在热路径里反复 compile |

### 1.2 错误处理

```typescript
// TS：try/catch，error 是 any
try { await x(); } catch (e) { log(sanitizeErrorMessage(e)); }
```
```go
// Go：显式 error，必须处理
if err := x(); err != nil {
    slog.Error("x failed", "err", sanitizeErrorMessage(err), "tenant", tenantID)
    return failOpen(original, "x_failed")  // 压缩类必须 fail-open 返回原文
}
```
**关键差异**：Go 没有 panic-catch 的常态错误处理。压缩/路由这类**绝不能 panic 损坏请求**的路径，每个外部调用都要 `err != nil` 分支并 fail-open。

### 1.3 并发模型

```typescript
// TS：单线程事件循环 + Promise.all
const results = await Promise.all(cands.map(c => fetch(c)));
```
```go
// Go：goroutine + WaitGroup + context + channel
ctx, cancel := context.WithCancel(ctx)
defer cancel()
results := make(chan SourceResult, len(cands))
var wg sync.WaitGroup
for _, c := range cands {
    c := c
    wg.Add(1)
    go func() {
        defer wg.Done()
        results <- SourceResult{C: c, R: fetch(ctx, c)}
    }()
}
go func() { wg.Wait(); close(results) }()
```
**Fusion 并行（phase3/07）必须**：每个 candidate 独立 limiter reservation、`context.Cancel` 取消未完成、race test 防 goroutine 泄漏。

### 1.4 SQLite → PostgreSQL 翻译

| OmniRoute (SQLite) | llm-gateway-go (PostgreSQL) |
|---|---|
| `getDbInstance()` 同步 better-sqlite3 | `*pgxpool.Pool` + `context.Context` 透传 |
| `db.prepare(sql).run(...)` | `pool.QueryRow(ctx, sql, args...).Scan(...)` |
| 迁移 `_omniroute_migrations` 表 | `sql/migrations/startup/NNN_*.sql` + golang-migrate |
| 单文件 DB | 多租户 + **RLS**（`app.tenant_id` session var） |

**翻译后必做**：所有新表 `FORCE ROW LEVEL SECURITY` + tenant 隔离 policy（参考 session-manager `migrations/000002_rls_policies`）。

---

## 2. 按特性的"可参考代码清单"

> 每行格式：`OmniRoute 参考文件` → `Go 目标位置` → `翻译方式`

### P1 提供商扩展

| OmniRoute 参考 | 用途 | Go 目标 | 翻译方式 |
|---|---|---|---|
| `src/shared/constants/providers.ts` | 290 provider 定义 | `provider/catalog/`（新建）+ `463_*.sql` seed | **导出 JSON → 幂等 SQL**，不建 Go 常量（见审计修正 #1） |
| `src/shared/validation/providerSchema.ts` | provider Zod 校验 | `provider/catalog/validate.go` | Zod schema → 手写 Go 校验函数（URL https 校验、protocol 枚举、重复键检查） |
| `open-sse/config/providerRegistry.ts` | base URL/auth/model 注册 | `provider/catalog/entry.go` | 只取**数据**（base_url/protocol/auth_kind/models），运行时由 `provider.NewClient().SetDB()` 解析 |
| `open-sse/utils/publicCreds.ts` `resolvePublicCred()` | OAuth client_id/secret | `provider/auth/resolver.go` | **逻辑翻译**：从 env/secret store 读，绝不写明文字面量 |

### R1 高级路由策略

| OmniRoute 参考 | 用途 | Go 目标 | 翻译方式 |
|---|---|---|---|
| `src/shared/constants/routingStrategies.ts` `ROUTING_STRATEGY_VALUES` | 17 策略枚举 | `domains/streaming/executors/routing_strategy.go` | 只取 cost/cache/context/headroom 4 种，做成 `RoutingMode` 字符串常量 |
| OmniRoute 的 cost/cache/headroom 评分（分散在 services） | 评分公式 | `router_strategy.go` `Strategy.Score()` | **公式翻译**为纯函数；权重照搬 phase1/02 §3 的系数 |
| —（无直接对应） | tier/sticky/billing | `router.go` 已有 | **直接复用**，不翻译 |

> llm-gateway-go 的路由比 OmniRoute 成熟（URSM v2 + FpSlots + Bandit）。**只翻译"评分纯函数"，不翻译路由编排**。

### C1/C2/C3/C4 压缩

这是翻译价值最高、对照最严格的领域。OmniRoute 压缩是**声明式规则 + 纯函数**，几乎可 1:1 翻译。

| OmniRoute 参考 | 用途 | Go 目标 | 翻译方式 |
|---|---|---|---|
| `open-sse/services/compression/lite.ts` | 5 条 Lite 规则 | `domains/hooks/compression/lite.go` | **近 1:1 翻译**（见 §3.1 详细对照） |
| `open-sse/services/compression/types.ts` | CompressionMode/Config | `domains/hooks/compression/types.go` | 枚举 + struct 翻译；`*float64` 处理可选 |
| `open-sse/services/compression/cavemanRules.ts` | Caveman 正则规则集 | `domains/hooks/compression/caveman_rules.go` | **正则 + replacement 字面量直接搬**，预编译 |
| `open-sse/services/compression/caveman.ts` | Caveman 引擎 | `domains/hooks/compression/caveman.go` | 翻译 protected-block 抽取/还原 + 规则应用 |
| `open-sse/services/compression/engines/rtk/*.ts` | RTK 工具输出过滤 | `domains/hooks/compression/rtk.go` | 翻译 commandDetector/deduplicator/lineFilter/smartTruncate；**规则 JSON DSL 照搬** |
| `open-sse/services/compression/engines/rtk/filterSchema.ts` | RTK 规则 Zod schema | `domains/hooks/compression/rtk_schema.go` | Zod → 手写校验 |
| `open-sse/services/compression/strategySelector.ts` | 压缩模式选择优先级 | `domains/hooks/compression/strategy_selector.go` | 翻译优先级链：assigned combo > combo override > auto-trigger > default > off |
| `open-sse/services/compression/stats.ts` | 压缩统计 | `domains/hooks/compression/metrics.go`（已存在，扩展） | **复用现有 metrics.go**，只加 stage-level 字段 |
| `open-sse/services/compression/preservation.ts` | 保护块抽取 | `domains/hooks/compression/protected_ranges.go` | 逻辑翻译 |

### M1 MCP Server

| OmniRoute 参考 | 用途 | Go 目标 | 翻译方式 |
|---|---|---|---|
| `open-sse/mcp-server/server.ts` `createMcpServer()` | MCP server 装配 | `mcp/server.go`（新建） | **重写**：Go 用自建 JSON-RPC dispatcher（见 phase2/05 §4.1） |
| `open-sse/mcp-server/schemas/tools.ts` `MCP_TOOLS` | 42 核心工具定义 | `mcp/tools/*.go` | 工具**定义数据**翻译为 Go struct；**handler 重写**调用 Go 侧能力 |
| `open-sse/mcp-server/scopeEnforcement.ts` | 30 scope 授权 | `mcp/scope.go` | scope 字符串列表翻译；判定逻辑接入 `registry.ToolRegistry.IsAllowed`（见审计修正 #3） |
| `open-sse/mcp-server/audit.ts` `logToolCall` | 审计落库 | `mcp/audit.go` | SQLite→PG：建 `mcp_audit` 表 + tenant RLS |
| `open-sse/mcp-server/httpTransport.ts` | SSE/HTTP transport | `mcp/sse.go` `mcp/http.go` | 重写，复用 gateway 已有 SSE 经验 |
| `open-sse/mcp-server/toolCardinality.ts` | 工具去重计数 | `mcp/tool_count.go` | 纯函数翻译 |

> **OmniRoute MCP 用 `@modelcontextprotocol/sdk`**。Go 侧**不依赖该 TS SDK**，自建 JSON-RPC（phase2/05 已给完整伪代码）。工具**定义**可参考，**传输**必须 Go 原生。

### A1 A2A Protocol

| OmniRoute 参考 | 用途 | Go 目标 | 翻译方式 |
|---|---|---|---|
| `src/lib/a2a/taskManager.ts` `A2ATaskManager` | 任务状态机 | `a2a/task/manager.go`（新建） | 状态迁移表 + TTL 清理**近 1:1 翻译**（见 §3.2） |
| `src/lib/a2a/taskExecution.ts` | 任务执行 | `a2a/task/execution.go` | 重写为调用 `Executor.Execute`，不 HTTP 回调自身 |
| `src/lib/a2a/streaming.ts` | SSE 事件格式 | `a2a/http/sse.go` | 事件类型翻译；SSE 用 Go 原生 |
| `src/lib/a2a/skills/*.ts`（6 个） | 6 技能 | `a2a/skills/*.go` | smart-routing 重写为调 Router；其余 5 个只读翻译 |
| `src/app/a2a/route.ts` | JSON-RPC endpoint | `a2a/http/handler.go` | 重写，接入 gateway auth middleware |

### R3 Fusion 路由

| OmniRoute 参考 | 用途 | Go 目标 | 翻译方式 |
|---|---|---|---|
| OmniRoute 智能路由解释（routing explanation） | Fusion metadata | `fusion/coordinator.go` | 只取"解释/cost envelope"输出格式 |
| —（OmniRoute 无完整 Fusion） | panel/parallel/judge | `fusion/{panel,evaluator,judge}.go` | **Go 原创**，OmniRoute 无直接对应；参考 phase3/07 伪代码 |

---

## 3. 重点翻译示例

### 3.1 Lite 压缩：`lite.ts` → `lite.go`（近 1:1）

OmniRoute `applyLiteCompression` 是 5 条顺序规则的纯函数管道，翻译直白：

```go
// domains/hooks/compression/lite.go
package compression

type LiteOptions struct {
    Model                string
    SupportsVision       *bool   // nil = 未指定
    PreserveSystemPrompt bool
}

// 规则 1：空白归一化（对应 collapseWhitespace + collapseNewlineRuns + trimTrailingHorizontalWhitespace）
func collapseWhitespace(body map[string]any, opts LiteOptions) (map[string]any, bool) {
    msgs, ok := body["messages"].([]any)
    if !ok { return body, false }
    applied := false
    out := make([]any, len(msgs))
    for i, m := range msgs {
        msg, _ := m.(map[string]any)
        if opts.PreserveSystemPrompt && msg["role"] == "system" { out[i] = msg; continue }
        content, _ := msg["content"].(string)
        normalized := normalizeMessageWhitespace(content) // collapseNewlineRuns + per-line trim
        if normalized != content {
            applied = true
            msg = cloneMap(msg); msg["content"] = normalized
        }
        out[i] = msg
    }
    if applied { body["messages"] = out }
    return body, applied
}
```

**翻译要点**：
- TS 的 `charCodeAt` 循环 → Go 的 `[]byte` 遍历或 `strings.Map`。
- TS 的 `body.messages.map(...)` 返回新数组 → Go 必须显式 `cloneMap` 避免 map aliasing。
- **安全保护**：每个规则包在 `safeTransform`（phase1/03 §3.2）里，失败 fail-open 返回原 body。
- **对照测试**：用 OmniRoute 同样的输入 JSON 作为 Go test fixture，断言输出一致。

### 3.2 A2A 状态机：`taskManager.ts` → Go（含 TTL 清理）

```go
// a2a/task/manager.go
package task

type State string
const (
    Submitted State = "submitted"
    Working   State = "working"
    Completed State = "completed"
    Failed    State = "failed"
    Cancelled State = "cancelled"
)

// 翻译自 taskManager.ts 的 VALID_TRANSITIONS（phase3/06 §5.2）
var validTransitions = map[State]map[State]bool{
    Submitted: {Working: true, Failed: true, Cancelled: true},
    Working:   {Completed: true, Failed: true, Cancelled: true},
    Completed: {}, Failed: {}, Cancelled: {},
}

// TTL 清理：TS 用 setInterval(60s) + unref → Go 用 time.Ticker + context
func (m *Manager) cleanupLoop(ctx context.Context) {
    ticker := time.NewTicker(60 * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done(): return
        case <-ticker.C: m.cleanupExpired(ctx)
        }
    }
}
```

**翻译要点**：
- TS `setInterval(...).unref()`（不阻止进程退出）→ Go 用 `context.Context` 控制生命周期，main 退出时 cancel。
- **生产用 PG 持久化**（phase3/06 §5.1），内存 store 仅测试用。OmniRoute 的 in-memory + optional SQLite → Go 的 `Store` 接口 + pgStore/memStore 两实现。
- 并发更新必须在事务里 `SELECT ... FOR UPDATE` 检查旧状态（phase3/06 §5.2），TS 单线程无需此。

### 3.3 Caveman 规则：正则字面量直接搬

```go
// domains/hooks/compression/caveman_rules.go
package compression

import "regexp"

// 预编译，对应 cavemanRules.ts 的 CAVEMAN_RULES
var (
    rePleasantries = regexp.MustCompile(`(?i)\b(i'd be happy to|i would be happy to|glad to help|thank you|thanks|no problem|you're welcome|absolutely|certainly|of course|sure)\b[,.!?\s]*`)
    rePoliteFraming = regexp.MustCompile(`(?i)\b(please|kindly|could you please|would you please|can you please|i would like you to|i want you to|i need you to)\b\s*`)
    reHedging = regexp.MustCompile(`(?i)\b(it seems like|it appears that|i think that|i believe that|probably|possibly|maybe it)\b\s*`)
    reFillerAdverbs = regexp.MustCompile(`(?i)(?<![a-z])\b(basically|essentially|actually|literally|simply|currently)\b\s*`)
)
```

**翻译要点**：
- Go `regexp`（RE2）**不支持回溯/lookbehind** `(?<!...)`。TS 用了 lookbehind 的规则（如 `filler_adverbs`、`pleasantries` 的 `(?<!make\s)`）需**改写为非 lookbehind 等价**或拆分匹配。这是少数不能 1:1 的地方。
- replacement 函数（TS `replacement: (match) => map[match]`）→ Go 用 `regexp.ReplaceAllStringFunc`。
- **保护块**（code block / URL / 路径）用 `preservation.ts` 的 `extractPreservedBlocks`/`restorePreservedBlocks` 模式翻译，先抽取占位再应用规则最后还原。

---

## 4. 融合技能：如何接入现有 Go 代码

### 4.1 装配式融合（所有特性的共同模式）

llm-gateway-go 的 `cmd/gateway/main.go` 是**唯一装配点**。每个新特性的融合遵循同一模式：

```text
新建独立 package（不污染 executor/router）
  → 定义接口 + 构造函数（依赖注入，不全局单例）
  → main.go 里 NewXxx(deps) 注入到 Executor/Router/handler
  → feature flag 默认 off，灰度开启
```

**反面教材**：不要像 OmniRoute 那样在 `chatCore.ts` 里直接 import 一堆 service。Go 要保持依赖单向（executor 不依赖 mcp/a2a/fusion，反过来可以）。

### 4.2 压缩融合：Stage/Pipeline 接入 Compressor

现有 `Executor.Compressor`（`executor.go:641`）是单一 trim 分发器。融合方式（phase2/04 §3）：

```text
协议转换完成（prepareRequestBody/finalizeOpenAIUpstreamBody）
  → 现有 context-window trim（transformation.CompressMessagesIfNeeded）
  → 【新增】Pipeline.Apply：Lite → RTK → Caveman（受 protect + budget 门禁）
  → 上游发送
```

**关键融合约束**：
- 接入点用 `Compressor` 暴露的统一 `Apply` 方法，**不要在 executor_chat.go 加第三处 body 改写**。
- retry 时**不重复压缩**：在 `ExecParams` request scope 缓存原始 body + compression event（phase1/03 Step 4）。
- 与 `RecoveryCoord`（`executor.go:781`）、`Memora`（`:661`）做 stage 去重，同一请求不既触发 session compaction 又被报为 Lite（phase2/04 §12）。

### 4.3 路由融合：Strategy 插件接入 planByTier

不重写 `PlanCandidatesWithContext`，只替换 tier bucket 内部排序（phase1/02 Step 3）：

```go
// 现有：planByTier 内用 p2cOrder 或 banditOrder
// 融合后：orderBucket 按 RoutingMode 选择 Strategy.Score 排序
func (r *Router) orderBucket(bucket []provider.Candidate, opts RouteOptions) []provider.Candidate {
    if opts.Mode == RoutingBalanced {
        return p2cOrder(bucket, r)  // 现有行为不变
    }
    // 新增：缓存 score 避免重复计算
    scored := make([]scoredCandidate, len(bucket))
    for i, c := range bucket {
        scored[i] = scoredCandidate{c, r.strategy(opts.Mode).Score(opts.Context, c, opts.Features, r)}
    }
    sort.SliceStable(scored, func(i, j int) bool { return scored[i].score < scored[j].score })
    // ...
}
```

**融合顺序不可变**（phase1/02 Step 4）：去重 → URSM/State 过滤 → health/fp slot → billing round → tier strategy → sticky → protocol affinity → URSM canary。**新策略必须在可用性过滤之后**，否则会把熔断/冷却候选排前面。

### 4.4 MCP/A2A 融合：复用 tool registry + auth

MCP/A2A 不重新造权限，接入现有边界：
- **工具权限**：`registry.ToolRegistry.IsAllowed(tenantID, toolID)`（`registry/tool_registry.go:334`，注意无 error，见审计修正 #3）。
- **工具拦截**：`domains/hooks/tools.ToolInterceptionHook`（`pipeline.Hook`，Priority 100）。
- **认证**：用 gateway 现有 auth middleware，不复制弱化版 API key 判断（phase2/05 §8、phase3/06 §4）。
- **tenant**：从已认证 principal 取，**禁止从 arguments 接受 `tenant_id` 覆盖** context tenant。

### 4.5 融合的 Go 习惯用法

- **接口小而专**：`Strategy`、`Stage`、`SingleCandidateExecutor`、`Skill`、`HeaderResolver`——每个只 1-3 方法。
- **构造注入，拒绝全局**：`NewCompressor()`、`NewRouter(sticky, lim)`、`NewExecutor(...)` 都是显式传依赖；新特性照此办理，不用 `sync.Once` 单例（OmniRoute 的 `SkillRegistry.getInstance()` 反模式）。
- **context 透传**：每个 DB/HTTP/长操作签名第一个参数是 `ctx context.Context`，用于超时/取消。
- **错误不外泄**：上游错误用 `sanitizeErrorMessage`/`safeError`，不返回 DB 连接串/token/绝对路径（phase2/05 §5.2、phase3/06 §6.1）。

---

## 5. 对照测试策略

| 层 | 方法 | 数据来源 |
|---|---|---|
| 单元 | Go table-driven test | **复用 OmniRoute golden fixture**（如压缩 before/after JSON） |
| 集成 | `httptest.Server` mock 上游 | 复用现有 `domains/streaming/executors/*_test.go` 模式 |
| 协议 | contract test（MCP/A2A JSON-RPC） | OmniRoute 的 `__tests__/*.test.ts` 翻译为 Go |
| 行为对齐 | 同输入跑 TS 与 Go，diff 输出 | 压缩/路由评分这类纯函数 |

**命令模板**（每个特性）：
```bash
go test ./domains/hooks/compression -run 'TestLite' -count=1
go test ./domains/hooks/compression -bench 'Benchmark.*Lite' -benchmem
go test ./mcp/... ./a2a/... -race -count=1   # 协议层必须 race
```

---

## 6. 分阶段落地路线（与现有方案对齐）

| 阶段 | 特性 | 主要翻译来源 | 融合点 | 迁移号 |
|---|---|---|---|---|
| 1 (9w) | P1 提供商 | providers.ts → catalog JSON → SQL | `provider.NewClient().SetDB` | 463 |
| 1 | R1 路由策略 | routingStrategies.ts 评分公式 | `Router.orderBucket` | (settings) |
| 1 | C1 Lite | lite.ts 近 1:1 | `Compressor.Apply` | (settings) |
| 2 (12w) | C2 RTK | engines/rtk/*.ts + filter JSON | Pipeline stage | — |
| 2 | C3 Caveman | cavemanRules.ts 正则 | Pipeline stage | — |
| 2 | C4 Stacked | strategySelector.ts 优先级 | Pipeline gate | — |
| 2 | M1 MCP | mcp-server/*.ts 工具定义 | `registry.ToolRegistry` + 新 transport | 464 |
| 3 (9w) | A1 A2A | a2a/*.ts 状态机+技能 | `Executor.Execute` + auth | 465 |
| 3 | R3 Fusion | (Go 原创) | `SingleCandidateExecutor` | — |
| 3 | M2/M3 MCP扩展 | mcp-server schemas 扩展 | M1 之上 | — |

> 工期/依赖/KPI 见 `docs/omniroute-ref/00-IMPLEMENTATION-ROADMAP.md`。

---

## 7. 风险与反模式清单

| 反模式 | 正确做法 |
|---|---|
| 把 290 provider 写成 Go 常量 | 生成 seed SQL，DB 做单一事实源 |
| 在 executor 里加第三处 body 改写 | 走 `Compressor.Apply` 统一 stage |
| 照搬 `@modelcontextprotocol/sdk` | Go 自建 JSON-RPC dispatcher |
| TS lookbehind 正则直接搬 | 改写为 RE2 兼容（Go regexp 不支持回溯） |
| `getInstance()` 全局单例 | 构造注入 |
| panic 当错误传播 | 显式 error 返回 + fail-open |
| nil 价格当 0 成本（免费） | unknown-cost penalty（phase1/02 §3.1） |
| 从 MCP/A2A arguments 接 tenant_id | 从认证 context 取 |
| 把 request ID 当 Prometheus label | 只用低基数标签（stage/mode/provider_family） |
| 流式请求盲目多路并发（Fusion） | 第一版 stream 强制 staged（phase3/07 §5） |

---

## 8. 文档导航

| 文档 | 作用 |
|---|---|
| `docs/omniroute-ref/00-IMPLEMENTATION-ROADMAP.md` | 总路线图 / 工期 / KPI |
| `docs/omniroute-ref/phase1/0{1,2,3}-*.md` | P1/R1/C1 详细设计 |
| `docs/omniroute-ref/phase2/0{4,5}-*.md` | C2-C4/M1 详细设计 |
| `docs/omniroute-ref/phase3/0{6,7}-*.md` | A1/R3 详细设计 |
| **`docs/omni-ref/00-AUDIT-EXISTING-DOCS.md`**（本文档同目录） | 方案 vs 真实代码核对 + 3 处修正 |
| **`docs/omni-ref/01-TS-TO-GO-FUSION-GUIDE.md`**（本文档） | 翻译方法论 + 参考代码清单 + 融合技能 |

**使用顺序**：审计(00) → 融合指南(01，本文) §1 通用方法论 → 按特性读对应 phase 方案，对照融合指南 §2 该特性的参考清单。
