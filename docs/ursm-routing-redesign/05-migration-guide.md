# URSM 迁移指南

## 概述

本指南详细说明如何从现有的状态管理系统迁移到URSM（统一路由状态管理系统）。

---

## 迁移策略

### 总体原则
1. **渐进式迁移**: 逐步替换，不做大爆炸式变更
2. **保持向后兼容**: 旧接口保留过渡期
3. **双写验证**: 新旧系统并行写入，验证一致性
4. **灰度上线**: 按流量百分比逐步切换

### 迁移阶段

```
Phase 0: 准备阶段 (1周)
  └─ 创建URSM代码，通过单元测试

Phase 1: 双写阶段 (1周)
  ├─ URSM接收状态更新（只写不读）
  └─ 验证数据一致性

Phase 2: 灰度读取 (1周)
  ├─ 5% 流量使用URSM查询
  ├─ 25% 流量使用URSM查询
  └─ 监控对比

Phase 3: 全量切换 (1周)
  ├─ 100% 流量使用URSM
  └─ 停止旧系统写入

Phase 4: 清理阶段 (1周)
  ├─ 移除旧代码
  └─ 文档更新
```

---

## 代码迁移清单

### 1. 待迁移的核心模块

#### 1.1 domains/credentialstate/manager.go
**迁移到**: domains/ursm/manager.go

**变更说明**:
- 原有的三层缓存逻辑保留
- 新增四层状态结构（Provider/Credential/Model/Node）
- 新增资源管理器集成

**迁移步骤**:
```bash
# 1. 复制有用的代码
cp domains/credentialstate/manager.go domains/ursm/manager_legacy.go.bak

# 2. 提取可复用函数
# - cacheKey生成
# - getFromMem/getFromRedis/getFromDB
# - setToMem/setToRedis

# 3. 移动旧代码到待删除目录
mkdir -p _to-be-deprecated/routing-old/credentialstate
mv domains/credentialstate/manager.go _to-be-deprecated/routing-old/credentialstate/
```

#### 1.2 credentialfpslot/node_state.go
**迁移到**: domains/ursm/node_state.go (部分功能合并到NodeState)

**变更说明**:
- NodeState结构保留，字段略有调整
- RecordNodeSuccess/RecordNodeFailure 逻辑移入URSM.RecordRequest
- GetNodeState/SetNodeState 移入URSM

**迁移步骤**:
```bash
# 1. 备份
cp credentialfpslot/node_state.go credentialfpslot/node_state.go.bak

# 2. 提取Redis Lua脚本到URSM
# recordNodeOutcomeScript → 集成到batch_writer.go

# 3. 保留指纹槽管理功能，移除节点状态管理
# credentialfpslot只负责指纹槽，不负责节点健康状态

# 4. 移动旧逻辑
mv credentialfpslot/node_state.go _to-be-deprecated/routing-old/credentialfpslot/
```

#### 1.3 domains/streaming/executors/router.go
**迁移到**: 同文件修改

**变更说明**:
- 移除filterAvailableWithStateManager()
- 统一使用URSM.GetAvailableNodes()
- 移除重复的FpSlots.Stats调用

**迁移步骤**:
```bash
# 1. 备份
cp domains/streaming/executors/router.go domains/streaming/executors/router.go.bak

# 2. 修改PlanCandidates方法
# 替换状态检查逻辑为URSM调用

# 3. 删除冗余方法
# - filterAvailableWithStateManager (line 198-220)
# - 重复的loadScore逻辑
```

#### 1.4 bg/credential_probe_v2.go
**迁移到**: 同文件修改

**变更说明**:
- writeHealth方法调用URSM.RecordProbeResult()
- 移除直接的cache.Set()调用

**迁移步骤**:
```bash
# 1. 备份
cp bg/credential_probe_v2.go bg/credential_probe_v2.go.bak

# 2. 在writeHealth末尾添加URSM同步
if c.stateManager != nil {
    c.stateManager.RecordProbeResult(ctx, ...)
}
```

---

## 数据库迁移

### 无需Schema变更

URSM复用现有表结构，无需新建表或修改字段。

### 数据迁移

无需数据迁移，URSM直接读取现有数据。

---

## API迁移

### 新增API

#### 1. POST /api/v1/routing/record-request
替代内部的状态回写逻辑，统一入口。

**旧代码**:
```go
// 分散在多处
credentialstate.Manager.UpdateOnSuccess()
credentialfpslot.Manager.RecordNodeSuccess()
```

**新代码**:
```go
ursm.RecordRequest(ctx, RecordRequestAPI{...})
```

#### 2. POST /api/v1/routing/update-provider
新增供应商状态修改API。

**旧代码**:
```go
// 直接修改DB
db.Exec("UPDATE providers SET enabled=$1 WHERE id=$2", ...)
```

**新代码**:
```go
ursm.UpdateProvider(ctx, UpdateProviderAPI{...})
```

#### 3. POST /api/v1/routing/update-credential
替代现有的手动状态修改。

**旧代码**:
```go
// admin/credential_monitor.go handlePromote/handleDemote
db.Exec("UPDATE credentials SET availability_state=...")
```

**新代码**:
```go
ursm.UpdateCredential(ctx, UpdateCredentialAPI{...})
```

### 废弃API

无废弃API，只是内部实现变更。

---

## 配置迁移

### 新增配置项

```yaml
# config.yaml
ursm:
  cache:
    mem_ttl: 10s
    redis_ttl: 5m
  
  fp_slot:
    default_limit: 20
    active_gate_seconds: 300
    slot_ttl_seconds: 1800
    pin_ttl_seconds: 86400
  
  scoring:
    price_weight: 0.3
    speed_weight: 0.4
    stability_weight: 0.3
```

### 环境变量

```bash
# 可选: 禁用URSM（用于回滚）
URSM_ENABLED=false

# 可选: 双写模式（Phase 1）
URSM_DUAL_WRITE=true
URSM_READ_OLD=true
```

---

## 代码适配示例

### 示例1: Executor请求完成

**旧代码**:
```go
// domains/streaming/executors/executor.go

func (e *Executor) Execute(ctx context.Context, req Request) Result {
    result := e.doUpstreamCall(req)
    
    // 分散的状态更新
    if result.Success {
        e.stateManager.UpdateOnSuccess(ctx, req.CredentialID, req.Model, result.LatencyMs, req.ID)
    } else {
        e.stateManager.UpdateOnFailure(ctx, req.CredentialID, req.Model, result.ErrorKind, req.ID)
    }
    
    // FpSlot节点状态
    if result.Success {
        e.fpSlots.RecordNodeSuccess(ctx, req.CredentialID, req.Model, req.ID)
    } else {
        e.fpSlots.RecordNodeFailure(ctx, req.CredentialID, req.Model, req.ID, string(result.ErrorKind))
    }
    
    return result
}
```

**新代码**:
```go
func (e *Executor) Execute(ctx context.Context, req Request) Result {
    result := e.doUpstreamCall(req)
    
    // 统一状态更新
    err := e.ursm.RecordRequest(ctx, ursm.RecordRequestAPI{
        RequestID:    req.ID,
        CredentialID: req.CredentialID,
        RawModel:     req.Model,
        SessionID:    req.SessionID,
        Success:      result.Success,
        LatencyMs:    result.LatencyMs,
        ErrorKind:    string(result.ErrorKind),
        Timestamp:    time.Now(),
    })
    
    if err != nil {
        slog.Warn("failed to record request", "error", err)
    }
    
    return result
}
```

### 示例2: Router候选节点筛选

**旧代码**:
```go
// domains/streaming/executors/router.go

func (r *Router) PlanCandidates(...) []provider.Candidate {
    // 从DB加载所有候选
    allCandidates := r.loadCandidatesDB(ctx, model)
    
    // 多处状态检查
    var available []provider.Candidate
    if r.StateManager != nil && r.StateManager.Enabled() {
        available = r.filterAvailableWithStateManager(ctx, allCandidates)
    } else {
        available = filterAvailable(allCandidates)
    }
    
    // 检查FpSlot节点状态
    available = r.filterHealthyNodes(available)
    
    // P2C排序
    ordered := p2cOrder(available, r)
    
    return ordered
}
```

**新代码**:
```go
func (r *Router) PlanCandidates(...) []provider.Candidate {
    // URSM一站式查询
    nodes, err := r.ursm.GetAvailableNodes(ctx, model, sessionID)
    if err != nil {
        slog.Warn("ursm get nodes failed", "error", err)
        return nil
    }
    
    // 转换为旧的Candidate结构（兼容层）
    candidates := make([]provider.Candidate, len(nodes))
    for i, node := range nodes {
        candidates[i] = nodesToCandidate(node)
    }
    
    return candidates
}

func nodesToCandidate(node ursm.RouteNode) provider.Candidate {
    return provider.Candidate{
        CredentialID:  node.CredentialID,
        RawModel:      node.RawModel,
        ProviderID:    node.ProviderID,
        Available:     node.Available,
        SuccessRate:   node.SuccessRate,
        P95LatencyMs:  node.P95LatencyMs,
        CompositeScore: node.CompositeScore,
        // ... 其他字段映射
    }
}
```

### 示例3: 管理员禁用凭据

**旧代码**:
```go
// admin/credential_monitor.go

func (h *Handler) handleSetManualDisabled(w http.ResponseWriter, r *http.Request) {
    var req struct {
        CredentialID   int  `json:"credential_id"`
        ManualDisabled bool `json:"manual_disabled"`
    }
    json.NewDecoder(r.Body).Decode(&req)
    
    // 直接更新DB
    _, err := h.db.Exec(ctx, 
        "UPDATE credentials SET manual_disabled=$1 WHERE id=$2",
        req.ManualDisabled, req.CredentialID)
    
    // 手动失效缓存
    if h.stateManager != nil {
        h.stateManager.Invalidate(ctx, req.CredentialID)
    }
    
    writeJSON(w, map[string]any{"success": true})
}
```

**新代码**:
```go
func (h *Handler) handleSetManualDisabled(w http.ResponseWriter, r *http.Request) {
    var req struct {
        CredentialID   int    `json:"credential_id"`
        ManualDisabled bool   `json:"manual_disabled"`
        Reason         string `json:"reason"`
    }
    json.NewDecoder(r.Body).Decode(&req)
    
    // 调用URSM API（自动处理级联、缓存失效、审计）
    err := h.ursm.UpdateCredential(ctx, ursm.UpdateCredentialAPI{
        CredentialID:   req.CredentialID,
        ManualDisabled: &req.ManualDisabled,
        Reason:         req.Reason,
        Actor:          getActorFromRequest(r),
    })
    
    if err != nil {
        writeError(w, err)
        return
    }
    
    writeJSON(w, map[string]any{"success": true})
}
```

---

## 测试策略

### Phase 1: 双写验证

```go
// 同时写入旧系统和URSM
func (e *Executor) Execute(ctx context.Context, req Request) Result {
    result := e.doUpstreamCall(req)
    
    // 旧系统
    e.stateManagerOld.UpdateOnSuccess(...)
    
    // 新系统（URSM）
    e.ursm.RecordRequest(...)
    
    // 异步对比
    go compareStates(e.stateManagerOld, e.ursm, req.CredentialID, req.Model)
    
    return result
}
```

### Phase 2: 灰度读取

```go
func (r *Router) PlanCandidates(...) []provider.Candidate {
    // 使用Feature Flag控制流量
    if featureFlag.IsEnabled("ursm_routing", userID) {
        // 新系统
        return r.planWithURSM(ctx, model, sessionID)
    }
    
    // 旧系统
    return r.planLegacy(ctx, model)
}
```

### 对比指标

```
# Prometheus指标
ursm_migration_state_diff_total{field}
ursm_migration_routing_diff_total
ursm_migration_resource_diff_total
```

---

## 回滚方案

### 回滚触发条件
- 状态不一致率 > 5%
- 路由决策错误率 > 1%
- P99延迟增加 > 50%
- 资源泄露

### 回滚步骤

#### Step 1: 停止URSM读取
```go
// 设置环境变量
URSM_READ_OLD=true

// 或Feature Flag
featureFlag.Disable("ursm_routing")
```

#### Step 2: 停止URSM写入
```go
URSM_DUAL_WRITE=false
```

#### Step 3: 重启服务
```bash
kubectl rollout restart deployment llm-gateway-go
```

#### Step 4: 验证
```bash
# 检查错误率
curl http://gateway/metrics | grep error_rate

# 检查延迟
curl http://gateway/metrics | grep latency_p99
```

### 回滚时间
- 准备时间: 5分钟
- 执行时间: 10分钟
- 验证时间: 5分钟
- 总计: 20分钟

---

## 监控与告警

### 迁移期间额外监控

```
# 状态一致性
ursm_migration_consistency_check_total
ursm_migration_consistency_mismatch_total{layer}

# 性能对比
ursm_routing_duration_old_vs_new{percentile}
ursm_cache_hit_ratio_old_vs_new

# 资源占用
ursm_memory_usage_bytes
ursm_goroutine_count
```

### 告警规则

```yaml
# 状态不一致告警
- alert: URSMStateMismatch
  expr: rate(ursm_migration_consistency_mismatch_total[5m]) > 0.05
  annotations:
    summary: "URSM状态与旧系统不一致率超过5%"

# 性能退化告警
- alert: URSMPerformanceDegradation
  expr: ursm_routing_duration_new_p99 > ursm_routing_duration_old_p99 * 1.5
  annotations:
    summary: "URSM路由延迟比旧系统高50%以上"
```

---

## FAQ

### Q1: 迁移期间旧代码何时删除？
**A**: Phase 4（全量切换后1周），确保无问题后删除。

### Q2: 如何确保无数据丢失？
**A**: URSM直接使用现有DB表，无数据迁移，无丢失风险。

### Q3: 迁移失败如何恢复？
**A**: Feature Flag立即切回旧系统，20分钟内完成。

### Q4: 性能会不会下降？
**A**: 不会，URSM引入三层缓存，预期性能提升2倍。

### Q5: 需要停机吗？
**A**: 不需要，灰度上线，无感知切换。

---

## 检查清单

### 迁移前
- [ ] URSM代码开发完成
- [ ] 单元测试通过（覆盖率>90%）
- [ ] 集成测试通过
- [ ] 压力测试达标
- [ ] Feature Flag配置完成
- [ ] 监控面板配置完成
- [ ] 回滚方案确认

### Phase 1: 双写
- [ ] URSM接收状态更新
- [ ] 状态一致性>95%
- [ ] 无性能影响
- [ ] 运行7天无问题

### Phase 2: 灰度读取
- [ ] 5%流量切换，监控24h
- [ ] 25%流量切换，监控48h
- [ ] 路由决策一致性>99%
- [ ] 性能无退化

### Phase 3: 全量切换
- [ ] 100%流量使用URSM
- [ ] 监控72h无异常
- [ ] 停止旧系统写入

### Phase 4: 清理
- [ ] 移除旧代码到待删除目录
- [ ] 更新文档
- [ ] 关闭Feature Flag
- [ ] 项目总结

---

## 附录

### 待删除文件清单

```
_to-be-deprecated/routing-old/
├── credentialstate/
│   ├── manager.go
│   ├── cache.go
│   └── ports.go
├── credentialfpslot/
│   └── node_state.go
├── streaming/executors/
│   └── router_legacy.go
└── README.md (说明为何废弃，何时可删)
```

### 代码统计

```bash
# 旧系统代码量
find domains/credentialstate -name "*.go" | xargs wc -l
# 约2000行

# 新系统代码量
find domains/ursm -name "*.go" | xargs wc -l
# 约3000行（含测试）

# 重复代码减少
# 预期减少70%
```
