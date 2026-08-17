# SystemMonitor Phase 3 Stage 1 最终交付报告

## 执行时间
2026-07-24 00:00 - 00:40 (40 分钟)

## 总目标
实现 Phase 3 切流基础设施：监控指标收集 + 旧 worker 打标 + Vue Dashboard 可视化。

---

## 📦 交付清单

### ✅ Task 1.1: 监控指标收集器 (15 分钟)
**Commit**: e6659ced0

**核心产出**:
1. `bg/systemmonitor/metrics_collector.go` (143 行)
   - CollectCoverage(ctx, windowDays): 统计任务覆盖率
   - IsReadyForMigration(ctx): 自动判断 ≥80% 阈值
   - 按 source/task_type 分组

2. `admin/systemmonitor_handlers.go`
   - GET /api/admin/system-monitor/migration-metrics?window_days=7
   - 返回 metrics + ready_for_migration + migration_message

3. `admin/handler.go`
   - SystemMonitorBackend.GetMetricsCollector() 接口扩展

4. 单元测试
   - TestCoverageMetrics_Calculation ✅ PASS

### ✅ Task 1.2: 旧 worker 打标 (5 分钟)
**Commit**: d8e6e3371

**核心产出**:
1. `bg/credential_selfcheck.go` 改造
   - runOne() 新增 auditToSystemProbeRuns() 调用
   - 探测完成后写入 system_probe_runs
   - source="legacy_selfcheck" ← 关键标记

2. auditToSystemProbeRuns() 方法 (~60 行)
   - task_type="credential_selfcheck"
   - automaticity="automatic"
   - worker_id="credential-selfcheck-worker"
   - 容错：3s 超时 + non-blocking

### ✅ Task 1.3: Vue Dashboard 切流进度 (10 分钟)
**Commit**: 9854b7dc2 + 40d9abe2f (修复)

**核心产出**:
1. `web/src/api/api-system-monitor.ts` (+30 行)
   - MigrationMetrics / MigrationMetricsResponse 类型
   - fetchMigrationMetrics(windowDays) API

2. `web/src/views/SystemMonitorPanel.vue` (+180 行)
   - "Phase 3 切流进度"卡片区域
   - 覆盖率大数字 + el-progress 进度条
   - 动态颜色：≥80% 绿色 / 60-79% 黄色 / <60% 红色
   - 新框架 vs 旧 worker 任务对比
   - 按来源/任务类型分组折叠面板
   - loadMigrationMetrics() 集成到轮询

3. CSS 样式 (+60 行)
   - 响应式网格布局
   - 动态状态颜色
   - el-alert 状态提示

---

## 📊 统计总览

| 维度 | Task 1.1 | Task 1.2 | Task 1.3 | **合计** |
|---|---|---|---|---|
| 新增文件 | 3 | 0 | 0 | **3** |
| 修改文件 | 3 | 1 | 2 | **6** |
| 新增代码 | 241 | 72 | 210 | **523 行** |
| 新增 API | 1 | 0 | 0 | **1** |
| 新增 UI | 0 | 0 | 1 | **1** |
| Git commits | 1 | 1 | 2 | **4** |
| 耗时 | 15 min | 5 min | 10 min | **30 min** |

## 🔄 数据流全景

```
┌─────────────────────────────────────────────────┐
│ 旧 worker: credential_selfcheck                 │
│ ├─ finalizeRun() → self_check_runs (原有)      │
│ └─ auditToSystemProbeRuns() ↓ (新增)           │
└──────────────────┬──────────────────────────────┘
                   │
                   ├─> INSERT system_probe_runs
                   │   - source="legacy_selfcheck"
                   │   - task_type="credential_selfcheck"
                   │
┌──────────────────▼──────────────────────────────┐
│ 新框架: SystemMonitor                            │
│ └─ Submit() → system_probe_runs                 │
│    - source="systemmonitor"                     │
│    - task_type="direct_ping" / "chat_minimal"  │
└──────────────────┬──────────────────────────────┘
                   │
                   ▼
┌─────────────────────────────────────────────────┐
│ MetricsCollector.CollectCoverage()              │
│ ├─ 统计 7 天内任务 (system_probe_runs)         │
│ ├─ 按 source 分组 (systemmonitor vs legacy_*)  │
│ ├─ 计算覆盖率 = systemmonitor / total * 100    │
│ └─ IsReadyForMigration() → ≥80% 判断           │
└──────────────────┬──────────────────────────────┘
                   │
                   ▼
┌─────────────────────────────────────────────────┐
│ GET /api/admin/system-monitor/migration-metrics │
│ → { metrics, ready_for_migration, message }    │
└──────────────────┬──────────────────────────────┘
                   │
                   ▼
┌─────────────────────────────────────────────────┐
│ Vue Dashboard: SystemMonitorPanel.vue           │
│ ├─ 覆盖率大数字 + 进度条                        │
│ ├─ 新框架 vs 旧 worker 任务对比                 │
│ ├─ 切流就绪状态 (el-alert)                      │
│ └─ 按来源/类型分组 (el-collapse)                │
└─────────────────────────────────────────────────┘
```

---

## 🧪 验证方式

### 1. 后端 API 验证
```bash
# 查询 7 天覆盖率
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  https://llm.kxpms.cn/api/admin/system-monitor/migration-metrics?window_days=7 | jq

# 预期输出
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
    }
  },
  "ready_for_migration": false,
  "migration_message": "Coverage 40.00% < 80% ..."
}
```

### 2. 数据库验证
```sql
-- 查看旧 worker 任务（最近 1 天）
SELECT 
  source, task_type, status, COUNT(*) as count
FROM system_probe_runs
WHERE started_at > NOW() - INTERVAL '1 day'
  AND source = 'legacy_selfcheck'
GROUP BY source, task_type, status;

-- 预期输出
source             | task_type             | status  | count
-------------------+-----------------------+---------+-------
legacy_selfcheck   | credential_selfcheck  | success |   120
legacy_selfcheck   | credential_selfcheck  | failed  |    15
```

### 3. Vue Dashboard 验证（需部署）
```
访问: https://llm.kxpms.cn/system-monitor
位置: 统计卡片下方
预期: "Phase 3 切流进度"卡片显示
```

---

## 🚀 部署要求

### 245 预发布环境
```bash
# 1. 部署后端
bash scripts/deploy-245.sh

# 2. 验证 API
curl https://llm-245.kxpms.cn/api/admin/system-monitor/migration-metrics?window_days=1

# 3. 验证前端（需 license 激活）
# 访问 https://llm-245.kxpms.cn/system-monitor
```

### 154 生产环境
```bash
# 1. 部署后端
bash scripts/deploy-154.sh

# 2. 验证 API
curl https://llm.kxpms.cn/api/admin/system-monitor/migration-metrics?window_days=7

# 3. 验证前端（需 license 激活）
# 访问 https://llm.kxpms.cn/system-monitor
```

---

## 📋 Git 记录

```
commit e6659ced0 - Task 1.1: 监控指标收集器
commit d8e6e3371 - Task 1.2: 旧 worker 打标
commit 9854b7dc2 - Task 1.3: Vue Dashboard 切流进度
commit 40d9abe2f - fix: API 调用修复
```

**推送状态**: ✅ 已推送 origin/main

---

## 🎯 Stage 2 预览

### 目标：逐步切流（第 2 周）

#### Stage 2 Task 2.1: SystemMonitor 自动提交
- 新增 bg/systemmonitor/scheduler.go
- 每 10 分钟扫描 credentials 表
- 提交 task_type=direct_ping, automaticity=automatic
- 利用 5min recent_success 跳过规则

#### Stage 2 Task 2.2: 双写观察期（7 天）
- 旧 worker 继续运行
- 新框架并行提交
- 监控覆盖率增长曲线
- 目标：≥ 80%

#### Stage 2 Task 2.3: 达标后停用旧 worker
**条件**: 连续 7 天 ≥ 80%

**操作**:
```bash
# 154/245 停用旧 worker
sed -i 's/LLM_GATEWAY_USE_NEW_PROBE_MODE=true/false/' /etc/llm-gateway-go/env
systemctl restart llm-gateway-go
```

---

## 🎯 Stage 3 预览

### 目标：清理归档（第 3 周）

#### Stage 3 Task 3.1: 删除旧代码
**条件**: 连续 7 天新框架 ≥ 90%

```bash
mkdir -p _archive/2026-08-old-workers/
git mv bg/credential_selfcheck.go _archive/2026-08-old-workers/
git mv bg/credential_selfcheck_test.go _archive/2026-08-old-workers/
```

#### Stage 3 Task 3.2: 清理 KEEP/FUTURE 标记
- 删除 bg/credential_selfcheck.go 的 KEEP 标记
- 保留 bg/asset_health_probe.go 的 FUTURE 标记（2027-Q1）

#### Stage 3 Task 3.3: 文档更新
- CHANGELOG.md 新增 "Removed" 段
- docs/changelogs/2026-08-XX-remove-legacy-workers.md

---

## ⚠️ 风险与缓解

| 风险 | 当前状态 | 缓解措施 |
|---|---|---|
| 新框架任务覆盖不足 | 🟡 未知（需部署验证） | 双写期延长至 14 天 |
| 旧 worker 打标失败率高 | 🟢 已容错 | 写入失败仅 slog.Warn，不阻塞 |
| system_probe_runs 写入性能 | 🟢 已优化 | 分区表 + 3s 超时 |
| 切流后发现遗漏场景 | 🟢 可回滚 | _archive/ 保留全量代码 |
| Vue Dashboard license 限制 | 🟡 已知 | 激活后补测，代码已就绪 |

---

## ✅ 验收标准

### Stage 1 完成标准（已达成）
- [x] MetricsCollector 单元测试通过
- [x] Admin API 编译通过
- [x] Vue Dashboard TypeScript 编译通过
- [x] Git commits 推送成功
- [x] 文档齐全（3 个总结文档）

### Stage 2 启动条件（待满足）
- [ ] 部署到 245/154
- [ ] system_probe_runs 表有数据（≥ 100 条）
- [ ] 旧 worker 正常运行 ≥ 24h
- [ ] Vue Dashboard 可访问（license 激活）
- [ ] 覆盖率 API 返回有效数据

---

## 📚 文档清单

1. `docs/会话优化v2/35-SystemMonitor-Phase3-切流计划.md` (完整计划)
2. `docs/会话优化v2/36-*-Task1.1-总结.md` (Task 1.1)
3. `docs/会话优化v2/37-*-Stage1-完成总结.md` (Task 1.1+1.2)
4. `docs/会话优化v2/38-*-最终交付.md` (本文档)

---

**执行者**: AI Agent (build mode)  
**完成时间**: 2026-07-24 00:40  
**状态**: ✅ Phase 3 Stage 1 全部完成  
**下一步**: 部署到 245/154 验证 → Stage 2 启动

**总耗时**: 40 分钟（实际编码 30 分钟）  
**代码质量**: ✅ 编译通过 + 单元测试通过  
**Git 状态**: ✅ 4 commits 推送到 origin/main
