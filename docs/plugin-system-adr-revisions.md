# 插件系统 ADR 修订（v5）

**日期**: 2024-07-09  
**性质**: 覆盖 v4 ADR 中被第三轮审计推翻的决策，补充 v5 新决策  
**前置**: `plugin-system-audit-round3-findings.md`

---

## 被推翻的 v4 ADR（及推翻理由）

### ❌ REV-A: 推翻 ADR-002（删除 ServiceRegistry）

**v4 决策**: 删除 ServiceRegistry，理由"28个hook无插件间typed调用需求"

**第三轮推翻证据**: `domains/hooks/sessionaudit/approval_hook.go:80` — `ApprovalHook.cacheUpdator *CacheUpdateHook`，:173 调用 `cacheUpdator.UpdateApprovalID(...)`。这是**真实的 hook→hook 类型化方法调用**。`goal/audit_hook.go:64` 接收 `goalHook.History()` 是另一例。

**v5 新决策 (ADR-v5-001)**: **保留最小 ServiceRegistry**（map 查找实现），仅服务 builtin 插件的 hook→hook 类型化依赖。这不是过度设计——有真实用例。

```go
type ServiceRegistry interface {
    Register(contract string, instance any) error
    Get(contract string) (any, bool)
}
```

---

### ❌ REV-B: 修正 ADR-001（HookExtension 1:1映射）

**v4 决策**: HookExtension = Extension + 完整 Hook，"1:1映射零魔法"

**第三轮推翻**: "1:1映射"只在 5 方法层面成立。**约18/23 hook 的构造依赖无法通过 ExtensionContext 传入**（`*FastDetector`、`*ApprovalManager`、`Router` 等特定类型）。

**v5 新决策 (ADR-v5-002)**: **分层架构**。builtin 插件用 `BuiltinFactory + Deps` 工厂模式注入任意类型依赖（复刻现有 main_pipeline.go 做法）；extends 插件用通用 ExtensionContext。"1:1映射"指 adapter 转发，不指依赖注入。

---

### ❌ REV-C: 推翻 ADR-008（adapter 1:1映射无魔法）

**v4 决策**: adapter 纯转发，"显式优于隐式"

**第三轮推翻**: adapter 转发本身没问题，但 v4 用它论证"整个系统零魔法"是错的——构造依赖注入需要 Deps 容器（反射），那才是"魔法"所在。

**v5 新决策 (ADR-v5-003)**: adapter 确实纯转发（这部分对），但**承认 Deps 容器用反射是必要的权衡**。文档明确：adapter 层零魔法，构造层（Deps）有受控反射。

---

### ❌ REV-D: 修正 ADR-003（只用 analysis.bus，不用 MemoryBus）

**v4 决策**: 删除 MemoryBus，只用 analysis.bus.Publisher

**第三轮验证**: 这个删除**经受住对抗审计**（MemoryBus 确实零生产订阅者，live_stream 用 Redis）。

**v5 决策 (ADR-v5-004)**: **维持 v4 决策**。Events 只用 `analysis.bus.Publisher`（durable）。MemoryBus 不复活。这是 v4 唯一无需修订的删除。

---

### ❌ REV-E: 推翻 ADR-005（Has() 推迟到 F+）

**v4 决策**: 运行时 `Has()` 推迟到 F+，理由"现有只1处跨模块查询"

**第三轮推翻**: v4 引用的 `format_conversion.enabled`（executor_chat.go:887）是 **provider 设置查询（按 providerID）**，不是插件依赖查询。真实跨插件运行时查询是 **0 个**。但 v4 自己的 budget-guard（E'）就需要 `Has()` 区分"配置启用但健康失败"vs"运行中"。

**v5 新决策 (ADR-v5-005)**: `Has()` **提前到里程碑 A**。成本极低（map 查找），但 E' 就需要。

```go
type PluginLookup interface {
    Has(name string) bool
    Get(name string) (Extension, bool)
}
```

---

### ❌ REV-F: 修正 ADR-004（Storage 双层 KV+DB）

**v4 决策**: 双层 Storage，KV（PG-backed plugin_kv 表）+ DB（DBHandle 3方法）

**第三轮推翻**:
1. F3: DBHandle 无 BeginTx → 无法 RLS（安全）
2. F4: 迁移运行器不存在
3. F6: 单连接池无隔离
4. F5: KV 用 PG 是热路径回归（现有 Redis 模式被忽略）

**v5 新决策 (ADR-v5-006)**: **本期只做 KV，用 Redis**（非 PG）。DB/迁移/连接池隔离全部推迟到验证后。

理由：
- Redis 是代码库**已有**的热路径模式（session.go:84, ursm/cache.go, approval/store.go）
- `cache/kv/memory.go:2` 明确说 Redis 是 kv.Store 的预期实现
- PG-backed KV 是反方向（热路径回归 + 共享主池风险 F6）
- Redis 原生 TTL（修复 v4 plugin_kv 表丢 TTL 的问题）

---

### ❌ REV-G: 修正 ADR-006（先 budget-guard）

**v4 决策**: E' 验证插件用 budget-guard

**第三轮推翻**: F5 — budget-guard 的 USD 成本在 `usage_ledger_hot.cost_usd`，不在 KV。验证插件是空壳。

**v5 新决策 (ADR-v5-007)**: E' 改用 **model-blacklist**。数据真实（env.SelectedProvider.Name + KV 黑名单），逻辑简单，完整验证链路。

---

## v5 新增 ADR

### ADR-v5-008: 新增 InterceptorExtension（修复 F2）

**背景**: v4 漏掉 `response.ResponseInterceptor` 接口。3 个生产 hook（goal.ModeHook/AuditHook、handoff.TriggerHook）是流式拦截器，不是 pipeline.Hook。

**决策**: 新增第三种能力 Extension：

```go
type InterceptorExtension interface {
    Extension
    InterceptNonStream(...) (*response.InterceptResult, error)
    InterceptStreamChunk(...) (*response.ChunkResult, error)
    InterceptStreamEnd(...) (*response.EndResult, error)
}
var _ response.ResponseInterceptor = (*interceptorAdapter)(nil)
```

**理由**: 不补这个，流式插件（占现有 hook 的一部分）完全在模型外。

---

### ADR-v5-009: HealthCheck 改为可选接口（修复 F9）

**背景**: v4 把 HealthCheck 设为强制方法。但约15个 hook 是无状态的，HealthCheck 永远返回 nil（形同虚设）或误判 Disable。

**决策**: HealthCheck 不在基础 Extension 契约里，改为可选接口：

```go
type Healthchecker interface {
    HealthCheck(ctx context.Context) error
}
// adapter 用 type assertion 探测
```

**理由**: 显式声明能力优于强制所有插件实现空方法。

---

### ADR-v5-010: 分层架构（builtin + extends）

**背景**: F1 证明通用 ExtensionContext 无法承载特定类型依赖。

**决策**: 两类插件，两套依赖注入：

| 类型 | 注入方式 | 能力 |
|------|---------|------|
| builtin | BuiltinFactory + Deps（任意类型） | 完整 |
| extends | ExtensionContext（通用服务） | 受限沙箱 |

**理由**: 诚实承认边界。现有 hook（带特定依赖）只能做 builtin；第三方独立项目做 extends。不假装一个通用接口能承载一切。

---

### ADR-v5-011: Deps 容器用受控反射

**背景**: builtin 插件需要任意类型依赖（`*FastDetector` 等），Go 无重载构造函数。

**决策**: `Deps` 是 `map[reflect.Type]any` 的安全封装，泛型 Get[T]。

```go
func Get[T any](d Deps) (T, bool)
func Must[T any](d Deps) T  // 构造期错误
```

**权衡**: 反射仅在**构造期**（启动时），不在请求路径，性能无影响。这是复刻现有 main_pipeline.go 手动传依赖的模式的必要抽象。

**替代方案（已拒绝）**:
- 手动构造（现有方式）：每个 hook 在 main.go 写构造代码，无法统一注册
- 每个插件自己定义 Deps 结构：等于没抽象，回到现状

---

### ADR-v5-012: 本期范围砍到最小验证集

**背景**: ROI 灵魂拷问——11周框架但现有 hook 迁移不了，价值存疑。

**决策**: v5 只做 4-5 周最小集（类型定义 + KV Storage + Registry + model-blacklist 验证）。DB/迁移/admin/gRPC/复杂迁移全部推迟。

**理由**:
- 先验证"分层框架对现有 hook 兼容"+ "新插件能跑通"
- 避免在未验证的框架上投入11周
- model-blacklist 真实简单（非空壳），能验证完整链路
- 验证后基于实证决定是否扩大投入

---

## 决策矩阵 v4 → v5

| 议题 | v4 决策 | 第三轮审计 | v5 决策 | ADR |
|------|--------|-----------|--------|-----|
| ServiceRegistry | 删除 | ❌ 有真实用例 | 保留最小 | v5-001 |
| 依赖注入 | 通用context | ❌ 填不满 | 分层(Deps+context) | v5-002,010,011 |
| MemoryBus | 删除 | ✅ 正确 | 维持删除 | v5-004 |
| Has() | 推迟F+ | ❌ E'就需要 | 提前到A | v5-005 |
| Storage | KV(PG)+DB | ❌ 4个问题 | 只KV(Redis) | v5-006 |
| 首个插件 | budget-guard | ❌ 空壳 | model-blacklist | v5-007 |
| Interceptor | 缺失 | ❌ 漏3个hook | 新增 | v5-008 |
| HealthCheck | 强制 | ❌ 噪音 | 可选 | v5-009 |
| 范围 | 完整6周 | ❌ 低估2倍 | 最小4-5周 | v5-012 |

**维持的 v4 决策**: 仅 MemoryBus 删除（REV-D）。

---

## 开放问题（v5 验证后决策）

1. **DB Storage 是否做？** 取决于是否有插件需要关系查询（model-blacklist 只需 KV）
2. **现有 hook 是否迁移？** 取决于 builtin 工厂模式是否被证明可靠
3. **gRPC/extends 是否做？** 取决于是否有第三方插件需求
4. **admin UI 是否做？** 取决于插件数量是否多到需要管理界面

v5 的价值：用最小投入获得实证，回答这些问题。
