# Hook插件化重构方案审计报告

**审计日期**: 2024-07-09  
**审计范围**: v3计划的可行性、架构一致性、风险评估  
**审计结论**: 需修正3处关键设计，可继续执行

---

## 执行摘要

经对 v3 计划进行逐条审计，发现以下关键问题需修正：

1. **接口数量膨胀** - Extension接口设计与现有架构存在不一致
2. **通讯层过度设计** - 三层通讯模型引入不必要的复杂度
3. **数据流审计通过** - Storage独立性设计符合现状
4. **依赖管理可行** - 但需简化实现

**建议**: 采用修正后的 v4 方案，保持最小化设计原则。

---

## 审计1: 架构一致性检查

### 发现的问题

**问题1.1: Extension接口与现有Hook/Plugin接口不对齐**

v3计划提出三个接口：
```go
type Extension interface {
    Metadata() Manifest
    Init(ctx ExtensionContext) error
    HealthCheck(ctx context.Context) HealthStatus
}

type RequestHookExt interface {  // 继承Extension
    Execute(ctx context.Context, env *domain.PipelineRequest) error
    OnError(ctx context.Context, env *domain.PipelineRequest, err error) error
}

type GovernanceExt interface {   // 继承Extension
    Inspect(ctx context.Context, env *domain.PipelineRequest) (*governance.Verdict, error)
}
```

审计发现现有架构已有两个生产接口：

1. **`pipeline.Hook`** (`domains/pipeline/pipeline.go:67-86`) - 5个方法：
   - `Name() string`
   - `Execute(ctx, env) error`
   - `Priority() int`
   - `Enabled(ctx, env) bool`
   - `OnError(ctx, env, err) error`
   
   当前有 **17个** `var _ pipeline.Hook` 实现（grep确认）

2. **`security.Plugin`** (`domains/security/plugin.go:43-47`) - 3个方法：
   - `Name() string`
   - `Direction() string`
   - `Inspect(ctx, env) (*governance.Verdict, error)`
   
   当前有 **7个** security.Plugin实现

**不一致点**：
- `RequestHookExt` 缺少 `Name()`、`Priority()`、`Enabled()` 三个 `pipeline.Hook` 的核心方法
- `GovernanceExt` 缺少 `Direction()` 这个 `security.Plugin` 的必需方法
- `Extension` 的 `Metadata()` 与 `Hook.Name()` 功能重复

**影响**: 如果按v3设计，adapter需要"发明"缺失的方法（如从Manifest推导Priority），破坏了"插件直接表达行为"的原则。

### 修正建议

**修正1.1**: Extension接口应该 = Hook接口 + 生命周期方法

```go
// Extension 统一插件契约（合并现有两种接口的公共部分）
type Extension interface {
    // 核心方法（pipeline.Hook + security.Plugin 的交集）
    Name() string
    
    // 生命周期（新增，所有插件共有）
    Init(ctx ExtensionContext) error
    HealthCheck(ctx context.Context) error
}

// HookExtension = Extension + 完整pipeline.Hook行为
type HookExtension interface {
    Extension
    Execute(ctx context.Context, env *domain.PipelineRequest) error
    Priority() int
    Enabled(ctx context.Context, env *domain.PipelineRequest) bool
    OnError(ctx context.Context, env *domain.PipelineRequest, err error) error
}

// GovernanceExtension = Extension + 完整security.Plugin行为
type GovernanceExtension interface {
    Extension
    Direction() string
    Inspect(ctx context.Context, env *domain.PipelineRequest) (*governance.Verdict, error)
}
```

这样 adapter 是 1:1 映射，不需要"魔法推导"。

---

## 审计2: 数据流边界验证

### 审计方法
逐文件追踪了28个hook→module的数据契约，验证v3计划的两条"铁律"。

### 审计结果: **通过** ✅

**铁律1验证（请求数据 = hook中介）**: 
- 确认模块包（`domains/outputcompliance`、`sessionaudit`、`promptinjection`）**不import `domain`**
- 确认所有模块入口方法接收**扁平类型化参数**（如 `Check(ctx, tenantID, output string)`）
- 确认hook是env的唯一写入者

**铁律2验证（持久化独立于hook）**:
- 确认模块自持 `*sql.DB`/`*pgxpool.Pool`
- 确认存在**无hook触发的后台操作**：`ApprovalManager.MarkTimeout` worker（`approval_manager.go:352`）每30秒扫`approval_queue`表
- 确认最小接口模板存在：`ApprovalDBTX`（`approval_manager.go:41`）、`analysis.DB`（`analysis/db.go:17`）

**返回契约验证**:
- 确认无字面"回调函数" - 唯一的func注入（`OwnerContextFunc`）方向相反（hook→module依赖注入）
- 确认返回路径 = **返回值 + hook写env**

### 修正建议

**修正2.1**: Storage接口完全符合现状，无需修改

继续使用双层Storage（KV默认 + DB声明式opt-in），但明确文档说明：
- Storage是"配置/持久化通道"，与请求路径正交
- 插件可在Execute外访问Storage（如后台worker）

---

## 审计3: 通讯层设计复杂度

### 发现的问题

**问题3.1: 层2（事件总线）存在两个现实问题**

v3计划的"层2"设计：
```
层2-ephemeral: eventbus.MemoryBus (进程内pub/sub)
层2-durable:   analysis.bus.Publisher (PG队列)
```

审计发现：
1. **`eventbus.MemoryBus` 在生产环境实质废弃** - grep确认**零生产订阅者**（只有测试订阅）
2. **真正的异步通道是 `analysis.bus.Publisher`** - 但它不是通用事件总线，而是领域特定的分析事件队列

**问题3.2: 层3（ServiceRegistry）引入循环依赖风险**

v3计划要求插件间typed调用走"共享contracts包"。审计发现：
- 现有代码**刻意避免**插件间直接调用（outputcompliance不调用sessionaudit，promptinjection不调用security）
- 唯一一处"跨模块能力复用"是`OwnerContextFunc`，但它是**构造期注入的函数闭包**，不是运行时服务发现

### 影响分析

如果实现完整三层通讯：
- 需为废弃的MemoryBus补订阅者（工作量大，价值低）
- 需设计contracts包避免循环依赖（复杂度高）
- 需引入服务发现的运行时开销

### 修正建议

**修正3.1**: 简化为两层通讯模型

```
层1（必需）: env.Metadata + typed访问 + per-plugin namespace
           → 这是现有~40个key的规范化，必须做

层2（可选，里程碑F）: 保留 analysis.bus.Publisher 作为durable事件通道
                      不实现通用ServiceRegistry（推迟到真正需要时）
```

**理由**:
- 层1是现有Metadata的增强，投入产出比最高
- eventbus.MemoryBus已被analysis.bus替代，无需复活
- ServiceRegistry在28个现有hook中找不到需求，属于过度设计

---

## 审计4: 依赖管理可行性

### 审计结果: 可行，但需简化

**现状**:
- `admin/modules.go` 的 `ModuleDependency`（UI层）已有依赖声明
- `buildV2DispatchPipeline` 的 nil-check（构建期）是静态门控
- **运行时依赖查询完全缺失**（只有1处 `format_conversion.enabled` 查询）

**v3计划的依赖管理**:
- 声明式 `depends_on` + `requires` (OSGi风格)
- 运行时 `Has()`/`Get()` (VS Code风格)
- 拓扑排序激活

### 修正建议

**修正4.1**: 分两期实现

**里程碑A（必需）**: 
- 声明式 `depends_on: [pluginID...]`
- 构建期拓扑排序（顺序注册到Registry）
- nil-check式存在性检查（保持现状）

**里程碑F（可选）**:
- 运行时 `Host.Plugins().Has(name)`
- 带cardinality的 `requires`
- 只在真正出现"optional依赖"需求时才做

**理由**: 现有28个hook都是必需依赖（nil就不注册），optional依赖是未来需求。

---

## 审计5: 里程碑依赖与风险

### 原计划的里程碑链

```
A(核心契约) → B(数据层) → C(统计/admin) → E(迁移示例) → F(新插件) → D(gRPC)
```

### 发现的风险

**风险5.1: B依赖A的接口稳定性**
- 如果A的Storage接口设计有误，B的三个表迁移需返工

**风险5.2: C依赖未稳定的admin集成点**
- `admin/modules.go` 的 `allModuleDefinitions` 是动态注入，与现有17个静态定义混合
- 测试 `modules_test.go:17` 硬编码期望 "17个模块"，动态注入会导致测试失败

**风险5.3: E(迁移outputcompliance)是最大风险点**
- outputcompliance有4个表、3处调用点、owner查询闭包
- 如果E失败，F和D都阻塞

### 修正建议

**修正5.1**: 调整里程碑顺序和范围

```
A1: 核心类型定义（Extension/HookExtension/GovernanceExtension）+ Manifest解析
    → 交付: types.go + manifest.yaml 解析，可独立评审

A2: ExtensionContext + Storage接口设计（纸面设计 + 接口定义）
    → 交付: context.go + storage.go 接口，配测试用例，**不做实现**

B: Storage实现 + 表迁移（基于A2的稳定接口）
   → 交付: KV/DB/Migrations 实现 + 3个表

C: Registry + Adapter（基于A1的稳定接口）
   → 交付: 进程内注册、拓扑排序、adapter适配现有Hook

D: Metrics + Health + Admin（基于C的稳定Registry）
   → 交付: 用量统计、健康检查、/api/admin/extensions

E': 新建简单插件（而非迁移复杂的outputcompliance）
   → 交付: 一个只读插件（如budget-guard），证明链路，风险低

F（推迟）: gRPC + extends/加载
E（推迟）: 迁移outputcompliance（等E'验证后再做）
```

**关键变化**:
- A拆成A1/A2，接口设计先于实现
- E改为新建简单插件（E'），降低风险
- F（gRPC）和E（复杂迁移）推迟到基础稳定后

---

## 修正后的方案摘要

### 核心修正（必须采纳）

| 编号 | 问题 | 修正 | 优先级 |
|------|------|------|--------|
| 1.1 | Extension接口不对齐Hook | HookExtension = Extension + 完整Hook接口 | P0 |
| 2.1 | Storage设计（已通过） | 保持双层Storage，明确文档 | P0 |
| 3.1 | 三层通讯过度设计 | 简化为两层（Metadata增强 + analysis.bus） | P0 |
| 4.1 | 依赖管理过于复杂 | 分两期，先做必需的depends_on | P1 |
| 5.1 | 里程碑风险 | 调整为A1→A2→B→C→D→E'，推迟F/E | P0 |

### 范围调整

**删除**（过度设计）:
- ❌ eventbus.MemoryBus复活计划
- ❌ 通用ServiceRegistry（推迟到真需求出现）
- ❌ 运行时Has()/Get()依赖查询（推迟）
- ❌ manifest的`requires`+cardinality（推迟）

**保留**（核心价值）:
- ✅ Extension生命周期（Init/HealthCheck）
- ✅ Storage双层（KV + DB + Migrations）
- ✅ Metadata规范化 + per-plugin namespace
- ✅ 声明式depends_on + 拓扑排序
- ✅ admin集成 + 用量统计

**推迟**（低优先级）:
- 🔄 gRPC + extends/（里程碑F）
- 🔄 迁移outputcompliance（里程碑E，等E'验证后）
- 🔄 ServiceRegistry（真需求出现时）

---

## 审计结论

### 总体评估: **方案可行，需采纳修正**

v3方案的核心设计（数据流边界、Storage独立性）经审计验证**完全符合现状**。但在接口设计和通讯层存在**过度设计**，需简化。

### 建议采纳的v4方案特点

1. **接口1:1对齐** - HookExtension就是Hook+生命周期，无需适配器魔法
2. **最小通讯层** - 只做Metadata规范化，不引入新总线
3. **渐进依赖管理** - 先做必需的，可选的等需求出现
4. **降低首个里程碑风险** - 新建简单插件而非迁移复杂模块

### 可预期的收益

- ✅ 插件可独立编译测试（Storage抽象）
- ✅ 插件生命周期管理（Init/Health/Metrics）
- ✅ 跨插件数据契约规范化（Metadata注册表）
- ✅ 声明式依赖+拓扑激活（解决现有"硬编码顺序"问题）
- ✅ admin统一管理（插件即模块）

### 不可预期的风险（需持续监控）

- ⚠️ A2接口设计可能需迭代（通过评审降低）
- ⚠️ 拓扑排序性能（>50插件时，需benchmark）
- ⚠️ manifest解析错误处理（需完善测试）

---

## 下一步行动

1. **立即**: 根据修正生成 v4 实施计划文档
2. **本周**: 评审 A1/A2 接口设计（Extension/Storage）
3. **下周**: 开始 A1 实现（类型定义+测试）

**评审检查清单**:
- [ ] HookExtension接口是否完全覆盖pipeline.Hook的5个方法？
- [ ] Storage.DB()返回的接口是否足够窄（可mock）？
- [ ] Manifest的depends_on是否足以表达现有admin.ModuleDependency？
- [ ] Adapter是否做到1:1映射无魔法？

---

**审计人**: Kiro (AI Agent)  
**复核建议**: 人工评审修正1.1（接口对齐）和修正3.1（通讯简化）
