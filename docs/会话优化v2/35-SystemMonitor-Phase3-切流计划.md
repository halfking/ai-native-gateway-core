# SystemMonitor Phase 3 切流计划

## 目标
将现有的旧 worker（CredentialSelfcheckWorker + AssetHealthProbe）逐步迁移到 SystemMonitor 统一框架，实现"唯一入口"。

## 前置条件（Phase 1+2 已完成）
✅ SystemMonitor 核心已投产（154 生产环境运行）
✅ system_probe_runs 审计表已建立
✅ KEEP/FUTURE 标记已就位
✅ 5 worker 并发运行正常

## Phase 3 分阶段执行（预计 2-3 周）

### Stage 1: 监控指标收集（第 1 周）

#### 1.1 实现监控指标 Collector
- 文件：`bg/systemmonitor/metrics_collector.go`
- 功能：
  - 统计 system_probe_runs 表中 7 天内的任务分布
  - 按 source 分组统计（systemmonitor 新 vs 旧 worker）
  - 计算覆盖率：`systemmonitor 任务数 / 总任务数`
  - 输出到 Prometheus metrics（可选）或日志

#### 1.2 添加旧 worker 打标
- 修改 `bg/credential_selfcheck.go` 和 `bg/asset_health_probe.go`
- 在它们的探测逻辑中插入 `source="legacy_selfcheck"` / `source="legacy_asset_health"`
- 写入同样的 system_probe_runs 表（复用 Audit 函数）
- **目的**：量化新旧并存期的任务分布

#### 1.3 Dashboard 指标面板
- 在 SystemMonitorPanel.vue 新增"切流进度"卡片
- 显示：
  - 新框架任务占比（目标 ≥ 80%）
  - 旧 worker 剩余任务占比
  - 预计切流时间（根据增长趋势）

### Stage 2: 逐步切流（第 2 周）

#### 2.1 CredentialSelfcheck 切流
**当前状态**：712 行，每 5 分钟全量扫描所有凭据

**迁移方案**：
1. 在 SystemMonitor 中实现 `submit_all_credentials()` 定时任务
2. 按 credential_id 提交 `task_type=direct_ping` 任务到队列
3. automaticity=automatic（利用 5min 跳过规则）
4. 第一周：双写模式（旧 worker 继续运行，新框架也提交）
5. 第二周：观察 system_probe_runs 覆盖率，≥ 80% 后停用旧 worker

**验证**：
```sql
SELECT 
  source,
  COUNT(*) as task_count,
  ROUND(100.0 * COUNT(*) / SUM(COUNT(*)) OVER(), 2) as percentage
FROM system_probe_runs
WHERE started_at > NOW() - INTERVAL '7 days'
  AND task_type IN ('direct_ping', 'credential_selfcheck')
GROUP BY source;
```

#### 2.2 AssetHealthProbe 延后
**当前状态**：205 行，FUTURE 标记

**决策**：保留到 Phase 4（2027-Q1），等 SystemMonitor 稳定后再实现 asset-level 监控。

### Stage 3: 清理与归档（第 3 周）

#### 3.1 删除旧 worker 代码
**条件**：连续 7 天 system_probe_runs 中新框架任务 ≥ 90%

**操作**：
```bash
# 1. 归档旧代码到 _archive/
mkdir -p _archive/2026-08-old-workers/
git mv bg/credential_selfcheck.go _archive/2026-08-old-workers/
git mv bg/credential_selfcheck_test.go _archive/2026-08-old-workers/

# 2. 从 cmd/gateway/main.go 移除启动逻辑
# 删除 CredentialSelfcheckWorker 初始化行

# 3. 清理 KEEP 标记
# asset_health_probe.go 保留（FUTURE 2027-Q1）
```

#### 3.2 更新文档
- CHANGELOG.md 新增 "Removed" 段
- docs/changelogs/2026-08-XX-remove-legacy-workers.md
- 更新 32-系统监测模块设计.md §6.4

#### 3.3 验收标准
- [ ] system_probe_runs 中 source="systemmonitor" 占比 ≥ 90%（7 天窗口）
- [ ] 旧 worker 代码已归档到 _archive/
- [ ] cmd/gateway/main.go 中无旧 worker 启动代码
- [ ] 所有测试通过
- [ ] 154 生产运行无异常（监控 24h）

## Stage 1 立即执行（当前任务）

### Task 1.1: 实现 metrics_collector.go
创建监控指标收集器，统计新旧任务占比。

### Task 1.2: 旧 worker 打标改造
修改 credential_selfcheck.go，在探测成功/失败时写入 system_probe_runs（source="legacy_selfcheck"）。

### Task 1.3: Vue Dashboard 切流进度卡片
在 SystemMonitorPanel.vue 新增第 5 张卡片"切流进度"。

---

## 风险与缓解

| 风险 | 影响 | 缓解 |
|---|---|---|
| 新框架任务覆盖不足 | 切流延期 | 双写期延长至 14 天 |
| 旧 worker 删除后发现遗漏 | 功能缺失 | _archive/ 保留全量代码，可随时恢复 |
| system_probe_runs 写入影响性能 | DB 压力 | 分区表 + 异步写入（已实现）|

## 时间线

```
Week 1 (2026-07-24 - 07-31): Stage 1 监控指标 + 旧 worker 打标
Week 2 (2026-08-01 - 08-08): Stage 2 双写模式 + 覆盖率观察
Week 3 (2026-08-09 - 08-15): Stage 3 清理归档（如覆盖率达标）
```

## 下一步行动

立即执行 **Stage 1 Task 1.1**：实现 metrics_collector.go
