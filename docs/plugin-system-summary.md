# Hook插件化重构方案总结

**日期**: 2024-07-09  
**状态**: 文档已完成，待第二轮审计验证

---

## 已交付文档

1. ✅ **审计报告** (`plugin-system-audit-report.md`)
   - 全面审计v3方案
   - 发现3处关键问题
   - 提出修正建议

2. ✅ **实施计划v4** (`plugin-system-implementation-plan-v4.md`)
   - 采纳全部审计修正
   - 详细里程碑划分（A1/A2/B/C/D/E'）
   - 风险控制措施

3. ✅ **架构决策记录** (`plugin-system-architecture-decision-records.md`)
   - 10条ADR记录所有关键决策
   - 每条ADR含背景/理由/后果/替代方案
   - 决策矩阵总览

---

## 核心修正（相比v3）

### 修正1: 接口完全对齐 ✅
**问题**: v3的RequestHookExt缺少Name/Priority/Enabled三个方法  
**修正**: HookExtension = Extension + 完整5方法Hook接口  
**ADR**: ADR-001

### 修正2: 简化通讯层 ✅
**问题**: 三层通讯引入废弃的MemoryBus和无需求的ServiceRegistry  
**修正**: 两层 - Metadata规范化 + analysis.bus  
**ADR**: ADR-002, ADR-003, ADR-007

### 修正3: 降低里程碑风险 ✅
**问题**: 首个迁移目标outputcompliance太复杂（4表/3调用点）  
**修正**: E'改为新建简单插件budget-guard，推迟复杂迁移  
**ADR**: ADR-006

---

## v4方案特点

### 设计原则
1. **零破坏** - 现有Hook/Plugin接口不变，28个hook原地保留
2. **数据流清晰** - 请求数据走hook中介，持久化独立（审计验证通过）
3. **最小化** - 删除所有无需求证据的功能（YAGNI原则）
4. **渐进式** - 分期实现，降低风险

### 核心组件

```
Extension (生命周期)
  ├─ HookExtension (1:1对齐pipeline.Hook)
  └─ GovernanceExtension (1:1对齐security.Plugin)

ExtensionContext (服务句柄)
  ├─ Storage (KV + DB + Migrations)
  ├─ Metadata (规范化注册表)
  ├─ Events (analysis.bus.Publisher)
  ├─ LLM (回调网关)
  └─ Settings/Blobs/Logger/Metrics

Registry (拓扑排序 + Adapter)
  └─ hookAdapter / governanceAdapter (1:1映射)
```

### 删除项（审计建议）
- ❌ ServiceRegistry（无需求证据）
- ❌ eventbus.MemoryBus复活（零生产订阅者）
- ❌ 运行时Has()/Get()依赖查询（推迟）
- ❌ manifest的requires+cardinality（推迟）

---

## 里程碑计划

```
A1: 核心类型定义 (1周)
  └─ Extension/HookExtension/Manifest

A2: Context+Storage接口 (1周) 
  └─ 纸面设计+mock验证，不做实现

B: Storage实现 (1.5周)
  └─ KV/DB/Migrations + 3个表

C: Registry+Adapter (1.5周)
  └─ 拓扑排序 + 1:1映射 + 接入pipeline

D: Metrics+Health+Admin (1周)
  └─ 用量统计 + 健康检查 + admin API

E': 简单插件验证 (1周)
  └─ budget-guard (KV + Verdict)

--- 推迟到基础稳定后 ---
F: gRPC + extends/ (2周)
E: 迁移outputcompliance (1.5周)
```

**总计**: 核心6周，完整交付9.5周

---

## 审计验证通过的设计

### ✅ 数据流边界
- 请求数据: hook读env → 扁平参数 → 模块返回 → hook写env
- 持久化: 模块自持DB，独立读写（不经hook）
- 后台worker验证: ApprovalManager.MarkTimeout无hook触发

### ✅ 返回契约
- 模块返回结构化结果（如*ComplianceResult）
- hook消费返回值并写回env
- 无字面"回调函数"（唯一的func注入方向相反）

### ✅ Storage独立性
- 复用ApprovalDBTX/analysis.DB窄接口模板
- KV namespace自动隔离
- DB表强制plugin_{id}_前缀
- 可mock，测试友好

---

## 关键决策总览

| 决策点 | 选择 | 理由 | ADR |
|-------|------|------|-----|
| Extension接口 | 完全对齐Hook | 1:1映射，零魔法 | 001 |
| 插件间通讯 | Metadata传递 | 现有模式足够 | 002 |
| 事件总线 | 只用analysis.bus | MemoryBus已废弃 | 003 |
| 持久化 | 双层Storage | KV默认+DB可选 | 004 |
| 依赖管理 | 分两期 | 先必需后可选 | 005 |
| 首个验证 | budget-guard | 简单低风险 | 006 |
| Metadata | 注册表机制 | 机器可检查 | 007 |
| Adapter | 1:1映射 | 显式优于隐式 | 008 |
| 独立项目运行时 | go-plugin | 成熟度高 | 009 |
| 表隔离 | 前缀而非schema | 简化权限 | 010 |

---

## 风险控制

### 设计阶段（已完成）
- ✅ A1/A2分离（接口设计先于实现）
- ✅ 纸面设计+mock验证
- ✅ 审计报告验证可行性

### 实施阶段（计划中）
- 里程碑A2有评审检查清单
- 每个里程碑有独立验收标准
- 不破坏验证清单贯穿始终
- E'简单插件验证链路后再做复杂迁移

### 已识别风险
- ⚠️ A2接口设计可能需迭代（缓解：评审+mock）
- ⚠️ 拓扑排序性能（缓解：benchmark，当前17插件无影响）
- ⚠️ admin模块计数测试失败（缓解：改为>=17）

---

## 预期收益

### 技术收益
- ✅ 插件可独立编译测试（Storage抽象）
- ✅ 插件生命周期管理（Init/Health/Metrics）
- ✅ 声明式依赖+拓扑激活（解决硬编码顺序）
- ✅ 跨插件数据契约规范化（Metadata注册表）
- ✅ admin统一管理（插件即模块）

### 业务收益
- 新功能可独立开发（如budget-guard）
- 第三方可贡献插件（extends/）
- A/B测试更容易（插件级开关）
- 故障隔离（插件健康检查+禁用）

---

## 下一步行动

### 立即
1. **人工评审三份文档** 
   - 审计报告的3处修正是否合理？
   - 实施计划v4的里程碑是否可行？
   - ADR的10条决策是否认同？

2. **第二轮审计验证**
   - 验证v4方案是否完全解决了审计发现的问题
   - 检查是否引入新问题
   - 确认所有决策有充分理由

### 本周
- 评审A1/A2接口设计
- 确认里程碑顺序
- 准备开发环境

### 下周
- 开始A1实现

---

## 文档清单

```
docs/
├── plugin-system-audit-report.md         (审计报告)
├── plugin-system-implementation-plan-v4.md  (实施计划)
├── plugin-system-architecture-decision-records.md  (ADR)
└── plugin-system-summary.md              (本文档)
```

**所有文档已完成，待评审和第二轮审计验证。**

---

## 评审检查清单

### 架构一致性
- [ ] HookExtension是否完整覆盖pipeline.Hook的5个方法？
- [ ] GovernanceExtension是否完整覆盖security.Plugin的3个方法？
- [ ] Adapter是否1:1映射无魔法？

### 数据流边界
- [ ] Storage独立性是否符合现状（后台worker证据）？
- [ ] 请求数据中介是否保持（hook读env→模块→hook写env）？
- [ ] 返回契约是否正确（返回值+hook写env，无回调）？

### 简化验证
- [ ] 是否删除了所有无需求证据的功能？
- [ ] 是否采纳了所有审计建议的修正？
- [ ] 里程碑是否降低了风险（E'简单插件优先）？

### 可实施性
- [ ] 每个里程碑是否有清晰的交付物？
- [ ] 测试策略是否完整？
- [ ] 不破坏验证清单是否可执行？

---

**准备就绪，等待评审和第二轮审计。**
