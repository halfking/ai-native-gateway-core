# Hook插件化重构架构决策记录（ADR）

**项目**: llm-gateway-go Hook插件化重构  
**日期**: 2024-07-09  
**状态**: 已提议待评审  

---

## ADR-001: 采用HookExtension接口完全对齐现有pipeline.Hook

### 背景

v3方案提出了`RequestHookExt`接口，只包含`Execute`和`OnError`两个方法。审计发现现有`pipeline.Hook`有5个方法（Name/Execute/Priority/Enabled/OnError），且所有17个实现都显式提供Priority和Enabled。

### 决策

**采纳**: HookExtension接口完全继承pipeline.Hook的5个方法

```go
type HookExtension interface {
    Extension  // Name/Init/HealthCheck
    Execute(ctx context.Context, env *domain.PipelineRequest) error
    Priority() int
    Enabled(ctx context.Context, env *domain.PipelineRequest) bool
    OnError(ctx context.Context, env *domain.PipelineRequest, err error) error
}
```

### 理由

1. **1:1映射，零魔法** - adapter不需要从Manifest推导Priority，插件直接表达行为
2. **保持一致性** - 现有17个Hook实现都有明确的Priority()返回值
3. **避免惊讶** - 插件开发者熟悉现有Hook接口，学习成本低
4. **类型安全** - 编译期断言`var _ pipeline.Hook = (*hookAdapter)(nil)`确保完整性

### 后果

- ✅ adapter实现简单（纯转发）
- ✅ 插件可完全控制Priority/Enabled逻辑
- ⚠️ 插件必须实现所有5个方法（但这与现状一致）

### 替代方案（已拒绝）

- **方案A**: 从Manifest.priority推导Priority() - 拒绝理由：破坏"插件直接表达行为"原则
- **方案B**: 使用默认值（Priority=100, Enabled=true） - 拒绝理由：隐藏行为，不符合显式优于隐式

---

## ADR-002: 删除ServiceRegistry，推迟到真实需求出现

### 背景

v3方案提出三层通讯模型，其中层3是typed ServiceRegistry用于插件间同步调用。审计发现现有28个hook中无任何插件间typed调用需求。

### 决策

**拒绝**: 不实现ServiceRegistry，推迟到真实需求出现

### 理由

1. **无需求证据** - 现有代码刻意避免插件间调用（outputcompliance不调sessionaudit）
2. **YAGNI原则** - You Aren't Gonna Need It - 过早抽象导致复杂度
3. **现有模式足够** - `OwnerContextFunc`闭包注入已满足唯一的"跨模块能力复用"需求
4. **避免循环依赖** - ServiceRegistry需要contracts包，引入额外复杂度

### 后果

- ✅ 简化实施（少一个核心组件）
- ✅ 避免过早设计
- ⚠️ 如未来真需要，需补充实现（但审计认为概率低）

### 未来触发条件

如出现以下需求，重新评估：
- 插件B需要主动调用插件A的方法（非Metadata传递）
- 需要运行时发现其他插件提供的能力
- 有3个以上插件需要复用同一能力接口

### 替代方案（当前采纳）

- **Metadata传递** - 层1的typed Metadata key足够传递跨插件状态
- **构造期注入** - 复用现有`OwnerContextFunc`闭包注入模式

---

## ADR-003: 复用analysis.bus.Publisher而非eventbus.MemoryBus

### 背景

v3方案提出两种事件总线：ephemeral (MemoryBus) 和 durable (analysis.bus)。审计发现MemoryBus在生产环境零订阅者，真正的异步通道是analysis.bus。

### 决策

**采纳**: 只暴露analysis.bus.Publisher作为ExtensionContext.Events

```go
type ExtensionContext struct {
    Events DurablePublisher  // analysis.bus.Publisher
}
```

### 理由

1. **MemoryBus已废弃** - grep确认零生产订阅者（只有测试mock）
2. **analysis.bus是真实通道** - 有生产消费者（intent_worker、session_summary_worker）
3. **语义明确** - durable、at-least-once、PG-backed，适合审计/分析类事件
4. **避免维护两套** - 减少复杂度

### 后果

- ✅ 插件发布的事件可被worker持久化消费
- ⚠️ 不适合高频临时通知（但审计认为无此需求）
- ⚠️ 如未来需要ephemeral事件，需重新设计

### 替代方案（已拒绝）

- **复活MemoryBus** - 需补订阅者，工作量大且价值低
- **两者都暴露** - 增加复杂度，插件需选择，易混淆

---

## ADR-004: Storage采用双层设计（KV默认 + DB声明式opt-in）

### 背景

插件需要持久化配置/状态/审计记录。借鉴Dify (namespaced KV) 和 Kong (plugin-owned tables + migrations)。

### 决策

**采纳**: 双层Storage接口

```go
type Storage interface {
    KV() KVStore          // Tier 1: namespaced, 自动前缀 plugin:{id}:tenant:{tid}:
    DB() DBHandle         // Tier 2: 窄接口，非裸pool
    Migrations() []Migration  // 声明式迁移
}
```

### 理由

1. **分层适配需求** - 简单插件用KV（如budget-guard计数），复杂插件用DB（如审计多字段查询）
2. **隔离性** - KV自动namespace避免冲突；DB表强制`plugin_{id}_`前缀
3. **可测试性** - DBHandle是窄接口（QueryRow/Query/Exec），可用pgxmock替换
4. **审计验证通过** - 符合现有"模块自持DB，独立读写"的边界

### 后果

- ✅ 简单插件零SQL（KV即可）
- ✅ 复杂插件有完整DB能力
- ✅ 可mock，测试友好
- ⚠️ 需维护KV backing表和迁移账本

### 关键设计点

- **KV namespace规则**: `plugin:{pluginID}:tenant:{tenantID}:{userKey}`
- **DB表前缀规则**: `plugin_{pluginID}_*`
- **迁移幂等**: `CREATE TABLE IF NOT EXISTS`, 版本记录到`plugin_migrations`
- **窄接口模板**: 复用`analysis.DB`和`ApprovalDBTX`的3方法接口

---

## ADR-005: 依赖管理分两期（先必需，后可选）

### 背景

v3方案提出完整依赖管理：声明式depends_on + requires(cardinality) + 运行时Has()/Get()。审计发现现有28个hook都是必需依赖（nil就不注册），无optional需求。

### 决策

**采纳**: 分两期实现

**里程碑A（必需）**:
- 声明式`depends_on: [pluginID...]`
- 构建期拓扑排序
- nil-check式存在性（保持现状）

**里程碑F+（可选，推迟）**:
- 运行时`Has(name)`
- 带cardinality的`requires`

### 理由

1. **YAGNI** - 现有hook无optional依赖需求
2. **降低复杂度** - 运行时查询需额外基础设施（注册表、性能优化）
3. **渐进交付** - 先满足已知需求（拓扑排序），后续按需扩展

### 后果

- ✅ 实施简化
- ✅ 解决核心问题（硬编码顺序 → 声明式depends_on）
- ⚠️ 如未来需要optional依赖，需补充实现

### 触发条件（重新评估）

如出现以下需求：
- 插件声明"如果X存在则启用Y功能，否则降级"
- 需要运行时检查其他插件状态
- 有3个以上插件需要optional依赖

---

## ADR-006: 先验证简单插件（budget-guard），推迟复杂迁移（outputcompliance）

### 背景

原计划里程碑E是迁移outputcompliance作为首个示例。审计发现outputcompliance有4个表、3处调用点、owner查询闭包，风险高。

### 决策

**采纳**: 里程碑E'改为新建简单插件budget-guard

**推迟**: 迁移outputcompliance到里程碑E（在E'验证后）

### 理由

1. **降低首次验证风险** - budget-guard逻辑简单（KV读写+判断），无外部依赖
2. **快速反馈** - 1周内可验证完整链路（Register→Execute→Verdict→Metrics→Admin）
3. **失败不影响现有功能** - budget-guard是新增，失败不破坏outputcompliance
4. **有实用价值** - 成本护栏是真实需求，非toy example

### 对比

| 特性 | outputcompliance | budget-guard |
|------|------------------|--------------|
| 表数量 | 4个 | 0个（只用KV） |
| 调用点 | 3个（hook/interceptor/control） | 1个（hook） |
| 外部依赖 | owner查询闭包、PII检测 | 无 |
| 风险 | 高（失败影响合规功能） | 低（新增，可回滚） |
| 实施周期 | 1.5周 | 1周 |

### 后果

- ✅ 快速验证插件系统可行性
- ✅ 降低项目风险
- ⚠️ outputcompliance迁移推迟（但审计认为E'后迁移更安全）

---

## ADR-007: Metadata规范化为注册表机制，而非继续注释契约

### 背景

现有`request_envelope.go:62-86`用注释记录~40个Metadata key的写者→读者契约，审计发现已过期（实际有更多未登记key）。

### 决策

**采纳**: 实现MetadataRegistry机器可检查注册表

```go
type MetadataRegistry interface {
    RegisterKey(key, pluginName, direction string) error  // read|write|both
    Validate() []string  // 返回悬空契约警告
}
```

### 理由

1. **可验证** - 机器检查悬空契约（写者无读者、读者无写者）
2. **自文档** - 注册即文档，不会过期
3. **IDE友好** - 可生成常量/类型提示
4. **审计需求** - 插件读写的key可追溯

### 后果

- ✅ Metadata契约不再依赖人工维护注释
- ✅ 启动期可检查悬空key
- ⚠️ 现有~40个key需迁移注册（一次性工作）

### 实施细节

- 插件在`Init()`调用`ctx.Metadata.RegisterKey("my_key", "my-plugin", "write")`
- host在加载完所有插件后调用`GlobalMetadataRegistry.Validate()`
- 警告输出到日志（不阻止启动，但CI可配置fail on warnings）

---

## ADR-008: adapter采用1:1映射，拒绝从Manifest推导行为

### 背景

可以让adapter从Manifest推导缺失的方法（如Priority()读manifest.priority），但这会隐藏插件行为。

### 决策

**采纳**: adapter纯转发，不推导

```go
type hookAdapter struct {
    ext HookExtension
}
func (a *hookAdapter) Priority() int {
    return a.ext.Priority()  // 直接转发，不读manifest
}
```

### 理由

1. **显式优于隐式** - 插件行为应在代码中明确表达
2. **类型安全** - 编译期确保方法存在
3. **调试友好** - Priority()返回值可在插件代码中断点
4. **避免双重真相** - 不在Manifest和代码中重复声明Priority

### 后果

- ✅ adapter实现简单（100行内）
- ✅ 行为明确
- ⚠️ Manifest的priority字段仅用于文档（但审计认为这是正确的，行为应在代码表达）

### 替代方案（已拒绝）

- **从Manifest推导** - 隐藏行为，调试难
- **Manifest校验** - 在Init时比对manifest.priority与ext.Priority()是否一致 - 过度复杂

---

## ADR-009: 采用hashicorp/go-plugin而非WASM作为独立项目运行时

### 背景

独立项目（extends/）可以用gRPC子进程（go-plugin）或WASM。审计建议优先go-plugin。

### 决策

**采纳**: 里程碑F使用hashicorp/go-plugin

**推迟**: WASM作为里程碑F+的可选运行时

### 理由

1. **语言一致** - 插件用Go编写，与主项目一致，开发体验好
2. **成熟度** - go-plugin经Terraform/Vault生产验证
3. **调试友好** - 子进程可attach debugger，WASM调试难
4. **功能完整** - DB/LLM回调容易实现（gRPC），WASM受限于host functions
5. **审计建议** - OSS调研确认go-plugin是Go生态的标准选择

### 后果

- ✅ 插件开发者用熟悉的Go工具链
- ✅ 崩溃隔离（子进程crash不影响主进程）
- ⚠️ 不支持非Go语言插件（但审计认为无此需求）
- ⚠️ 跨版本兼容需HandshakeConfig协商

### WASM保留场景

如未来需要以下特性，重新评估WASM：
- 非Go语言插件（Rust/TinyGo）
- 强沙箱（网络隔离、CPU/内存限制）
- 热加载（WASM模块可热替换，go-plugin需重启）

---

## ADR-010: 表前缀plugin_{id}_而非schema隔离

### 背景

插件owned表可以用schema隔离（每个插件一个schema）或表前缀隔离。

### 决策

**采纳**: 表前缀`plugin_{pluginID}_*`

### 理由

1. **简化权限** - 单一schema，RLS策略统一
2. **迁移简单** - 不需要CREATE SCHEMA权限
3. **备份友好** - 单schema备份，不需要逐schema
4. **审计友好** - 所有插件表在同一`\dt plugin_*`列表
5. **Kong先例** - Kong也用表前缀而非schema隔离

### 后果

- ✅ 实施简单
- ✅ 权限管理简单
- ⚠️ 表名较长（但可读性强）

### 替代方案（已拒绝）

- **schema隔离** - 权限复杂，备份复杂
- **统一命名约定** - 无前缀，冲突风险高

---

## 决策矩阵总结

| ADR | 决策 | 状态 | 优先级 |
|-----|------|------|--------|
| 001 | HookExtension完全对齐Hook | 采纳 | P0 |
| 002 | 删除ServiceRegistry | 拒绝实现，推迟 | P0 |
| 003 | 只用analysis.bus，不用MemoryBus | 采纳 | P0 |
| 004 | Storage双层（KV+DB） | 采纳 | P0 |
| 005 | 依赖管理分两期 | 采纳 | P1 |
| 006 | 先验证budget-guard，后迁移outputcompliance | 采纳 | P0 |
| 007 | Metadata注册表机制 | 采纳 | P1 |
| 008 | adapter 1:1映射无魔法 | 采纳 | P0 |
| 009 | go-plugin优先于WASM | 采纳 | P1 |
| 010 | 表前缀而非schema隔离 | 采纳 | P1 |

---

## 开放问题

### Q1: 如何处理插件间的数据共享？

**现状**: 通过Metadata传递（如audit_result）  
**决策**: 保持现状，不引入ServiceRegistry  
**触发重新评估**: 如有3+插件需要typed接口调用

### Q2: 插件升级时的迁移兼容性？

**现状**: 未设计  
**提议**: Migrations的Version字段支持向前兼容（Up脚本幂等）  
**待决策**: 是否需要Down脚本强制可回滚？

### Q3: 多租户下的插件级配置？

**现状**: manifest.config是全局的  
**提议**: 支持tenant-level override（settings层）  
**待决策**: 里程碑D实施时确认

---

## 参考文献

- [审计报告](./plugin-system-audit-report.md)
- [实施计划v4](./plugin-system-implementation-plan-v4.md)
- [OSGi Declarative Services](https://docs.osgi.org/specification/osgi.cmpn/7.0.0/service.component.html)
- [VS Code Extension API](https://code.visualstudio.com/api/references/extension-manifest)
- [Kong Plugin Development](https://developer.konghq.com/custom-plugins/)
- [hashicorp/go-plugin](https://github.com/hashicorp/go-plugin)
- [Dify Plugin System](https://docs.dify.ai/en/develop-plugin/features-and-specs/)

---

**版本**: 1.0  
**批准**: 待评审  
**下次更新**: 里程碑A1完成后，补充实际实施中的决策
