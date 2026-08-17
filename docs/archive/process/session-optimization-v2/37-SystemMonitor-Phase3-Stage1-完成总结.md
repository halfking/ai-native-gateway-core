# SystemMonitor Phase 3 Stage 1 完成总结

## 执行时间
2026-07-24 00:00 - 00:25 (25 分钟)

## 目标
实现监控指标收集 + 旧 worker 打标，为 Phase 3 切流提供数据基础。

## Stage 1 交付清单

### ✅ Task 1.1: 监控指标收集器 (15 分钟)

#### 核心成果
1. **MetricsCollector** (143 行)
   - `CollectCoverage(ctx, windowDays)`: 统计任务覆盖率
   - `IsReadyForMigration(ctx)`: 自动判断 ≥80% 阈值
   - 按 source 分组: systemmonitor vs legacy_*
   - 按 task_type 分组

2. **Admin API**
   - `GET /api/admin/system-monitor/migration-metrics?window_days=7`
   - 返回完整 metrics + ready_for_migration 判断

3. **接口扩展**
   - SystemMonitorBackend.GetMetricsCollector()
   - SystemMonitor.metricsCollector 字段

4. **文档**
   - docs/会话优化v2/35-SystemMonitor-Phase3-切流计划.md
   - docs/会话优化v2/36-*-Task1.1-总结.md

#### Git
```
commit e6659ced0
feat(systemmonitor): Phase 3 Stage 1 Task 1.1 - 监控指标收集器
```

### ✅ Task 1.2: 旧 worker 打标 (5 分钟)

#### 核心成果
1. **credential_selfcheck 改造**
   - runOne() 增加 auditToSystemProbeRuns() 调用
   - 探测完成后写入 system_probe_runs 表
   - source="legacy_selfcheck" ← 关键标记

2. **auditToSystemProbeRuns() 方法** (~60 行)
   - task_type="credential_selfcheck"
   - automaticity="automatic"
   - worker_id="credential-selfcheck-worker"
   - 容错设计: 3s 超时 + 非阻塞

3. **状态映射**
   - success → success
   - partial → success (至少 1 轮成功)
   - failed → failed

#### Git
```
commit d8e6e3371
feat(systemmonitor): Phase 3 Stage 1 Task 1.2 - 旧 worker 打标
```

## Stage 1 整体统计

| 项 | Task 1.1 | Task 1.2 | 合计 |
|---|---|---|---|
| 新增文件 | 3 | 0 | 3 |
| 修改文件 | 3 | 1 | 4 |
| 新增代码 | 241 | 72 | 313 行 |
| 新增 API | 1 | 0 | 1 |
| Git commits | 1 | 1 | 2 |
| 耗时 | 15 min | 5 min | 20 min |

## 数据流全景

```
┌────────────────────────────────────────────┐
│  旧 worker: credential_selfcheck           │
│  - finalizeRun() → self_check_runs         │
│  - auditToSystemProbeRuns() ↓              │
└────────────────┬───────────────────────────┘
                 │
                 ├─> INSERT system_probe_runs
                 │   source="legacy_selfcheck"
                 │
┌────────────────▼───────────────────────────┐
│  新框架: SystemMonitor                      │
│  - Submit() → system_probe_runs            │
│    source="systemmonitor"                  │
└────────────────┬───────────────────────────┘
                 │
                 ▼
┌────────────────────────────────────────────┐
│  MetricsCollector.CollectCoverage()        │
│  - 统计 7 天内任务                          │
│  - 按 source 分组                          │
│  - 计算覆盖率 = systemmonitor / total      │
│  - 判断 ≥ 80% → ready_for_migration        │
└────────────────┬───────────────────────────┘
                 │
                 ▼
┌────────────────────────────────────────────┐
│  GET /api/admin/.../migration-metrics      │
│  → Vue Dashboard (Task 1.3, 待完成)        │
└────────────────────────────────────────────┘
```

## 验证方式

### 本地测试（需部署到 154/245）

#### 1. 查看旧 worker 任务
```sql
SELECT 
  source, task_type, status, COUNT(*) as count
FROM system_probe_runs
WHERE started_at > NOW() - INTERVAL '1 day'
  AND source = 'legacy_selfcheck'
GROUP BY source, task_type, status
ORDER BY count DESC;
```

预期输出：
```
source             | task_type             | status  | count
-------------------+-----------------------+---------+-------
legacy_selfcheck   | credential_selfcheck  | success |   120
legacy_selfcheck   | credential_selfcheck  | failed  |    15
```

#### 2. 查看覆盖率
```bash
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  https://llm.kxpms.cn/api/admin/system-monitor/migration-metrics?window_days=7 | jq
```

预期输出：
```json
{
  "metrics": {
    "window_days": 7,
    "total_tasks": 200,
    "system_monitor_tasks": 80,
    "legacy_tasks": 120,
    "coverage_percent": 40.0,
    "by_source": {
      "systemmonitor": 80,
      "legacy_selfcheck": 120
    },
    "by_task_type": {
      "direct_ping": 60,
      "chat_minimal": 20,
      "credential_selfcheck": 120
    }
  },
  "ready_for_migration": false,
  "migration_message": "Coverage 40.00% < 80% (SystemMonitor: 80, Legacy: 120, Total: 200). Need more time."
}
```

## Stage 2 预览：逐步切流（第 2 周）

### Stage 2 Task 2.1: 启用 SystemMonitor 自动提交
**目标**: 让 SystemMonitor 定时提交所有凭据的 direct_ping 任务

**实现**:
1. 新增 `bg/systemmonitor/scheduler.go`
2. 每 10 分钟扫描 credentials 表
3. 提交 task_type=direct_ping, automaticity=automatic
4. 利用 5min recent_success 跳过规则避免重复

### Stage 2 Task 2.2: 双写观察期（7 天）
- 旧 worker 继续运行（legacy_selfcheck）
- 新框架并行提交（systemmonitor）
- 监控覆盖率变化曲线

### Stage 2 Task 2.3: 达标后停用旧 worker
**条件**: 连续 7 天覆盖率 ≥ 80%

**操作**:
```bash
# 154/245 停用旧 worker
sed -i 's/LLM_GATEWAY_USE_NEW_PROBE_MODE=true/LLM_GATEWAY_USE_NEW_PROBE_MODE=false/' /etc/llm-gateway-go/env
systemctl restart llm-gateway-go
```

## Stage 3 预览：清理归档（第 3 周）

### Stage 3 Task 3.1: 删除旧代码
**条件**: 连续 7 天新框架任务 ≥ 90%

```bash
mkdir -p _archive/2026-08-old-workers/
git mv bg/credential_selfcheck.go _archive/2026-08-old-workers/
git mv bg/credential_selfcheck_test.go _archive/2026-08-old-workers/
# 从 cmd/gateway/main.go 删除启动逻辑
```

### Stage 3 Task 3.2: 更新文档
- CHANGELOG.md 新增 "Removed" 段
- docs/changelogs/2026-08-XX-remove-legacy-workers.md
- 更新 32-系统监测模块设计.md §6.4

## 风险与缓解

| 风险 | 当前状态 | 缓解 |
|---|---|---|
| 新框架任务覆盖不足 | 🟡 未知（需部署验证） | 双写期延长至 14 天 |
| 旧 worker 打标失败率高 | 🟢 已容错（non-blocking） | 写入失败仅 slog.Warn |
| system_probe_runs 写入性能 | 🟢 已优化 | 分区表 + 3s 超时 |
| 切流后发现遗漏场景 | 🟢 可回滚 | _archive/ 保留全量代码 |

## 下一步行动

### 立即可做：Task 1.3 Vue Dashboard
- 在 SystemMonitorPanel.vue 新增"切流进度"卡片
- 调用 /api/admin/system-monitor/migration-metrics
- 实时显示覆盖率 + 进度条
- 预计 30 分钟

### 需部署验证：Stage 1 效果
- 部署到 245/154
- 观察 system_probe_runs 表增长
- 验证 legacy_selfcheck 任务正常写入
- 确认 MetricsCollector API 可用

---

**执行者**: AI Agent (build mode)  
**完成时间**: 2026-07-24 00:25  
**状态**: ✅ Stage 1 完成（Task 1.1 + 1.2），待 Task 1.3  
**Git**: e6659ced0 + d8e6e3371  
**总耗时**: 25 分钟
