# Dashboard 统计页面数据来源分析文档

**生成时间**: 2026-11-19  
**分析范围**: https://llm.itestu.cn/dashboard?tab=stats 中所有统计元素  
**目标**: 整理数据来源，确认统计数据标准对齐

> **2026-11-21 统计口径对齐轮更新**（数据关系表单同步）：
>
> 1. **会话宇宙对齐**：stats tab 的 `/api/admin/dashboard/session-overview`
>    缺省套上与 `/admin/session-analytics` 物化视图（981/994）**同一**的会话宇宙
>    （sessions×session_dim join + owner 双源键 + is_probe +
>    session_analytics_is_probe；片段 `domains/analytics.SessionUniverseJoin/
>    SessionUniverseWhere`，迁移侧契约门对拍）。此前直读 session_summaries 时
>    ~80% 为探测/自检会话（981 实测）——这是「两边对不上」的第一大项。
>    `include_probes=1` 回历史全量宇宙；响应回传 `probe_scope`
>    （excluded/included/degraded_included，降级可见）。
> 2. **对齐指标行（两级更新策略并存）**：响应新增 `aligned` 块——读
>    `user_session_aggregates`/`model_usage_aggregates` 物化视图（15 分钟一刷，
>    缓存面），SQL 与会话分析总览页**同一实现**（`domains/analytics/aligned_source.go`
>    单一真源）⇒ 与 `/admin/session-analytics` 数字构造相等；面板自有腿
>    （session_summaries 直读）仍是即时查询面（新鲜值）。窗口=上海日历日
>    `[start,end]`（days 预设自动折算；显式 start_date/end_date 透传；响应
>    `window_mode` + `aligned.start_date/end_date` 披露）。物化视图未就绪时
>    `aligned_source=unavailable`，对齐行缺席（比假绿诚实）。
> 3. **top 数据跨页深链**：stats tab 模型行与 session-analytics 总览 top 模型行
>    → `/admin/usage-trends?model=X&start=S&end=E`（usage-trends 原生支持深链
>    参数）；对齐行「打开会话分析」→ `/admin/session-analytics?start_date=&end_date=`
>    （总览页现支持该 query 初始化区间）。
> 4. **request-logs 包含探测**：`/api/logs` 行集升级为「业务两臂 ∪ 探测臂」
>    （`db.SessionFamilyTurnsSourceWindowedAllSQL`；探测臂 =
>    session_turns_probe_hot 118 列投影 + usage_facts 费用按 request_id 挂回）⇒
>    包含探测的总请求/总token/总费用对齐 board `include_probes=1`（== 供应商对账）；
>    缺省业务 lane == 内部对账。响应回传 `traffic_lane`（business/all/probe_history）。

---

## 一、页面概览

Dashboard 页面包含多个 tab：
- **board**: 总览（系统现在能不能用、今天跑了多少、下一步该做什么）
- **stream**: 实时流
- **stats**: 会话与统计
- **selfcheck**: 系统自检

本文档主要分析 **stats 标签**（会话与统计）中的所有数据元素及其来源。

---

## 二、Stats Tab 主要统计栏目

### 2.1 核心 KPI 指标

根据 `SessionStatsPanel.vue` 和 `DashboardStatsRow.vue`，stats tab 显示以下核心指标：

| 指标名称 | 显示值 | 数据来源 | 数据库来源 | 备注 |
|---------|--------|---------|-----------|------|
| **总会话数** (totalSessions) | 数字 + 增长率 | `GET /api/admin/dashboard/sessions/overview` | `session_summaries` 视图 | overview.total_sessions |
| **活跃会话** (activeSessions) | 数字 | 同上 | 同上 | overview.active_sessions |
| **总成本** (totalCost) | USD 金额 + 增长率 | 同上 | 同上 | overview.cost_stats.total_cost_usd |
| **合规率** (complianceRate) | 百分比 | 同上 | 同上 | overview.compliance_stats.compliance_rate |
| **平均健康分** (avgHealthScore) | 分数 | 同上 | 同上 | overview.health_distribution.avg_score |
| **平均延迟** (avgLatency) | 毫秒 | 同上 | 同上 | overview.cost_stats.avg_latency_ms |
| **总请求数** (totalRequests) | 数字 | 同上 | 同上 | overview.cost_stats.total_requests |

**API 端点**:
- `GET /api/admin/dashboard/sessions/overview?days={n}`
- 数据口径: 最近 N 天（默认 7 天，可选 30 天）

### 2.2 次级 KPI 指标

| 指标名称 | 数据来源字段 | 说明 |
|---------|-------------|------|
| **24h 新增会话** | overview.new_sessions_24h | 最近 24 小时内新建的会话数 |
| **24h 关闭会话** | overview.closed_sessions_24h | 最近 24 小时内关闭的会话数 |
| **平均每会话成本** | overview.cost_stats.avg_cost_per_session | 总成本 / 总会话数 |
| **违规数** | overview.compliance_stats.violation | 合规检查中违规的会话数 |

### 2.3 趋势图表数据

**会话趋势图** (SessionTrendChart):
- **数据来源**: `GET /api/admin/dashboard/sessions/trend?days={n}`
- **数据库来源**: `session_summaries` 视图按日期聚合
- **展示维度**:
  - 每日新增会话 (new_sessions)
  - 每日活跃会话 (active_sessions)
  - 每日关闭会话 (closed_sessions)
  - 每日总成本 (total_cost)
  - 每日总请求数 (total_requests)

**健康分布图** (HealthGradeChart):
- **数据来源**: overview.health_distribution
- **展示**: 不同健康等级的会话分布

### 2.4 成本明细

| 指标项 | 数据字段 | 说明 |
|-------|---------|------|
| 总成本 | cost_stats.total_cost_usd | 周期内总花费（USD） |
| 输入成本 | cost_stats.input_cost_usd | 输入 token 成本 |
| 输出成本 | cost_stats.output_cost_usd | 输出 token 成本 |
| 平均每请求成本 | cost_stats.avg_cost_per_request | 总成本 / 总请求数 |
| 最高成本会话 | cost_stats.max_cost_session | 单个会话最高花费 |
| 成本增长率 | cost_stats.cost_growth_pct | 对比上一周期的增长百分比 |

### 2.5 合规明细

| 指标项 | 数据字段 | 说明 |
|-------|---------|------|
| 合规 | compliance_stats.compliant | 完全合规的会话数 |
| 警告 | compliance_stats.warning | 有警告但未违规的会话数 |
| 违规 | compliance_stats.violation | 违规的会话数 |
| Prompt Injection 检测 | compliance_stats.prompt_injection_detected | 检测到提示注入的次数 |
| PII 检测 | compliance_stats.pii_detected | 检测到个人身份信息的次数 |

### 2.6 排名列表

**Top 客户端** (top_clients):
- 数据字段: overview.top_clients
- 排序: 按请求数降序
- 显示: 客户端名称、请求数、成本

**Top 任务** (top_tasks):
- 数据字段: overview.top_tasks
- 排序: 按执行次数降序

**模型使用** (model_usage):
- 数据字段: overview.model_usage
- 显示: 模型名称、调用次数、成本、Token 用量

### 2.7 性能与错误信号

**Pipeline 延迟** (SessionStatsSignals):
- **数据来源**: `GET /api/admin/dashboard/sessions/performance?days={n}`
- **展示**: 各阶段延迟统计

**错误统计** (SessionStatsSignals):
- **数据来源**: `GET /api/admin/dashboard/sessions/errors?days={n}`
- **展示**: 错误类型、数量、比率

---

## 三、Board Tab 统计栏目（对照）

虽然本文档重点是 stats tab，但 board tab 也包含重要统计数据，作为对照：

### 3.1 Board Tab 核心指标

从当前页面显示的数据（来自浏览器截取）：

| 栏目 | 当前显示值 | 数据来源 | 口径说明 |
|------|-----------|---------|---------|
| **今日请求** | 4,426 | GET /api/admin/dashboard/board | 今日口径（按 UTC 日聚合） |
| **成功率** | 96.8% | 同上 | 成功请求 / 总请求 |
| **失败请求** | 140 笔 | 同上 | 今日失败的请求数 |
| **Token 用量** | 93.8M | 同上 | 今日消耗的总 Token 数 |
| **今日花费** | $81.92 | 同上 | 今日总成本（USD） |
| **启用密钥** | 120 / 255 | 同上 | 已启用 / 总数 |
| **在用模型** | 9 | 同上 | 今日有调用的模型数 |

### 3.2 近 7 日消耗趋势

- **API 端点**: `GET /api/admin/dashboard/board?days=7`
- **数据口径**: 与专家面看板同一口径，按 UTC 日聚合
- **展示维度**:
  - 每日请求数
  - 每日 Token 消耗
  - 每日成本（USD）
- **7 天合计**: 79,511 笔请求 · 4.0B tokens · $9,168.40

### 3.3 接入清单（API Keys）

**最近在用** 列表:
- 排序: 按最近一次使用时间
- 列: 客户端/密钥、状态、最近使用时间、累计请求数
- 口径: **累计口径**（该密钥自签发以来的全部请求）

**用得最多** 列表:
- 排序: 按累计请求数
- 列: 客户端/密钥、累计请求、花费/预算、状态
- 口径: **累计口径**

> ⚠️ **重要说明**: 当前没有按密钥的**当日统计口径**

### 3.4 上游健康

- 可用: 18 个
- 告警: 5 个
- 不可达: 5 个
- 未探活: 30 个

### 3.5 最常用的模型

- 排序: 按今日请求数
- 展示: 模型名称、今日请求数、今日花费
- 口径: **今日口径**

---

## 四、数据库层数据来源

### 4.1 核心数据表

| 表名 | 用途 | 关键字段 |
|-----|------|---------|
| `session_turns` | 会话轮次记录 | session_id, turn_number, created_at, status, cost_usd |
| `llm_requests` | LLM 请求记录 | id, session_id, turn_id, model, status, tokens_input, tokens_output, cost_usd |
| `api_keys` | API 密钥管理 | id, key_hash, status, created_at, last_used_at |
| `gateway_key_usage` | 密钥使用统计 | key_id, request_count, token_count, cost_usd |
| `error_analysis_queue` | 错误分析队列 | id, error_type, status, created_at |
| `error_analysis_results` | 错误分析结果 | id, queue_id, is_false_positive, confidence_score, analysis_duration_ms |

### 4.2 聚合视图

| 视图名 | 用途 | 定义位置 | 最新修订 |
|-------|------|---------|---------|
| `session_summaries` | 会话汇总统计 | migration 960+ | - |
| `v_error_analysis_stats` | 错误分析统计 | migration 971, 977, **980** | **980 对齐修复** |
| `daily_usage_summary` | 每日使用汇总 | migration 950+ | - |

### 4.3 PI 分析统计对齐（迁移 980）

**背景**（2026-11-19 批判审计 F1 发现）:
- 统计卡与列表一致性根修（提交 24d352f17）把 Go 侧 GetStats 改成「全量域 + 全部 5 状态」口径
- 但 977 订正后的 `v_error_analysis_stats` 视图仍是旧口径：只有 `analyzed_count`/`pending_count`，**没有 processing/failed/skipped**

**修复**（迁移 980）:
- 视图补充三列: `processing_count`, `failed_count`, `skipped_count`
- 口径对齐: 与 `error_collector.go GetStats` 逐项一致

**视图列定义**（980 最终形态）:
```sql
SELECT
    error_type,
    COUNT(DISTINCT queue_id) AS total_errors,        -- 全量域
    COUNT(DISTINCT queue_id) FILTER (WHERE status = 'pending') AS pending_count,
    COUNT(DISTINCT queue_id) FILTER (WHERE status = 'processing') AS processing_count,  -- 新增
    COUNT(DISTINCT queue_id) FILTER (WHERE status = 'analyzed') AS analyzed_count,
    COUNT(DISTINCT queue_id) FILTER (WHERE status = 'failed') AS failed_count,          -- 新增
    COUNT(DISTINCT queue_id) FILTER (WHERE status = 'skipped') AS skipped_count,        -- 新增
    COUNT(DISTINCT queue_id) FILTER (WHERE is_false_positive = TRUE) AS false_positive_count,
    AVG(confidence_score) AS avg_confidence,
    AVG(analysis_duration_ms) AS avg_analysis_time_ms,
    SUM(llm_tokens_used) AS total_tokens_used,
    MAX(created_at) AS last_error_at
FROM per_queue
GROUP BY error_type;
```

---

## 五、API 端点总览

### 5.1 Dashboard API

| 端点 | 方法 | 参数 | 返回数据 | 用途 |
|-----|------|------|---------|------|
| `/api/admin/dashboard/board` | GET | `days` (默认 7) | BoardPayload | 总览看板数据 |
| `/api/admin/dashboard/operational` | GET | - | BoardOperationalPayload | 后台任务、自检 |
| `/api/admin/dashboard/sessions/overview` | GET | `days` | SessionOverview | 会话总览 KPI |
| `/api/admin/dashboard/sessions/trend` | GET | `days` | TrendData | 会话趋势 |
| `/api/admin/dashboard/sessions/performance` | GET | `days` | PerformanceStats | 性能统计 |
| `/api/admin/dashboard/sessions/errors` | GET | `days` | ErrorStats | 错误统计 |

### 5.2 统计辅助 API

| 端点 | 方法 | 参数 | 返回数据 | 用途 |
|-----|------|------|---------|------|
| `/api/admin/usage-by-model` | GET | `days` | ModelUsage[] | 按模型的使用统计 |
| `/api/admin/hot-api-keys` | GET | `days` | HotApiKeyEntry[] | 热门 API Keys |

---

## 六、统计标准对齐分析

### 6.1 口径一致性检查

| 统计项 | Board Tab 口径 | Stats Tab 口径 | 是否对齐 | 备注 |
|-------|---------------|---------------|---------|------|
| 请求数统计 | 今日口径 | 可选 7/30 天 | ⚠️ 时间范围不同 | 两者服务不同用途 |
| 成本统计 | 今日口径 | 可选 7/30 天 | ⚠️ 时间范围不同 | 同上 |
| Token 统计 | 今日口径 | 可选 7/30 天 | ⚠️ 时间范围不同 | 同上 |
| API Key 统计 | **累计口径** | - | ⚠️ 无当日口径 | 已知限制 |
| 错误分析统计 | - | **全量域 + 5 状态** | ✅ **已对齐** | 迁移 980 修复 |

### 6.2 已修复的对齐问题

**问题**: 统计卡↔列表「三重不一致」（2026-11-19 修复前）
- 状态盲区: 只显示 pending/analyzed，不显示 processing/failed/skipped
- 7 天域错位: 统计卡用 7 天过滤，列表用全量
- 死零: 平均耗时硬编码为 0

**修复结果**（迁移 980 + 提交 24d352f17）:
- ✅ 视图补全 5 状态列: processing_count, failed_count, skipped_count
- ✅ GetStats 改用全量域（无时间过滤）
- ✅ 平均耗时改为从结果表真实聚合

**验证结果**（三台部署验证通过）:
- 211 生产实况: 修复前总错误=0（错误显示），修复后=2（正确）
- 修复前失败不可见，修复后失败=2（与列表一致）
- 修复前平均耗时=0ms（死零），修复后=88760ms（真实值）

### 6.3 已登记的口径差异（非缺陷）

| 项目 | 差异说明 | 级别 | 处置 |
|-----|---------|------|------|
| **F2**: GetStats 三段查询无事务一致性 | 队列聚合/结果聚合/by_type 三段查询，并发写入可能瞬间不一致 | P3 | 管理面可容忍 |
| **F3**: 误报率分母差异 | 误报率分母 = results_total（结果域），与「已分析」卡（队列状态域）不同源 | 口径披露 | 有意设计，结果域才是正确分母 |
| 视图消费面 | v_error_analysis_stats 仍零运行时消费者 | P3 | 未来接线时复用 sqlguard 门 |

---

## 七、数据流图

```
┌─────────────────────────────────────────────────────────────┐
│                        用户请求                              │
└──────────────────────────┬──────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────┐
│                    Gateway 核心逻辑                          │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐   │
│  │ 路由分发  │─▶│ 模型调用  │─▶│ 成本计算  │─▶│ 响应返回  │   │
│  └──────────┘  └──────────┘  └──────────┘  └──────────┘   │
└──────────────┬────────────────┬────────────────┬───────────┘
               │                │                │
               ▼                ▼                ▼
    ┌──────────────┐  ┌──────────────┐  ┌──────────────┐
    │ llm_requests │  │session_turns │  │ api_keys     │
    │    (写入)     │  │   (写入)     │  │  (更新)      │
    └──────┬───────┘  └──────┬───────┘  └──────┬───────┘
           │                  │                  │
           │                  │                  │
           ▼                  ▼                  ▼
    ┌──────────────────────────────────────────────────┐
    │         后台聚合任务（定时/触发）                   │
    │  ┌────────────┐  ┌────────────┐  ┌────────────┐ │
    │  │session_    │  │daily_usage │  │gateway_key │ │
    │  │summaries   │  │_summary    │  │_usage      │ │
    │  └────────────┘  └────────────┘  └────────────┘ │
    └──────────────────────────────────────────────────┘
                           │
                           ▼
    ┌──────────────────────────────────────────────────┐
    │              Dashboard API 层                     │
    │  ┌────────────────┐  ┌────────────────┐         │
    │  │ GET /board     │  │ GET /sessions  │         │
    │  │ GET /operational│  │    /overview   │         │
    │  └────────────────┘  └────────────────┘         │
    └──────────────┬───────────────────────────────────┘
                   │
                   ▼
    ┌──────────────────────────────────────────────────┐
    │           前端 Dashboard 页面                     │
    │  ┌──────────┐  ┌──────────┐  ┌──────────┐      │
    │  │ Board Tab │  │ Stats Tab│  │Stream Tab│      │
    │  └──────────┘  └──────────┘  └──────────┘      │
    └──────────────────────────────────────────────────┘
```

---

## 八、标准对齐验证清单

### 8.1 当前已对齐项 ✅

- [x] 错误分析统计视图 (`v_error_analysis_stats`) 包含完整 5 状态
- [x] GetStats API 与视图口径一致（迁移 980）
- [x] 统计卡与列表数据一致（提交 24d352f17）
- [x] 平均耗时使用真实聚合值（非硬编码）
- [x] Board API 按 UTC 日聚合（与专家面一致）

### 8.2 需要注意的差异项 ⚠️

- [ ] API Key 统计仅有累计口径，**无当日口径**（已知限制）
- [ ] Board Tab（今日口径）vs Stats Tab（7/30天口径）时间范围不同（设计如此）
- [ ] GetStats 三段查询无事务一致性（管理面可容忍）
- [ ] 误报率分母使用结果域而非队列域（有意设计）

### 8.3 数据质量保障机制

1. **数据库迁移门** (`sqlguard`):
   - TestMigration980ViewAlignmentDelivered 六断言
   - 变异负控测试（摘字段⇒红，还原⇒绿）

2. **前端类型守卫**:
   - TypeScript 严格模式
   - API 响应类型定义（BoardPayload, SessionOverview 等）

3. **后端测试**:
   - GetStats 钉测 ×2
   - 迁移测试覆盖

4. **部署验证**:
   - 三台生产环境（211/154/245）部署后终验
   - 5 断言/台，ALL_PASS ×3

---

## 九、总结与建议

### 9.1 数据来源完整性

✅ **已完成**: 所有显示在 Dashboard Stats Tab 的数据都有明确的：
1. API 端点定义
2. 数据库表/视图来源
3. 字段映射关系
4. 口径说明

### 9.2 统计标准对齐度

✅ **高度对齐**: 经过迁移 980 修复后，核心统计口径已对齐：
- 错误分析统计: 视图 ↔ API ↔ 前端 三层一致
- 成本统计: 计算逻辑统一
- 会话统计: 聚合口径明确

### 9.3 建议改进项（可选）

1. **API Key 当日统计**: 考虑增加按密钥的当日请求数/成本统计（当前仅累计）
2. **视图消费验证**: v_error_analysis_stats 补齐后可增加运行时消费者或 nightly 对拍探针
3. **事务一致性**: 若 GetStats 升级为对账级需求，可包单事务处理三段查询
4. **口径文档化**: 在前端 tooltip 补充口径说明（如误报率分母为结果域）

---

## 十、附录

### A. 关键文件清单

**前端**:
- `web/src/views/DashboardView.vue` - 主入口
- `web/src/views/DashboardViewV2.vue` - V2 实现
- `web/src/components/SessionStatsPanel.vue` - Stats Tab 主面板
- `web/src/components/analytics/DashboardStatsRow.vue` - KPI 行
- `web/src/components/analytics/SessionTrendChart.vue` - 趋势图
- `web/src/composables/useDashboard.ts` - Dashboard 数据钩子

**后端**:
- `domains/promptinjection/error_collector.go` - GetStats 实现
- API handlers (待补充具体路径)

**数据库**:
- `sql/migrations/startup/980_pi_analysis_stats_view_alignment.sql` - 最新视图定义
- `sql/migrations/startup/977_*.sql` - 视图初始定义
- `sql/migrations/startup/971_*.sql` - 视图创建

**审计文档**:
- `docs/audit/2026-11-19-PI分析统计一致性批判审计-报告.html` - 对齐审计报告
- `docs/12小时内修订审计-2026-11-19-0800.md` - 修订记录

### B. 验证脚本

```bash
# 数据库视图验证
psql -d gateway -c "SELECT * FROM v_error_analysis_stats;"

# API 端点测试
curl -H "Authorization: Bearer $TOKEN" \
  "https://llm.itestu.cn/api/admin/dashboard/sessions/overview?days=7"

# 前端测试
cd web && npm run test -- src/components/SessionStatsPanel.vue
```

---

**文档维护**: 当添加新的统计栏目或修改数据口径时，请同步更新本文档。
