# P2-C: Background Worker 架构决策文档

> **状态**: PENDING - 需要 owner 决策  
> **优先级**: P2  
> **创建时间**: 2026-09-01  
> **关联**: handoff_20260901_021000.md §遗留任务

---

## 问题背景

### 当前状况

整个集群（154+245）所有实例都运行在 `traffic-only` 模式，**没有任何实例运行完整的 bg services**。

**受影响的 Background Workers**:

| Worker | 功能 | 影响 | 风险等级 |
|--------|------|------|----------|
| **PartitionManager** | 分区滚转、归档 | 11 月起可能无人创建分区 | 🔴 HIGH |
| **CredentialRecovery** | 凭据探测恢复 | 凭据状态长期停滞 | 🟡 MEDIUM |
| **AuditTrimmer** | 审计日志清理 | 日志堆积 | 🟢 LOW |
| **ModelQuality** | 模型质量统计 | 监控数据缺失 | 🟢 LOW |

### 为什么现在能正常运行？

- **9/10 月分区已存在**：由历史 full 实例创建或手动预创建
- **MaterializedViewRefresher 例外**：作为特殊处理，**无条件在所有实例启动**（包括 traffic-only）

### 代码证据

#### MaterializedViewRefresher 的特殊处理（main.go:3175-3186）

```go
// 2026-08-31: Materialized view refresher runs on ALL instances
// (including traffic-only) because blue-green clusters have no
// full-role instance, yet analytics endpoints need fresh views.
// Safe to run everywhere: REFRESH CONCURRENTLY + advisory lock.
if dbConn != nil && dbConn.Enabled() {
    mvRefresher := bg.NewMaterializedViewRefresher(dbConn.Pool())
    mvRefresher.Start()
    defer mvRefresher.Stop()
}
```

**关键设计**:
- ✅ 无条件启动（不检查 `bgDataPlaneOnly`）
- ✅ 使用 advisory lock 防止并发执行
- ✅ 幂等操作（`REFRESH MATERIALIZED VIEW CONCURRENTLY`）

#### 其他 Workers 的门控（main.go:3189）

```go
if dbConn != nil && dbConn.Enabled() && !cfg.IsTrafficOnly() {
    // 只有 full-role 实例才启动以下 workers:
    credRecovery = bg.NewCredentialRecovery(...)
    partitionManager = bg.NewPartitionManager(...)
    // ... 其他 workers
}
```

**问题**: 集群中所有实例都是 traffic-only，这个条件块**永远不会执行**。

---

## 解决方案选项

### 选项 A: 启动专用 full-role 实例 ⭐️ **推荐**

**实施方式**:

在 252 或独立服务器上启动一个 full-role 实例，专门运行 bg workers。

**配置示例**:

```bash
# /etc/llm-gateway-go/env on 252
LLM_GATEWAY_RUNTIME_ROLE=full  # 或者不设置（默认 full）
LLM_GATEWAY_LISTEN=:8783       # 不同端口，避免冲突
LLM_GATEWAY_BG_MODE=control-plane  # 只运行 bg workers，不接受流量
```

**优点**:
- ✅ **最小代码变更**: 无需修改代码
- ✅ **清晰分离**: bg workers 与 traffic-handling 完全解耦
- ✅ **资源隔离**: bg 任务不影响流量实例性能
- ✅ **易于监控**: 单一实例便于日志查看和问题定位
- ✅ **现有架构**: 已有 `bgDataPlaneOnly` 门控机制

**缺点**:
- ❌ 需要额外机器资源（252 有足够资源）
- ❌ 增加一个运维目标（需要监控健康状态）

**实施步骤**:

1. **在 252 上部署 full-role 实例**:
   ```bash
   # 复制二进制和配置
   scp /opt/llm-gateway-go/llm-gateway-go root@252:/opt/llm-gateway-go-bg/
   
   # 创建 systemd 服务
   cat > /etc/systemd/system/llm-gateway-go-bg.service <<'EOF'
   [Unit]
   Description=LLM Gateway Go Background Workers
   After=network.target postgresql.service
   
   [Service]
   Type=simple
   User=llm-gateway
   WorkingDirectory=/opt/llm-gateway-go-bg
   EnvironmentFile=/etc/llm-gateway-go/env
   Environment="LLM_GATEWAY_LISTEN=:8783"
   Environment="LLM_GATEWAY_BG_MODE=control-plane"
   ExecStart=/opt/llm-gateway-go-bg/llm-gateway-go
   Restart=always
   RestartSec=10
   
   [Install]
   WantedBy=multi-user.target
   EOF
   
   systemctl daemon-reload
   systemctl enable llm-gateway-go-bg
   systemctl start llm-gateway-go-bg
   ```

2. **验证 bg workers 启动**:
   ```bash
   journalctl -u llm-gateway-go-bg -f | grep -E "partitionManager|credRecovery"
   ```

3. **监控健康状态**:
   ```bash
   # 添加健康检查脚本
   curl http://localhost:8783/health
   ```

**风险**:
- 🟡 单点故障（但 bg workers 本身是幂等的，短时间停机影响不大）
- 🟢 可通过主备模式缓解（advisory lock 自动协调）

---

### 选项 B: 移除关键 worker 的 traffic-only 门控

**实施方式**:

仿照 MaterializedViewRefresher 的模式，将关键 workers 改为：
- 在所有实例上启动
- 使用 advisory lock 协调
- 确保操作幂等

**需要修改的 Workers**:

1. **PartitionManager** (高优先级)
   - 改为无条件启动
   - 已有 advisory lock 机制（需验证）
   - 操作幂等（CREATE IF NOT EXISTS）

2. **CredentialRecovery** (高优先级)
   - 需要添加 advisory lock
   - 确保探测结果不重复写入

**代码变更示例**:

```go
// Before (main.go:~3189)
if dbConn != nil && dbConn.Enabled() && !cfg.IsTrafficOnly() {
    partitionManager = bg.NewPartitionManager(...)
    partitionManager.Start(...)
}

// After
if dbConn != nil && dbConn.Enabled() {
    // PartitionManager uses advisory lock internally, safe on all instances
    partitionManager = bg.NewPartitionManager(...)
    partitionManager.Start(...)
}
```

**需要在 PartitionManager 中验证/添加的逻辑**:

```go
// bg/partition_manager.go
const partitionManagerLockKey int64 = 336_2026_06_26

func (pm *PartitionManager) runCycle(ctx context.Context) {
    // Try to acquire advisory lock
    var acquired bool
    err := pm.db.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", partitionManagerLockKey).Scan(&acquired)
    if err != nil || !acquired {
        // Another instance is running, skip this cycle
        return
    }
    defer pm.releaseLock(ctx)
    
    // Run actual partition work...
}
```

**优点**:
- ✅ 无需额外实例
- ✅ 高可用（任意实例可执行）
- ✅ 自动故障转移（advisory lock）

**缺点**:
- ❌ **代码变更风险**: 需要仔细验证每个 worker 的幂等性
- ❌ **调试困难**: 多个实例竞争，日志分散
- ❌ **资源竞争**: 所有实例都尝试获取锁，增加 DB 负载
- ❌ **测试复杂度**: 需要验证并发场景

**实施步骤**:

1. **审计 PartitionManager 的幂等性**:
   - 检查 `ensureNextMonthPartitions` 是否使用 `CREATE IF NOT EXISTS`
   - 检查 `archiveOldPartitions` 是否支持重入
   - 检查 `promoteBatch` 是否处理重复行

2. **添加 advisory lock**:
   - 在每个 worker 的循环开始获取锁
   - 失败则跳过本次循环

3. **逐个 worker 迁移**:
   - 先迁移 PartitionManager（最高优先级）
   - 验证运行正常后再迁移其他 worker

**风险**:
- 🔴 **高风险**: 如果幂等性有问题，可能导致数据重复或冲突
- 🟡 **调试困难**: 问题排查需要关联多个实例的日志
- 🟡 **回滚复杂**: 需要重新编译部署

---

## 对比分析

| 维度 | 选项 A (专用实例) | 选项 B (移除门控) |
|------|------------------|------------------|
| **实施难度** | 🟢 低（无代码变更） | 🔴 高（需要验证幂等性） |
| **部署时间** | 🟢 1-2 小时 | 🟡 1-2 天（含测试） |
| **代码风险** | 🟢 零 | 🔴 中高 |
| **资源成本** | 🟡 需要额外实例 | 🟢 无 |
| **高可用性** | 🟡 单点（可主备） | 🟢 多实例自动切换 |
| **监控调试** | 🟢 单一实例，日志集中 | 🔴 多实例，日志分散 |
| **回滚方案** | 🟢 停止服务即可 | 🟡 需要重新部署 |

---

## 推荐方案：选项 A + 渐进式优化

### 短期（立即执行）

**采用选项 A**: 在 252 上启动专用 bg worker 实例

**理由**:
1. ✅ **零代码风险**: 无需修改代码，直接部署
2. ✅ **快速解决**: 1-2 小时内解决 11 月分区问题
3. ✅ **易于回滚**: 有问题直接停止服务
4. ✅ **资源充足**: 252 作为数据库服务器，资源充裕

### 中期（可选优化）

在短期方案稳定运行后，**评估是否迁移到选项 B**：

**迁移条件**:
1. PartitionManager 已验证完全幂等
2. 团队有足够测试资源
3. 监控和日志聚合方案就绪

**迁移优先级**:
1. **PartitionManager** - 最关键，优先迁移
2. **CredentialRecovery** - 次要，可延后
3. **AuditTrimmer** - 低优先级
4. **ModelQuality** - 低优先级

---

## 现有 MaterializedViewRefresher 的经验

### 成功案例

MaterializedViewRefresher 已成功运行在所有 traffic-only 实例上：

**关键设计**:
```go
// bg/materialized_view_refresher.go:46
const mvRefreshLockKey int64 = 632_2026_08_31

func (r *MaterializedViewRefresher) refreshAll(ctx context.Context) error {
    // 1. 尝试获取 advisory lock
    var acquired bool
    err := r.db.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", mvRefreshLockKey).Scan(&acquired)
    if err != nil {
        return fmt.Errorf("advisory_lock query: %w", err)
    }
    if !acquired {
        // 其他实例正在刷新，跳过
        slog.Info("refresh cycle skipped: another instance holds the lock")
        return nil
    }
    defer r.releaseLock(ctx)
    
    // 2. 执行幂等操作
    _, err = r.db.Exec(ctx, "REFRESH MATERIALIZED VIEW CONCURRENTLY routing_analytics_7d")
    // ...
}
```

**验证结果**:
- ✅ 154/245 同时运行，无冲突
- ✅ 每 10 分钟只有一个实例执行刷新
- ✅ Advisory lock 自动协调

### 可复用模式

其他 worker 可以复用这个模式：

```go
// 通用 advisory lock 模板
type WorkerWithLock struct {
    db      *pgxpool.Pool
    lockKey int64
}

func (w *WorkerWithLock) runWithLock(ctx context.Context, fn func() error) error {
    var acquired bool
    err := w.db.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", w.lockKey).Scan(&acquired)
    if err != nil || !acquired {
        return nil // Skip this cycle
    }
    defer w.db.Exec(ctx, "SELECT pg_advisory_unlock($1)", w.lockKey)
    
    return fn()
}
```

---

## 实施计划（选项 A）

### Phase 1: 部署专用实例（预计 2 小时）

**任务清单**:

- [ ] 在 252 创建 `/opt/llm-gateway-go-bg` 目录
- [ ] 复制最新的 llm-gateway-go 二进制（build 1866）
- [ ] 创建 systemd 服务文件
- [ ] 配置环境变量（复用 `/etc/llm-gateway-go/env`）
- [ ] 启动服务并验证日志

**验证步骤**:

```bash
# 1. 检查服务状态
systemctl status llm-gateway-go-bg

# 2. 验证 PartitionManager 启动
journalctl -u llm-gateway-go-bg -n 100 | grep -i partition

# 3. 验证数据库连接
journalctl -u llm-gateway-go-bg -n 100 | grep -i "database connected"

# 4. 检查 bg workers 启动日志
journalctl -u llm-gateway-go-bg -n 200 | grep -E "credRecovery|partitionManager|materializedViewRefresher"
```

### Phase 2: 监控设置（预计 1 小时）

**监控项**:

1. **服务健康检查**:
   ```bash
   # 添加到 cron
   */5 * * * * curl -f http://localhost:8783/health || systemctl restart llm-gateway-go-bg
   ```

2. **分区创建监控**:
   ```sql
   -- 检查下月分区是否存在
   SELECT tablename 
   FROM pg_tables 
   WHERE schemaname='public' 
     AND tablename LIKE 'request_logs_%'
   ORDER BY tablename DESC 
   LIMIT 5;
   ```

3. **日志告警**:
   ```bash
   # 添加日志监控
   journalctl -u llm-gateway-go-bg -f | grep -E "ERROR|FATAL" | while read line; do
       # 发送告警
       echo "$line" | # 发送到 Lark
   done
   ```

### Phase 3: 文档更新（预计 30 分钟）

**需要更新的文档**:

- [ ] `docs/deployment/deployment-guide.md` - 添加 bg worker 实例部署说明
- [ ] `docs/architecture/background-workers.md` - 说明 bg worker 架构
- [ ] `README.md` - 更新架构图

---

## 风险评估

### 选项 A 的风险

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|----------|
| 252 实例故障 | 🟡 中 | 🟡 中 | 配置主备实例 + advisory lock 自动切换 |
| 资源不足 | 🟢 低 | 🟢 低 | 252 资源充足，仅运行 bg workers |
| 配置错误 | 🟡 中 | 🟢 低 | 部署前在测试环境验证 |
| 网络分区 | 🟢 低 | 🟡 中 | bg workers 容忍短时间中断 |

### 选项 B 的风险

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|----------|
| 幂等性问题 | 🔴 高 | 🔴 高 | 完整的单元测试 + 集成测试 |
| 并发冲突 | 🟡 中 | 🔴 高 | Advisory lock + 乐观锁 |
| 日志混乱 | 🔴 高 | 🟡 中 | 日志聚合 + 分布式追踪 |
| 回滚困难 | 🟡 中 | 🔴 高 | 特性开关 + 灰度发布 |

---

## 决策检查清单

在做出最终决策前，请确认：

- [ ] **资源评估**: 252 是否有足够资源运行 bg worker 实例？
- [ ] **时间约束**: 11 月分区需要在何时创建？（最晚 10 月 31 日）
- [ ] **团队能力**: 是否有足够人力进行选项 B 的开发和测试？
- [ ] **风险承受**: 能否接受选项 B 的代码变更风险？
- [ ] **长期规划**: 未来集群架构是否会改变（如增加 full-role 实例）？

---

## 后续行动

### 如果选择选项 A

1. **立即行动**:
   - [ ] 在 252 部署 bg worker 实例
   - [ ] 验证 PartitionManager 正常运行
   - [ ] 配置监控和告警

2. **后续优化**:
   - [ ] 评估是否需要主备实例
   - [ ] 考虑是否迁移到选项 B

### 如果选择选项 B

1. **准备工作**:
   - [ ] 审计 PartitionManager 代码
   - [ ] 编写单元测试和集成测试
   - [ ] 在测试环境验证

2. **分阶段实施**:
   - [ ] 第一阶段: 只迁移 PartitionManager
   - [ ] 观察 1-2 周
   - [ ] 第二阶段: 迁移其他 workers

---

## 参考资料

- [handoff_20260901_021000.md](../../.handoff/handoff_20260901_021000.md) - 工作交接文档
- [bg/materialized_view_refresher.go](../../bg/materialized_view_refresher.go) - MV refresher 实现
- [bg/partition_manager.go](../../bg/partition_manager.go) - PartitionManager 实现
- [cmd/gateway/main.go:2431](../../cmd/gateway/main.go#L2431) - bgDataPlaneOnly 定义
- [cmd/gateway/main.go:3175-3186](../../cmd/gateway/main.go#L3175-L3186) - MV refresher 无条件启动

---

**文档版本**: 1.0  
**最后更新**: 2026-09-01  
**决策状态**: PENDING - 等待 owner 决策  
**联系人**: 查看 handoff 文档
