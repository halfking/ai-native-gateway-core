# 经验教训：billing_mode vs plan_type 业务逻辑错误

**日期**: 2026-07-10  
**问题**: Migration 327-334 引入了错误的业务逻辑假设  
**后果**: claude-fable-5, mimo-v2.5-pro 等模型无法路由

---

## 🚨 问题根因

### 错误的假设

Migration 327（2026-07-03）引入了一个**根本性的设计错误**：

```sql
-- 错误的假设：billing_mode 必须与 plan_type 一致
WHEN c.plan_type IN ('token_plan', 'code_plan', 'agent_plan')
     AND cmb.billing_mode NOT IN ('token_plan', 'code_plan', 'agent_plan') THEN false
```

这个假设认为：如果 credential 是套餐类型（token_plan），那么 binding 的 billing_mode 也必须是套餐类型。

### 为什么这是错误的

**billing_mode** 和 **plan_type** 是两个**完全独立的概念**：

| 概念 | 含义 | 用途 | 示例 |
|------|------|------|------|
| `billing_mode` | 从供应商**采购**的计费方式 | 记录成本 | per_token, free, token_plan |
| `plan_type` | 向客户**销售**的计费方式 | 收费客户 | token, token_plan, code_plan |

**正常商业场景**：
- 我们可以按量采购（per_token），但以套餐方式（token_plan）卖给客户
- 我们可以免费采购（free），但以按量方式（per_token）卖给客户
- 这两个字段**不应该**有强制一致性约束

### 错误的影响

1. **claude-fable-5**:
   - credential 17: plan_type='token_plan'（套餐销售）
   - binding: billing_mode='per_token'（按量采购）
   - 结果: is_routable=false, reason='plan_incompatible_cmb_requires_per_token'

2. **mimo-v2.5-pro**:
   - 同样的 plan_type vs billing_mode 不匹配
   - 额外问题：重复的 provider_model 条目

---

## 📚 经验教训

### 教训 1：深入理解业务场景后再修改代码

**错误做法**：
- 看到 credential 6 的 plan_type='token_plan' 和 cmb.billing_mode='per_token' 不匹配
- 假设这是"数据不一致"
- 通过 migration 强制让它们一致

**正确做法**：
- 理解这两个字段的业务含义
- 询问业务人员：为什么这两个字段会不同？
- 确认这是正常商业场景，不是数据错误

### 教训 2：修改业务逻辑前先确认 SSOT

**错误做法**：
- 直接在视图中添加一致性校验
- 假设 plan_type 应该是 SSOT

**正确做法**：
- 确认每个字段的业务含义和来源
- billing_mode 来自 provider_model_bindings（采购侧）
- plan_type 来自 credentials（销售侧）
- 两者独立，不需要一致性

### 教训 3：数据库视图修改需要谨慎

**错误做法**：
- 修改视图定义，添加新的校验逻辑
- 没有考虑对下游代码的影响

**正确做法**：
- 修改视图前，检查所有依赖该视图的代码
- 确保视图列数、列名、列顺序不变
- 提供完整的回滚方案

### 教训 4：迁移脚本需要有回滚机制

**错误做法**：
- migration 334 直接修改了 145 行数据（billing_mode = plan_type）
- 没有保留原始数据

**正确做法**：
- 修改数据前先备份
- 记录原始值到审计表
- 提供 down migration

---

## ✅ 正确的做法

### 1. 修改业务逻辑前

```
1. 理解字段的业务含义
2. 确认数据来源（哪个表、哪个字段）
3. 询问业务人员确认需求
4. 检查现有代码如何使用这些字段
5. 设计方案后找同事 review
```

### 2. 修改数据库视图前

```
1. 检查所有依赖该视图的代码
2. 确保列数、列名、列顺序不变
3. 只修改逻辑，不修改结构
4. 提供 down migration
5. 在测试环境验证
```

### 3. 修改生产数据前

```
1. 备份原始数据
2. 记录修改前后的差异
3. 提供回滚脚本
4. 在低峰期执行
5. 执行后立即验证
```

---

## 🔧 如何避免类似问题

### 代码 Review 时的检查点

1. **业务逻辑一致性**：修改是否符合业务场景？
2. **字段含义**：是否正确理解每个字段的用途？
3. **下游影响**：修改是否影响其他代码？
4. **回滚方案**：如果出问题，能否快速回滚？
5. **数据备份**：修改数据前是否备份？

### 数据库修改的检查点

1. **视图兼容性**：列数、列名、列顺序是否一致？
2. **依赖检查**：哪些代码依赖这个视图？
3. **性能影响**：修改是否影响查询性能？
4. **约束检查**：是否添加了正确的约束？
5. **审计日志**：是否记录了修改历史？

---

## 📝 关键决策记录

### 2026-07-10：移除 plan_type vs billing_mode 校验

**决策**：
- 移除 migration 327 引入的 plan_type vs billing_mode 一致性校验
- 理由：billing_mode 是采购模式，plan_type 是销售模式，两者应该独立

**影响**：
- claude-fable-5, mimo-v2.5-pro 等模型恢复可路由
- 不再限制合理的采购-销售模式组合

**验证**：
- 执行 SQL 验证模型可路由状态
- 监控实时请求流确认恢复

---

## 🎯 总结

**核心教训**：
1. **理解业务场景**：修改代码前先理解业务需求
2. **确认字段含义**：不要假设字段的用途，要确认
3. **谨慎修改视图**：数据库视图修改需要全面考虑
4. **提供回滚方案**：任何修改都要有回滚机制

**正确的心态**：
- 不是"数据不一致"就是错误
- 有些"不一致"是正常的业务场景
- 修改前先问"为什么"，再问"怎么做"

---

**记录人**: OpenCode Agent  
**日期**: 2026-07-10  
**相关文档**: `docs/migration-335-336-audit.md`
