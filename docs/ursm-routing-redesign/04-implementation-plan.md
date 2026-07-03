# URSM 实施计划

## 任务分解

本项目采用多代理并行开发模式，分为7个主要任务包。

---

## Task Package 1: 核心架构 (Core Architecture)

**负责人**: Agent-Core  
**预计工期**: 3天  
**优先级**: P0 (阻塞其他任务)

### 子任务

#### T1.1 创建包结构
```bash
domains/ursm/
├── manager.go
├── state.go
├── cache.go
├── errors.go
├── config.go
└── manager_test.go
```

#### T1.2 实现状态结构
- [x] ProviderState
- [x] CredentialState  
- [x] ModelState
- [x] NodeState
- [x] RouteNode
- [x] StateUpdate

#### T1.3 实现Manager骨架
```go
type Manager struct {
    db    *pgxpool.Pool
    redis *redis.Client
    
    providerCache   *LayerCache[ProviderState]
    credentialCache *LayerCache[CredentialState]
    modelCache      *LayerCache[ModelState]
    nodeCache       *LayerCache[NodeState]
    
    fpSlotMgr  *FingerprintSlotManager
    concSlotMgr *ConcurrencySlotManager
    costScorer *CostScorer
    batchWriter *BatchWriter
}
```

#### T1.4 实现LayerCache[T]泛型缓存
- [x] Get(ctx, key) 三层查询
- [x] Set(ctx, key, value) 写入
- [x] Invalidate(ctx, key) 失效
- [x] 单元测试

**验收标准**:
- [ ] 所有结构体定义完成
- [ ] Manager创建和销毁正常
- [ ] LayerCache泛型编译通过
- [ ] 缓存读写测试通过

---

## Task Package 2: 资源限额管理 (Resource Managers)

**负责人**: Agent-Resource  
**预计工期**: 4天  
**依赖**: Task 1  
**优先级**: P0

### 子任务

#### T2.1 指纹槽管理器
```bash
domains/ursm/
├── fp_slot_manager.go
├── fp_slot_manager_test.go
└── fp_slot_scripts.go (Redis Lua)
```

**功能**:
- [x] CheckAndAcquire() - 检查并获取指纹槽
- [x] Release() - 释放指纹槽（刷新TTL）
- [x] ForceUnpin() - 强制解绑pin
- [x] LRU抢占逻辑
- [x] Pin复用机制

**Lua脚本**:
- acquireSlotScript (单槽获取)
- acquireLRUScript (LRU抢占)
- releaseSlotScript (释放)
- forceUnpinScript (解绑)

#### T2.2 并发槽管理器
```bash
domains/ursm/
├── conc_slot_manager.go
├── conc_slot_manager_test.go
└── conc_slot_scripts.go (Redis Lua)
```

**功能**:
- [x] CheckAndAcquire() - 检查并获取并发槽
- [x] Release() - 释放并发槽
- [x] 全局计数 + 会话计数

**Lua脚本**:
- acquireConcurrencyScript
- releaseConcurrencyScript

#### T2.3 集成测试
- [ ] 指纹槽饱和场景
- [ ] 并发槽饱和场景
- [ ] LRU抢占正确性
- [ ] 同一客户端多并发共享指纹槽
- [ ] 资源泄露测试

**验收标准**:
- [ ] 指纹槽LRU抢占测试通过
- [ ] 并发槽计数准确（压力测试）
- [ ] 无资源泄露（运行1小时，资源归零）

---

## Task Package 3: 批量写入器 (Batch Writer)

**负责人**: Agent-Writer  
**预计工期**: 3天  
**依赖**: Task 1  
**优先级**: P0

### 子任务

#### T3.1 实现BatchWriter
```bash
domains/ursm/
├── batch_writer.go
├── batch_writer_test.go
└── sql_templates.go
```

**功能**:
- [x] ApplyUpdates() - 原子批量更新
- [x] 按层级排序（Provider → Credential → Model → Node）
- [x] DB事务包装
- [x] 异步缓存失效

#### T3.2 实现各层更新逻辑
- [x] applyProviderUpdate()
- [x] applyCredentialUpdate()
- [x] applyModelUpdate()
- [x] applyNodeUpdate()

#### T3.3 级联传播逻辑
- [ ] Provider禁用 → 级联Credential
- [ ] Credential禁用 → 释放资源
- [ ] 永久故障 → 标记Model broken

**验收标准**:
- [ ] 事务回滚测试通过
- [ ] 级联更新正确
- [ ] 缓存失效及时
- [ ] 并发写入无冲突

---

## Task Package 4: 状态更新API (State APIs)

**负责人**: Agent-API  
**预计工期**: 3天  
**依赖**: Task 1, 3  
**优先级**: P1

### 子任务

#### T4.1 实现RecordRequest API
```go
func (m *Manager) RecordRequest(ctx context.Context, req RecordRequestAPI) error
```

**逻辑**:
1. 验证参数
2. 分类错误
3. 构建StateUpdate列表
4. 调用BatchWriter.ApplyUpdates()
5. 触发探测（如需）

#### T4.2 实现UpdateProvider API
```go
func (m *Manager) UpdateProvider(ctx context.Context, req UpdateProviderAPI) error
```

**逻辑**:
1. 加锁
2. 验证权限
3. 构建StateUpdate列表（含级联）
4. 审计日志
5. 调用BatchWriter.ApplyUpdates()

#### T4.3 实现UpdateCredential API
```go
func (m *Manager) UpdateCredential(ctx context.Context, req UpdateCredentialAPI) error
```

#### T4.4 实现RecordProbeResult (内部)
```go
func (m *Manager) RecordProbeResult(ctx context.Context, result ProbeResult) error
```

#### T4.5 参数验证
使用 `go-playground/validator/v10`

**验收标准**:
- [ ] API参数验证完备
- [ ] 错误分类逻辑正确
- [ ] 审计日志记录完整
- [ ] 单元测试覆盖率>90%

---

## Task Package 5: 路由查询API (Routing APIs)

**负责人**: Agent-Routing  
**预计工期**: 4天  
**依赖**: Task 1, 2  
**优先级**: P1

### 子任务

#### T5.1 实现IsAvailable()
```go
func (m *Manager) IsAvailable(ctx context.Context, credentialID int, model string) (bool, string)
```

**逻辑**: 四层级联检查

#### T5.2 实现GetAvailableNodes()
```go
func (m *Manager) GetAvailableNodes(ctx context.Context, model string, sessionID string) ([]RouteNode, error)
```

**逻辑**:
1. 加载候选节点
2. 逐个检查可用性
3. 获取指纹槽
4. 获取并发槽
5. 成本排序
6. 应用策略

#### T5.3 实现CostScorer
```bash
domains/ursm/
├── cost_scorer.go
└── cost_scorer_test.go
```

**功能**:
- calculatePriceScore()
- calculateSpeedScore()
- calculateStabilityScore()
- CalculateCompositeScore()
- SortByCompositeScore()

#### T5.4 实现策略应用
- Tier分层
- Billing分轮
- Sticky优先

**验收标准**:
- [ ] 路由决策结果正确
- [ ] 成本排序符合预期
- [ ] 性能达标（P99<50ms）
- [ ] 资源分配无泄露

---

## Task Package 6: Router/Executor适配 (Integration)

**负责人**: Agent-Integration  
**预计工期**: 3天  
**依赖**: Task 4, 5  
**优先级**: P1

### 子任务

#### T6.1 修改Router
```go
// domains/streaming/executors/router.go

func (r *Router) PlanCandidates(...) []provider.Candidate {
    nodes, err := r.URSM.GetAvailableNodes(ctx, model, sessionID)
    // ...
}
```

#### T6.2 修改Executor
```go
// domains/streaming/executors/executor.go

func (e *Executor) Execute(...) {
    defer e.URSM.ReleaseResources(...)
    // ...
    e.URSM.RecordRequest(...)
}
```

#### T6.3 移除冗余代码
将以下代码移动到 `_to-be-deprecated/routing-old/`:
- domains/credentialstate/manager.go
- credentialfpslot/node_state.go (部分功能)
- domains/streaming/executors/router.go 中的重复逻辑

#### T6.4 探测器适配
- CredentialProbeV2.SetStateManager()
- ModelProbeRunner.SetStateManager()
- 双向注入逻辑

**验收标准**:
- [ ] 路由决策结果与原逻辑一致
- [ ] 性能无退化
- [ ] 旧代码全部迁移
- [ ] 无编译错误

---

## Task Package 7: 测试与文档 (Testing & Documentation)

**负责人**: Agent-QA  
**预计工期**: 4天  
**依赖**: Task 6  
**优先级**: P1

### 子任务

#### T7.1 单元测试补全
- 所有Manager方法
- 所有API方法
- 错误分支覆盖
- 目标覆盖率: 90%

#### T7.2 集成测试
```bash
domains/ursm/integration_test.go
```

**场景**:
- 完整路由流程
- 状态级联更新
- 资源抢占
- 探测触发

#### T7.3 压力测试
```bash
domains/ursm/benchmark_test.go
```

**目标**:
- 10k QPS
- P99延迟 < 50ms
- 无内存泄露

#### T7.4 故障注入测试
- Redis宕机
- DB慢查询
- 供应商全禁用
- 指纹槽全饱和

#### T7.5 文档完善
- [ ] API使用示例
- [ ] 监控配置指南
- [ ] 故障排查手册
- [ ] 运维手册

**验收标准**:
- [ ] 单元测试覆盖率≥90%
- [ ] 集成测试全通过
- [ ] 压力测试达标
- [ ] 故障注入全fail-open

---

## 并行执行计划

### Week 1: 基础架构
```
Day 1-3: Task 1 (Core Architecture)
Day 2-5: Task 2 (Resource Managers) - 并行从Day 2开始
Day 2-4: Task 3 (Batch Writer) - 并行从Day 2开始
```

### Week 2: API层
```
Day 6-8:  Task 4 (State APIs)
Day 6-9:  Task 5 (Routing APIs) - 并行
```

### Week 3: 集成与测试
```
Day 10-12: Task 6 (Integration)
Day 13-16: Task 7 (Testing & Documentation)
```

---

## 代理分工

### Agent-Core
- **主要职责**: 核心架构、状态结构、缓存层
- **技能要求**: 泛型编程、并发控制
- **输出**: domains/ursm/manager.go, cache.go, state.go

### Agent-Resource
- **主要职责**: 指纹槽、并发槽管理器
- **技能要求**: Redis Lua、并发控制、资源管理
- **输出**: fp_slot_manager.go, conc_slot_manager.go

### Agent-Writer
- **主要职责**: 批量写入器、级联传播
- **技能要求**: 事务管理、SQL优化
- **输出**: batch_writer.go, sql_templates.go

### Agent-API
- **主要职责**: 状态更新API、参数验证
- **技能要求**: API设计、错误处理
- **输出**: api_record.go, api_update.go

### Agent-Routing
- **主要职责**: 路由查询API、成本评分
- **技能要求**: 算法设计、性能优化
- **输出**: routing.go, cost_scorer.go

### Agent-Integration
- **主要职责**: Router/Executor适配、代码迁移
- **技能要求**: 重构、向后兼容
- **输出**: 修改后的router.go, executor.go

### Agent-QA
- **主要职责**: 测试、文档
- **技能要求**: 测试设计、技术写作
- **输出**: *_test.go, 文档

---

## 风险与应对

### 风险1: 缓存不一致
**概率**: 中  
**影响**: 高  
**应对**: 
- 缩短TTL
- 增加缓存失效触发点
- 监控不一致率

### 风险2: 性能退化
**概率**: 低  
**影响**: 高  
**应对**:
- 基准测试对比
- 缓存预热
- 连接池调优

### 风险3: 资源泄露
**概率**: 中  
**影响**: 高  
**应对**:
- 严格的defer释放
- 定期资源巡检
- 监控资源占用

### 风险4: 向后兼容问题
**概率**: 低  
**影响**: 中  
**应对**:
- 灰度上线
- A/B测试
- 快速回滚机制

---

## Milestone检查点

### M1: 核心架构完成 (Day 5)
- [ ] LayerCache泛型编译通过
- [ ] 资源管理器测试通过
- [ ] BatchWriter测试通过

### M2: API层完成 (Day 9)
- [ ] 所有API测试通过
- [ ] 参数验证完备
- [ ] 审计日志记录

### M3: 集成完成 (Day 12)
- [ ] Router适配完成
- [ ] Executor适配完成
- [ ] 旧代码迁移完成

### M4: 测试通过 (Day 16)
- [ ] 单元测试≥90%
- [ ] 集成测试通过
- [ ] 压力测试达标
- [ ] 文档完善

---

## 上线计划

### Phase 1: 金丝雀 (5% 流量)
- 部署到1台服务器
- 监控24小时
- 关键指标对比

### Phase 2: 小规模 (25% 流量)
- 扩展到3台服务器
- 监控48小时
- 性能对比

### Phase 3: 全量上线
- 全部服务器
- 监控72小时
- 下线旧代码

---

## 监控仪表板

### 核心指标
- ursm_routing_decision_duration_seconds (P50/P95/P99)
- ursm_cache_hit_ratio
- ursm_fp_slot_usage_ratio
- ursm_conc_slot_usage_ratio
- ursm_state_update_errors_total

### 告警规则
- P99延迟 > 100ms (持续5分钟)
- 缓存命中率 < 50% (持续5分钟)
- 状态更新错误 > 10/min
- 资源饱和拒绝 > 1000/min

---

## 回滚计划

### 回滚触发条件
- P99延迟增加50%以上
- 错误率增加100%以上
- 资源泄露导致OOM
- 严重功能缺陷

### 回滚步骤
1. 停止新流量路由到URSM
2. 恢复旧代码路径
3. 重启服务
4. 验证回滚成功
5. 根因分析

### 回滚时间目标
- RTO (恢复时间目标): 15分钟
- RPO (恢复点目标): 0 (无数据丢失)

---

## 成功标准

### 功能完整性
- [ ] 所有API功能正常
- [ ] 路由决策结果正确
- [ ] 资源管理无泄露
- [ ] 状态更新及时

### 性能达标
- [ ] 路由决策P99 < 50ms
- [ ] 状态查询QPS > 10k
- [ ] 缓存命中率 > 80%

### 质量达标
- [ ] 单元测试覆盖率 ≥ 90%
- [ ] 集成测试通过率 100%
- [ ] 压力测试无崩溃
- [ ] 故障注入全fail-open

### 可维护性
- [ ] 代码重复减少70%
- [ ] 文档完善
- [ ] 监控覆盖全面
