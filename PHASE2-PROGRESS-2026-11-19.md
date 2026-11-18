# Phase 2 进度报告：M1 领域并行实现（初始阶段）

> 报告日期: 2026-11-19  
> 阶段: Phase 2 - M1 领域并行实现（启动）  
> 状态: 🔄 进行中  
> 参考: [PHASE-NEXT-IMPLEMENTATION-PLAN.md](../../docs/architecture/unified-architecture/PHASE-NEXT-IMPLEMENTATION-PLAN.md)

## 执行摘要

Phase 2 的基础架构已搭建完成。已创建 Inventory（库存）和 Commercial（报价）两个核心领域的服务接口，设计了完整的数据库 schema，为后续实现数据持久化逻辑奠定了基础。

## 已完成工作

### 1. 领域目录结构创建 ✅

```
llm-gateway-go/domains/
├── inventory/
│   ├── internal/           # 内部实现（待添加）
│   └── service.go          # InventoryPort 实现骨架
├── commercial/
│   ├── internal/           # 内部实现（待添加）
│   └── service.go          # QuotePort 实现骨架
├── trading/
│   └── internal/           # 预留目录
└── ledger/
    └── internal/           # 预留目录
```

### 2. 数据库 Schema 设计 ✅

**文件**: `migrations/maas_v01_schema.sql`

**覆盖领域**:
- **Inventory**: `maas_holds`, `maas_hold_lines`, `maas_inventory_snapshots`, `maas_sellable_items`
- **Commercial**: `maas_quotes`
- **Trading**: `maas_entitlements`
- **Ledger**: `maas_usage_holds`, `maas_usage_settlements`, `maas_usage_corrections`
- **Cross-domain**: `maas_idempotency_log`

**设计要点**:
- ✅ 金额字段使用 `TEXT` 存储 decimal string
- ✅ 状态机用 `CHECK` 约束强制（如 `status IN ('pending', 'committed', ...)`）
- ✅ 幂等键索引：`(tenant_id, command_type, idempotency_key)`
- ✅ OCC 版本控制：`inventory_revision` 字段
- ✅ 围栏令牌（fencing token）防止并发确认冲突

### 3. Inventory 服务骨架 ✅

**文件**: `domains/inventory/service.go`

**实现接口**:
```go
type Service struct {
    pool *pgxpool.Pool
}

// 实现 maascontracts.InventoryPort 的 5 个方法：
- QuerySellable(...)  // 查询可售库存
- HoldCreate(...)     // 创建预留（pending）
- HoldConfirm(...)    // 确认预留（committed）
- HoldRelease(...)    // 释放预留（aborted）
- HoldExpire(...)     // 过期预留（expired）
```

**当前状态**: 
- ✅ 接口签名完整
- ✅ 编译通过
- 🔄 业务逻辑待实现（标记为 TODO）

### 4. Commercial 服务骨架 ✅

**文件**: `domains/commercial/service.go`

**实现接口**:
```go
type Service struct {
    pool *pgxpool.Pool
}

// 实现 maascontracts.QuotePort 的 1 个方法：
- EstimateQuote(...)  // 纯估价（无状态）
```

**当前状态**:
- ✅ 接口签名完整
- ✅ 编译通过
- ✅ 强制 `HoldCreated: false`（符合契约 v0.1）
- 🔄 价格计算逻辑待实现

## 技术决策

### 1. 数据库策略：同库不同表

按照实施规划 §2.2，Phase 2 采用同库不同表策略：

```go
// 事务边界示例（未来实现）
type Orchestrator struct {
    inventory  *inventory.Service
    commercial *commercial.Service
    db         *sql.Tx
}

func (o *Orchestrator) CreateAndConfirmHold(ctx context.Context, req HoldRequest) error {
    tx, _ := o.db.BeginTx(ctx, nil)
    defer tx.Rollback()
    
    hold, _ := o.inventory.HoldCreate(ctx, tx, req)
    quote, _ := o.commercial.EstimateQuote(ctx, tx, hold.Items)
    _ = o.inventory.HoldConfirm(ctx, tx, hold.ID, quote)
    
    return tx.Commit()
}
```

**优势**:
- 简化事务管理（ACID 保证）
- 无需 Saga 协调
- 降低初期实现复杂度

**未来迁移路径**:
- 中期：Outbox/Inbox 模式准备
- 长期：按需拆库（金额表独立审计库）

### 2. 接口实现验证

两个服务都通过编译时接口验证：

```go
// inventory/service.go
var _ maascontracts.InventoryPort = (*Service)(nil)

// commercial/service.go
var _ maascontracts.QuotePort = (*Service)(nil)
```

编译通过确保接口签名完全匹配契约定义。

## 待实现工作

### 高优先级（P0）

#### I 泳道：Inventory 完整实现

**HoldCreate** (预估 2-3 工作日):
1. 检查库存可用性（防止超卖）
2. 插入 `maas_holds` 记录（status='pending'）
3. 插入 `maas_hold_lines` 明细
4. 生成围栏令牌（fencing token）
5. 幂等性检查：查询 `maas_idempotency_log`
6. 记录幂等日志

**HoldConfirm** (预估 1-2 工作日):
1. 验证围栏令牌匹配
2. 检查 hold 状态（必须是 pending）
3. 检查是否过期（expires_at < now()）
4. 更新状态为 committed
5. 减库存（实际扣减）
6. 幂等性处理

**QuerySellable** (预估 1工作日):
1. 查询 `maas_inventory_snapshots`
2. Join `maas_sellable_items`
3. 计算可售数量（总量 - 已预留）
4. 返回快照

**HoldRelease/HoldExpire** (预估 1工作日):
1. 状态机验证
2. 加回库存
3. 记录操作日志

#### Q 泳道：Commercial 完整实现

**EstimateQuote** (预估 2工作日):
1. 查询价格表（需先设计 `maas_price_versions` 表）
2. 计算总金额：`sum(quantity * unit_price)`
3. 应用折扣策略
4. 生成报价快照
5. 插入 `maas_quotes` 记录
6. 确保 `hold_created = false`

### 中优先级（P1）

- **T 泳道（Trading）**: 实现 EntitlementPort（依赖 I 完成）
- **L 泳道（Ledger）**: 实现 LedgerPort（依赖 T 完成）
- **集成测试**: testcontainers + PostgreSQL
- **单元测试**: 各领域独立测试套件

### 低优先级（P2）

- **U 泳道（UI）**: 微前端集成
- **R 泳道（Runtime）**: 宿主装配和路由
- **C0 泳道（Contracts）**: 契约破坏性变更门禁

## 时间估算

| 任务 | 预估时间 | 依赖 |
|------|---------|------|
| Inventory 完整实现 | 5-7 工作日 | 无 |
| Commercial 完整实现 | 2-3 工作日 | 无 |
| Trading 实现 | 3-4 工作日 | Inventory |
| Ledger 实现 | 4-5 工作日 | Trading |
| 集成测试框架 | 2 工作日 | 并行 |
| **总计（I+Q）** | **7-10 工作日** | - |
| **总计（全部核心）** | **16-21 工作日** | - |

## 技术风险

| 风险 | 影响 | 概率 | 缓解措施 |
|------|------|------|----------|
| 并发预留超卖 | 高 | 中 | PostgreSQL 行锁 + OCC 版本 |
| 幂等性实现复杂 | 中 | 低 | idempotency_log 表 + 唯一索引 |
| 事务死锁 | 中 | 中 | 锁定顺序约定 + 超时重试 |
| 价格计算精度 | 高 | 低 | decimal string + 单元测试 |

## 下一步行动

### 立即行动
- [ ] 提交 Phase 2 初始工作（领域骨架 + schema）
- [ ] 设计价格表 schema（`maas_price_versions`, `maas_price_rules`）
- [ ] 实现 Inventory.HoldCreate 的数据持久化逻辑

### 短期计划（1周内）
- [ ] 完成 Inventory 完整实现
- [ ] 完成 Commercial 完整实现
- [ ] 添加单元测试（至少覆盖 happy path）

### 中期计划（2-3周内）
- [ ] 实现 Trading 和 Ledger 领域
- [ ] 添加集成测试框架
- [ ] 端到端流程测试：QuerySellable → EstimateQuote → HoldCreate → HoldConfirm → ActivateEntitlement

## 成功标准（Phase 2 完成）

- [ ] I 泳道：Inventory 所有方法实现完整并通过测试
- [ ] Q 泳道：Commercial 估价逻辑实现并通过测试
- [ ] T 泳道：Trading 权益激活实现并通过测试
- [ ] L 泳道：Ledger 计量结算实现并通过测试
- [ ] 集成测试：核心流程端到端测试通过
- [ ] 无超卖验证：并发预留压力测试通过
- [ ] 账平验证：使用量 + 释放量 = 预留量

## 参考文档

- [Phase 1 交付总结](./PHASE1-DELIVERY-2026-11-19.md)
- [金额类型审计报告](./docs/MONEY-TYPE-AUDIT-2026-11-19.md)
- [OpenAPI Contract v0.1](../../docs/maas/contracts/openapi.yaml)
- [并行计划权威文档](../../docs/maas/13-parallel-workstreams-and-assembly.md)

---

**报告负责人**: Kiro AI  
**复核状态**: 待复核  
**下一里程碑**: 完成 I 和 Q 泳道实现（预计 7-10 工作日）
