# Hook插件化重构实施计划（v4：经审计修正版）

**版本**: v4  
**日期**: 2024-07-09  
**状态**: 待评审  
**前置审计**: `docs/plugin-system-audit-report.md`

---

## 审计修正摘要

经过全面审计（详见审计报告），v3方案的核心设计验证通过，但发现三处过度设计需简化：

| 修正项 | 问题 | v4方案 |
|-------|------|--------|
| 接口对齐 | Extension接口缺少Hook核心方法 | HookExtension = Extension + 完整5方法Hook |
| 通讯简化 | 三层模型引入废弃/无需求的机制 | 两层：Metadata规范化 + analysis.bus |
| 风险控制 | 首个迁移目标过于复杂 | 新建简单插件验证，推迟复杂迁移 |

---

## 设计原则（不可破坏的约束）

### 约束1: 现有接口零破坏
- `pipeline.Hook`（5方法：Name/Execute/Priority/Enabled/OnError）保持不变
- `security.Plugin`（3方法：Name/Direction/Inspect）保持不变
- `PipelineRequest` 只增不删
- `buildV2DispatchPipeline` 只追加loader调用，不重写
- 现有28个hook原地保留

### 约束2: 数据流边界清晰（审计验证通过）
- **请求数据** = hook从env读字段 → 扁平参数传模块 → 模块返回结果 → hook写回env
- **持久化/配置** = 模块自持DB，独立读写（不经hook），后台worker无hook触发
- **返回契约** = 返回值 + hook写env（无回调函数）

### 约束3: 最小化设计
- 删除无需求证据的功能（ServiceRegistry、MemoryBus复活）
- 分两期实现依赖管理（先必需，后可选）
- 推迟高风险迁移（outputcompliance等E'验证后再做）

---

## 核心设计

### 设计1: Extension接口族（与现有接口1:1对齐）

```go
// domains/extensions/types.go

// Extension 统一插件基础契约（生命周期）
type Extension interface {
    Name() string
    Init(ctx ExtensionContext) error
    HealthCheck(ctx context.Context) error
}

// HookExtension = Extension + 完整pipeline.Hook接口
// 编译期断言: var _ pipeline.Hook = (*hookAdapter)(nil)
type HookExtension interface {
    Extension
    // 以下5个方法1:1对应pipeline.Hook，adapter零魔法
    Execute(ctx context.Context, env *domain.PipelineRequest) error
    Priority() int
    Enabled(ctx context.Context, env *domain.PipelineRequest) bool
    OnError(ctx context.Context, env *domain.PipelineRequest, err error) error
}

// GovernanceExtension = Extension + 完整security.Plugin接口
// 编译期断言: var _ security.Plugin = (*governanceAdapter)(nil)
type GovernanceExtension interface {
    Extension
    // 以下2个方法1:1对应security.Plugin
    Direction() string
    Inspect(ctx context.Context, env *domain.PipelineRequest) (*governance.Verdict, error)
}
```

**关键**: adapter是1:1映射，Priority/Direction由插件直接提供，不从Manifest推导。

**审计依据**: 
- 现有17个`pipeline.Hook`实现都有明确的Priority()返回值
- 现有7个`security.Plugin`实现都有明确的Direction()返回值
- 无需adapter"发明"缺失方法

### 设计2: ExtensionContext（插件的服务句柄）

```go
// domains/extensions/context.go

type ExtensionContext struct {
    Manifest Manifest
    Config   json.RawMessage  // 已按ConfigSchema校验
    
    // 持久化通道（铁律2：独立于hook）
    Storage  Storage          // KV + DB + Migrations
    
    // 通讯层（简化为两层）
    Metadata MetadataRegistry  // 层1：Metadata key注册表，规范化现有~40个key
    Events   DurablePublisher  // 层2：PG-backed异步（复用analysis.bus.Publisher）
    
    // 依赖查询（里程碑A，只读）
    Dependencies DependencyInfo  // 构建期填充的depends_on信息
    
    // 复用网关服务
    LLM      LLMClient         // 回调网关LLM（多模型复核用）
    Settings SettingsStore     // RegisterSpec / EffectiveValue
    Blobs    BlobStorage       // attachments.StorageBackend
    Logger   *slog.Logger
    Metrics  MetricsRecorder   // RecordCounter/RecordHistogram
}
```

**审计删除项**（无需求证据）:
- ❌ `Host.Services()` ServiceRegistry - 现有28个hook无插件间typed调用需求
- ❌ `Events` ephemeral MemoryBus - grep确认零生产订阅者
- ❌ `Host.Plugins().Has()` 运行时查询 - 现有只1处跨模块查询

**保留项**（有需求证据）:
- ✅ `Storage` - 所有模块都有DB/配置读写需求
- ✅ `MetadataRegistry` - 现有~40个Metadata key需规范化
- ✅ `Events` durable - `analysis.bus.Publisher`是真正的异步通道
- ✅ `LLM` - `sessionaudit.LLMDetectorClient`已有多模型复核需求

### 设计3: Storage接口（双层，审计通过）

```go
// domains/extensions/storage.go

type Storage interface {
    // Tier 1（默认）：namespaced KV
    // key自动前缀: plugin:{pluginID}:tenant:{tenantID}:
    KV() KVStore  // 复用cache/kv.Store形状
    
    // Tier 2（声明式opt-in）：插件owned表
    DB() DBHandle  // 窄接口（analysis.DB形状），非裸pool
    Migrations() []Migration
}

// 窄接口，可mock（审计推荐模板：analysis.DB + ApprovalDBTX）
type DBHandle interface {
    QueryRow(ctx context.Context, sql string, args ...any) Row
    Query(ctx context.Context, sql string, args ...any) (Rows, error)
    Exec(ctx context.Context, sql string, args ...any) (CommandTag, error)
}

type Migration struct {
    Version string  // "001", "002" 顺序
    Up      string  // CREATE TABLE IF NOT EXISTS plugin_{id}_xxx
    Down    string  // DROP TABLE IF EXISTS plugin_{id}_xxx (可选)
}

// KVStore 复用 cache/kv.Store 形状
type KVStore interface {
    Lookup(ctx context.Context, key string) ([]byte, bool, error)
    Store(ctx context.Context, key string, payload []byte, ttl time.Duration) error
    Invalidate(ctx context.Context, key string) error
}
```

**审计依据**: 
- `ApprovalDBTX`（`approval_manager.go:41`）是"故意为pgxmock可替换而设计"
- `analysis.DB`（`analysis/db.go:17`）是窄接口模板
- 模块已有独立持久化：`ApprovalManager.MarkTimeout` worker无hook触发

### 设计4: Manifest（声明式配置）

```yaml
# extends/<name>/manifest.yaml
apiVersion: llmgateway/v1
kind: Extension
metadata:
  name: budget-guard
  version: 1.0.0
  author: security-team
  description: "Per-tenant budget enforcement"
spec:
  type: hook        # hook | governance
  phases: [pre_routing]
  priority: 80      # 同phase内排序
  direction: input  # 仅governance type需要: input|output|both
  
  # 依赖管理（里程碑A）
  depends_on: []    # 插件级依赖，用于拓扑排序
  
  # 配置（里程碑A）
  configSchema:     # JSON Schema，加载期校验
    type: object
    properties:
      budget_usd: {type: number, minimum: 0}
    required: [budget_usd]
  config:
    budget_usd: 100.0
  
  # 能力声明（里程碑A）
  permissions:
    db: true           # 需要Storage.DB()
    llm_invoke: false  # 需要ExtensionContext.LLM()
    network: []        # 允许的外部host（WASM用）
  
  # 迁移（里程碑B）
  migrations:
    - version: "001"
      up: |
        CREATE TABLE IF NOT EXISTS plugin_budget_guard_usage (
          tenant_id text, month text, usage_usd numeric,
          PRIMARY KEY (tenant_id, month)
        )
```

**审计建议**: depends_on已足够表达现有`admin.ModuleDependency`，无需复杂的`requires`+cardinality。

### 设计5: MetadataRegistry（规范化现有~40个key）

```go
// domains/extensions/metakeys.go

type MetadataRegistry interface {
    // 插件注册它读写的key
    RegisterKey(key, pluginName, direction string) error  // direction: read|write|both
    
    // 检查悬空契约（写者无读者、读者无写者）
    Validate() []string  // 返回警告列表
    
    // 查询key的所有者
    Owners(key string) (writers, readers []string)
}

// 全局注册表（启动期填充）
var GlobalMetadataRegistry = NewMetadataRegistry()
```

**现有key迁移**: 在`Init()`时调用`RegisterKey`声明读写，host在加载完所有插件后调用`Validate()`检查悬空。

**审计依据**: `request_envelope.go:62-86`的注释契约已过期，需机器可检查的注册表。

### 设计6: 依赖管理（简化，分两期）

**里程碑A（必需）**: 
```go
// Registry 加载时拓扑排序
func (r *Registry) Register(ext Extension, ctx ExtensionContext) error {
    // 解析 manifest.depends_on
    // 检查循环依赖
    // 拓扑排序后顺序注册
}
```

**里程碑F+（可选，推迟）**:
- 运行时 `Host.Plugins().Has(name)`
- 带cardinality的 `requires`
- 只在出现"optional依赖"真需求时实现

**审计依据**: 现有28个hook都是必需依赖（nil就不注册），无optional需求。

---

## 里程碑（修正后顺序：降低风险）

### A1: 核心类型定义（1周）

**交付物**: 
- `domains/extensions/types.go`
  - Extension/HookExtension/GovernanceExtension接口
  - Manifest结构体
  - 编译期断言: `var _ pipeline.Hook`

**测试**:
- 接口方法完整性（5方法Hook、3方法Plugin+基础）
- Manifest YAML解析（valid/invalid cases）

**验收标准**:
- `go build ./domains/extensions` 通过
- 接口评审通过（确认1:1对齐）
- Manifest解析测试全绿

**不破坏验证**:
- [ ] `go build ./...` 全绿（新增包，不影响现有）
- [ ] `go test ./domains/pipeline ./domains/security` 全绿

---

### A2: ExtensionContext + Storage接口（1周）

**交付物**:
- `domains/extensions/context.go`
  - ExtensionContext结构体
  - 各字段接口定义（LLMClient、SettingsStore、BlobStorage等）
- `domains/extensions/storage.go`
  - Storage/DBHandle/KVStore接口
  - Migration结构体

**测试**:
- mock测试验证可替换性
- DBHandle接口是否足够窄（3个方法：QueryRow/Query/Exec）
- 配pgxmock示例

**验收标准**:
- 接口评审通过
- mock测试证明可测试性
- 文档说明各接口职责

**关键**: 本里程碑**只定义接口，不做实现**。确保接口稳定后再进B。

---

### B: Storage实现 + 表迁移（1.5周）

**交付物**:
- `domains/extensions/kv_store.go`
  - KVStore实现（backing表 `plugin_kv`）
  - namespace前缀自动添加
- `domains/extensions/migrator.go`
  - 幂等迁移运行器
  - 读manifest.migrations，应用到DB
  - 记录到`plugin_migrations`
- `db/migrations/`新增SQL:
  - `400_plugin_kv.sql` - KV backing表
  - `401_plugin_migrations.sql` - 迁移账本
  - `402_plugin_usage_hot.sql` - 用量统计表

**测试**:
- KV隔离测试（不同plugin/tenant不碰撞）
- 迁移幂等测试（重复应用无副作用）
- 并发安全测试
- 表前缀验证（plugin_{id}_*）

**验收标准**:
- 单测覆盖率 >80%
- 迁移可回滚（Down脚本）
- 文档说明KV namespace规则

**风险控制**: 
- 如A2接口需调整，B只需修改实现，接口调用者不受影响

---

### C: Registry + Adapter（1.5周）

**交付物**:
- `domains/extensions/registry.go`
  - Registry（推广security.Registry模式）
  - Register/List/Get方法
  - depends_on拓扑排序
  - 循环依赖检测
- `domains/extensions/adapter.go`
  - hookAdapter: HookExtension → pipeline.Hook (1:1映射)
  - governanceAdapter: GovernanceExtension → security.Plugin (1:1映射)
  - 编译期断言全部通过
- `domains/extensions/loader.go`
  - LoadBuiltin(ext) - 注册进程内插件
  - HooksForPhase(phase) - 返回pipeline.Hook切片
  - GovernancePlugins() - 返回security.Plugin切片
- 接入点修改:
  - `cmd/gateway/main_pipeline.go` 在`buildV2DispatchPipeline`末尾追加loader调用

**测试**:
- 拓扑排序正确性（A→B→C顺序）
- 循环依赖检测（A→B→A报错）
- adapter 1:1映射验证（Priority/Direction直接转发）
- 编译期断言通过

**验收标准**:
- 拓扑排序benchmark（100插件 <1ms）
- adapter无"魔法推导"逻辑
- 接入点diff review通过

**不破坏验证**:
- [ ] `buildV2DispatchPipeline`只在末尾追加，不修改现有stage
- [ ] 现有pipeline测试全绿

---

### D: Metrics + Health + Admin（1周）

**交付物**:
- `domains/extensions/metrics.go`
  - MetricsDecorator（包裹HookExtension.Execute）
  - 记录: plugin_name, tenant_id, phase, status, latency_ms, severity
  - UPSERT到`plugin_usage_hot`（复用`registry/usage_stats.go:11`模式）
  - 异步写明细到`plugin_call_events`
- `domains/extensions/health.go`
  - HealthMonitor（后台goroutine）
  - 周期调用Extension.HealthCheck()
  - 连续N次失败 → Registry.Disable(name)
- `admin/extensions_handler.go`
  - `GET /api/admin/extensions` - 列出所有插件+状态
  - `GET /api/admin/extensions/{name}` - 单个详情+最近N天用量
  - `POST /api/admin/extensions/{name}/enable|disable` - 运行态开关
  - `GET /api/admin/extensions/{name}/stats?days=30` - 用量曲线
- settings集成:
  - 动态注册`extensions.<name>.enabled` spec
  - `admin/modules.go`的`allModuleDefinitions`动态追加discovered插件
- 测试修正:
  - `admin/modules_test.go:17` 改为 `assert.GreaterOrEqual(len(modules), 17)`

**测试**:
- UPSERT并发测试
- 健康状态转换测试（Healthy→Unhealthy→Disabled）
- admin API集成测试

**验收标准**:
- admin页面可见插件列表
- 用量统计写入验证
- 健康检查覆盖失败恢复场景

**风险控制**:
- admin模块计数测试改为`>=`避免硬编码

---

### E': 新建简单插件验证链路（1周）

**交付物**: `domains/extensions/builtin/budget_guard.go`（builtin形式）

**插件设计**:
```go
type BudgetGuardExtension struct {
    storage  Storage
    budgetUSD float64
}

func (e *BudgetGuardExtension) Name() string { return "budget-guard" }
func (e *BudgetGuardExtension) Priority() int { return 80 }
func (e *BudgetGuardExtension) Execute(ctx, env) error {
    // 读 env.TenantID
    // Storage.KV().Lookup("usage:" + month)
    // 如果超预算，返回 Verdict{Allow:false, Code:"budget_exceeded"}
    // env.EnsureGovernance().RecordVerdict(v)
    return nil
}
```

**验证目标**:
- 完整链路: Register → Init → Execute → Verdict → interception.Engine → 403
- Metrics写入: plugin_usage_hot有记录
- Admin可见: /api/admin/extensions列出budget-guard
- Health检查: 模拟失败，验证Disable

**为何选这个插件**:
- 逻辑简单（只读KV+判断）
- 无复杂依赖（不需owner查询、不需多表join）
- 有实用价值（成本护栏是真需求）
- 风险低（失败不影响现有功能）

**对比outputcompliance**:
- ❌ outputcompliance: 4表、3调用点、owner闭包、PII检测
- ✅ budget-guard: 1个KV key、纯计算、无外部依赖

**验收标准**:
- 预算超限请求返回403
- admin统计正确
- 健康检查通过

**不破坏验证**:
- [ ] 现有请求路径不受影响（budget-guard可通过enabled开关）

---

## 推迟到基础稳定后的里程碑

### F: gRPC + extends/加载（2周）

**交付物**:
- `domains/extensions/grpc/extensions.proto`
  - Extension服务（Metadata/Init/Execute/Inspect/HealthCheck）
  - Host回调服务（LLM/Metrics/KV/DB）
- `domains/extensions/grpc_loader.go`
  - hashicorp/go-plugin集成
  - HandshakeConfig
  - 扫描`extends/*/manifest.yaml`
  - 启动二进制、握手、Init
- 示例独立项目: `extends/example-greet/`
  - 独立go.mod
  - 实现Extension gRPC服务
  - README说明编译步骤

**推迟理由**: 
- 需A-E'验证进程内插件系统稳定
- gRPC跨版本兼容需额外设计

---

### E: 迁移outputcompliance（1.5周）

**交付物**:
- `domains/outputcompliance`改造为实现HookExtension
- 用Storage.DB()替换裸`*sql.DB`
- 用Storage.Migrations()声明4个表
- 保持行为100%一致

**推迟理由**:
- 等E'验证链路后再做复杂迁移
- outputcompliance有owner查询闭包、3处调用点，风险高

---

## 测试策略

### 单元测试
- 每个新包（extensions/*）独立单测
- adapter 1:1映射验证（Priority/Direction直接转发）
- Storage KV隔离/迁移幂等/并发安全
- 拓扑排序正确性+循环检测
- MetadataRegistry悬空契约检测

### 集成测试
- builtin Extension → Register → pipeline.Execute → Verdict → 统计写入
- 健康检查失败 → Disable → admin查询状态=disabled
- budget-guard超限 → 403 + admin统计++

### 回归测试
- `go build ./...` 全绿
- `go test ./domains/pipeline ./domains/security` 全绿（未改包）
- `admin/modules_test.go` 通过（改为`>=17`）

---

## 风险与缓解

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|----------|
| A2接口设计需迭代 | 中 | 高 | 纸面设计+评审，有mock验证后再实现B |
| 拓扑排序性能(>50插件) | 低 | 中 | 预留benchmark，当前17插件无影响 |
| admin模块计数测试失败 | 高 | 低 | `modules_test.go`改为`>=17` |
| E'简单插件仍失败 | 低 | 高 | 选最简用例（只读Metadata），有备选方案 |
| KV namespace冲突 | 低 | 中 | 前缀规则明确文档，加冲突检测 |

---

## 不破坏验证清单

实施前/中/后持续验证：

### 接口不变
- [ ] `pipeline.Hook` 接口未修改（5个方法签名不变）
- [ ] `security.Plugin` 接口未修改（3个方法签名不变）
- [ ] `PipelineRequest` 只增字段，无删除

### 现有代码不变
- [ ] 现有28个hook的 `var _ pipeline.Hook` 断言全部保留
- [ ] `buildV2DispatchPipeline` 只在末尾追加loader调用
- [ ] `domains/pipeline/pipeline.go` 零修改
- [ ] `domains/security/{plugin,registry,hook}.go` 零修改

### 测试全绿
- [ ] `go build ./...` 通过
- [ ] `go test ./domains/pipeline` 全绿
- [ ] `go test ./domains/security` 全绿
- [ ] `go test ./admin` 通过（modules_test改后）

---

## 文档交付

随代码交付：
1. ✅ `docs/plugin-system-audit-report.md`（已完成）
2. ✅ `docs/plugin-system-implementation-plan-v4.md`（本文档）
3. ⏳ `docs/plugin-system-architecture-decision-records.md`（ADR，下一步）
4. ⏳ `domains/extensions/README.md`（插件开发指南，里程碑A1）
5. ⏳ `extends/budget-guard/README.md`（示例，里程碑E'）

---

## 下一步行动

### 本周
1. **评审本文档** - 确认v4方案是否采纳全部审计修正
2. **评审接口设计** - HookExtension/Storage/ExtensionContext
3. **确认里程碑顺序** - A1→A2→B→C→D→E'

### 下周
1. **开始A1实现** - types.go + Manifest解析
2. **准备A2评审材料** - Storage接口设计草稿+mock示例

### 评审检查清单
- [ ] HookExtension是否完整覆盖pipeline.Hook的5个方法？
- [ ] GovernanceExtension是否完整覆盖security.Plugin的3个方法？
- [ ] Storage.DB()返回的接口是否足够窄（3方法）？
- [ ] Manifest的depends_on是否足以表达现有admin.ModuleDependency？
- [ ] Adapter是否1:1映射无魔法？
- [ ] 是否删除了所有无需求证据的功能（ServiceRegistry/MemoryBus/Has()）？

---

**版本历史**:
- v1-v2: 初始设计（已废弃）
- v3: 数据/通讯/依赖为一等公民（审计后发现过度设计）
- v4: 审计修正版（本文档，简化+降低风险）

**批准**: 待评审  
**实施**: 待批准后开始
