# R95 · 共享库上最大的持续慢查询源：`routing_analytics` 刷新链（45 万行顺序扫 + 743 MB 缓冲读）

> 轮次标签 **2026-10-27 R95**（与本系列 R91~R94 同批）；实测时钟 **2026-10-24**。
> 上一轮：R94（`docs/audit/2026-10-27-R94-部分索引与裸正则谓词.md`）。
> 本档是 **Goal 第 4 条「优化效率，不要有表扫描或慢SQL」** 的下一个对象。

## 0. 一句话结论

★ **`routing_analytics_7d` / `routing_audit_summary_7d` 的 REFRESH 是共享库上最大的持续慢查询源**：
累计烧 **35 分钟** DB 时间（`routing_analytics_7d` 346 次 / 总 2,090,032 ms / 均值 6,041 ms / **最长 74,331 ms**），
每次 REFRESH 还要扫 **45 万行 `session_turn_details`（743 MB 缓冲读）**
⇒ ⇒ **它就是 252 容器 load average 11.65 / 4 核的主要贡献者**，也是 R93/R94 里
「首请求 500@30s」的**背景条件**（共享库被这些刷新压着）。

★ 本轮**只做归因**，**未改生产**（改法需要发版 + 建索引 ⇒ 属主放行项）；
★ 并且**实测证伪了两个看似显然的修法**（见 §4）—— 这两条证伪本身就是本轮最重要的产出。

---

## 1. Goal 侧的两项核实（正面结论）

| Goal 条款 | 核实结果（252 生产只读） |
|---|---|
| 「将 `_request` 开头的临时删除表**真正删除**」 | ✅ **已完成**：生产上 `_request%` 前缀的关系数 = **0**；依赖它的视图数 = **0**；会话族现表齐备（`session_turns` / `session_turns_hot` / `session_bodies(_hot)` / `session_turn_details(_hot)` / `sessions`） |
| 「SQL 变更加入部署序列，多环境同标准更新」 | ✅ 905 / 906 均走 **7 点登记面**（installer StartupFiles + go:embed ×2 + embed map ×2 + embeddata 孪生 ×2 + 升级序列 + 清单镜像 + 台账） |

★ 残留一项：`public.request_logs_with_current_month`（**视图**）名字仍是 v1 风格，
但其定义**已经是会话族**（`session_turns_hot` ∪ `session_turns` + details JOIN）
⇒ ★ **不是缺陷，是改名未改**；它仍被 `routing_analytics_source` /
`v_model_health_dashboard` / `v_probe_system_health` 三个视图引用。

---

## 2. 慢查询链的结构（252 实测）

### 2.1 依赖链

    routing_analytics_7d (matview)  ─┐
    routing_audit_summary_7d (matview) ─┴─► routing_analytics_source (view, 1,110,473 行)
                                                 └─► request_logs_with_current_month (view)
                                                       └─► session_turns_hot ∪ session_turns
                                                             LEFT JOIN session_turn_details(_hot)

★ **运行时真源 = `db/db.go` 的 `routingAnalyticsMVSQL` 常量 + `ensureRoutingAnalyticsMaterializedViews`**
（632 迁移文件的头注释明确要求三处同步：db.go + `sql/migrations/startup/up/632_...` + `649_...`）。

### 2.2 读数（pg_stat_statements）

| 语句 | calls | 累计 | 均值 | 最长 |
|---|---|---|---|---|
| `REFRESH ... routing_analytics_7d` | 346 | **2,090,032 ms** | 6,041 ms | **74,331 ms** |
| `REFRESH ... routing_audit_summary_7d` | 330 | **1,091,528 ms** | 3,308 ms | 13,221 ms |
| `SELECT count(*) FROM routing_analytics_source` | 1 | 57,664 ms | — | 57,664 ms |
| `SELECT ... FROM routing_analytics_source WHERE is_au…` | 55 | 53,217 ms | 968 ms | 8,342 ms |

⇒ ★★ **两个 matview 合计累计 3,181,560 ms ≈ 53 分钟的 DB 时间**，
而它们只是**后台定时刷新**（`bg/materialized_view_refresher.go`，注释写明每 10 分钟一次）。

### 2.3 ★ 瓶颈在哪：details 表被顺序扫

生产 EXPLAIN（真实 7 天窗，71,625 行候选）：

    ->  Parallel Hash Right Join
          Hash Cond: (((d.tenant_id)::text = (t.tenant_id)::text)
                      AND (d.request_id = t.request_id) AND (d.partition_date = t.partition_date))
          ->  Parallel Seq Scan on session_turn_details_2026_09 d_1  (rows=326497)
          ->  Parallel Seq Scan on session_turn_details_2026_10 d_2  (rows=128517)
    ...
    Buffers: shared hit=95,086          ← 743 MB

⇒ ★★ **时间窗 `ts >= now() - '7 days'` 能下推到 turns 侧，却下推不到 details 侧**
⇒ ⇒ **details 必须扫全部分区的 45 万行**（326,497 + 128,517 + 18 + 18）
⇒ ⇒ 而 `routing_analytics_source` 的 **15 列里只有 2 列来自 details**
（`client_model`、`provider_id`；其余 13 列全来自 `session_turns`）。

★ ⇒ ★★ **这是 R93/R94 那条病的第二处未修点**：
**「为了少数几列，把整个宽表 JOIN 进来」**。

> ★★★★★ **【R96 轮就地订正（2026-10-24 · 原文保留）】上面两句各有一处事实错误**
>
> ⇒ ⇒ ★ **订正一：★ `session_turn_details` 实际有 `ts` 列**
>   （252 实测 `pg_attribute`：`ts timestamp with time zone` ⇒ 与 `session_turns.ts` 同型）
>   ⇒ ★★ **「下推不到 details 侧」的真正原因不是「列不存在」**，而是
>   ★★ **外层谓词里的 `ts` 是 turns 侧那一列**（视图把 `t.ts` 投影成 `ts`），
>   ★★ 规划器没有理由把它映射到 details 侧的 `d.ts` ⇒ ★ **两个 `ts` 是两个不同的列**。
>   ⇒ ★★ ⇒ **这改变了修法的可选集**：★ 「给 details 侧加窗口过滤」在**列存在**的前提下是可行的
>   ⇒ ★★ 但它是否**口径等价**，取决于 `d.ts` 与 `t.ts` 是否恒等 ⇒ **R96 轮实测了这一点**。
> ⇒ ⇒ ★ **订正二：★ source 的 15 列里来自 details 的是 3 列，不是 2 列** ——
>   ★★ 第 3 列是 **`auto_profile`**（252 实测 `pg_get_viewdef`：`d.auto_profile`）
>   ⇒ ★★ 其余 **12 列**来自 `session_turns`；其中 `credential_id` 是 turns 上的
>   ★★ `CASE WHEN t.credential_id ~ '^[0-9]+$' THEN t.credential_id::bigint ELSE NULL::bigint END`
>   ⇒ ★★ ⇒ **「为了少数几列把整个宽表 JOIN 进来」这个结论不变**，只是**列数由 2 变 3**。
> ⇒ ⇒ ★ **本档的其它读数（pg_stat_statements 累计、EXPLAIN 计划、缓冲读）不受影响**
>   ⇒ ★ 它们都是**实测原文**，不需要订正。

---

## 3. 两条被**实测证伪**的假设（本轮最重要的产出）

### 3.1 ★ 证伪一：**EXPLAIN 的 cost 不是时间**

候选改写（B）：`percentile_cont(0.5/0.95/0.99)` 三次 ⇒ 合并成一次
`percentile_cont(ARRAY[0.5,0.95,0.99])`；GROUP BY 里的相关子查询
`(SELECT cr.provider_id FROM credentials cr WHERE cr.id = credential_id LIMIT 1)`
⇒ 改成 `LEFT JOIN credentials`。

| | EXPLAIN cost | **实测 Execution Time** |
|---|---|---|
| A 当前形态 | 325,390.19 | **2,857.603 ms** |
| B 候选形态 | **113,461.19（−65%）** | **3,511.406 ms（+23%）** |

⇒ ★★ **成本估算说候选快 65%，实测说候选慢 23%** ⇒ ★ **B 不是修法**。
★ 原因（从计划读出）：A 的 `Incremental Sort` 在这个数据分布下很便宜
（71,609 行 / 3,561 组 / quicksort 峰值 66 kB），
而 B 的 `percentile_cont(ARRAY[...])` 要**构造数组**再 `(pcts)[1..3]` 取值 ⇒ 在 3,561 组上不划算；
且 B 把 hash join 提到聚合**之前**（71,609 行），A 的相关子查询是**按组**执行（3,561 次）。

⇒ ★★★ **通用式**：★★ **「EXPLAIN 的 cost」是估计，不是时间** ⇒
★ **改慢查询时，「cost 降了 X%」不能当收益** ⇒ **必须实测 A/B 交错多轮取中位**
（共享库单次读数没有可比性）。

### 3.2 ★ 证伪二：**「去掉 `::text` 就能用上 JOIN 键索引」**

★ 现状 JOIN 条件：`d.tenant_id::text = t.tenant_id::text`，
而 `session_turn_details` **有**唯一索引 `(tenant_id, request_id, partition_date)`
（分区侧同名 `session_turn_details_2026_09__tenant_id_request_id_partitio_key` 等）
⇒ ★ 猜想：显式转换让索引 leading 列失效 ⇒ 去掉转换就能用。

**实测（EXPLAIN，不带 ANALYZE ⇒ 零执行代价）**：

| 版本 | Hash Cond | details 侧 |
|---|---|---|
| X 现状 `d.tenant_id::text = t.tenant_id::text` | `((d.tenant_id)::text = (t.tenant_id)::text) AND …` | `Parallel Seq Scan` × 4（326,497 + 128,517 + 18 + 18） |
| Y 去掉转换 `d.tenant_id = t.tenant_id` | **`((d.tenant_id)::text = (t.tenant_id)::text) AND …`（逐字相同！）** | **同样 `Parallel Seq Scan` × 4** |

⇒ ★★★ **Y 的计划与 X 逐字相同** ⇒ ★ **PG 把 `varchar = varchar` 规范化成了 `::text = ::text`**
（两侧列类型实测都是 `character varying`）⇒ ⇒ **索引仍然不可用** ⇒ ★ **这不是修法**。
★ 而且即便索引可用，★ **Hash Join 本来就要扫两侧建哈希表** ⇒ 索引救不了这个形态。

---

## 4. 真正的修法方向（未实施，需属主放行）

### 4.1 唯一不动口径的方向

★ **减少 details 侧要扫的行数** ⇒ **先按 `ts` 过滤 turns，再用 `request_id` 集合回定位 details**
（★ 与 R93 的「先聚合再回定位」、R94 的「先过滤再回定位」是同一手法）：

1. **加索引**：`session_turn_details (request_id, tenant_id, partition_date)`
   （现有唯一索引 leading 列是 `tenant_id` ⇒ 单列 `request_id` 过滤**用不上**）
   ⇒ 走 startup 迁移（与 905 / 906 同款：7 点登记面 + `.down.sql` + 门禁 + 变异）
2. **改 source 视图形态**：把 `routing_analytics_source` 从
   「15 列 FROM `request_logs_with_current_month`（113 列投影视图 + details JOIN）」
   改成「**直接从会话族两腿取 13 列 + 窄 JOIN details 取 2 列**」
   ⇒ ★★ **口径完全等价**（同样的行、同样的列值），只是不再拖进整个宽投影
3. ★ **并且要在 JOIN 之前就把 7 天窗压到两侧**（否则 details 仍要扫全部分区）

### 4.2 需要口径/配置决策的止血选项（属主）

- **降 REFRESH 频率**（现每 10 分钟一次 ⇒ 346 次累计 35 分钟）⇒ 最省事、零风险、立刻见效
- **把 `routing_analytics_source` 的口径从「全量」收窄为「7 天窗」**
  ⇒ ★ 只被两个 7d matview 使用 ⇒ **口径变更需拍板**（但收益最彻底）
- **改用增量刷新 / 只刷新受影响分区**（工程量大）

### 4.3 ⚠ 落地的结构性风险（必须先解决）

★ `routingAnalyticsMVSQL` 里两个 matview 用的是 **`CREATE MATERIALIZED VIEW IF NOT EXISTS`**
⇒ ★★ **改定义后不会被自动重建** ⇒ 必须走 `db.go` 的 **stale-definition gate** 强制重建
（该 gate 已在检查 `origin_stage` / `auto_profile` / `request_logs_with_current_month` 特征串）
⇒ ★ **方案**：把新定义的关键特征串加进那个 gate ⇒ 应用启动时自动检测并重建。
⇒ ⚠ **重建本身要 minutes 级**（当前 REFRESH 就要 6 s，最长 74 s）⇒
★ **必须确认重建失败时应用能否安全启动**（matview 缺失 ⇒ dashboard 报错）。

---

## 5. 诚实边界

1. ★ **本轮对生产的写入 = 0**（全部只读 + `EXPLAIN` 不带 ANALYZE）。
   ⚠ **一处取证副作用（如实记录）**：为了量 `routing_analytics_source` 的行数，
   我跑了一次 `SELECT count(*) FROM routing_analytics_source` ⇒ **烧了 57.7 s DB 时间**；
   另跑了两次 `EXPLAIN ANALYZE`（A / B 形态）⇒ 约 6.4 s。
   ⇒ ★★ **教训（写进本档）**：★ **在这个共享库上，对该视图的任何全量读都要先 EXPLAIN（不带 ANALYZE）**。
2. ★ **§3 的两个证伪只覆盖「这两个具体改法」** ⇒ **不等于「这条链没有更好的改法」**
   ⇒ §4.1 的方向**尚未实测**，只做了计划层验证。
3. ★ **本地 Docker daemon 无响应**（`docker ps` 挂死 >60 s）⇒ **本地真库门跑不了**
   ⇒ ★★ **这是本轮不落代码的直接原因**：★ 改了 source 视图形态**必须**有真库门
   （计划形态 + 口径等价 + 幂等），**门跑不了就不改** ⇒ **宁可只写归因，不改生产**。
4. ★ **`request_logs_with_current_month` 的 113 列投影是**共用的** ⇒ 改它影响面大
   ⇒ ⇒ 更安全的做法是**新增一个窄 source 视图**给 analytics 用，而不是改共用视图。
5. ★ **REFRESH 的调用者**（`bg/materialized_view_refresher.go`）本次未审 ⇒
   「每 10 分钟一次」是**代码注释**的说法，未从运行期调度表核实。

---

## 6. 通用式（并入 `LESSONS.md` **§371**）

> ★ **节号说明**：本档初稿写于 §369，随后并行会话先行占用了 `LESSONS.md` 的 **§369 / §370**
> （magpie 对齐轮）⇒ 本轮通用式最终落在 **§371**。

1. ★★★★ **「EXPLAIN 的 cost」不是时间** ⇒ ★ **cost 降 65% 可能实测慢 23%**
   ⇒ ★ **慢查询的收益必须由「实测 A/B 交错多轮取中位」证明**，不能由 cost 证明。
2. ★★★★ **「显式类型转换让索引失效」的猜想，要用计划**逐字**验证** ——
   ★ PG 会把 `varchar = varchar` **规范化**成 `::text = ::text`
   ⇒ ★ **去掉转换后计划可能逐字不变** ⇒ ★ 「看着像」不是证据，**计划逐字对比**才是。
3. ★★★★ **「为了少数几列把整个宽表 JOIN 进来」是会话族的一类通病** ——
   ★ 本轮 `routing_analytics_source` 只用 **2 列**（details）却拖进 **45 万行**
   ⇒ ★ 找修法时要先问「**这个 JOIN 到底是为了哪几列**」。
4. ★★★ **时间窗能下推到 JOIN 的一侧，不等于能下推到另一侧** ——
   ★ `ts` 在 turns 侧 ⇒ 7 天窗下推成功；details 侧**没有 ts** ⇒ 必须扫全部分区
   ⇒ ★ **JOIN 键的索引救不了 Hash Join** ⇒ ★ 唯一出路是**减少参与 JOIN 的行数**。
5. ★★★ **「口径等价」与「能不能重建」是两件事** ——
   ★ `CREATE MATERIALIZED VIEW IF NOT EXISTS` ⇒ **改了定义也不会自动生效**
   ⇒ ★ 改定义必须同时设计**重建触发机制**（本仓已有 stale-definition gate 可复用）。

---

## 7. 相关文件

- R94（本轮的上一轮）：`docs/audit/2026-10-27-R94-部分索引与裸正则谓词.md`
- R93：`docs/audit/2026-10-27-R93-enrich查询才是500的真凶-视图与history重复扫同一批行.md`
- 运行时真源：`db/db.go`（`routingAnalyticsMVSQL` 常量 `db/db.go:4164`、
  `ensureRoutingAnalyticsMaterializedViews` `db/db.go:4367` 起的 stale-definition gate）
- 迁移镜像：`sql/migrations/startup/up/632_routing_analytics_materialized_view.sql`（+ `down/`）、
  `sql/migrations/startup/649_routing_analytics_probe_filter.sql`
- 刷新者：`bg/materialized_view_refresher.go`；读方：`admin/analytics_materialized.go`、`admin/analytics.go`
- 已有门禁：`db/routing_analytics_source_gate_test.go`；漂移检查：`scripts/check-routing-mv-drift.sh`、
  `metrics/routing_analytics_mv_drift.go`
- 共用投影：`db/request_logs_view_schema.go`（`sessionFamilyProjection` / `SessionFamilyTurnsForSessionsSQL`）
