# Hook插件化重构实施计划（v5：分层架构 + 最小验证集）

**版本**: v5  
**日期**: 2024-07-09  
**状态**: 待评审  
**前置**: `plugin-system-audit-round3-findings.md`（第三轮对抗审计，发现9个问题）

---

## v5 相对 v4 的根本转变

v4 假设"所有插件通过通用 ExtensionContext 获取依赖"——第三轮审计证明这是错的（F1：23个 hook 中约18个的特定类型依赖无法走通用 context）。v5 承认两类插件的现实，**分层处理**：

| 插件类型 | 依赖注入方式 | 能力 | 适用场景 |
|---------|------------|------|---------|
| **builtin（第一方）** | 工厂模式，构造期注入**任意类型**依赖（`*FastDetector`、`*ApprovalManager`、`*CacheUpdateHook`…） | 完整，可表达现有 hook 的真实依赖 | 现有 hook 迁移、复杂新功能 |
| **extends（第三方）** | 只能通过通用 `ExtensionContext`（Storage/LLM/Has/Services） | 受限，但沙箱化 | 独立项目、热加载、不信任代码 |

这直接回应了 F1（通用 context 填不满特定依赖）、F2（InterceptorExtension 补流式）、F8（Has 提前）。

### 范围控制（回应 ROI 灵魂拷问）

**v5 只做最小验证集（4-5周）**，不追求大而全：
- ✅ 做：类型定义（分层）、Storage（只KV）、Registry+Adapter、一个真实简单插件验证
- ⏸️ 推迟：DB 迁移运行器（F4说需从零建）、admin/health/metrics、gRPC/extends 加载
- 🎯 目的：先验证"分层框架对现有 hook 兼容" + "新插件能跑通"，再决定是否扩大投入

---

## F1-F9 修复对照表

| 缺陷 | v5 修复 | 落实位置 |
|------|--------|---------|
| F1 构造依赖 | 分层：builtin 用工厂注入任意类型，extends 用通用 context | 设计1+设计2 |
| F2 漏 Interceptor | 新增 InterceptorExtension | 设计1 |
| F3 DBHandle无事务 | 本期**不做DB**（只KV），DBHandle设计留到后续，届时含 BeginTx | 设计4 |
| F4 迁移运行器不存在 | 本期**不做DB迁移**，推迟到验证后 | 范围声明 |
| F5 budget-guard无数据源 | E' 改为 **model-blacklist**（数据在 env，真实简单） | 里程碑E' |
| F6 连接池无隔离 | 本期不做DB，KV 用 Redis（已存在），不碰主PG池 | 设计4 |
| F7 工作量低估 | 按现实重估，v5 范围砍到4-5周 | 里程碑 |
| F8 Has()依据错 | Has() 提前到里程碑A（成本极低） | 设计2 |
| F9 HealthCheck强制 | 改为可选接口 Healthchecker | 设计1 |

---

## 设计1: 分层 Extension 接口族（修复 F1, F2, F9）

### builtin 层：工厂模式（解决 F1 的核心）

```go
// domains/extensions/types.go

// BuiltinFactory 是 builtin 插件的构造器。host 在 buildV2DispatchPipeline
// 中用真实依赖调用它，返回一个 Extension。
//
// 这是 v5 的核心设计：builtin 插件**不**通过通用 context 获取特定类型依赖，
// 而是通过工厂接收任意类型——与现有 main_pipeline.go 的构造方式一致。
//
// deps 是一个类型安全的依赖容器（见设计2 Deps），host 往里塞 *FastDetector 等。
type BuiltinFactory func(deps Deps) (Extension, error)

// Extension 统一基础契约（仅生命周期 + 标识，不含 HealthCheck——F9 修复）
type Extension interface {
    Name() string
    Init(ctx ExtensionContext) error
}

// Healthchecker 可选接口（F9 修复：不强制）
// adapter 用 type assertion: if hc, ok := ext.(Healthchecker); ok { ... }
type Healthchecker interface {
    HealthCheck(ctx context.Context) error
}
```

### 三种能力 Extension（解决 F2，对齐三种现有接口）

```go
// HookExtension 对齐 pipeline.Hook（5方法）—— 1:1 真映射，因为 Priority/Enabled 由插件自己提供
type HookExtension interface {
    Extension
    Execute(ctx context.Context, env *domain.PipelineRequest) error
    Priority() int
    Enabled(ctx context.Context, env *domain.PipelineRequest) bool
    OnError(ctx context.Context, env *domain.PipelineRequest, err error) error
}
var _ pipeline.Hook = (*hookAdapter)(nil)  // adapter 纯转发

// GovernanceExtension 对齐 security.Plugin（3方法）
type GovernanceExtension interface {
    Extension
    Direction() string
    Inspect(ctx context.Context, env *domain.PipelineRequest) (*governance.Verdict, error)
}
var _ security.Plugin = (*governanceAdapter)(nil)

// InterceptorExtension 对齐 response.ResponseInterceptor（F2 修复：流式插件）
// 现有 3 个生产 hook（goal.ModeHook/AuditHook、handoff.TriggerHook）走这条路径
type InterceptorExtension interface {
    Extension
    InterceptNonStream(ctx context.Context, req *response.InterceptRequest) (*response.InterceptResult, error)
    InterceptStreamChunk(ctx context.Context, chunk []byte, meta *response.StreamMeta) (*response.ChunkResult, error)
    InterceptStreamEnd(ctx context.Context, meta *response.StreamMeta) (*response.EndResult, error)
}
var _ response.ResponseInterceptor = (*interceptorAdapter)(nil)
```

**关键**：一个插件可以实现多种能力（如既 HookExtension 又 Healthchecker），host 用 type assertion 探测。

---

## 设计2: Deps 容器 + ExtensionContext（解决 F1, F8）

### Deps：类型安全的依赖容器（builtin 专用）

```go
// domains/extensions/deps.go

// Deps 是一个按类型存取的依赖容器（generic map[reflect.Type]any 的安全封装）。
// host（main_pipeline.go）在构造期往里塞任意类型依赖：
//   deps.Add(detector)            // *sessionaudit.FastDetector
//   deps.Add(approvalMgr)         // *sessionaudit.ApprovalManager
//   deps.Add(cacheHook)           // *sessionaudit.CacheUpdateHook  (F1: hook→hook依赖)
// builtin 工厂从里面取：
//   func(d Deps) (Extension, error) {
//       det := mustGet[*sessionaudit.FastDetector](d)
//       mgr := mustGet[*sessionaudit.ApprovalManager](d)
//       return &SessionAuditHook{detector: det, approvalMgr: mgr}, nil
//   }
type Deps struct {
    vals map[reflect.Type]any
}

func (d Deps) Add(v any)              // 按反射类型存
func Get[T any](d Deps) (T, bool)     // 泛型取
func Must[T any](d Deps) T            // 取不到 panic（构造期错误）
```

**为什么这是对的**：它**复刻现有 main_pipeline.go 的真实做法**——现有代码就是构造期传 `*FastDetector`、`*ApprovalManager`。v4 错在试图用通用 context 替代；v5 把这个真实模式抽象成容器，builtin 插件照样能拿到任意类型依赖。`ApprovalHook.cacheUpdator *CacheUpdateHook`（F1 的 hook→hook 依赖）也通过 Deps 传递。

### ExtensionContext：通用服务句柄（builtin + extends 共用）

```go
// domains/extensions/context.go

type ExtensionContext struct {
    Manifest Manifest
    Config   json.RawMessage

    // 持久化（本期只 KV，见设计4）
    Storage Storage

    // 通讯
    Metadata MetadataRegistry  // 层1：规范化 env.Metadata key
    Events   DurablePublisher  // 层2：复用 analysis.bus.Publisher

    // F8 修复：运行时插件查询（提前到 A）
    Plugins PluginLookup  // Has(name) / Get(name)

    // F1 部分修复：插件间类型化能力（可选，仅 builtin 用得到）
    Services ServiceRegistry  // Register(contract, instance) / Get(contract)

    // 网关服务
    LLM      LLMClient
    Settings SettingsStore
    Logger   *slog.Logger
}

type PluginLookup interface {
    Has(name string) bool                      // F8: 提前到 A，成本极低
    Get(name string) (Extension, bool)
}

type ServiceRegistry interface {
    Register(contract string, instance any) error  // F1: hook→hook 类型化调用
    Get(contract string) (any, bool)
}
```

**注**：ServiceRegistry 在本期是**最小实现**（map 查找），仅服务于 builtin 的 hook→hook 依赖（如 ApprovalHook→CacheUpdateHook）。extends 插件调用它需要声明权限。这不是过度设计——F1 证明有真实用例。

---

## 设计3: Registry + Adapter + 拓扑排序

```go
// domains/extensions/registry.go

type Registry struct {
    mu        sync.RWMutex
    byName    map[string]loaded  // name → {ext, deps, manifest}
    order     []string           // 拓扑排序后的加载顺序
}

type loaded struct {
    ext      Extension
    manifest Manifest
    deps     []string  // depends_on
}

// RegisterBuiltin 注册第一方插件工厂
// 拓扑排序保证依赖先注册；循环依赖报错
func (r *Registry) RegisterBuiltin(name string, fac BuiltinFactory, deps Deps, manifest Manifest) error

// 按能力分组返回（adapter 1:1 转发，零魔法）
func (r *Registry) HooksForPhase(phase pipeline.Phase) []pipeline.Hook           // HookExtension → hookAdapter
func (r *Registry) GovernancePlugins() []security.Plugin                          // GovernanceExtension → governanceAdapter
func (r *Registry) Interceptors() []response.ResponseInterceptor                  // InterceptorExtension → interceptorAdapter (F2)
func (r *Registry) Healthy(ctx context.Context) map[string]error                  // 只检查 Healthchecker (F9)
```

**接入 main_pipeline.go（F7 修复：逐 phase 插入，非"末尾追加"）**：

v4 的"末尾追加"是错的（pre_routing 插件会在 return 之后，永不执行）。v5 的接入方式：

```go
// cmd/gateway/main_pipeline.go buildV2DispatchPipeline 内
// 在每个已有 phase 边界，追加该 phase 的 extension hooks（外科手术式，6处）
p.AddStage(&pipeline.PipelineStage{
    Name: "client_identity", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
    Hooks: append([]pipeline.Hook{identity.NewClientIdentityHook()},
        deps.Extensions.HooksForPhase(pipeline.PhasePreRouting)...),  // ← 追加该phase的extension
})
// governance stage 追加 GovernancePlugins
// response 拦截器链追加 Interceptors()
```

**约束保持**：现有 24 个 AddStage 不重写，只在对应 phase 的 Hooks 切片末尾 `append` extension hooks。现有 stage 行为不变。

---

## 设计4: Storage（本期只 KV，修复 F3/F4/F6 的范围）

### 本期范围声明（重要）

第三轮审计证明：DB 抽象（F3 事务/RLS、F4 迁移运行器、F6 连接池隔离）每一个都是独立的复杂工程。**v5 本期不做 DB**，只做 KV。理由：
- KV 足够验证 E'（model-blacklist 不需要 DB）
- DB 的三个问题（F3/F4/F6）需要专门设计，不应混在最小验证集里
- 推迟到 v5 验证通过后，单独里程碑处理

### KV 设计（用 Redis，不用 PG —— 修复 F6，学习现有模式）

```go
// domains/extensions/storage.go

type Storage interface {
    KV() KVStore  // 本期唯一实现
    // DB() DBHandle       // 推迟
    // Migrations() []Migration  // 推迟
}

type KVStore interface {
    Lookup(ctx context.Context, key string) ([]byte, bool, error)
    Store(ctx context.Context, key string, payload []byte, ttl time.Duration) error
    Invalidate(ctx context.Context, key string) error
}
```

**为什么 KV 用 Redis 而非 PG（F6 修复 + 学习现有模式）**：
- 第三轮审计发现：代码库**已有** Redis 热路径模式（`session.RedisClient` session.go:84，`ursm/cache.go` Memory→Redis→DB 三级，`approval/store.go` PG+Redis 混合）
- `cache/kv/memory.go:2` 注释明确说 Redis 是 `kv.Store` 的**预期未来实现**
- v4 选 PG 是反方向（热路径回归 + 共享主池风险）
- v5 KV 实现：复用 `session.RedisClient`，key 前缀 `plugin:{id}:tenant:{tid}:`，**不碰主 PG 池**（F6 解决）

**TTL 处理（F5 相关）**：Redis 原生支持 EXPIRE，KV schema 正确，不像 v4 的 plugin_kv 表丢 TTL。

---

## 设计5: MetadataRegistry（规范化现有 ~40 个 key）

```go
// domains/extensions/metakeys.go

type MetadataRegistry interface {
    RegisterKey(key, pluginName, direction string) error  // read|write|both
    Validate() []string  // 悬空契约警告
    Owners(key string) (writers, readers []string)
}
var GlobalMetadataRegistry = NewMetadataRegistry()
```

插件在 `Init()` 声明读写，host 加载完后 `Validate()`。现有 ~40 个 key 一次性迁移注册。

---

## 里程碑（v5 最小验证集：4-5周）

### A: 类型定义 + Registry + Adapter（1.5周）

**交付**：
- `domains/extensions/types.go`：Extension/HookExtension/GovernanceExtension/InterceptorExtension/Healthchecker
- `domains/extensions/deps.go`：Deps 容器
- `domains/extensions/context.go`：ExtensionContext（含 PluginLookup.Has）
- `domains/extensions/registry.go`：Registry + 拓扑排序 + 循环检测
- `domains/extensions/adapter.go`：hookAdapter/governanceAdapter/interceptorAdapter（1:1转发）
- 编译期断言全部通过

**验收**：
- 接口对齐验证：HookExtension 覆盖 pipeline.Hook 5方法；InterceptorExtension 覆盖 ResponseInterceptor 3方法
- adapter 纯转发（grep 确认无 manifest 推导）
- Deps 容器泛型 Get[T] 测试
- 拓扑排序+循环检测测试

**不破坏验证**：
- [ ] `go build ./...` 全绿
- [ ] `go test ./domains/pipeline ./domains/security` 全绿（未改）

---

### B-minimal: KV Storage（1周）

**交付**：
- `domains/extensions/storage.go`：Storage/KVStore 接口
- `domains/extensions/kv_redis.go`：Redis 实现，复用 `session.RedisClient`
- key 前缀 `plugin:{id}:tenant:{tid}:`
- TTL 正确（Redis EXPIRE）

**不做**（推迟）：DB、迁移运行器、plugin_kv 表、连接池隔离

**验收**：
- KV 隔离测试（不同 plugin/tenant 不碰撞）
- TTL 过期测试
- Redis 不可用时优雅降级（返回 false，不 panic）
- **不碰主 PG 池**（F6 验证）

---

### C: MetadataRegistry + 接入 main_pipeline.go（1周）

**交付**：
- `domains/extensions/metakeys.go`：MetadataRegistry + 悬空契约检测
- 现有 ~40 个 Metadata key 迁移注册（一次性，文档化）
- `cmd/gateway/main_pipeline.go`：逐 phase 接入（6处 append，非末尾追加）
- `cmd/gateway/main.go`：构造 Deps，注入现有依赖，调用 RegisterBuiltin

**验收**：
- buildV2DispatchPipeline 现有 24 个 stage 行为不变
- 新增 extension hooks 在正确 phase 运行（pre_routing 的不在 return 后）
- MetadataRegistry.Validate() 无悬空警告（或警告已登记）

**不破坏验证**：
- [ ] 现有 pipeline 测试全绿
- [ ] 现有 hook 优先级不变（extension 用 Priority() 自己声明）

---

### E': model-blacklist 验证插件（1周）

**为什么换掉 budget-guard（F5 修复）**：budget-guard 的成本数据在 `usage_ledger_hot`，不在 KV，是空壳。

**model-blacklist 插件设计**：
```go
// domains/extensions/builtin/model_blacklist.go
// 真实简单：读 env 里的模型名，查 KV 黑名单，命中则拒绝
type ModelBlacklistExt struct {
    kv KVStore
}
func (e *ModelBlacklistExt) Name() string { return "model-blacklist" }
func (e *ModelBlacklistExt) Priority() int { return 90 }  // pre_routing 早期
func (e *ModelBlacklistExt) Enabled(ctx, env) bool {
    return env != nil && env.SelectedProvider != nil
}
func (e *ModelBlacklistExt) Execute(ctx, env) error {
    model := env.SelectedProvider.Name
    tenant := env.TenantID
    key := "blacklist:" + model
    if _, blocked, _ := e.kv.Lookup(ctx, key); blocked {
        v := &governance.Verdict{
            PluginName: "model-blacklist", Allow: false,
            Severity: 2, Code: "model.blacklisted", Reason: "model " + model,
        }
        env.EnsureGovernance().RecordVerdict(v)
        env.StatusCode = 403
        return fmt.Errorf("model %s blacklisted for tenant %s", model, tenant)
    }
    return nil
}
```

**为什么这个验证有效**（对比 budget-guard）：
- ✅ 数据真实存在于 env（SelectedProvider.Name）+ KV（黑名单），无空壳
- ✅ 完整链路：Deps注入 → Register → Init → Execute(PreRouting) → Verdict → 现有 interception.Engine → 403
- ✅ 用了 KV（Redis）读写
- ✅ 用了 Metadata（可选：写 "model_blacklist_checked"）
- ✅ 逻辑简单（查表+判断），风险低
- ✅ 有真实价值（模型访问控制是真需求）

**黑名单管理**：通过 KV Store 写入（如 admin 脚本或另一个简单工具），本期不做 admin UI。

**验收**：
- 黑名单模型请求返回 403
- 非黑名单模型正常通过
- 现有请求路径不受影响（Enabled 控制）

---

## 推迟到 v5 验证后的里程碑

### D: DB Storage（含 F3/F4/F6 完整修复）
- DBHandle 含 BeginTx（F3）
- 迁移运行器从零构建（F4，预计3周）
- 连接池隔离（F6）
- outputcompliance 驱动迁移评估

### F: Admin + Health + Metrics
- `/api/admin/extensions/*`
- HealthMonitor（基于可选 Healthchecker，F9）
- MetricsDecorator（plugin_usage_hot）

### G: gRPC + extends/ 加载
- 第三方独立项目能力
- 通用 ExtensionContext 沙箱（extends 不能用 Deps，只能用通用服务）

### E: 复杂 hook 迁移（outputcompliance 等）
- 验证 builtin 工厂模式能承载真实复杂 hook
- 验证 hook→hook 依赖（ServiceRegistry）

---

## 测试策略

### 单元测试
- adapter 1:1 转发验证（Hook/Governance/Interceptor 三种）
- Deps 容器泛型存取
- 拓扑排序（正常序/循环检测/钻石依赖）
- KV 隔离 + TTL + Redis 降级
- MetadataRegistry 悬空检测

### 集成测试
- model-blacklist 完整链路（Register → Execute → 403）
- 现有 hook + extension 共存（同 phase，优先级正确）

### 回归测试
- `go build ./...` 全绿
- `go test ./domains/pipeline ./domains/security` 全绿（未改包）
- buildV2DispatchPipeline 现有 stage 行为不变

---

## 风险与缓解

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| Deps 容器反射性能 | 低 | 低 | 构造期一次性，非请求路径 |
| 接入 main_pipeline.go 引入回归 | 中 | 高 | 逐 phase append，现有测试守卫，code review |
| Redis 不可用 | 中 | 中 | KV 优雅降级（Lookup 返回 false），不 panic |
| model-blacklist 验证不足 | 低 | 中 | E' 后立即评估是否需要第二个插件 |

**已消除的风险**（v5 范围控制）：
- ✅ F4 迁移运行器（本期不做 DB）
- ✅ F3 事务/RLS（本期不做 DB）
- ✅ F6 连接池（KV 用 Redis，不碰主池）
- ✅ F5 空壳插件（model-blacklist 数据真实）

---

## 不破坏验证清单

### 接口不变
- [ ] `pipeline.Hook` 接口未修改
- [ ] `security.Plugin` 接口未修改
- [ ] `response.ResponseInterceptor` 接口未修改
- [ ] `PipelineRequest` 只增不删

### 现有代码不变
- [ ] 现有 25 处 `var _ pipeline.Hook` 断言全部保留
- [ ] 现有 3 处 `var _ response.ResponseInterceptor` 断言保留
- [ ] `domains/pipeline/pipeline.go` 零修改
- [ ] `domains/security/{plugin,registry,hook}.go` 零修改
- [ ] `buildV2DispatchPipeline` 现有 24 个 stage 不重写，仅 phase 边界 append

### 测试全绿
- [ ] `go build ./...` 通过
- [ ] `go test ./domains/pipeline` 全绿
- [ ] `go test ./domains/security` 全绿

---

## v5 的诚实声明

1. **本期不迁移任何现有 hook**。v5 验证的是"框架能兼容现有 hook 的构造模式（Deps）+ 一个新插件能跑通"。现有 hook 原地保留。
2. **本期不做 DB/迁移/admin/gRPC**。这些是独立工程（F3/F4/F6 证明复杂），推迟到验证后。
3. **model-blacklist 是验证工具，非 flagship 功能**。它证明链路，真实价值在于验证后的扩展。
4. **ROI 判断延后**。v5（4-5周）验证"分层框架可行"后，再决定是否投入 D/F/G/E（约再 7-8 周）。如果 v5 发现框架对现有 hook 仍不兼容，止损。

---

## 文档交付

1. ✅ `plugin-system-audit-report.md`（第一轮）
2. ✅ `plugin-system-implementation-plan-v4.md`（v4，已被第三轮推翻）
3. ✅ `plugin-system-architecture-decision-records.md`（v4 ADR，需修订）
4. ✅ `plugin-system-audit-round3-findings.md`（第三轮，9个发现）
5. ✅ `plugin-system-implementation-plan-v5.md`（本文档）
6. ⏳ `plugin-system-adr-revisions.md`（v5 ADR 修订，下一步）

---

**版本历史**：
- v1-v2: 初始设计（废弃）
- v3: 数据/通讯/依赖为一等公民（第三轮发现过度设计 + 隐藏致命缺陷）
- v4: 审计修正（第三轮证明修正不足，9个问题未解决）
- **v5: 分层架构 + 最小验证集（采纳 F1-F9 全部修复，砍范围到4-5周）**

**批准**: 待评审
