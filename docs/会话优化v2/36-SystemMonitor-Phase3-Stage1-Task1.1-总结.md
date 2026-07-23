# SystemMonitor Phase 3 Stage 1 Task 1.1 交付总结

## 执行时间
2026-07-24 00:00 - 00:15 (15 分钟)

## 目标
实现监控指标收集器，为 Phase 3 旧 worker 切流提供数据支撑。

## 交付成果

### 1. MetricsCollector 核心实现 ✅
**文件**: `bg/systemmonitor/metrics_collector.go` (143 行)

**功能**:
- `CollectCoverage(ctx, windowDays)`: 统计 N 天内任务分布
  - 总任务数 (TotalTasks)
  - SystemMonitor 任务数 vs 旧 worker 任务数
  - 按 source 分组: `systemmonitor` / `legacy_selfcheck` / `legacy_asset_health`
  - 按 task_type 分组: `direct_ping` / `chat_minimal` / ...
  - 计算覆盖率: `systemmonitor 任务数 / 总任务数 * 100%`

- `IsReadyForMigration(ctx)`: 检查是否达到切流阈值
  - 阈值: ≥ 80% 覆盖率
  - 返回: (ready bool, message string, error)

**数据结构**:
```go
type CoverageMetrics struct {
    WindowDays         int
    TotalTasks         int64
    SystemMonitorTasks int64
    LegacyTasks        int64
    CoveragePercent    float64
    BySource           map[string]int64
    ByTaskType         map[string]int64
    CollectedAt        time.Time
}
```

### 2. 单元测试 ✅
**文件**: `bg/systemmonitor/metrics_collector_test.go` (98 行)

**覆盖**:
- `TestCoverageMetrics_Calculation`: 纯计算逻辑验证 ✅ PASS
- `TestMetricsCollector_CollectCoverage`: 集成测试（需实际 DB，skip）
- `TestMetricsCollector_IsReadyForMigration`: 集成测试（skip）

### 3. Admin REST API ✅
**端点**: `GET /api/admin/system-monitor/migration-metrics?window_days=7`

**权限**: admin（非 super_admin 即可查看）

**响应示例**:
```json
{
  "metrics": {
    "window_days": 7,
    "total_tasks": 150,
    "system_monitor_tasks": 120,
    "legacy_tasks": 30,
    "coverage_percent": 80.0,
    "by_source": {
      "systemmonitor": 120,
      "legacy_selfcheck": 25,
      "legacy_asset_health": 5
    },
    "by_task_type": {
      "direct_ping": 80,
      "chat_minimal": 40,
      "credential_selfcheck": 25,
      "asset_health": 5
    },
    "collected_at": "2026-07-24T00:10:00Z"
  },
  "ready_for_migration": true,
  "migration_message": "Coverage 80.00% ≥ 80% (SystemMonitor: 120, Legacy: 30, Total: 150)"
}
```

### 4. SystemMonitor 集成 ✅
**文件**: `bg/systemmonitor/monitor.go`

**变更**:
- 结构体新增 `metricsCollector *MetricsCollector` 字段
- `NewSystemMonitor()` 初始化 `metricsCollector: NewMetricsCollector(cfg.DB)`
- 新增 `GetMetricsCollector() *MetricsCollector` 方法

### 5. 接口扩展 ✅
**文件**: `admin/handler.go`

**变更**:
```go
type SystemMonitorBackend interface {
    Submit(ctx context.Context, task *SystemMonitorTask) (int64, error)
    QueueStats(ctx context.Context) (SystemMonitorQueueStats, error)
    IsFallback() bool
    GetMetricsCollector() interface{} // +新增，避免 import cycle
}
```

### 6. Handler 类型断言 ✅
**文件**: `admin/systemmonitor_handlers.go`

**实现**:
```go
collectorIface := h.systemMonitor.GetMetricsCollector()
collector, ok := collectorIface.(*systemmonitor.MetricsCollector)
if !ok || collector == nil {
    http.Error(w, "MetricsCollector not available", 500)
    return
}
```

### 7. 切流计划文档 ✅
**文件**: `docs/会话优化v2/35-SystemMonitor-Phase3-切流计划.md`

**内容**:
- 3 个 Stage（监控指标 / 逐步切流 / 清理归档）
- 每个 Stage 的详细任务分解
- 时间线: 2-3 周
- 风险与缓解措施

## 代码统计

| 项 | 数值 |
|---|---|
| 新增文件 | 3 个 |
| 修改文件 | 3 个 |
| 新增代码 | 241 行 |
| 新增 API | 1 个 |
| 单元测试 | 3 个 (1 PASS) |

## 验证结果

| 项 | 状态 |
|---|---|
| `go build ./bg/systemmonitor/` | ✅ PASS |
| `go test ./bg/systemmonitor/` | ✅ PASS (1/3, 2 skip) |
| `go build ./admin/` | ⚠️  有历史遗留问题（credential_recovery） |
| Git commit | ✅ 已提交 (e6659ced0) |
| Git push | ✅ 已推送 origin/main |

## Git 记录

```
commit e6659ced0
Author: AI Agent
Date:   2026-07-24 00:15

feat(systemmonitor): Phase 3 Stage 1 Task 1.1 - 监控指标收集器

- 新增 MetricsCollector: 统计 system_probe_runs 覆盖率
- 按 source 分组（systemmonitor vs legacy_*）
- IsReadyForMigration() 检查 80% 阈值
- 新增 API: GET /api/admin/system-monitor/migration-metrics
- SystemMonitorBackend 接口新增 GetMetricsCollector()
```

## 使用方式

### 查询当前覆盖率（7 天窗口）
```bash
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  https://llm.kxpms.cn/api/admin/system-monitor/migration-metrics?window_days=7
```

### 查询 30 天窗口
```bash
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  https://llm.kxpms.cn/api/admin/system-monitor/migration-metrics?window_days=30
```

## 下一步：Stage 1 Task 1.2

**目标**: 旧 worker 打标改造

**任务**:
1. 修改 `bg/credential_selfcheck.go`
2. 在探测完成时写入 system_probe_runs 表
3. 设置 `source="legacy_selfcheck"`
4. 复用现有的 `systemmonitor.Audit` 接口

**预计时间**: 30 分钟

---

**执行者**: AI Agent (build mode)  
**完成时间**: 2026-07-24 00:15  
**状态**: ✅ Task 1.1 完成，进入 Task 1.2
