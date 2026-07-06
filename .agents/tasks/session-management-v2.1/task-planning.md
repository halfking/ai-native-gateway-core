# 会话管理 v2.1 并行任务规划

> 基于 `docs/session-management-analytics-plan.md` 拆解为可并行执行的子任务
> 
> **执行策略**：按阶段（Phase）并行启动多个子代理，每个代理负责独立的功能模块

---

## 任务依赖关系图

```
Phase 0（基础设施，必须先完成）
  └─ T0.1 数据库 Schema 增强（migration 355/356）
  └─ T0.2 健康评分核心逻辑（Go package）
  
Phase 1（后端 API，可并行，依赖 Phase 0）
  ├─ T1.1 分析中心时间序列 API（4端点）
  ├─ T1.2 分析中心分布归因 API（3端点）
  ├─ T1.3 健康评分 API（2端点）
  ├─ T1.4 用量成本增强 API（3端点）
  └─ T1.5 实时会话增强 API（健康列支持）

Phase 2（前端 UI，可并行，依赖 Phase 1 对应后端）
  ├─ T2.1 分析中心仪表板（Vue 组件）
  ├─ T2.2 实时会话增强（SSE + 健康列）
  ├─ T2.3 全景图健康面板（Vue 组件）
  └─ T2.4 用量成本视图增强

Phase 3（集成与优化，依赖 Phase 2）
  ├─ T3.1 端到端集成测试
  ├─ T3.2 性能优化（索引/缓存）
  └─ T3.3 文档更新（API 文档 / 用户手册）
```

---

## Phase 0: 基础设施（串行，2 个子任务）

### T0.1 数据库 Schema 增强 ⭐ 优先级 P0

**目标**：创建 migration 355/356，增加健康评分相关列与索引

**交付物**：
- `sql/migrations/startup/355_session_analytics_indexes.sql`
- `sql/migrations/startup/356_session_health_columns.sql`

**详细任务**：
1. migration 355：创建分析查询优化索引
   ```sql
   CREATE INDEX idx_request_logs_gw_session_id ON request_logs (gw_session_id);
   CREATE INDEX idx_request_logs_tenant_ts ON request_logs (tenant_id, ts DESC);
   CREATE INDEX idx_request_logs_ts_day ON request_logs (date_trunc('day', ts));
   CREATE INDEX idx_session_summaries_health ON session_summaries (health_grade) WHERE health_grade IS NOT NULL;
   CREATE INDEX idx_session_summaries_outcome ON session_summaries (outcome) WHERE outcome IS NOT NULL;
   CREATE INDEX idx_session_summaries_health_score ON session_summaries (health_score DESC) WHERE health_score IS NOT NULL;
   ```

2. migration 356：session_summaries 增列
   ```sql
   ALTER TABLE session_summaries
       ADD COLUMN IF NOT EXISTS health_score INT,
       ADD COLUMN IF NOT EXISTS health_grade CHAR(1),
       ADD COLUMN IF NOT EXISTS outcome VARCHAR(20),
       ADD COLUMN IF NOT EXISTS last_health_at TIMESTAMPTZ;
   ```

**验收标准**：
- migrations 执行成功无错误
- 索引创建完成，EXPLAIN 显示查询使用索引
- health_score/health_grade/outcome 列可写入

**预计工时**：0.5 人日

---

### T0.2 健康评分核心逻辑 ⭐ 优先级 P0

**目标**：实现 `admin/session_health.go`，封装 penalty 计算逻辑

**交付物**：
- `admin/session_health.go`（核心计算函数）
- `admin/session_health_test.go`（单元测试）

**详细任务**：
1. 定义 HealthScoreConfig 结构体（可配置阈值）
2. 实现 `ComputeHealth(summary AnalyticsSessionSummary, config HealthScoreConfig) SessionHealth`
3. 实现 8 项扣分逻辑（见文档 11.4.1）
4. 实现等级映射（A-F）与 outcome 分类
5. 单元测试覆盖率 >80%

**关键代码结构**：
```go
type HealthScoreConfig struct {
    ErrorEndedPenalty      int
    AbandonedPenalty       int
    PerErrorPenalty        int
    // ... 其他配置项
}

type SessionHealth struct {
    HealthScore    int
    HealthGrade    string
    Outcome        string
    OutcomeReason  string
    Penalties      []PenaltyItem
}

func ComputeHealth(summary AnalyticsSessionSummary, config HealthScoreConfig) SessionHealth {
    score := 100
    penalties := []PenaltyItem{}
    // 8 项扣分逻辑
    return SessionHealth{...}
}
```

**验收标准**：
- 单元测试通过，边界情况覆盖（空会话/全成功/全失败）
- 与文档 11.4.3 的 5 个边界情况示例一致
- 可配置性验证（修改阈值后结果正确变化）

**预计工时**：1 人日

---

## Phase 1: 后端 API（并行，5 个子任务）

### T1.1 分析中心时间序列 API ⭐ 优先级 P0

**目标**：实现 4 个时间序列端点

**交付物**：
- `admin/session_analytics_timeseries.go`
- 4 个端点实现

**详细任务**：
1. `GET /api/admin/session-analytics/activity`
   - 请求参数：date_from/date_to/granularity/tenant_id/filters
   - 响应：series[]（按日期分组的会话数/请求数/成本/token）
   - 数据源：request_logs 按 date_trunc(granularity, ts) 聚合
   - 缺日补零逻辑

2. `GET /api/admin/session-analytics/cost-trend`
   - 响应：series[]（input_cost + output_cost 堆叠）
   - 趋势判定逻辑（前后 50% 对比）

3. `GET /api/admin/session-analytics/latency-trend`
   - 响应：series[]（p50/p90/p99 + stream_first_chunk_p50）
   - 使用 percentile_cont SQL 函数

4. `GET /api/admin/session-analytics/health-trend`（依赖 T0.2）
   - 响应：series[]（平均健康分 + 各等级占比）

**SQL 优化**：
- 利用 T0.1 创建的索引
- 时间范围强制 ≤90 天
- 结果缓存 5 分钟（内存 LRU）

**验收标准**：
- 4 端点返回正确格式数据
- P90 延迟 <1.5s（7 天范围）
- 租户隔离验证通过
- 缺日补零验证

**预计工时**：2 人日

---

### T1.2 分析中心分布归因 API ⭐ 优先级 P0

**目标**：实现 3 个分布分析端点

**交付物**：
- `admin/session_analytics_breakdown.go`
- 3 个端点实现

**详细任务**：
1. `GET /api/admin/session-analytics/model-breakdown`
   - 响应：by_model[] + by_provider[]（请求数/成本/token/延迟/错误率）
   - 长尾处理：占比 <2% 合并为"其他"

2. `GET /api/admin/session-analytics/session-shape`
   - 响应：request_count_buckets[]（quick/standard/deep/marathon）+ duration_buckets[]
   - 按文档 4.2.3 的分桶标准

3. `GET /api/admin/session-analytics/health-distribution`（依赖 T0.2）
   - 响应：grade_distribution（A-F）+ outcome_distribution + compliance_distribution + latency_buckets

**验收标准**：
- 分桶边界正确（如 quick: 1-5 请求）
- 长尾合并逻辑验证
- 空数据返回空数组（不报错）

**预计工时**：1.5 人日

---

### T1.3 健康评分 API ⭐ 优先级 P1

**目标**：实现健康评分写入与查询端点

**交付物**：
- `admin/session_health_api.go`
- 后台 worker：`bg/session_health_worker.go`

**详细任务**：
1. `GET /api/admin/sessions/<id>/health`
   - 响应：SessionHealth 完整结构（含 penalties）
   - 若未计算则实时计算并写入

2. `POST /api/admin/sessions/<id>/recompute-health`（管理员）
   - 强制重算健康分
   - 审计日志记录

3. 后台 worker（每小时）
   - 扫描 `last_request_at < now-1h AND health_score IS NULL`
   - 批量计算并写入
   - Prometheus 指标：session_health_computed_total

4. 会话停止时写入（集成到 session_state_snapshots 写入逻辑）

**验收标准**：
- 单会话健康详情返回正确
- worker 定时执行验证
- 会话停止时健康分已写入

**预计工时**：1.5 人日

---

### T1.4 用量成本增强 API ⭐ 优先级 P1

**目标**：实现成本归因与同比环比端点

**交付物**：
- `admin/usage_enhanced.go`

**详细任务**：
1. `GET /api/admin/usage/cost-trend?group_by=model|provider|intent`
   - 响应：按指定维度分组的成本趋势
   - 支持多维下钻

2. `GET /api/admin/usage/period-compare?current=2026-07&previous=2026-06`
   - 响应：current/previous/change_pct/change_abs/by_dimension
   - 环比/同比逻辑（等长周期）

3. `GET /api/admin/usage/cache-economics`
   - 响应：cache_hit_ratio/dollars_saved/dollars_spent/effective_cost_ratio
   - 计算公式见文档 4.6.4

**验收标准**：
- 同比环比计算正确（手工验证样本）
- 缓存命中率计算正确
- 支持按意图/工作类型归因

**预计工时**：1.5 人日

---

### T1.5 实时会话增强 API ⭐ 优先级 P1

**目标**：实时会话列表支持健康列排序与过滤

**交付物**：
- `admin/session_state_handlers.go`（扩展现有）

**详细任务**：
1. sessionListItem 增加 health_grade/health_score 字段
2. 列表查询关联 session_summaries.health_score
3. 支持 ?sort=health 排序
4. 支持 ?health_grade=D,F 过滤
5. 默认排序调整：status(error>waiting>active) + health_score DESC

**验收标准**：
- 健康列正确显示
- 按健康分排序验证
- D/F 会话置顶验证

**预计工时**：0.5 人日

---

## Phase 2: 前端 UI（并行，4 个子任务）

### T2.1 分析中心仪表板 ⭐ 优先级 P0

**目标**：创建完整的分析中心前端视图

**交付物**：
- `web/src/views/admin/SessionAnalyticsDashboard.vue`
- `web/src/composables/useChart.ts`（Chart.js 封装）
- 12 个子组件

**详细任务**：
1. 页面布局（DashboardModuleGate + FilterBar + StatsRow + Grid）
2. 7 张 KPI 卡片（含环比副指标）
3. 4 个时间序列图表（活动/成本/延迟/健康）
4. 3 个分布图表（模型/形态/健康）
5. 热门会话表格（可点击跳转全景图）
6. 统一过滤器（sessionStorage 持久化）

**技术选型**：
- Chart.js（项目已有依赖）
- 组合式 API（Vue 3）
- 防抖（useDebounceFn，500ms）

**验收标准**：
- 所有图表正确渲染
- 过滤器联动刷新
- 点击图表元素下钻到会话列表
- 刷新后过滤器状态保留

**预计工时**：4 人日

---

### T2.2 实时会话增强 ⭐ 优先级 P1

**目标**：实时会话列表 SSE 消费 + 健康列

**交付物**：
- `web/src/views/admin/SessionManagement.vue`（扩展）

**详细任务**：
1. 列表增加健康等级列（A-F 徽标带颜色）
2. EventSource 消费 `/api/admin/live-stream`
3. SSE 事件处理（session.error → 红色高亮 + 顶部告警）
4. 局部刷新单行（不全量重载）
5. 僵尸会话标识（>1h 无活动灰显）

**验收标准**：
- SSE 连接成功，实时推送生效
- 健康列正确显示徽标
- 异常会话红色高亮
- 局部刷新 <200ms

**预计工时**：1.5 人日

---

### T2.3 全景图健康面板 ⭐ 优先级 P1

**目标**：全景图增加健康面板，支持诊断导航

**交付物**：
- `web/src/components/session/HealthPanel.vue`

**详细任务**：
1. 健康面板组件（健康分/等级/结果/扣分明细）
2. penalties 列表渲染（每项可点击）
3. 点击跳转逻辑（高延迟→时间线/模型切换→模型切换面板）
4. 空数据态（健康分未计算显示"建设中"）

**验收标准**：
- 健康面板正确渲染
- 扣分项点击跳转验证
- 与文档 4.3.3 的 ASCII 原型一致

**预计工时**：1 人日

---

### T2.4 用量成本视图增强 ⭐ 优先级 P2

**目标**：用量成本页面增加归因与对比视图

**交付物**：
- `web/src/views/admin/UsageCost.vue`（扩展）

**详细任务**：
1. 成本归因切换（按模型/提供商/意图）
2. 同比环比对比卡片
3. 缓存经济学面板
4. 成本趋势图（可按维度分组）

**验收标准**：
- 归因切换正确显示
- 同比环比数值正确
- 缓存节省计算正确

**预计工时**：2 人日

---

## Phase 3: 集成与优化（串行，3 个子任务）

### T3.1 端到端集成测试 ⭐ 优先级 P1

**目标**：验证 5 个主线场景端到端可走通

**交付物**：
- `tests/e2e/session_management_scenarios_test.go`

**详细任务**：
按文档 3.2 的 5 个场景编写集成测试：
1. 场景 1：发现并处置高成本异常会话
2. 场景 2：合规违规会话复盘
3. 场景 3：基于优化建议的成本优化闭环
4. 场景 4：实时会话监控与远程干预
5. 场景 5：用量成本对账与同比环比

**验收标准**：
- 5 个场景测试全部通过
- 覆盖跨模块联动（分析中心→全景图→实时会话）

**预计工时**：2 人日

---

### T3.2 性能优化 ⭐ 优先级 P2

**目标**：确保 P90 延迟达标

**交付物**：
- 性能测试报告
- 优化代码

**详细任务**：
1. 分析端点压测（90 天范围，并发 10）
2. 慢查询识别与优化（EXPLAIN ANALYZE）
3. 结果缓存实现（内存 LRU，5min TTL）
4. 前端图表渲染优化（虚拟滚动/懒加载）

**验收标准**：
- 时间序列端点 P90 <1.5s（7 天）/<5s（90 天）
- KPI 端点 P90 <500ms
- 全景图加载 P90 <1.5s

**预计工时**：1.5 人日

---

### T3.3 文档更新 ⭐ 优先级 P2

**目标**：更新 API 文档与用户手册

**交付物**：
- `docs/api/session-analytics.md`（OpenAPI）
- `docs/user-guide/session-management.md`

**详细任务**：
1. 14 个新增端点的 OpenAPI 规格
2. 用户手册（含截图）：如何使用分析中心/全景图/健康智能
3. 运维手册：健康分计算逻辑/troubleshooting

**预计工时**：1 人日

---

## 并行执行方案

### 阶段 1：Phase 0（串行，必须先完成）
```bash
# 单个代理顺序执行
agent1: T0.1 (0.5d) → T0.2 (1d)
总耗时：1.5 天
```

### 阶段 2：Phase 1（5 个代理并行）
```bash
agent2: T1.1 (2d)
agent3: T1.2 (1.5d)
agent4: T1.3 (1.5d)
agent5: T1.4 (1.5d)
agent6: T1.5 (0.5d)
总耗时：max(2, 1.5, 1.5, 1.5, 0.5) = 2 天（并行）
```

### 阶段 3：Phase 2（4 个代理并行）
```bash
agent7: T2.1 (4d)
agent8: T2.2 (1.5d)
agent9: T2.3 (1d)
agent10: T2.4 (2d)
总耗时：max(4, 1.5, 1, 2) = 4 天（并行）
```

### 阶段 4：Phase 3（串行或并行）
```bash
agent11: T3.1 (2d)
agent12: T3.2 (1.5d) // 可与 T3.1 并行
agent13: T3.3 (1d)
总耗时：max(2, 1.5) + 1 = 3 天
```

---

## 总工期估算

| 模式 | Phase 0 | Phase 1 | Phase 2 | Phase 3 | 总计 |
|------|---------|---------|---------|---------|------|
| **串行**（1 人） | 1.5d | 7d | 8.5d | 4.5d | **21.5 天** |
| **并行**（13 代理）| 1.5d | 2d | 4d | 3d | **10.5 天** |

**并行加速比**：2.05x

---

## 风险与依赖

| 风险 | 概率 | 缓解 |
|------|:----:|------|
| Phase 0 阻塞所有后续 | 高 | 优先完成，充分测试 |
| 前后端接口不匹配 | 中 | Phase 1 完成后立即联调 |
| 健康分计算逻辑偏差 | 中 | T0.2 完成后灰度观察 |
| 性能不达标 | 中 | T3.2 提前介入，边开发边优化 |

---

## 下一步行动

**立即执行**：
1. ✅ 审批本规划
2. 启动 Phase 0（单代理）
   ```bash
   # 创建 agent 执行 T0.1 + T0.2
   ```
3. Phase 0 完成后立即启动 Phase 1（5 个并行代理）

**代理启动命令模板**（待执行）：
```bash
# Phase 0
zcode agent --task=T0.1+T0.2 --working-dir=. --output=phase0-report.md

# Phase 1（并行）
zcode agent --task=T1.1 --working-dir=. &
zcode agent --task=T1.2 --working-dir=. &
zcode agent --task=T1.3 --working-dir=. &
zcode agent --task=T1.4 --working-dir=. &
zcode agent --task=T1.5 --working-dir=. &
wait
```

---

*任务规划版本：v1.0*
*编制日期：2026-07-06*
*基于产品规划文档 v2.1*
