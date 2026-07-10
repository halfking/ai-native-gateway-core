# Hook插件化重构方案 - 第三轮审计发现报告

**审计日期**: 2024-07-09
**审计性质**: 对抗性深度审计（critical/adversarial），质疑前两轮结论
**审计对象**: v4方案（docs/plugin-system-implementation-plan-v4.md）
**审计结论**: ⚠️ **方案存在严重设计缺陷，不能按现状实施。需重大修正（v5）。**

---

## ⚠️ 重要说明：前两轮审计的局限性

前两轮审计（report.md + verification-round2.md）的主要工作是**确认**方案，所以得出"22/22通过"的乐观结论。第三轮采用**对抗性**方法，专门寻找会让实施崩溃的问题。

结果是：发现了 **9个严重问题**，其中4个是 CRITICAL（致命级），会直接导致实施失败。前两轮遗漏这些问题的原因是：审计深度停留在接口名称层面，没有逐一对照真实的 hook 构造函数、没有验证 DBHandle 的方法签名、没有核实迁移运行器是否存在。

---

## 审计发现汇总

| # | 严重度 | 问题 | 影响 |
|---|--------|------|------|
| F1 | 🔴 CRITICAL | HookExtension 无法承载现有 hook 的构造依赖 | 23个 hook 中约18个无法迁移 |
| F2 | 🔴 CRITICAL | 漏掉 `response.ResponseInterceptor` 接口 | 3个生产 hook 完全在模型之外 |
| F3 | 🔴 CRITICAL | DBHandle 无法表达事务/RLS | 插件无法参与租户隔离（安全问题） |
| F4 | 🔴 CRITICAL | 迁移运行器根本不存在 | 里程碑B 不可实现 |
| F5 | 🟠 HIGH | budget-guard 的数据源不存在 | 验证插件 E' 是空壳 |
| F6 | 🟠 HIGH | 单连接池无隔离 | 一个坏插件拖垮整个网关 |
| F7 | 🟠 HIGH | 里程碑工作量被低估约 2 倍 | 6 周实际需 11+ 周 |
| F8 | 🟡 MEDIUM | 运行时 Has() 删除理由事实错误 | 依据被推翻 |
| F9 | 🟡 MEDIUM | HealthCheck 对无状态 hook 是噪音 | 强制要求是设计异味 |

---

## 🔴 F1: HookExtension 无法承载现有 hook 的构造依赖（致命）

### 发现

v4 的核心论点是"HookExtension = Extension + 完整 pipeline.Hook，1:1 映射零魔法"。**这是错误的。** "1:1映射"只在 Execute/Priority/Enabled/OnError 这 5 个方法层面成立，但**构造期依赖**无法通过 `ExtensionContext` 传入。

逐个核对了现有 hook 的构造函数（grep `var _ pipeline.Hook` 得到 25 处断言/23 个类型）。约 18 个 hook 需要的依赖**不在 ExtensionContext 的任何字段里**：

| Hook | 构造依赖 | ExtensionContext 缺失 | 文件:行 |
|---|---|---|---|
| SessionAuditHook | `*FastDetector`, `*eventbus.MemoryBus` | 两者都没 | sessionaudit/hook.go:31 |
| ApprovalGateHook | `*pending.Store`, `*ApprovalManager`, `*MemoryBus` | 3个自定义依赖 | approval_gate.go:31 |
| ApprovalHook | `*ApprovalManager`, `*CacheUpdateHook`, `ApprovalNotifier` | 全缺 | approval_hook.go:90 |
| CacheLookupHook | `cache.Store` | 缺 | cache/hook.go:43 |
| SecurityHook | `*Registry`（含7个plugin） | 缺 | security/hook.go:25 |
| RoutingHook | `Router`（nil会panic） | 缺 | routing/hook.go:28 |
| ModeHook | `GoalStore`, `LLMCaller`, `HistoryStore` | 4个依赖全缺 | goal/mode_hook.go:153 |
| ... | （约18个） | | |

### 根本原因

`ExtensionContext{Storage, Metadata, Events, LLM, Settings, Blobs}` 是**通用服务句柄**，而真实 hook 需要**特定类型的依赖**（如 `*sessionaudit.FastDetector`、`*compression.SessionCache`）。这是两类完全不同的东西，通用 context 无法替代。

### 雪上加霜：hook→hook 类型依赖

更严重的是，**一个 hook 直接持有另一个 hook 的实例**：
- `ApprovalHook.cacheUpdator *CacheUpdateHook`（approval_hook.go:80），调用 `cacheUpdator.UpdateApprovalID(...)`（:173）
- `AuditHook` 构造时接收 `goalHook.History()`（goal/audit_hook.go:64 + goal_control.go:189）

这种"插件 A 持有插件 B 的实例并调用其方法"的需求，**正是 v4 删除的 ServiceRegistry 应该解决的**。`depends_on` 只解决顺序，不解决"把 B 的实例传给 A"。

### 修复方向

**必须引入"构造依赖注入"机制**，不能只靠通用 ExtensionContext。三个可选方案（详见 v5 修复章节）。

---

## 🔴 F2: 漏掉 `response.ResponseInterceptor` 接口（致命）

### 发现

v4 的整个心智模型是 `HookExtension = pipeline.Hook`。但**3 个生产 hook 根本不是 pipeline.Hook**，而是 `response.ResponseInterceptor`：

```go
// domains/hooks/response/types.go:71
type ResponseInterceptor interface {
    InterceptNonStream(ctx, req *InterceptRequest) (*InterceptResult, error)
    InterceptStreamChunk(ctx, chunk []byte, meta *StreamMeta) (*ChunkResult, error)
    InterceptStreamEnd(ctx, meta *StreamMeta) (*EndResult, error)
}
```

- `goal.ModeHook`（mode_hook.go:172,358,370）
- `goal.AuditHook`（audit_hook.go:58,64）
- `handoff.TriggerHook`（trigger_hook.go:222,241,247）

它们在 `cmd/gateway/goal_control.go:237` 被组装成拦截器**链**，而不是 pipeline stage：
```go
interceptors := []response.ResponseInterceptor{goalHook, auditHook, handoffHook}
```

### 影响

HookExtension 模型完全无法表达这 3 个插件。它们处理的是**流式响应**（SSE chunk），和 pipeline.Hook 的"整请求"模型不同。这是 v4 设计的最大盲区。

### 修复方向

必须新增第三种 Extension：`InterceptorExtension`（对齐 ResponseInterceptor）。

---

## 🔴 F3: DBHandle 无法表达事务/RLS（致命，且是安全问题）

### 发现

v4 宣称 DBHandle（3方法：QueryRow/Query/Exec）"像 ApprovalDBTX，pgxmock 可替换"。**这是事实错误：**

- `ApprovalDBTX`（approval_manager.go:41-44）实际是 **`BeginTx` + `Exec`**，**没有** QueryRow/Query
- `analysis.DB`（analysis/db.go:17）才是 QueryRow/Query/Exec，但**它无法表达事务**

v4 把两个**不同的**接口合并成了一个**虚构**的接口。

### 致命后果：无法参与租户隔离（RLS）

代码库的租户隔离（RLS）模型是**事务级 GUC**：每个租户写入在事务内执行 `SET LOCAL app.current_tenant = ...`（approval_manager.go:384, bus/publisher.go:75 等，**30+ 处**）。DBHandle 没有 `BeginTx`，所以**插件无法设置租户 GUC，无法满足 RLS 契约**。这是安全设计漏洞，不是小事。

### 雪上加霜：outputcompliance 是 database/sql，不是 pgx

里程碑 E 想用 Storage.DB() 替换 outputcompliance 的裸 `*sql.DB`。但 DBHandle 是 pgx 形状（返回 pgx.Row），而 outputcompliance 是 database/sql 形状（ExecContext/QueryRowContext）。这是**驱动迁移**，不是 drop-in。里程碑 E 不是 1.5 周能完成的。

### 修复方向

- DBHandle 必须支持事务（加 `BeginTx`，或对齐 ApprovalDBTX）
- 必须显式文档 RLS 参与机制（插件如何安全写租户数据）
- outputcompliance 迁移必须重估为 driver 迁移工作量

---

## 🔴 F4: 迁移运行器根本不存在（致命）

### 发现

里程碑 B 说"复用 db.go 的 ensure*Schema 模式"。**这个模式不能复用，因为根本没有通用运行器：**

- `db/db.go:60-166` 的 `Open()` 是**硬编码的 Go 函数链**（依次调用 ~13 个 `ensure*` 方法），不是目录扫描/embed.FS
- `db/db.go` 全文 **2168 行**，有 ~30 个手写的 `ensure*Schema` 函数（每个表一个），**没有** `schema_migrations` 版本账本表
- `db/migrations/` 只有 5 个文件；db.go 注释里引用的 013/120/306 等迁移文件**在本仓库不存在**（属于另一个服务）

### 影响

里程碑 B 不是"复用模式"，而是**从零构建迁移运行器**，包括：版本账本表、顺序保证、幂等性、失败恢复（Postgres 事务 DDL 不简单：`CREATE INDEX CONCURRENTLY` 不能在事务里；中途失败重放需要逐语句跟踪）、Down/回滚正确性验证。

### 现实工作量

参照 db.go 的复杂度，生产级迁移运行器约需 **3 周**，不是 v4 估计的 1.5 周。

### 修复方向

里程碑 B 拆分为 B1（设计+账本表+运行器核心）和 B2（插件迁移集成），重估 3 周。

---

## 🟠 F5: budget-guard 的数据源不存在

### 发现

E' 验证插件 budget-guard 的设计是"读 `Storage.KV().Lookup("usage:"+month)`，纯计算"。但 **USD 成本根本不在 KV 里**，而在 `usage_ledger_hot.cost_usd`（telemetry/client.go:535 写入）。

聚合读法是 `SELECT SUM(cost_usd) ... FROM usage_ledger_with_current_month GROUP BY tenant_id`（admin/usage.go:100），依赖一个视图。

### 影响

budget-guard 要么：
1. 直接查 `usage_ledger_hot`（那就不是"读KV+纯计算"，是分区热表聚合查询，性能敏感）
2. 需要一个**新的后台 worker 聚合 usage → KV**（计划中完全没列出）
3. 读一个永不被写入的 KV（成本护栏变成空壳）

v4 选 budget-guard 的理由是"逻辑简单只读KV"（:445），**这个前提是假的**。真正实现至少需 2 周。

### 修复方向

E' 换一个真正简单的插件（见 v5 修复），或为 budget-guard 补充 usage 聚合 worker 里程碑。

---

## 🟠 F6: 单连接池无隔离（生产安全）

### 发现

整个进程只有**一个** `*pgxpool.Pool`（MaxConns=32, db.go:29），被 50+ 调用点共享，包括热路径写 `usage_ledger_hot`。v4 的 Storage.DB() 挂在同一个池上，DBHandle **没有查询超时、没有每插件连接预算、没有熔断器**。

### 影响

一个写得烂的插件（慢查询/泄漏 Rows 游标）会耗尽 32 连接，**整个网关**（包括关键的热请求路径）被拖慢。这是典型的 3am 告警场景。v4 风险表完全没提这个。

### 修复方向

- DBHandle 的所有方法强制 `context.WithTimeout`
- 或为插件分配**独立子池**（带连接上限）
- 风险表必须补充此项

---

## 🟠 F7: 里程碑工作量低估约 2 倍

逐项核实：

| 里程碑 | v4 估计 | 现实 | 低估原因 |
|---|---|---|---|
| A1 | 1周 | 1-1.5周 | 低估了 JSON Schema/权限/迁移验证 |
| A2 | 1周 | 1周 | 合理 |
| B | 1.5周 | **3周** | 迁移运行器从零构建（F4） |
| C | 1.5周 | **2.5周** | main_pipeline.go 接入不是"末尾追加"，是逐 phase 插入（F7详述） |
| D | 1周 | **2.5周** | 4端点+metrics+health+settings+modules 应拆 D1/D2/D3 |
| E' | 1周 | **2周** | budget-guard 数据源不存在（F5） |
| **总计** | **6周** | **~11周** | 低估约 2 倍 |

### C 的具体问题："末尾追加"是错的

`buildV2DispatchPipeline`（main_pipeline.go:268-487）有 **24 个 AddStage**，跨多个 phase，有 ~15 个条件守卫。一个 `pre_routing` 插件"追加在末尾"会在 `return p` 之后，**永远不执行**。必须在正确的 phase 边界插入（约 6 处外科手术式插入）。v4 约束"只追加不重写"（:540）与"pre_routing 插件要在 routing 之前运行"**直接矛盾**。

---

## 🟡 F8: 运行时 Has() 删除理由事实错误

v4（:116）说"现有只1处跨模块查询：format_conversion.enabled"，以此删除运行时 Has()。**这处引用是错的**：`executor_chat.go:887` 的 `ProviderSettings.GetBool(ctx, providerID, "format_conversion.enabled")` 是按 **providerID** 查 provider 设置，**不是插件名查询**，不是依赖查询。真正的跨插件运行时查询实际是 **0 个**。

虽然计数错了，但"0个"反而更支持删除——**除了** v4 自己的 budget-guard：如果 budget-guard 想在 output-compliance 健康时才联动，它需要区分"配置启用但健康检查失败"和"真正运行中"，这需要运行时 `Has()`。v4 把它推到 F+，但 E' 就需要了。

### 修复方向

`Has(name)` 实现成本极低（map 查找），应在里程碑 A 提供，而非 F+。

---

## 🟡 F9: HealthCheck 对无状态 hook 是噪音

v4 把 `HealthCheck(ctx) error` 设为 Extension 基础契约的**强制方法**，里程碑 D 还做"N次失败→Disable"。但约 15 个 hook 是无状态的（compression/routing/transform/client-identity/stream/tracing...），它们的 HealthCheck 永远返回 nil（功能形同虚设），或者因瞬时噪音误判 Disable。

### 修复方向

HealthCheck 设为**可选接口**（`Healthchecker`，type assertion），不放进基础 Extension。

---

## v5 修复方案（核心要点）

### 修复 F1: 引入构造依赖注入

不能只用通用 ExtensionContext。引入**三层依赖机制**：

```go
type ExtensionContext struct {
    // 通用服务（保留）
    Storage Storage
    LLM     LLMClient
    // ...
    
    // 新增：类型化服务注册表（解决 hook→hook 类型依赖，如 ApprovalHook→CacheUpdateHook）
    Services ServiceRegistry  // F1: 必须恢复（部分），用于插件间类型化调用
    
    // 新增：运行时查询（解决 budget-guard 需要 output-compliance 健康状态）
    Plugins PluginLookup      // F8: Has(name) 提前到 A
}

// 新增：构造器模式，解决"特定类型依赖无法通过通用 context 传入"
// 插件用 Register(constructor) 注册一个工厂函数，host 用真实依赖调用
type Constructor func(deps Deps) (Extension, error)
```

**关键转变**：v4 假设"所有依赖走 ExtensionContext"——这是错的。v5 承认：通用服务走 context，**特定类型依赖走构造期注入**（工厂模式，复用现有 main_pipeline.go 的 nil-check 模式）。

### 修复 F2: 新增 InterceptorExtension

```go
// 第三种 Extension，对齐 response.ResponseInterceptor
type InterceptorExtension interface {
    Extension
    InterceptNonStream(ctx, req *InterceptRequest) (*InterceptResult, error)
    InterceptStreamChunk(ctx, chunk []byte, meta *StreamMeta) (*ChunkResult, error)
    InterceptStreamEnd(ctx, meta *StreamMeta) (*EndResult, error)
}
```

### 修复 F3: DBHandle 支持事务 + RLS

```go
type DBHandle interface {
    QueryRow(...) Row
    Query(...) (Rows, error)
    Exec(...) (CommandTag, error)
    BeginTx(ctx, opts) (Tx, error)  // 必须加，否则无法 RLS
}

// 显式 RLS 助手
func (s Storage) TenantScope(ctx, tenantID) (context.Context, error)  // 设置 GUC
```

outputcompliance 迁移（E）重估为 **database/sql → pgx 驱动迁移**，单独评估。

### 修复 F4: 迁移运行器单独里程碑

里程碑 B 拆为：
- B1（2周）：设计+`plugin_migrations` 账本+运行器核心+失败恢复
- B2（1周）：插件迁移声明式集成+跨插件顺序保证

### 修复 F5: E' 换真正简单的插件

放弃 budget-guard（数据源不存在）。改为 **request-logger** 或 **header-injector**：
- 只读 `env.Metadata`（已有数据，无外部依赖）
- 写一个 Verdict 或注入 header
- 真正"纯计算无依赖"，1 周可达

或保留 budget-guard 但补一个 **usage 聚合 worker 里程碑**（额外 1 周）。

### 修复 F6: 连接池隔离

DBHandle 所有方法强制 `context.WithTimeout(stmtTimeout)`。高风险插件走独立子池（连接上限可配）。

### 修复 F7: 里程碑重估

总工期从 6 周调整为 **11-12 周**（核心），gRPC/复杂迁移仍推迟。或**砍范围**：v5 只做 A1+A2+B1+C（最小进程内框架）+ E'（简单插件），验证后再决定是否继续。

### 修复 F8: Has() 提前

`Host.Plugins().Has(name)` 放进里程碑 A（成本极低）。

### 修复 F9: HealthCheck 可选

```go
type Healthchecker interface {
    HealthCheck(ctx context.Context) error
}
// adapter 用 type assertion: if hc, ok := ext.(Healthchecker); ok { ... }
```

---

## 审计建议：v5 的根本性问题

这一轮发现的问题，根源是**前两轮审计没有质疑"这个方案到底要解决什么"**：

### 灵魂问题：ROI 是否成立？

v4 的交付物是：~6 周（现实 11 周）的框架 + 一个验证插件。现有 23 个 hook **原地保留不迁移**（因为 F1/F2 证明大多数迁移不了）。那这个框架的价值是什么？

- 如果价值是"未来新插件用统一框架"——那应该先有 1-2 个**真实的新插件需求**驱动设计，而不是先建框架
- 如果价值是"统一现有混乱"——但 F1 证明现有 hook 大多无法迁移，统一不了
- budget-guard 这种"只读KV纯计算"的插件，**今天用 40 行 pipeline.Hook 就能实现**，不需要 11 周的框架

### 建议的根本性重新审视

在投入 11 周前，建议先回答：

1. **有没有 1-2 个真实的新插件需求**（不是 toy）？如果有，用它们驱动接口设计（而不是想象）
2. **现有 hook 的迁移是否真的需要**？如果不需要，框架 ROI 存疑；如果需要，F1/F2 证明必须先解决依赖注入和 InterceptorExtension
3. **是否应该"小步快跑"**：先只做 E'（一个真实新插件，直接实现 pipeline.Hook，零框架），验证需求，再决定是否值得抽象框架？

**最诚实的结论**：当前没有足够的真实插件需求来证明一个通用插件框架的投入。更稳妥的路径是**先实现 1-2 个真实新功能（直接用 pipeline.Hook）**，在过程中发现共性，**再**抽象框架——而不是先建框架再找插件。

---

## 附录：被推翻的前两轮结论

| 前两轮结论 | 第三轮推翻 |
|---|---|
| "HookExtension 1:1映射零魔法" | ❌ F1: 18/23 hook 的构造依赖无法映射 |
| "现有17/28个hook" | ❌ 实际 25 断言/23 类型 + 3 个 Interceptor |
| "DBHandle 像 ApprovalDBTX" | ❌ F3: ApprovalDBTX 有 BeginTx 无 QueryRow，两者不同 |
| "复用 db.go ensure*Schema" | ❌ F4: 没有通用运行器，是2168行手写函数 |
| "budget-guard 只读KV纯计算" | ❌ F5: 成本数据不在 KV |
| "eventbus.MemoryBus 是过度设计" | ✅ F8: 这个删除是对的（确实无生产订阅者） |
| "ServiceRegistry 是过度设计" | ❌ F1: approval_hook.go:80 有真实 hook→hook 类型调用 |
| "Has() 推迟到F+" | ❌ F8: budget-guard（E'）就需要，依据也错了 |
| "6周可交付核心" | ❌ F7: 现实约 11 周 |

**唯一经受住对抗审计的删除**：MemoryBus（确实废弃）。

---

## 下一步

**不建议立即开始实施 v4。** 建议先：

1. **决策**：是否同意 v5 的"砍范围/先验证真实需求"建议？
2. **如果继续**：采纳 F1-F9 的修复，生成 v5 实施计划（预计核心 11-12 周，或砍到最小 3-4 周验证）
3. **如果重新审视**：先实现 1-2 个真实新插件（直接 pipeline.Hook），0 框架投入，验证需求

等待你的决策方向。
