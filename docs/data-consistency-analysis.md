# 数据统计口径一致性分析报告

## 执行摘要

系统当前存在**三条独立的数据管道**，使用不同的聚合层级、时间过滤逻辑和排除规则，导致跨页面的数据统计不一致。这些差异**部分是有意设计**（为了数据质量），**部分是历史遗留**（时区和时间窗口不统一）。

### 关键发现

1. **session_summaries 有意排除内部请求**：会话标题/摘要生成等内部回环流量不计入 request_count（约占 10%），但这些请求在 usage_facts 中存在
2. **时间过滤逻辑不一致**：
   - 会话分析：`first_request_at >= CURRENT_DATE`（会话开始时间，服务器本地日期）
   - 用户用量：`occurred_at >= now() - interval`（请求发生时间，滑动窗口）
   - 对账报表：UTC 日边界，但物理分区是上海日历日
3. **聚合粒度不同**：请求级（usage_facts）vs 会话级（session_summaries）vs 日级（report_snapshots）

---

## 一、数据管道架构

### 1.1 usage_facts（事实源 - 请求级）

**数据源**: 单请求事实表，每个 LLM 请求一条记录（可能多条共享 request_id）

**维护方式**: 请求完成时实时写入

**用途**:
- 对账报表（`report_snapshots` 的上游）
- 用户用量统计（`/api/admin/users/usage-summary`）
- 成本核算

**关键字段**:
- `occurred_at`: 请求发生时间（UTC）
- `status`: success / failure / rate_limited
- `total_tokens`, `credits_charged`, `cost_amount`
- `api_key_id` → `api_keys.owner_user`（用户关联）

**包含范围**: **全部请求**，包括：
- 用户请求
- 内部回环请求（标题生成、摘要生成）
- 探针合成会话（如配置）

---

### 1.2 session_summaries（会话级聚合）

**数据源**: 由 `SessionSummaryDimWriter` 维护的增量聚合表

**维护方式**: Hook 驱动的增量 UPSERT（每个终态请求触发一次更新）

**用途**:
- 会话分析页面（`/api/admin/session-analytics/`）
- 会话管理列表
- Admin 会话详情

**关键字段**:
- `session_key`: 会话 ID
- `first_request_at`, `last_request_at`: 会话时间边界
- `request_count`, `success_count`, `error_count`: 请求计数
- `total_cost_usd`, `total_tokens`: 成本和 token 统计

**有意排除**（来源: `session_summary_dim.go:36-42`）:
1. **in_progress 状态**：只在终态 entry 上更新
2. **内部回环请求**：会话标题/摘要自动生成（历史数据显示约占 10%）
3. **探针合成会话**：与 turns 表同口径过滤

**关键差异**: 这是**唯一不从 usage_facts 聚合的统计表**，由独立的 hook 管道维护

---

### 1.3 report_snapshots（日级快照 - 多维度）

**数据源**: `reportrollup.RollupDay()` 每日从 usage_facts 聚合

**维护方式**: 定时 worker 按 UTC 日批量 UPSERT

**用途**:
- 对账报表（`ReconciliationReport`）
- 租户仪表板
- MaaS 用量统计

**聚合维度** (scope):
- `provider_model`: 供应商-模型组合
- `tenant`: 租户
- `user`: 最终用户
- `person`: 人员（跨租户）
- `tenant_grain`: 租户最细粒度（含 error_kind）
- `user_grain`: 用户最细粒度（含 error_kind）

**时间语义**:
- `report_date`: UTC 日（[00:00, +24h)）
- **物理分区边界**: 上海日历日（与 UTC 日不对齐）
- **口径注记** (`rollup.go:156-159`): "一个 UTC 日窗横跨两个物理分区"

---

## 二、API 端点统计口径对比表

| API 路径 | 数据源 | 时间过滤 | 排除规则 | 聚合级别 | 处理函数 |
|---------|--------|---------|---------|---------|---------|
| `/api/admin/session-analytics/stats` | `session_summaries` | `first_request_at >= CURRENT_DATE` | 内部回环、探针、in_progress | 会话级 | `HandleSessionAnalyticsStats` |
| `/api/admin/session-analytics/clients` | `session_summaries` + `session_dim` | 同上 + owner 过滤 | 同上 | 会话级 | `handleClientAnalyticsList` |
| `/api/admin/users/usage-summary` | `usage_facts` + `api_keys` | `occurred_at >= now() - N days` | 仅排除空 owner_user | 请求级 → 用户聚合 | `handleUserUsageSummary` |
| `/api/admin/users/{id}/stats` | `usage_facts` + `api_keys` | `occurred_at >= now() - N days` | 同上 | 请求级 → 用户聚合 | `handleUserStats` |
| `/api/admin/shadow-reconciliation` | ASM 内部报表（转发） | （ASM 侧定义） | （ASM 侧定义） | Turn 级 | `handleShadowReconciliation` |
| `/api/admin/session-audit/stats` | `session_summaries` | 可配置时间范围 | 内部回环、探针、in_progress | 会话级 | `handleSessionAuditStats` |

---

## 三、具体不一致场景分析

### 3.1 场景一：会话分析 vs 用户用量（请求数不一致）

**表现**: 
- 会话分析页面显示「今日 1000 个请求」
- 用户用量页面显示「过去 24 小时 1100 个请求」

**根本原因**:

1. **时间窗口差异**:
   ```sql
   -- 会话分析（session_analytics_handler.go:405）
   WHERE ss.first_request_at >= CURRENT_DATE
   
   -- 用户用量（user_usage_stats.go:164）
   WHERE f.occurred_at >= now() - ($1 * INTERVAL '1 day')
   ```
   - 会话分析：服务器本地日期的 00:00 开始
   - 用户用量：当前时刻往前推 24 小时（滑动窗口）

2. **内部回环请求**:
   - `usage_facts` 包含标题生成、摘要生成等内部请求（约 10%）
   - `session_summaries` 排除这些内部请求

3. **聚合粒度**:
   - 会话分析：按 `session_key` 聚合，一个会话多个请求算作该会话的 `request_count`
   - 用户用量：直接 COUNT(*) usage_facts 行数

**预期差异**: 用户用量 ≈ 会话分析 × 1.1（内部回环约 10%）

---

### 3.2 场景二：对账报表 vs 仪表板（时区偏移）

**表现**:
- 对账报表「11 月 18 日」显示 5000 请求
- 仪表板「11 月 18 日」显示 4800 请求

**根本原因**:

1. **UTC vs 本地日边界**:
   - `report_snapshots.report_date`: UTC 日（[00:00 UTC, 24:00 UTC)）
   - 仪表板可能使用 `CURRENT_DATE`（服务器本地时间，中国 = UTC+8）
   
2. **分区边界不对齐** (`rollup.go:156-159`):
   ```
   R69 口径注记：本函数日桶是 UTC 日，而 usage_facts 的物理日分区边界
   （750/751）是上海日历日——一个 UTC 日窗横跨两个物理分区
   ```
   - UTC 日 2026-11-18 = [2026-11-18 00:00 UTC, 2026-11-19 00:00 UTC)
   - 对应上海时间 = [2026-11-18 08:00 CST, 2026-11-19 08:00 CST)
   - 跨越两个物理分区（11-18 和 11-19）

---

### 3.3 场景三：会话总数不匹配

**表现**:
- Admin 会话列表显示 100 个会话
- 对账报表「会话数」显示 110 个

**根本原因**:

1. **探针会话过滤**:
   - `session_summaries`: 排除探针合成会话（`IsProbeSyntheticSession`）
   - `usage_facts`: 包含探针会话（除非特定过滤）

2. **会话定义差异**:
   - `session_summaries`: 每个 `session_key` 一行
   - 对账报表: 可能按 `gw_session_id` 或 request 侧维度统计

---

## 四、时间过滤逻辑详细对比

### 4.1 会话分析（Session Analytics）

**代码位置**: `admin/session_analytics_handler.go:405`

```sql
WHERE ss.first_request_at >= CURRENT_DATE
```

**语义**:
- 过滤条件：会话的**第一个请求**发生在今日 00:00 之后
- 时区：数据库服务器本地时区（通常是 CST = UTC+8）
- 窗口类型：固定日边界（每日 00:00 重置）

**特点**:
- 只看会话开始时间，不管后续请求
- 一个会话只被计入它开始的那一天
- 跨日会话的后续请求不影响计数

---

### 4.2 用户用量（User Usage）

**代码位置**: `admin/user_usage_stats.go:164`

```sql
WHERE f.occurred_at >= now() - ($1 * INTERVAL '1 day')
  AND COALESCE(k.owner_user, '') <> ''
```

**语义**:
- 过滤条件：每个**请求**的发生时间在最近 N 天内
- 时区：UTC（usage_facts.occurred_at 是 UTC）
- 窗口类型：滑动窗口（相对于查询时刻）

**特点**:
- 请求级统计，不按会话分组
- 同一个会话的不同请求可能落在不同的日期窗口
- 实时统计，每次查询窗口不同

---

### 4.3 对账报表（Report Rollup）

**代码位置**: `domains/reportrollup/rollup.go:162-163`

```go
start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
end := start.Add(24 * time.Hour)
```

**SQL**:
```sql
WHERE occurred_at >= $1 AND occurred_at < $2
```

**语义**:
- 过滤条件：UTC 日边界 [00:00 UTC, 24:00 UTC)
- 时区：强制 UTC
- 窗口类型：固定 UTC 日

**物理执行** (`rollup.go:156-159`):
```
本函数日桶是 UTC 日，而 usage_facts 的物理日分区边界
（750/751）是上海日历日——一个 UTC 日窗横跨两个物理分区，partition
pruning 仍生效（扫 2 个分区而非 1 个），数据正确性无损。
```

**特点**:
- UTC 日标准化，便于跨时区对账
- 与数据库服务器本地时间可能相差 8 小时
- 分区扫描效率：每个 UTC 日查询需要扫描 2 个物理分区

---

## 五、数据口径统一建议

### 5.1 短期修正（向后兼容）

#### 选项 A：标准化时间窗口（推荐）

**目标**: 所有统计 API 使用相同的时间过滤逻辑

**修改**:
1. **会话分析** → 改用 UTC 日边界：
   ```sql
   -- 当前
   WHERE ss.first_request_at >= CURRENT_DATE
   
   -- 修改后
   WHERE ss.first_request_at >= date_trunc('day', now() AT TIME ZONE 'UTC')
   ```

2. **用户用量** → 提供「日期模式」和「滑动窗口模式」两种选项：
   ```sql
   -- 日期模式（与会话分析对齐）
   WHERE f.occurred_at >= date_trunc('day', $1::timestamptz AT TIME ZONE 'UTC')
     AND f.occurred_at < date_trunc('day', $1::timestamptz AT TIME ZONE 'UTC') + interval '1 day'
   
   -- 滑动窗口模式（保持现有行为）
   WHERE f.occurred_at >= now() - ($1 * INTERVAL '1 day')
   ```

**影响**:
- 会话分析的「今日」定义变化（本地 00:00 → UTC 00:00）
- 中国用户感知：「今日」从早上 8 点开始计算（而非午夜）
- 需要前端 UI 说明时区语义

---

#### 选项 B：文档化差异（最小改动）

**目标**: 保持现有逻辑，在 API 文档和前端 UI 中明确说明差异

**实施**:
1. 在每个统计 API 的文档中注明：
   - 时间窗口定义（固定日边界 vs 滑动窗口）
   - 时区（UTC vs 本地）
   - 排除规则（是否包含内部请求）

2. 前端 UI 增加提示：
   ```
   会话分析：统计今日（服务器本地时间 00:00 起）新建会话
   用户用量：统计最近 24 小时所有请求（含内部请求）
   对账报表：按 UTC 日汇总（UTC 00:00-24:00）
   ```

3. 创建「统计口径说明」页面，解释预期差异：
   ```
   为什么会话数和请求数不一致？
   - 会话分析按会话聚合，一个会话算一次
   - 用户用量按请求计数，一个会话可能有多个请求
   - 会话分析排除标题生成等内部请求（约 10%）
   ```

**优点**:
- 零代码修改，无回归风险
- 保持现有 API 行为

**缺点**:
- 用户需要理解复杂的统计差异
- 长期维护负担

---

### 5.2 中期优化（数据层统一）

#### 方案：usage_facts 增加标记列

**目标**: 在 usage_facts 中显式标记请求类型，让所有统计 API 使用统一的过滤逻辑

**实施**:

1. **迁移**: 增加字段
   ```sql
   ALTER TABLE usage_facts 
   ADD COLUMN is_internal_loopback BOOLEAN DEFAULT FALSE,
   ADD COLUMN is_probe_synthetic BOOLEAN DEFAULT FALSE;
   
   CREATE INDEX idx_usage_facts_filters 
   ON usage_facts(occurred_at, is_internal_loopback, is_probe_synthetic)
   WHERE NOT is_internal_loopback AND NOT is_probe_synthetic;
   ```

2. **写入侧**: Hook 写入时设置标记
   ```go
   entry.IsInternalLoopback = detectInternalLoopback(req)
   entry.IsProbeSynthetic = IsProbeSyntheticSession(sessionID)
   ```

3. **读取侧**: 所有统计 API 使用相同的 WHERE 子句
   ```sql
   -- 「用户可见请求」标准过滤器
   WHERE NOT COALESCE(is_internal_loopback, false)
     AND NOT COALESCE(is_probe_synthetic, false)
   ```

4. **对账报表**: 提供「全量」和「用户可见」两种口径
   ```sql
   -- 全量口径（财务对账）
   SELECT COUNT(*) FROM usage_facts WHERE ...
   
   -- 用户可见口径（业务统计）
   SELECT COUNT(*) FROM usage_facts 
   WHERE NOT is_internal_loopback AND NOT is_probe_synthetic AND ...
   ```

**优点**:
- 数据源统一，口径明确
- 灵活：可选择包含/排除特定类型
- 索引优化：部分索引加速常用查询

**缺点**:
- 需要迁移回填历史数据
- 增加存储和写入开销（约 2 字节/行）

---

### 5.3 长期架构（统一聚合层）

#### 方案：构建统一的 OLAP 聚合表

**目标**: 引入独立的 OLAP 聚合层，作为所有统计 API 的唯一数据源

**架构**:

```
usage_facts (OLTP 事实表)
    ↓ 
    ↓ (实时流 / 定时批处理)
    ↓
unified_analytics_cubes (OLAP 聚合表)
    ├── by_session_day (会话-日粒度)
    ├── by_user_day (用户-日粒度)
    ├── by_tenant_day (租户-日粒度)
    └── by_provider_model_day (供应商-模型-日粒度)
    ↓
    ↓ (统一查询接口)
    ↓
所有统计 API
```

**实施**:

1. **创建聚合表**（Timescale hypertable 或 ClickHouse）:
   ```sql
   CREATE TABLE unified_analytics_cubes (
       report_date DATE NOT NULL,
       scope TEXT NOT NULL,  -- 'session' / 'user' / 'tenant' / 'provider_model'
       scope_key TEXT NOT NULL,
       tenant_id TEXT,
       
       -- 计数指标
       request_count_total BIGINT,
       request_count_user_visible BIGINT,  -- 排除内部/探针
       success_count BIGINT,
       error_count BIGINT,
       
       -- 成本指标
       total_cost_usd NUMERIC(20,6),
       total_credits BIGINT,
       
       -- Token 指标
       total_tokens BIGINT,
       prompt_tokens BIGINT,
       completion_tokens BIGINT,
       
       -- 时间指标
       avg_latency_ms INT,
       p50_latency_ms INT,
       p95_latency_ms INT,
       p99_latency_ms INT,
       
       -- 元数据
       updated_at TIMESTAMPTZ DEFAULT NOW(),
       
       PRIMARY KEY (report_date, scope, scope_key)
   );
   ```

2. **定时聚合任务**（每日 02:00 UTC）:
   ```go
   func AggregateAnalyticsCubes(ctx context.Context, day time.Time) error {
       // 从 usage_facts 聚合各个维度
       aggregateBySession(ctx, day)
       aggregateByUser(ctx, day)
       aggregateByTenant(ctx, day)
       aggregateByProviderModel(ctx, day)
   }
   ```

3. **统一查询层**:
   ```go
   type AnalyticsQuery struct {
       Scope      string    // 'session' / 'user' / 'tenant'
       StartDate  time.Time
       EndDate    time.Time
       IncludeInternal bool  // 是否包含内部请求
       Timezone   string    // 'UTC' / 'Asia/Shanghai'
   }
   
   func QueryAnalyticsCubes(ctx context.Context, q AnalyticsQuery) ([]CubeRow, error)
   ```

4. **迁移路径**:
   - Phase 1: 建表 + 批处理聚合，与现有系统并行运行
   - Phase 2: 新 API 使用聚合表，旧 API 保持不变
   - Phase 3: 逐步迁移旧 API 到聚合表
   - Phase 4: 下线 session_summaries 和 report_snapshots（保留归档）

**优点**:
- 单一真相源（Single Source of Truth）
- 查询性能大幅提升（预聚合）
- 支持多种时区和口径
- 便于增加新维度

**缺点**:
- 大规模架构改造
- 需要额外存储（约 usage_facts 的 5-10%）
- 实时性降低（日级延迟，可通过实时流缓解）

---

## 六、推荐实施计划

### 阶段 1：立即执行（1 周）

1. **文档化现状**（本文档）
   - ✅ 已完成：统计口径对比表
   - 待完成：生成前端可读的「统计说明」页面

2. **增加调试端点**
   ```go
   // GET /api/admin/debug/stats-comparison?date=2026-11-18
   // 返回同一时间窗口下各个统计源的数据对比
   type StatsComparison struct {
       Date                   string
       SessionAnalytics       StatsSnapshot
       UserUsage              StatsSnapshot
       ReportRollup           StatsSnapshot
       ExpectedDiscrepancies  []string
   }
   ```

3. **前端 UI 增加提示**
   - 会话分析页面：「统计今日新建会话（本地时间，排除内部请求）」
   - 用户用量页面：「统计最近 N 天所有请求（含标题生成）」
   - 增加「统计口径差异说明」链接

---

### 阶段 2：短期修正（2-4 周）

**优先级 1**: 时间窗口标准化

1. 实施「选项 A：标准化时间窗口」
2. 在 API 中增加 `timezone` 参数（默认 UTC，可选 Asia/Shanghai）
3. 前端增加时区选择器

**优先级 2**: usage_facts 标记列

1. 迁移脚本：增加 `is_internal_loopback` 和 `is_probe_synthetic`
2. Hook 写入侧：设置标记
3. 回填历史数据（可选，根据 request_id 特征识别）

---

### 阶段 3：中期优化（1-2 个月）

1. 统一所有统计 API 使用 usage_facts（而非 session_summaries）
2. session_summaries 仅保留非聚合列（title, summary, key_topics）
3. 聚合列从 usage_facts 实时查询（或使用物化视图）

---

### 阶段 4：长期架构（3-6 个月）

1. 评估 ClickHouse / Timescale / DuckDB 作为 OLAP 层
2. 构建 unified_analytics_cubes
3. 逐步迁移所有统计 API
4. 下线 session_summaries 和 report_snapshots 的聚合列

---

## 七、测试验证计划

### 7.1 数据一致性测试

**测试用例**:
```sql
-- 同一 UTC 日，usage_facts vs session_summaries
WITH usage AS (
    SELECT COUNT(*) as cnt, SUM(total_tokens) as tokens
    FROM usage_facts
    WHERE occurred_at >= '2026-11-18 00:00:00 UTC'
      AND occurred_at < '2026-11-19 00:00:00 UTC'
      AND NOT is_internal_loopback
),
session AS (
    SELECT SUM(request_count) as cnt, SUM(total_tokens) as tokens
    FROM session_summaries
    WHERE first_request_at >= '2026-11-18 00:00:00 UTC'
      AND first_request_at < '2026-11-19 00:00:00 UTC'
)
SELECT 
    u.cnt as usage_count,
    s.cnt as session_count,
    (u.cnt - s.cnt) as diff,
    ROUND(100.0 * (u.cnt - s.cnt) / NULLIF(u.cnt, 0), 2) as diff_pct
FROM usage u, session s;
```

**预期结果**:
- `diff_pct` 应接近 10%（内部回环请求比例）
- 如果偏差超过 15%，需要排查数据异常

---

### 7.2 时区边界测试

**测试场景**: UTC 日跨越本地日

```
UTC 2026-11-18 00:00 = CST 2026-11-18 08:00
UTC 2026-11-19 00:00 = CST 2026-11-19 08:00

测试请求：
- occurred_at = '2026-11-18 02:00:00 UTC' (CST 10:00)
  → 在 UTC 日 11-18
  → 在 CST 日 11-18
  
- occurred_at = '2026-11-18 23:00:00 UTC' (CST 11-19 07:00)
  → 在 UTC 日 11-18
  → 在 CST 日 11-19
```

**验证**:
1. 对账报表（UTC 日）应将两个请求都计入 11-18
2. 如果会话分析用 CST 日，应将第二个请求计入 11-19

---

## 八、参考资料

### 代码位置

- **session_summaries 维护**: `internal/sessionv2mirror/session_summary_dim.go`
- **usage_facts 聚合**: `domains/reportrollup/rollup.go`
- **会话分析 API**: `admin/session_analytics_handler.go`
- **用户用量 API**: `admin/user_usage_stats.go`
- **对账报表**: `admin/shadow_reconciliation.go`

### 相关迁移

- **750/751**: usage_facts 物理分区（上海日历日）
- **563**: session_summaries 触发器（已废弃）
- **606**: 会话身份扩展
- **745/746/759**: report_snapshots 建表
- **890**: V1 族退役（_request_logs_hot 删除）

### 设计文档

- `docs/reconciliation/design-report-rollup.md`: 对账报表设计
- `docs/omni-ref3`: V2 会话存储迁移计划（A2/A6）
- `docs/session-governance-2026-11`: 会话治理方案（P1-4）

---

## 附录 A：快速诊断检查表

当收到「数据不一致」反馈时，按此检查表排查：

- [ ] **确认对比的时间窗口**：
  - 会话分析用的是「今日」（本地 00:00 起）还是「最近 24 小时」？
  - 用户用量用的是哪个 days 参数？
  - 对账报表选择的是哪个 UTC 日？

- [ ] **确认对比的数据范围**：
  - 是否包含内部请求（标题生成约 10%）？
  - 是否包含探针会话？
  - 是否包含 in_progress 状态？

- [ ] **确认聚合粒度**：
  - 是按会话数统计，还是按请求数统计？
  - 是按用户聚合，还是按租户聚合？

- [ ] **检查物理数据**：
  ```sql
  -- 验证 usage_facts 和 session_summaries 的行数
  SELECT 
      (SELECT COUNT(*) FROM usage_facts 
       WHERE occurred_at >= CURRENT_DATE) as usage_rows,
      (SELECT COUNT(*) FROM session_summaries 
       WHERE first_request_at >= CURRENT_DATE) as session_rows;
  ```

- [ ] **查看调试日志**：
  - `user_usage_summary observation` 日志（性能指标）
  - SessionSummaryDimWriter 的 upsert 执行情况
  - ReportRollupWorker 的 daily run 结果

---

## 附录 B：术语表

| 术语 | 定义 |
|-----|------|
| **内部回环请求** | 系统自动生成的标题、摘要请求，占总请求量约 10% |
| **探针合成会话** | 健康检查、监控等合成流量 |
| **usage_facts** | 请求级事实表，OLTP 实时写入 |
| **session_summaries** | 会话级聚合表，由 Hook 增量维护 |
| **report_snapshots** | 日级快照表，由定时任务批量聚合 |
| **UTC 日** | [00:00:00 UTC, 23:59:59 UTC] |
| **本地日** | 数据库服务器本地时区的日边界（中国 = UTC+8） |
| **滑动窗口** | 相对于查询时刻的固定时长（如「最近 24 小时」） |
| **固定日边界** | 按日历日的固定边界（如「今日 00:00 起」） |

---

**文档版本**: 1.0  
**创建日期**: 2026-11-19  
**作者**: Kiro (Analysis & Documentation)  
**审核状态**: 待审核
