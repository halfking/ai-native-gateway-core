# URSM v2 迁移状态报告

**日期**: 2026-08-06  
**项目**: llm-gateway-go-2

## 执行摘要

URSM v2 已经**完全实现并集成**到项目中。系统采用**渐进式迁移策略**，支持三种运行模式，确保平滑过渡和零停机部署。

## 迁移状态: ✅ 完成

### 1. URSM v2 核心实现 ✅

**位置**: `domains/ursm/v2/`

已实现的核心组件:
- ✅ `manager.go` - URSM v2 门面和主接口
- ✅ `cache/nodemirror.go` - 16分片LRU镜像(M2优化)
- ✅ `cache/lru.go` - 线程安全的泛型LRU实现
- ✅ `store/store.go` - Redis Lua脚本原子操作
- ✅ `statesource/statesource.go` - 路由状态源追踪
- ✅ `recovery/manager.go` - 恢复门控机制
- ✅ `rollout/controller.go` - 金丝雀/影子部署控制器

### 2. 三种运行模式 ✅

URSM v2 支持以下三种模式，通过配置动态切换:

#### Mode 1: Off (默认)
```go
Mode: api.ModeOff
```
- URSM v2 完全关闭
- 使用 legacy `credentialstate.Manager`
- 零性能开销
- 适用于: 未启用URSM v2的环境

#### Mode 2: Shadow (影子模式)
```go
Mode: api.ModeShadow
ShadowDoubleWrite: true  // 可选
```
- URSM v2 与 legacy 并行运行
- 路由决策仍使用 legacy state manager
- URSM v2 **只写不读**(记录遥测数据)
- 适用于: 数据对比验证期

#### Mode 3: Authoritative (权威模式) 
```go
Mode: api.ModeAuthoritative
Ready: true  // 通过 recovery gate 控制
```
- URSM v2 完全接管路由状态判断
- **Legacy state manager 不参与健康判定**
- 提供最优性能(LRU mirror + 分片锁)
- 适用于: 生产环境全量切换

### 3. 代码集成点 ✅

#### 3.1 主程序初始化 (`cmd/gateway/main.go`)

```go
var ursmV2Mgr *ursmv2.Manager
if enableURSMv2 {
    ursmV2Mgr = ursmv2.New(ursmv2.Dependencies{
        Redis:  fpSlotRedis,
        FP:     fpSlots,
        Conc:   limiter,
        RPM:    rpm,
        Config: ursmv2Config,
        Logger: logger,
    })
}
```

✅ URSM v2 Manager 已正确初始化并传递给 executor

#### 3.2 Executor 集成 (`domains/streaming/executors/executor.go`)

```go
type Executor struct {
    // ...
    URSMv2 *ursmv2.Manager  // 2026-07-21 URSM v2 plan T8
    // ...
}
```

✅ Executor 持有 URSM v2 引用，支持:
- `RecordRequest()` - 请求成功/失败时写入状态
- `FilterAndScore()` - 候选节点过滤和评分
- `PlanCandidates()` - 路由决策

#### 3.3 状态后端选择 (`domains/streaming/executors/state_backend.go`)

```go
func selectStateBackend(ursmv2Mgr URSMv2Manager, stateMgr credentialstate.StateProvider, ctx context.Context) StateBackend {
    // 1. URSM v2 authoritative + Ready → URSMv2Backend
    if ursmv2Mgr != nil && ursmv2Mgr.Mode() == ursmv2api.ModeAuthoritative {
        if ready := ursmv2Mgr.Ready(ctx); ready {
            return &URSMv2Backend{mgr: ursmv2Mgr}
        }
    }
    // 2. StateManager 启用 → LegacyStateBackend  
    if stateMgr != nil && stateMgr.Enabled() {
        return &LegacyStateBackend{sm: stateMgr}
    }
    // 3. 兜底 → DBOnlyBackend
    return &DBOnlyBackend{}
}
```

✅ 状态后端选择逻辑清晰，优先级正确

### 4. 并发安全设计 ✅

#### 4.1 NodeMirror 分片锁 (M3优化)
```go
const NodeMirrorShards = 16

type NodeMirror struct {
    shards  [NodeMirrorShards]*LRU[string, NodeView]
    softTTL time.Duration
}
```
- ✅ 16个独立LRU分片，避免全局锁竞争
- ✅ FNV-1a哈希分片，确保同key总是到同一分片
- ✅ 每个分片独立的 `sync.Mutex`

#### 4.2 Generation 单调性保证
```go
func (m *NodeMirror) applyToLRU(v NodeView) {
    l.Update(key, func(old NodeView, exists bool) (NodeView, bool) {
        if !exists {
            return v, true // 首次写入
        }
        if v.Generation < old.Generation {
            return v, false // 拒绝旧世代
        }
        if v.Generation == old.Generation && v.SourcePriority < old.SourcePriority {
            return v, false // 同世代但优先级不更高
        }
        return v, true // 接受更新
    })
}
```
- ✅ 原子的 read-modify-write
- ✅ 避免 TOCTOU 竞态
- ✅ 与 Redis Lua 脚本逻辑对齐

#### 4.3 Recovery Gate
```go
func (m *Manager) Ready(ctx context.Context) bool {
    if m == nil {
        return false
    }
    return m.recovery.Ready(ctx)
}
```
- ✅ Redis 健康检查自动关闭 v2 权威模式
- ✅ 恢复后自动重新开启
- ✅ 防止 Redis 不可用时的路由错误

### 5. Legacy State Manager 保留原因 ✅

**Legacy `credentialstate.Manager` 仍然保留，这是设计决策:**

1. **渐进式迁移**: 运维团队可以通过配置开关在运行时切换模式
2. **灰度发布**: Shadow/Canary 模式需要 legacy 作为对照组
3. **故障回退**: URSM v2 异常时自动降级到 legacy
4. **零停机**: 不需要重启服务即可切换路由策略

**代码注释确认:**
```go
// 2026-07-26: URSM v1→v2 统一。文档同步更新：v1 (domains/ursm) 已迁入
// _to-be-deprecated/ursm/，本接口不再有 v1 分支。
```

- ✅ URSM v1 已弃用
- ✅ Legacy `credentialstate` 作为 fallback 保留
- ✅ 无 v1/v2 混用风险

### 6. 测试覆盖 ✅

URSM v2 相关测试文件:
```
domains/ursm/v2/cache/lru_test.go
domains/ursm/v2/cache/nodemirror_test.go
domains/ursm/v2/cache/migrate_fpslots_test.go
domains/ursm/v2/statesource/statesource_test.go
cmd/gateway/main_credentialstate_assembly_test.go
```

- ✅ LRU 并发测试
- ✅ NodeMirror 分片测试
- ✅ 状态源计数器测试
- ✅ 模式切换集成测试

### 7. 性能优化 ✅

#### M2 优化 (LRU Mirror)
- **目标**: 减少 Redis 读取延迟
- **实现**: 进程内 LRU 缓存，soft-TTL 30秒
- **效果**: 热路径零 Redis IO

#### M3 优化 (分片锁)
- **目标**: 减少锁竞争
- **实现**: 16 个独立 LRU 分片
- **效果**: 并发 FilterAndScore 可并行执行

#### Fail-Open 设计
- **Ready=false**: 自动降级到 legacy
- **Redis 不可用**: LRU mirror 继续服务
- **节点缺失**: Available=false，过滤掉

### 8. 监控和可观测性 ✅

#### 8.1 路由状态源追踪 (S-3)
```go
const (
    StateSourceNodeMirrorHit   = "node_mirror_hit"
    StateSourceNodeMirrorMiss  = "node_mirror_miss"
    StateSourceNodeMirrorStale = "node_mirror_stale"
    StateSourceFallback        = "fallback"
    StateSourceAuthoritative   = "authoritative"
    StateSourceCanary          = "canary"
    StateSourceOff             = "off"
)
```
- ✅ Prometheus 指标暴露
- ✅ 日志结构化记录
- ✅ 失败率可计算

#### 8.2 恢复门控状态
```go
func (m *Manager) RecoveryStats() recovery.Stats {
    return m.recovery.Stats()
}
```
- ✅ 最后恢复时间
- ✅ 最后错误信息
- ✅ 观测到的 key 数量

### 9. 配置示例 ✅

#### 生产环境配置 (Authoritative)
```yaml
ursm_v2:
  enabled: true
  mode: authoritative
  redis_key_prefix: "ursm:v2:"
  lru_mirror_size: 100000
  lru_mirror_soft_ttl: 30s
  node_ttl: 1800
  window_5m_ttl: 600
  window_30m_ttl: 1800
  cool_seconds: 300
```

#### 灰度发布配置 (Canary)
```yaml
ursm_v2:
  enabled: true
  mode: canary
  canary_percent: 10
  canary_tenants: ["tenant-alpha", "tenant-beta"]
  shadow_double_write: true
```

#### 数据验证配置 (Shadow)
```yaml
ursm_v2:
  enabled: true
  mode: shadow
  shadow_double_write: true
```

## 结论

✅ **URSM v2 迁移状态: 完成**

- URSM v2 核心功能已全部实现
- 与 executor 集成完整
- 三种运行模式支持完善
- 并发安全设计优秀
- Legacy fallback 机制完备
- 监控和可观测性到位

**推荐行动:**

1. ✅ 代码已就绪，可以部署
2. 📊 建议先在测试环境以 Shadow 模式运行1-2周，验证数据一致性
3. 🚀 验证通过后，以 Canary 10% 灰度1周
4. 🎯 无异常后切换到 Authoritative 全量

**当前模式**: 根据配置动态决定(支持运行时切换)

---

**审核人**: Kiro AI Assistant  
**审核日期**: 2026-08-06  
**下次审查**: URSM v2 全量上线后 1 个月
