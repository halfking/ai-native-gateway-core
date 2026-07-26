# URSM (Unified Routing State Management)

## 概述

URSM是llm-gateway-go的统一路由状态管理系统，负责管理Provider、Credential、Model、Node四层状态，并提供资源限额管理和成本评分功能。

## 当前状态

### Task Package 1: 核心架构 ✅ (已完成)

**完成时间**: 2026-07-03

**实现内容**:
1. ✅ 状态结构定义 (`state.go`)
   - ProviderState / CredentialState / ModelState / NodeState
   - RouteNode (路由节点)
   - StateUpdate (状态更新)
   - Layer枚举

2. ✅ Manager主管理器骨架 (`manager.go`)
   - 四层缓存初始化
   - Start/Stop生命周期方法
   - Enabled()检查方法
   - 预留后续任务的方法桩

3. ✅ LayerCache[T]泛型三层缓存 (`cache.go`)
   - Get(ctx, key) - 三层查询（mem → redis → db）
   - Set(ctx, key, value) - 写入缓存
   - Invalidate(ctx, key) - 失效缓存
   - Clear() - 清空内存缓存

4. ✅ 配置结构 (`config.go`)
   - Config / FpSlotConfig / ScoringWeights
   - DefaultConfig() / LoadFromEnv()
   - 支持环境变量覆盖

5. ✅ 错误定义 (`errors.go`)
   - 业务错误常量
   - 路由决策/状态/系统错误

6. ✅ 缓存Key生成 (`keys.go`)
   - 状态层Key (provider/credential/model/node)
   - 资源管理Key (fp_slot/fp_pin/conc_slot/conc_session)

**测试覆盖率**: 43.0%

**单元测试**:
- ✅ cache_test.go (5个测试，全部通过)
  - TestLayerCache_MemoryCache
  - TestLayerCache_MemoryCacheTTL
  - TestLayerCache_Invalidate
  - TestLayerCache_Clear
  - TestLayerCache_ConcurrentAccess

- ✅ state_test.go (6个测试套件，全部通过)
  - TestProviderState_IsAvailable (3个子测试)
  - TestCredentialState_IsAvailable (5个子测试)
  - TestModelState_IsAvailable (4个子测试)
  - TestNodeState_IsAvailable (5个子测试)
  - TestLayer_String (5个子测试)

**编译状态**: ✅ 编译通过
```bash
go build ./domains/ursm  # 成功
go test ./domains/ursm -v  # 全部通过
```

## 后续任务

### Task Package 2: 资源限额管理 (待实现)
- FingerprintSlotManager (指纹槽管理)
- ConcurrencySlotManager (并发槽管理)

### Task Package 3: 批量写入器 (待实现)
- BatchWriter (原子批量更新)
- 级联传播逻辑

### Task Package 4: 状态更新API (待实现)
- RecordRequest API
- UpdateProvider API
- UpdateCredential API

### Task Package 5: 路由查询API (待实现)
- GetAvailableNodes()
- IsAvailable()
- CostScorer

### Task Package 6: Router/Executor适配 (待实现)
- 集成到现有路由系统

### Task Package 7: 测试与文档 (待实现)
- 集成测试
- 压力测试
- 文档完善

## 设计原则

1. **Fail-Open**: 所有查询失败时返回可用，不阻塞请求
2. **三层缓存**: Memory (10s) → Redis (5min) → DB (权威源)
3. **泛型设计**: LayerCache[T]支持任意状态类型
4. **可配置**: 所有TTL和阈值可通过环境变量调整
5. **类型安全**: 强类型状态结构，避免运行时错误

## 使用示例

```go
import "github.com/kaixuan/llm-gateway-go/domains/ursm"

// 创建管理器
cfg := ursm.LoadFromEnv()
mgr := ursm.NewManager(db, redisClient, cfg)

// 启动
ctx := context.Background()
mgr.Start(ctx)
defer mgr.Stop()

// 检查状态
available, reason := mgr.IsAvailable(ctx, credentialID, model)
if !available {
    log.Printf("Node unavailable: %s", reason)
}
```

## 贡献者

- Agent-Core (Task Package 1 实现)
