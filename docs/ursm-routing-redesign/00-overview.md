# URSM 统一路由状态管理系统 - 项目概览

## 项目背景

当前系统存在以下问题：
1. **状态不一致**: Router、ProbeV2、StateManager各自更新状态，无统一协调
2. **循环依赖风险**: ProbeV2 → StateManager → ProbeV2的隐式循环
3. **缺少供应商级状态传播**: 供应商禁用时未级联更新凭据状态
4. **FpSlot节点状态与StateManager重复**: 两个并行的状态跟踪系统
5. **探测结果写回路径过长**: 4层更新，部分写入成功部分失败
6. **并发控制维度混淆**: 指纹槽和并发槽混用

## 项目目标

构建统一路由状态管理系统(Unified Routing State Manager, URSM)，实现：
1. **单一数据源(SSOT)**: 每种状态只有一个权威来源
2. **原子更新**: 状态变更要么全成功要么全失败
3. **层级传播**: 上层状态变化自动级联到下层
4. **双维度限流**: 指纹槽(客户端数)和并发槽(请求数)分离管理
5. **成本优化排序**: 价格+速度+稳定性加权

## 核心概念

### 状态层级（由大到小）
```
供应商(Provider)
  ├─ enabled, manual_disabled
  └─ 影响所有凭据
  
凭据(Credential)
  ├─ status, lifecycle_status, availability_state
  ├─ health_status, quota_state, manual_disabled
  └─ 影响所有模型
  
模型(Model)
  ├─ offer_available, binding_available
  ├─ probe_state
  └─ 影响单个(凭据+模型)组合
  
节点(Node)
  ├─ consecutive_failures, disabled
  ├─ success_count, failure_count
  └─ 实时请求统计
```

### 资源限额（双维度）
```
指纹槽(Fingerprint Slots)
  ├─ 限制: 同时连接的不同客户端数
  ├─ 目的: 防止指纹识破
  └─ 特征: 同一客户端可持有多个并发请求

并发槽(Concurrency Slots)
  ├─ 限制: 同时活跃的请求数
  ├─ 目的: 防止凭据过载
  └─ 特征: 每个活跃请求占用1个并发槽
```

### 节点定义
```
节点 = 凭据 + 模型 + 状态 + 历史

状态 = 可用性状态(4层级联) + 健康指标
历史 = 价格 + 速度 + 稳定性 → 综合成本
资源 = 指纹槽池 + 并发槽池
```

## 架构设计

### 核心组件
```
URSM Manager
  ├─ 四层状态缓存 (Provider/Credential/Model/Node)
  ├─ 资源管理器 (FpSlot/Concurrency)
  ├─ 成本评分器 (Cost Scorer)
  ├─ 批量写入器 (Batch Writer)
  └─ 探测器注入点 (Probe Submitter)
```

### 数据流
```
请求 → 路由决策 → 资源获取 → 执行 → 状态回写
        ↓
    [URSM查询]    [URSM更新]
        ↓              ↓
    三层缓存 ←─── 批量写入器
    (mem/redis/db)      ↓
                    级联传播
```

## 实施计划

### Phase 1: 核心架构（第1周）
- 创建 domains/ursm 包
- 实现四层状态结构
- 实现三层缓存
- 实现批量写入器

### Phase 2: 资源限额（第2周）
- 实现指纹槽管理器
- 实现并发槽管理器
- LRU抢占逻辑
- 资源释放机制

### Phase 3: 状态API（第3周）
- RecordRequest API
- UpdateProvider API
- UpdateCredential API
- RecordProbeResult (内部)

### Phase 4: Router适配（第4周）
- 修改Router使用URSM
- 移除冗余代码
- 适配Executor

### Phase 5: 探测器集成（第5周）
- CredentialProbeV2适配
- ModelProbeRunner适配
- 双向注入

### Phase 6: 测试验证（第6周）
- 单元测试(90%覆盖率)
- 集成测试
- 压力测试
- 故障注入测试

### Phase 7: 上线（第7周）
- 文档完善
- 监控配置
- 灰度上线

## 预期效果

### 性能
- 路由决策延迟: 50ms → 20ms
- 状态查询QPS: 5k → 20k
- 指纹槽利用率: 60% → 85%

### 质量
- 代码重复: -70%
- 状态不一致: 5次/天 → 0
- 故障排查: 2h → 30min

### 可观测性
- 状态变更可追踪: 100%
- 资源占用可视化: 实时
- 决策过程可回放: 支持

## 文档索引

- [00-overview.md](./00-overview.md) - 项目概览（本文档）
- [01-api-spec.md](./01-api-spec.md) - API规范
- [02-tech-design.md](./02-tech-design.md) - 技术设计
- [03-state-machine.md](./03-state-machine.md) - 状态机设计
- [04-implementation-plan.md](./04-implementation-plan.md) - 实施计划
- [05-migration-guide.md](./05-migration-guide.md) - 迁移指南
- [06-monitoring.md](./06-monitoring.md) - 监控方案

## 参与人员

- 需求提出: @用户
- 架构设计: @OpenCode
- 开发执行: 多代理并行

## 项目时间线

- 启动时间: 2026-07-03
- 预计完成: 2026-08-21 (7周)
- 当前状态: Phase 0 - 文档编写中
