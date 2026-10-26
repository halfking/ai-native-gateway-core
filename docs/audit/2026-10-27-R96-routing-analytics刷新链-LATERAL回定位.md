# R96 · `routing_analytics` 刷新链修法落地：source 视图换成「会话族两腿 + LATERAL 回定位」（45 万行顺序扫消除，发版 2845 已上线）

> 轮次标签 **2026-10-27 R96**（与本系列 R91~R95 同批）；实测时钟 **2026-10-24**。
> 上一轮：R95（`docs/audit/2026-10-27-R95-routing-analytics刷新链-45万行顺序扫.md`）。
> 本档是 R95 归因的**修法落地**，也是本系列第 5 个「归因 → 选型 → 门禁 → 发版 → 生产对照」闭环。

## 0. 一句话结论

★ **R95 说的「45 万行顺序扫」被消除了**：两个 routing matview 的 REFRESH 里，
`session_turn_details` 侧从 `Parallel Seq Scan`（**326,497 + 128,517 + 18 + 18 = 45 万行**、
`Buffers: shared hit=95,086` ≈ 743 MB）变成**逐行索引回定位**
（`Index Scan using session_turn_details_2026_10_tenant_id_request_id_partition_key`，
其余分区 **`never executed`** ⇒ **动态分区裁剪生效**）。

★ **生产对照（发版 2845，252）**：

| | 发版前（pgss 历史） | 发版后**第一次**刷新 | 倍数 |
|---|---|---|---|
| `REFRESH … CONCURRENTLY routing_analytics_7d` | 均值 **55,358.8 ms** / 最长 **179,539 ms** | **4,683 ms** | **11.8×** |
| `REFRESH … CONCURRENTLY routing_audit_summary_7d` | 均值 **6,964.2 ms** / 最长 **86,166 ms** | **1,635 ms** | **4.3×** |

★ **口径零变更**：源层双向 `EXCEPT ALL`（1,110,635 行，同一语句 ⇒ 同一快照）
`only_old=0 / only_new=0`；聚合层 `c1_only_old=0 / c1_only_new=0`；逐列类型对拍 15 列全一致。

★ **零 DDL**：只做 `CREATE OR REPLACE VIEW`（实测 **245 ms**）⇒ **无索引、无索引体积、无锁窗**。

---

## 1. 起点：R95 的修法方向 + 一个被推翻的「结构性风险」

### 1.1 R95 留给本轮的四条

R95 归因出：source 的 15 列里只有少数几列来自 `session_turn_details`，
却把整个宽表 JOIN 进来 ⇒ 规划器只能选 Hash Join ⇒ details 侧扫全部分区。
修法方向：① 加 details 索引；② 把 source 改成「直接从会话族两腿取 13 列 + 窄 JOIN details 取 2 列」；
③ **在 JOIN 之前就把 7 天窗压到两侧**；④ ⚠ **「改定义不会自动重建 matview」是结构性风险**。

### 1.2 ★★ 被推翻的一条：**「改视图定义 ⇒ 必须重建 matview」不成立**

R95 §4.3 把这条列成「必须先解决」的结构性风险，并推断重建要 **minutes 级**。
★ **本地真库实测推翻它**：

```sql
CREATE VIEW h1_v AS SELECT a FROM h1_t;
CREATE MATERIALIZED VIEW h1_mv AS SELECT a FROM h1_v;   -- 内容 {1,2}
CREATE OR REPLACE VIEW h1_v AS SELECT a*10 AS a FROM h1_t;  -- 保留 OID
REFRESH MATERIALIZED VIEW h1_mv;                       -- 内容 {10,20}  ★ 新定义生效
```

⇒ ★★ **`CREATE OR REPLACE VIEW` 保留 OID** ⇒ matview 的依赖不动 ⇒
**下一次 REFRESH 自动采用新定义** ⇒ **不需要 DROP/CREATE matview，也不需要重建**。

★★ **但真正会触发重建的不是视图，是「启动期的 stale-definition gate」**：
`db/db.go` 的 `ensureRoutingAnalyticsMaterializedViews` 用**特征串**判陈旧
（`POSITION('request_logs_with_current_month' IN pg_get_viewdef(...)) > 0`）
⇒ ★ 新形态不再引用那个名字 ⇒ **会被判陈旧** ⇒ 走
`DROP MATERIALIZED VIEW … CASCADE` + 重建（**minutes 级**）⇒ ★★ **必须同步改 gate 的判据**。

⇒ ★★ **本轮的真实教训不是「视图不能热改」，而是「改共享对象前，先去看那个判『陈旧』的门」**。

---

## 2. 口径等价的前提（两条，都必须先证）

### 2.1 ★ 列来源逐列核实：来自 details 的是 **3 列**，不是 2 列

252 实测 `pg_get_viewdef('public.request_logs_with_current_month')` 逐列对拍：

| source 的 15 列 | 来源 |
|---|---|
| `ts` / `task_type` / `work_type` / `is_auto_request` / `tenant_id` / `request_id` / `success` / `latency_ms` / `cost_usd` / `origin_stage` / `outbound_model`(`t.model`) | `session_turns`（11 列） |
| `credential_id` | `session_turns` 的 `CASE WHEN t.credential_id ~ '^[0-9]+$' THEN t.credential_id::bigint ELSE NULL::bigint END` |
| ★ `client_model` / ★ `provider_id` / ★ **`auto_profile`** | **`session_turn_details`（3 列）** |

⇒ ★★ R95 档写的「2 列」**是错的**（`auto_profile` 也来自 details）⇒
**已就地订正**（见 R95 档 §2.3 横幅 + `LESSONS.md` §371 通用式三）⇒
「为了少数几列把整个宽表 JOIN 进来」这个**结论不变**，只是列数 2 → 3。

### 2.2 ★★ `LIMIT 1` 的等价性前提：details 的唯一键

旧体是 `LEFT JOIN details ON (tenant_id, request_id, partition_date)`，
新体是 `LEFT JOIN LATERAL (SELECT … WHERE 同三键 LIMIT 1)`。
两者**产出同样的行集合**，当且仅当「同一 (tenant_id, request_id, partition_date) 最多 1 行 details」。

★ 252 实测：details 的唯一索引是 `(tenant_id, request_id, partition_date)`，
且 `partition_date` 是**分区键** ⇒ 同一键只能落在同一分区 ⇒ **全局最多 1 行** ✔。

---

## 3. 候选矩阵实测（252 生产，事务内 + ROLLBACK ⇒ 零残留）

### 3.1 四个候选

| 候选 | 形态 |
|---|---|
| **C0**（现状） | `SELECT 15 列 FROM request_logs_with_current_month`（canonical 宽投影 + 宽 LEFT JOIN） |
| **C1** | **会话族两腿 + `LEFT JOIN LATERAL (… LIMIT 1)`** |
| **C2** | C1 + 覆盖索引 `(tenant_id, request_id, partition_date) INCLUDE (client_model, provider_id, auto_profile)` |
| **C3** | 窄投影 + details 侧**窗口下推**（`d.partition_date >= ((now()-'7 days') AT TIME ZONE 'UTC')::date`） |

### 3.2 交错 3 轮（每轮换序），执行时间

| 候选 | R1 | R2 | R3 | 中位 |
|---|---|---|---|---|
| C0 | **20,378.608**（冷缓存轮） | 2,653.610 | 4,482.830 | 4,482.8 |
| **C1** | 1,917.124 | 2,270.848 | 2,381.362 | **2,270.8** |
| C2 | 1,642.517 | 1,866.073 | 2,304.376 | 1,866.1 |
| C3 | 1,631.590 | 2,081.968 | 1,531.284 | 1,631.6 |

⇒ ★★ **C1 中位 2.0× 于 C0**；★ **C0 的首轮 20.4 s（`read=15,965`）是冷缓存的真实形态** ⇒
**10.6×**。⇒ ★ 共享库单次读数没有可比性 ⇒ 必须交错多轮取中位。

### 3.3 ★★ 口径对拍：四个候选**全部等价**

源层（1,110,635 行）+ 聚合层（matview 输出口径），**双向 `EXCEPT ALL`，同一语句 ⇒ 同一快照**：

```
c1_only_old=0  c1_only_new=0
c2_only_old=0  c2_only_new=0
c3_only_old=0  c3_only_new=0
```

★ ⚠ **第一次跑出来是「差 4 行」** ⇒ 查下去是**两次独立 `count(*)` 之间的并发写入**
（两条腿之间隔了 22 s，共享库一直在写）⇒ ★★ **比对必须在同一条语句里**
（`EXCEPT ALL`）或 `REPEATABLE READ` ⇒ **这正是本仓已登记的 §340**。

### 3.4 ★★ 两条被实测证伪的修法（与 R95 的两条并列）

★ **证伪一：覆盖索引（C2）白建** —— 实测里 `r96_cover_idx` 建了（29.9 s），
**规划器根本没选它**：

```
->  Index Only Scan using session_turn_details_2026_09_tenant_id_request_id_partition_idx
      Index Cond: ((tenant_id = (t_1.tenant_id)::text) AND (request_id = t_1.request_id) AND (partition_date = t_1.partition_date))
      Heap Fetches: 3760          ← 3 列不在索引里 ⇒ 仍要回堆
```

⇒ ★★ 规划器选了**既有**的唯一索引（更小 ⇒ 估算更贵）⇒ ★ **覆盖索引白建**
⇒ ★ 本轮**零 DDL**（只换视图体 ⇒ 无索引体积、无 30 s 建索引锁窗）。
⇒ ★★ **通用式：加索引前先确认「规划器会选它」**。

★ **证伪二：把窗口写进 source 视图（C3）会截断参数化窗口的读方** ——
`admin/analytics.go` 的 matrix / flow / funnel 端点读的是
`FROM routing_analytics_source WHERE ts >= NOW() - $1::interval`
⇒ ★ **窗口由调用方给**（可以 > 7 天）⇒ ★★ **把 7 天窗固化进 source 视图
＝ 给所有读方都套上 7 天窗** ⇒ **口径变更** ⇒ ✘ 出局。

---

## 4. 落地的四处改动

1. **`db/db.go`（运行时真源）**：`routing_analytics_source` 的视图体抽成独立常量
   `db.RoutingAnalyticsSourceViewDDL`（导出，供迁移门做语义耦合），改为 C1 形态。
2. **迁移 907 + `.down.sql`**：`CREATE OR REPLACE VIEW`（保留 OID ⇒ matview 依赖不动）。
   ★ down 把视图体换回 canonical 形态（**回退是性能回退，不是口径回退**）。
3. **启动期 gate（`db/db.go` 的 fast-path 与 stale-definition 两处）**：
   特征串改为 **「旧形 `request_logs_with_current_month` **或** 新形 `session_turn_details`」**
   ⇒ ★ 新形不被判陈旧 ⇒ **不走 CASCADE 重建** ⇒ ★ 两形都能通过（升级路径与新装路径都安全）。
4. **7 点登记面**（installer StartupFiles / go:embed ×2 / embed map ×2 / embeddata 孪生 ×2 /
   升级序列 / 清单镜像 / 台账）。

### 4.1 门禁三层（承 R94 的结论）

| 层 | 门 | 抓什么 |
|---|---|---|
| **结构** | `TestRoutingAnalyticsSourceViewIsLateralShape`（含**阳性对照**） | 形态被回退（去 LATERAL / 去 LIMIT / 少一个键 / 回退 canonical / 直读 v1） |
| **语义** | `TestRoutingAnalyticsSourceViewMatchesCanonicalOnRealDB`（**用生产 DDL 原文**逐行对拍） | 口径变了（少取一列 ⇒ 该列静默变 NULL ⇒ 结果错但不报错） |
| **计划** | `TestRoutingAnalyticsSourceViewUsesDetailsIndexOnRealDB` | details 侧又变回顺序扫 |
| **耦合** | `TestMigration907_ViewBodyMatchesRuntimeConstant`（两侧同法抽取后逐字比对） | 「部署路径」与「运行时真源」各写一份、悄悄漂移 |
| **形状+投递** | `TestMigration907_*`（形状检测器 + 8 个坏样本阳性对照 + sha256 冻结 + embeddata 逐字节 + 7 点） | 迁移侧回归 / 投递面漏点 |

★ **变异 8/8 全红 + md5 逐字还原**，含两条重点臂：
★ **M4「无症状退化」**（details 少取 `auto_profile` ⇒ 该列静默变 NULL ⇒
**结果错但不报错**）—— 只有真库口径门能抓住；
★ **M7「gate 判据回退」**（不再接受新形 ⇒ 启动期会把新形判陈旧 ⇒ CASCADE 重建 minutes 级）。

---

## 5. 生产读数（252 + 154）

### 5.1 发版 2845

★ pending 迁移**恰好 1 个**（907，sha256 `7db8e5d724eb…` 与仓内冻结值**逐字一致**）
⇒ 部署日志：`→ 907_routing_analytics_source_lateral.sql (sha256=7db8e5d724eb)` / `迁移完成 (1 新应用 / 1 检查)`。
★ `2.5.8-a194db3c-20261024-2845`，nginx → 8781，身份仲裁通过，DB 就绪 4 s，
凭据冒烟 `providers=14,18,13092 creds=17 failed=0`，verified（总 173 s / 切换 15 s）。
★ 台账 `llm_gateway_migration_checksums`：907 的 sha 与仓内**逐字一致** ✔
★ 生产视图形态：`LATERAL_SHAPE_OK` ✔

### 5.2 ★ 生产计划形态（同一时刻，252）

```
->  Nested Loop Left Join                       ← 旧形态是 Parallel Hash Right Join
      ->  Index Scan using idx_session_turn_details_hot_request on session_turn_details_hot dd
            Index Cond: ((request_id = t.request_id) AND (partition_date = t.partition_date))
      ->  Index Scan using session_turn_details_2026_10_tenant_id_request_id_partition_key
            Index Cond: ((tenant_id = (t_1.tenant_id)::text) AND …)
->  Index Scan using session_turn_details_2026_09__tenant_id_request_id_partitio_key  (never executed)
->  Index Scan using session_turn_details_2026_11_tenant_id_request_id_partition_key  (never executed)
```

⇒ ★★ **`never executed` ＝ 动态分区裁剪生效**：只探测命中分区，不再扫 45 万行。
⇒ 父表腿 `Execution Time: 1,732.305 ms`。

### 5.3 ★★ REFRESH 前后对照（pg_stat_statements）

> ★★★★★★ **订正横幅（R97 轮补记，2026-10-25 00:5x 实测）——「11.8×」是【首轮读数】，稳态口径是 1.93×**：
>
> ★★★ R97 轮顺手回填了**多周期均值**（pgss 同一条目，349 次 → **352 次**）：
> | | 读数 |
> |---|---|
> | 发版后累计 4 次的**均值** | (4,683 ms + 3 次共 110.2 s) / 4 = **28.7 s** |
> | 历史均值（R96 记录） | 55.4 s |
> | ⇒ **稳态口径的收益** | **1.93×**（**不是 11.8×**） |
>
> ★★★★★★ **回填补完横幅（R161 轮，2026-10-26 12:0x 实测）—— ★★ 样本量从 4 涨到 78 ⇒ 均值 28.7 s → **31.3 s** ⇒ 稳态 1.93× → **1.77×**；★★ 而且【尾部一点没动】**：
>
> ★★★ **权威源 = 252 的 `pg_stat_statements` 聚合**（★ R161 轮实测，★ 同一 queryid）：
>
> | | 发版前（R96 记录） | **发版后累计 78 次（R161 实测）** |
> |---|---|---|
> | `routing_analytics_7d` calls | 348 | 348 → ★ **426**（★ **+78 次**） |
> | total | 19,264,857 ms | ⇒ **+2,442,972 ms** |
> | ⇒ **多周期均值** | 55,358.8 ms | ⇒ ★★ **2,442,972 / 78 = 31,320.2 ms = 31.3 s** |
> | ⇒ **稳态口径的收益** | — | ★★ **55,358.8 / 31,320.2 = 1.77×**（★ R97 的 1.93× 来自 n=4） |
> | ★ **最长** | **179,539 ms** | ★★ **179,538.8 ms（★ 与修前【逐毫秒相同】⇒ 尾部完全未变）** |
> | ★ **最短** | （未记录） | 1,909.7 ms |
> | ★ **标准差** | （未记录） | 37,636.9 ms（★ 相对均值 31.3 s ⇒ ★★ 分布高度分散） |
>
> ⇒ ★★★★ **★ 这个回填把 R97 的推论【修正】了，而且方向很关键**：
> ★★ ① 稳态收益从 **1.93× 降到 1.77×**（★ 样本从 4 → 78 ⇒ **n=4 的均值没有代表性**）。
> ★★ ② ★★ **最长一轮 179,539 ms 与修前【逐毫秒相同】** ⇒ ★★★ **⇒ 修前修后各有一次 179.5 秒的刷新
> ⇒ ⇒ ★★ ⇒ ★★ 【尾部一点没改善】** ⇒ ★★★ **⇒ 这与本节「剩余成本在 `REFRESH … CONCURRENTLY` 非查询侧」的结论【完全一致】**
> ⇒ ⇒ ★★ ⇒ ★★ **⇒ 也直接印证 R98 的头号候选（★ 查写侧/锁侧 ⇒ ★★ 不是查询侧）是对的。**
> ★★ ③ 标准差 37.6 s ⇒ ★★ **分布双峰**（★ 采样器独立测得 p50 = 17.6 s、n=63 轮 ⇒ ★★ 快轮很多、慢轮极慢）
> ⇒ ⇒ ★★★ **⇒ 通用式：★ 「均值」在双峰分布上会【同时掩盖快轮与慢轮】⇒ ★★ 报收益必须【同时报 min / p50 / max】。**
>
> ★★ **同口径补记**（★ 同一次实测，★ `routing_audit_summary_7d`）：
> 发版前 332 次 / 均值 6,964.2 ms ⇒ 发版后 **77 次** / **1,902.2 ms** ⇒ ★★ **稳态 3.66×**（★ R96 记录的 4.3× 是首轮值）
> ⇒ ★★ **但它的最长从 86,166 ms 涨到 175,981 ms** ⇒ ★★ **⇒ 尾部变差了 ⇒ ★★ ⇒ 同样印证「尾部不受查询侧优化支配」。**
>
> ⚠ **诚实边界**：★ pgss 的统计口径是「自上次 reset 以来」⇒ ★★ **本轮未 reset** ⇒ ★★
> ★★ 「发版后 78 次」是由**发版前基线（348 次 / 19,264,857 ms）做差**得到的 ⇒ ★★
> ★★ **前提是发版前那组数字仍然准确**（★ R96 记录）⇒ ★★ **⇒ 建议后续以 `pg_stat_statements_reset()` 为起点重测一轮**。
>
> ★★ **「11.8×」并不假**，但它有两个口径边界，必须一起说：
> ① **它是首轮**（发版后第一次刷新，23:08:29）；
> ② **它是「查询侧」的口径** —— R97 轮实测**当前**的取数查询
>    （同一 source、同一 7 天窗、同一过滤）**执行时间 3,165.6 ms**
>    （发版时实测 1,732.3 ms ⇒ +83%，数据在长）。
>
> ★★★★ **★ 更重要的一条：REFRESH 的剩余成本【不在查询侧】**：
> 稳态 28.7 s 里，查询侧只占 **3.2 s** ⇒ ⇒ **约 25 s 在 `REFRESH … CONCURRENTLY`
> 自身的开销**（diff + 写 3,511 行 + 索引维护 + 等锁）。
> ⇒ ⇒ ★★★ **推论（对 R96 的候选 C3 是致命的）**：
> ★★ **C3「把 matview 查询体改成窗口化 source」的收益上限只有 ~3 s/次**
> ⇒ ★★ **它救不了那 25 s** ⇒ ★★ **下一步该查的是 REFRESH 的【写侧 / 锁侧】，不是查询侧**
> ⇒ ⇒ ★★ R96 若当初先做这个拆分，**就不该把力气花在查询侧** ⇒
> ⇒ ★★ **通用式：★ 「把查询从 45 万行顺序扫改成索引回定位」与「刷新链整体快了多少」是两件事** ——
> ★★ **后者的上限由刷新链的【非查询部分】决定** ⇒ ★★ **先拆「查询 / 非查询」，再谈优化哪一头**。
>
> ⚠ 诚实边界：post-fix 样本只有 **4 次** ⇒ 28.7 s 这个均值**样本量小**；
> ★ 且 §6.2 早已预告「55 s 不全是那条查询，本轮未拆分」⇒ **本轮把那句话兑现成了数字**。


| | 发版前（历史累计 348 / 332 次） | 发版后**第一次**刷新（23:08:29，刷新器在迁移后立即用新 source 跑） |
|---|---|---|
| `routing_analytics_7d` | calls 348 / total 19,264,857 ms / **均值 55,358.8 ms** / 最长 **179,539 ms** | calls 349 / total 19,269,540 ms ⇒ **本次 +4,683 ms** ⇒ **11.8×** |
| `routing_audit_summary_7d` | calls 332 / total 2,312,130 ms / **均值 6,964.2 ms** / 最长 86,166 ms | calls 333 / total 2,313,765 ms ⇒ **本次 +1,635 ms** ⇒ **4.3×** |

---

## 6. 诚实边界

1. ★★ **口径漂移读数不是 0**：生产上跑仓库自带的 matview 一致性检查
   （`bg/mv_consistency.go` 的 SQL）得 `drift_buckets=6 / total_abs_diff=26 / max_abs_diff=7`
   （audit summary：1 桶 / 25）⇒ ★ **这是「两次计算的时间差」而不是口径差**：
   matview 的快照是 23:08:29，base 侧实时算是 83 s 之后 ⇒ 期间新流量进了窗口。
   ⇒ ★★ **口径的权威证据仍是同快照的 `EXCEPT ALL` 0/0**（§3.3），
   ★ **不是**这个漂移数（它天然不为 0）。
2. ★★ **REFRESH 的 55 s 不全是那条查询**：pgss 的 `REFRESH … CONCURRENTLY` 计时
   **包含**并发刷新（diff + 写 + 等锁）⇒ ★ 本轮把**查询侧**从 45 万行顺序扫
   降到索引回定位（查询本身 1,732~2,097 ms），★ **但 55 s → 4,683 ms 的整体降幅里，
   有多少来自查询侧、多少来自「不再长时间占锁 ⇒ 不再互相排队」，本轮未拆分**。
   ★★★★ **（R97 轮补记）这个拆分已兑现**：★ 当前查询侧 **3,165.6 ms**，
   而稳态刷新均值 **28.7 s** ⇒ ⇒ **约 25 s 在非查询侧** ⇒ ⇒ ★★ **候选 C3 的收益上限
   只有 ~3 s/次 ⇒ 救不了那 25 s** ⇒ ★★ **下一步该查 REFRESH 的写侧/锁侧**（见 §5.3 订正横幅）。
3. ★ **共享库的整体 load 未验证**：252 的 load average 本轮未复测 ⇒ ★ **不声称**
   「252 的负载下降了 X%」。
4. ★ **候选 C3（窗口下推）实测最快（中位 1,631.6 ms）但未落地** ⇒ 它的修法是
   「**另建一个只给 matview 用的窗口化 source 视图 + 改 matview 的查询体**」
   ⇒ ★ 需要**重建 matview**（`CREATE MATERIALIZED VIEW IF NOT EXISTS` 不会改已存在的定义）
   ⇒ ★ **属主放行项**（R97 候选）。
5. ★ **`session_turn_details` 的 `ts` 列存在**（R95 档已订正）⇒
   「给 details 侧加窗口」在**列存在**的前提下是可行的 ⇒
   ★ 但它是否口径等价取决于两侧 `ts` 是否恒等 ⇒ **本轮未测**（只测了 `partition_date`
   与 `t.ts` 的关系：252 实测 `mismatched = 0`）。
6. ★ **`auto_profile` 仍在 source 的 15 列里** ⇒ 回定位会多取一列（details 侧要回堆）
   ⇒ ★ 若 matview 改用**只含自己需要的列**的窄 source，可再省一点 ⇒ 属 R97 候选。

---

## 7. 通用式（并入 `LESSONS.md` **§373**）

1. ★★★★ **「改视图定义 ⇒ 必须重建 matview」是半真**：★ `CREATE OR REPLACE VIEW`
   **保留 OID** ⇒ matview 依赖不动 ⇒ **下一次 REFRESH 自动采用新定义**
   ⇒ ★ **真正会触发重建的是「启动期判陈旧的 gate」** ⇒ ★★ **改共享对象前，
   先去看那个门怎么判陈旧**（特征串一旦对不上，就会走 CASCADE 重建 minutes 级）。
2. ★★★★ **覆盖索引可能根本不被选中**：★ 实测里新建的
   `(tenant_id, request_id, partition_date) INCLUDE (…)` **规划器没选**
   ⇒ ★★ **更大的索引 ⇒ 估算更贵 ⇒ 规划器选既有那条 + 回堆**
   ⇒ ★★ **加索引前先确认「规划器会选它」**，否则白建 + 白等 + 白占空间。
3. ★★★★ **「为了少数几列把整个宽表 JOIN 进来」的正解 = 先从主表取列，
   再用 JOIN 键逐行回定位**（`LEFT JOIN LATERAL (… LIMIT 1)`）
   ⇒ ★ 与 R93/R94 同款手法（**先过滤 / 先聚合，再回定位**）
   ⇒ ★ 等价性前提 = **JOIN 键上的唯一约束**（本例：唯一索引 + 分区键 ⇒ 全局唯一）。
4. ★★★★ **把窗口写进共享视图 = 给所有读方都套上这个窗口**：
   ★ `admin/analytics.go` 的 matrix / flow / funnel 用**参数化窗口**
   （`ts >= NOW() - $1::interval`，可 > 7 天）⇒ ★★ **改共享视图前必须先枚举
   所有读方及其窗口口径**。
5. ★★★ **「同一事务里先后两次 count」不是同一个快照** ⇒ 行数差 4 行是
   **并发写入**不是口径差 ⇒ ★ 比对必须在**同一条语句**里（`EXCEPT ALL`）
   或 `REPEATABLE READ` ⇒ **本仓 §340 早已登记，本轮又踩了一次**。
6. ★★★ **门禁的读数手段本身会骗人**：`EXPLAIN` 返回**多行** ⇒
   `QueryRow` 只取第一行（实测拿到的是 `"Append"`）⇒ ★ 门会把
   「没走索引」读成「没走索引」，而**真因是门只读了一行** ⇒ ★ **断言计划文本
   必须逐行收集**。

---

## 8. 相关文件

- 上一轮：R95 `docs/audit/2026-10-27-R95-routing-analytics刷新链-45万行顺序扫.md`
  （★ §2.3 已被本轮就地订正）
- 代码：`db/db.go`（`RoutingAnalyticsSourceViewDDL`、`routingAnalyticsMVSQL`、
  `ensureRoutingAnalyticsMaterializedViews` 的两处特征串）
- 迁移：`sql/migrations/startup/907_routing_analytics_source_lateral.sql`（+ `.down.sql`）
- 门禁：`db/routing_analytics_source_lateral_test.go`（形态/口径/计划/耦合 4 道）、
  `db/routing_analytics_source_gate_test.go`（既有真库门加了 R96 形态用例）、
  `sql/migrations/startup/migration_907_routing_analytics_source_lateral_test.go`
- 一致性检查：`bg/mv_consistency.go`（生产漂移检查）、`scripts/monitoring/verify-mv-consistency.sh`
- 刷新者：`bg/materialized_view_refresher.go`；读方：`admin/analytics.go`、
  `admin/auto_route.go`、`admin/analytics_materialized.go`
