# Auto-Routing v2 · P1 闭环 252 真库只读 E2E 取证（2026-09-25 轮）

- 取证时间：2026-09-26 02:52–03:05 CST（252 服务器时钟；轮次名义日期 2026-09-25）
- 取证库：252 `pg-252-pg17` 容器（PostgreSQL 17.10），`llm_gateway` 库，`llm_gateway` 用户，容器内 unix socket 本地认证（无密码、无 DSN 打印，与 `docs/06-deployment/04-runbooks/ops/245-runbook.md` §5 监控条目同款通道）
- 纪律：全程仅 SELECT / EXPLAIN（EXPLAIN ANALYZE 仅一次带 LIMIT 100 的纯 SELECT），零 INSERT/UPDATE/DELETE/DDL，未调 generate/apply，未注入合成流量
- 证据级别：本文所有数字均来自本轮实跑输出（命令逐条附后），无一处来自本机单测或推导

---

## 0. 摘要

1. **UNION 两支确认**：`auto_route_selections_all` 的所有执行计划均为 Append 节点，同时规划 `auto_route_selections_hot`（UNION 上支）与 `auto_route_selections` 父表全部 4 个分区（2026_08/2026_09/2026_10/default，UNION 下支）。analyzer 四类查询（Q1–Q4）的 EXPLAIN 读面全部命中 `_all` 视图展开后的两支，无一处漏读基表只读单表。
2. **三重零**：`task_type_corrections` 全表 **0 行**（无任何人工标注）；`tuning_params` 全表 **0 行**（四键全缺，analyzer 走代码默认 `thresholds.llm_confidence=0.70`）；Q1–Q4 原式在 252 上全部返回 0 行/0 计数。
3. **词表失配实锤（本轮最重要发现）**：`auto_route_selections_all` 22607 行的 classifier 只有 `heuristic_v2`(22563)/`llm_v2`(44)，`classifier='heuristic'` 命中 **0 行**——而 `taskprofile/analyzer.go:392,429,484,509` 四处仍过滤旧词 `'heuristic'`。写入侧出处：`autoroute/decision_v2.go:385` `Classifier: cls.Classifier + "_v2"`（V2 决策路径统一追加 `_v2` 后缀）；旁证：`admin/annotation_handler.go:349` 的读面已用 `IN ('llm','llm_v2')` 双词表兼容，analyzer 未跟上。即：**即使后续人工标注补齐，P1 analyzer 的整个读面（修正样本/分母/回放）在 V2 写入路径的数据上恒为空，闸门永远判零草稿**。
4. 演示结论：按 readonlyDemoSteps 原式跑完整链（Q1→Q2→参数→4a 聚合→4b 覆盖率→Q3/Q4 回放），输出即「闸门判零草稿」——窗口无修正 + 读面词表失配，本身即有效审计结论。诊断变体（非源码 SQL，已标注）显示词表若对齐，分母为 chat=12848/creative=4968/code=3483/reasoning=1260/planning=4，置信度候选覆盖 0.844@0.85，数据面完全够 P1 闭环用。

---

## 1. 连接与身份确认

```
ssh 252 "hostname; date; docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway \
  -c 'SELECT current_database() AS db, current_user AS usr, inet_server_addr() AS srv, version();'"
```

输出：`iZbp15h19t8xjr2ltjzjurZ` / `Sat Sep 26 02:52:59 AM CST 2026`；
`db=llm_gateway, usr=llm_gateway, srv=(空=unix socket), PostgreSQL 17.10 (Debian 17.10-1.pgdg13+1)`。

视图定义（`\d+ auto_route_selections_all`）：`auto_route_selections_hot ... UNION ALL SELECT ..., 'parent'::text AS storage_tier FROM auto_route_selections`，附加 `storage_tier` 列（hot/parent）。父表 `auto_route_selections` 为 RANGE(partition_date) 分区表，4 分区 + default；两表各有 `uq_ars_request`/`uq_ars_hot_request` UNIQUE(request_id, partition_date) 与 `idx_ars_task_profile_ts`/`idx_ars_hot_task_profile_ts` (task_type, profile, ts DESC)。

## 2. 视图执行计划（UNION 两支确认）

```
EXPLAIN SELECT * FROM auto_route_selections_all;                     -- 纯计划
EXPLAIN ANALYZE SELECT * FROM auto_route_selections_all LIMIT 100;   -- 带界实测
```

纯计划（节选）：

```
Append  (cost=0.00..1221.61 rows=22709 width=507)
  ->  Seq Scan on auto_route_selections_hot        (rows=3)
  ->  Seq Scan on auto_route_selections_2026_08    (rows=1)
  ->  Seq Scan on auto_route_selections_2026_09    (rows=22703)
  ->  Seq Scan on auto_route_selections_2026_10    (rows=1)
  ->  Seq Scan on auto_route_selections_default    (rows=1)
```

ANALYZE 实测：`Limit (actual rows=100) → Append`，hot 实际 3 行、2026_08 0 行、2026_09 97 行即满 LIMIT（2026_10/default never executed）；Planning 9.31ms，**Execution 0.36ms**。纯 SELECT 不取锁，无锁表风险。

**结论：UNION 两支（hot 上支 + 基表/父表下支）均被规划**；父表自身再按分区展开成 4 个子扫描。

## 3. analyzer 四类查询 EXPLAIN（读面 = `_all` 确认）

参数代入：window=30、LIMIT=5000、示例阈值带 [0.65,0.70)。四处计划共同点：**均含 `Append` 节点覆盖 hot + 父表 4 分区**，即读面确为 `auto_route_selections_all`（两支齐），无漏读基表。

| 查询 | 顶层节点 | 关键细节 |
|---|---|---|
| Q1 修正样本 JOIN（analyzer.go:386-396） | `Limit → Sort → Nested Loop`（外层 corrections Seq Scan，agrees=false 过滤） | 内层 Append：hot/08/10/default Seq Scan + **2026_09 走 `Index Scan using auto_route_selections_2026_09_request_id_partition_date_idx`（Index Cond: request_id = c.request_id）** |
| Q2 分母 volumes（analyzer.go:426-431） | `GroupAggregate → Sort → Append` | 5 个 Seq Scan（ts 过滤无 ts 前导索引可用；2026_09 代价 1334≈全分区扫描） |
| Q3 阈值回放（analyzer.go:477-486） | `Aggregate → Append`（5 Seq Scan，confidence 带 + classifier + ts 过滤） | SubPlan EXISTS 用 `Index Scan using task_type_corrections_request_id_key` |
| Q4 关键词回放（analyzer.go:502-511） | `Aggregate → Append` | **2026_09 走 `Index Scan using ..._task_type_profile_ts_idx`（Index Cond: task_type='reasoning' AND ts>=...）**，hot/其余分区 Seq Scan；SubPlan 同 Q3 |

**Seq Scan 风险评估**：Q2/Q3 的 ts-only 谓词在父表只能 Seq Scan 月分区（当前 22.7k 行，代价 ~1.3k，毫秒级，无风险）；Q1/Q4 的 request_id/task_type 谓词已命中索引。数据量线性增长时 Q2/Q3 全分区扫描随之增长——当前规模非缺陷，记录为观察项。

## 4. 只读计数

```
SELECT count(*) AS total, count(*) FILTER (WHERE agrees=false) AS disagrees,
       count(*) FILTER (WHERE agrees=true) AS agrees_true FROM task_type_corrections;
→ total=0, disagrees=0, agrees_true=0        （按 auto/human 分桶同样 0 行）

SELECT (SELECT count(*) FROM auto_route_selections_hot) AS hot_total,
       (SELECT count(*) FROM auto_route_selections)     AS parent_total,
       (SELECT count(*) FROM auto_route_selections_all) AS all_total;
→ hot=3, parent=22604, all=22607

近 30 天按 tier×classifier： hot  heuristic_v2=2, llm_v2=1
                          parent heuristic_v2=22561, llm_v2=43
全表 classifier 词表：      heuristic_v2=22563, llm_v2=44, 'heuristic'=0
ts 范围：min=2026-09-07 09:35, max=2026-09-26 00:01（parent 止于 09-20 13:04，hot 仅 23:55–00:01 三行——写入断档与当晚新写入并存，另见 §6 观察）
task_type 全表分布：chat=12858, creative=4985, code=3491, reasoning=1269, planning=4
reasoning 的 domain_hint 非空值仅 general(957)
tuning_params 全表 0 行（四键皆缺 → analyzer 代码默认 thresholds.llm_confidence=0.70, analyzer.go:533）
```

## 5. readonlyDemoSteps 只读演示（原式实跑）

步骤 1（Q1 原式，30d/LIMIT 5000）→ **0 行**。步骤 2（Q2 原式）→ **0 行**。步骤 3（tuning_params 四键）→ **0 行**（默认生效）。步骤 4a（pair 聚合+类型闸门+主导 hint）→ **0 行**。步骤 4b（阈值候选覆盖率）→ **0 行**。步骤 5a（Q3 原式 [0.65,0.70)）→ `would_touch=0, would_fix_proxy=0`。步骤 5b（Q4 原式 reasoning/general）→ `matched=0, matched_corrected=0`。

→ 闸门判零草稿：无 keyword_add（corr 空 + 泛词黑名单），无 threshold_change（minGlobalCorrected=10 不满足）。**演示输出即「零草稿」这一有效结论。**

步骤 6（可选只读管理面）：245 `GET /api/admin/auto-route/tuning/proposals?status=pending` → **HTTP 401 `authentication required`**（路由已注册、鉴权墙在位；未持 token，仅只读探测，未 POST）。

**readonlyDemoSteps 4a SQL 自身缺陷（须登记）**：原式 `pair AS (SELECT auto_t, human_t, ...)` 引用了 `corr` 中不存在的列名（corr 未做 auto_t/human_t 别名），252 实跑报 `column "human_t" does not exist`。本轮按语义最小修正为 `c.auto_task_type/c.human_task_type` 后跑通（口径不变，corr 为空故结果仍 0 行）。

**诊断变体（非源码 SQL，仅演示读面失配代价）**：
- 词表对齐 `classifier IN ('heuristic','heuristic_v2')` 的分母：chat=12848, creative=4968, code=3483, reasoning=1260, planning=4。
- heuristic_v2 行的置信度候选覆盖：v=0.85→coverage 0.844, 0.80/0.75/0.70→0.844, 0.65→0.834, 0.60→0.815, 0.55/0.50→0.814（total=22563）。

## 6. 发现清单

- **F1（读面词表失配，阻断级）**：`taskprofile/analyzer.go:392,429,484,509` 过滤 `classifier='heuristic'`；写入侧 `autoroute/decision_v2.go:385` 追加 `_v2` 后缀；252 全库 22607 行 0 命中。P1 闭环在 V2 写入路径数据上永远读不到样本/分母/回放命中。修复方向：analyzer 四处与 `admin/annotation_handler.go:349` 对齐为双词表 `IN ('heuristic','heuristic_v2')`（或经迁移归一）。本轮只读，未改代码未改库。
- **F2（标注零启动）**：`task_type_corrections` 0 行——闭环冷启动缺口依旧，无人工标注则永远零草稿（与 F1 叠加为双保险零）。
- **F3（参数零启动）**：`tuning_params` 0 行，四键走代码默认；闭环首次 generate 时 oldV 全部取默认值。
- **F4（观察项）**：selection 写入存在 09-20 13:04 → 09-25 23:55 断档，hot 仅存今晚 3 行（heuristic_v2×2/llm_v2×1，chosen_model=glm-5.2/deepseek-v4-flash）；与 245 蓝绿切换史一致，供下轮运维核对，非本轮结论。
- **F5（文档缺陷）**：readonlyDemoSteps 4a 演示 SQL 列名缺陷（见 §5），已在本文修正留档。

## 7. 纪律声明

本轮 252 上执行的全部语句：SELECT / EXPLAIN / EXPLAIN ANALYZE（一次，LIMIT 100）/ psql 元命令（\d+、\echo）。未执行 generate（`POST /api/admin/auto-route/tuning/proposals/generate`）、未执行 apply（proposals/:id/approve 及任何 tuning_params UPDATE）、未发任何写流量到网关；`wroteGenerate=false, wroteApply=false`。245 仅一次无凭据 GET（401）。远端与本地临时 SQL 文件均已删除。

## 8. 未覆盖（本轮未做，留待后续）

- **F1 修复未做**：analyzer 四处词表（`taskprofile/analyzer.go:392,429,484,509`）本轮一行未改——只读纪律；修复（对齐 `admin/annotation_handler.go:349` 双词表 `IN ('heuristic','heuristic_v2')`）及回归属后续执行轮。
- **apply 链路未演练**：generate/apply 均未执行，P1「提案→回放→批准→生效」的写入侧端到端仍缺；管理面仅一次无凭据 GET（401），未验证带凭据的 proposal 列表/详情读取。
- **EXPLAIN ANALYZE 仅一次**（视图 LIMIT 100）：Q1–Q4 只有纯 EXPLAIN 计划、无实测耗时；Q2/Q3 Seq Scan 随数据量增长的趋势无压测数据。
- **观察窗口单薄**：hot 仅今晚 3 行，`_all` 的近期代表性受 F4 断档影响；tier 过配率等运行基线在本轮无从建立（该工作归 P2 影子期，见 `docs/planning/AUTO_ROUTING_V2_P2_TIER_SELECTOR_DESIGN.md`）。
- **154 节点未探测**：本轮取证仅覆盖 252（PG）与 245（管理面 401 探测）。
- **4a SQL 修正只在本文件留档**：未回写 readonlyDemoSteps 的出处文档。

**复核记录（同轮独立复核席）**：四项特别核对（NewTierSelector 零生产构造、两套 tier 词汇分列、analyzer 已用 `_all`、本文件存在且无密码/无写语句）全部独立复现成立；本文件 F1 关键引用逐条抽核属实（`autoroute/decision_v2.go:385` 的 `Classifier: cls.Classifier + "_v2"`、`admin/annotation_handler.go:349-350` 双词表、analyzer 四处过滤行号）。复核纠正一处引用精度：658 迁移特征列为 **15** 列（非此前一处口径的 14）。

## 附录：命令清单（脱敏，无密码/DSN）

1. `ssh 252 "docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway -c '<SELECT 身份/视图/计划/计数>'"`（§1–§4 各节原句）
2. `EXPLAIN SELECT * FROM auto_route_selections_all;` / `EXPLAIN ANALYZE ... LIMIT 100;`
3. `EXPLAIN <Q1|Q2|Q3|Q4 原式，参数 30/5000/0.65-0.70/reasoning/finance>`
4. 演示 SQL 经 `scp` 至 252 `/tmp` 后 `docker exec -i pg-252-pg17 psql -U llm_gateway -d llm_gateway -v ON_ERROR_STOP=1 < /tmp/<file>` 执行（步骤1/2/4a/4b/5a/5b + 两个标注"诊断变体"的查询），完毕即删。
5. `ssh 245 "curl -s -w '%{http_code}' 'http://127.0.0.1:8781/api/admin/auto-route/tuning/proposals?status=pending'"` → 401。
