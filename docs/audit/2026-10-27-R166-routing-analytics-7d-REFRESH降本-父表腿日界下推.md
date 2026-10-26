# R166 · routing_analytics_7d REFRESH 降本轮（907 残留探针的结构性收口）

> 会话：ZCode（checkout `llm-gateway-go-4`）。承接 R147 §3.4 第 4 条
> 「routing_analytics_7d（0.96h/天）的降本未被本轮覆盖，量级比 matrix 大，建议下一轮单独分解」。
> **对生产的写入 = 0**（252 全程只读：pgss / pg_get_viewdef / EXPLAIN ANALYZE（只读 SELECT）/
> 目录表查询；EXPLAIN ANALYZE 与 R147 §3.2 同款先例）。
> 落地面 = 仓库内迁移 910（未应用，pending；上线由部署列车执行，属主授权面）。

## §1 结论（TL;DR）

1. **读数推进**：R147 的 4.97h/293 次/61.0s 均值是 10-24（907 应用前）的 pgss 截面。
   907 已于 **10-24 23:07:42+08** 应用 252（schema_migrations 实查）。以 R147 读数为基线做
   pgss 增量：**907 后均值 ≈ 40.6s**（Δ158 次 / Δ1.782h）⇒ 907 的 LATERAL 换形在生产兑现
   **1.5×**（61.0 → 40.6s），方向正确但 REFRESH 仍是单 stmt 最大头（现值累计 451 次 / 6.752h）。
2. **EXPLAIN 分解（252 生产，只读）**：907 形态的剩余成本 = **父表腿 ~2.75 万次/刷新 的
   LATERAL 逐行探针**——TIMING ON 实测探针节点 `actual time=0.366ms × 27,486 loops ≈ 10.1s`
   （57%）+ hot 腿 0.249ms × 3,208 ≈ 0.8s；整计划 **~147K buffers**（父表腿 133,827 =
   27,526 次 × 4 页 btree 下降）。聚合层本身很轻（28K 行输入 / 2,277 组 / quicksort 3.5MB /
   零外排 / credentials SubPlan 仅 2,824 loops）。
   ★ **探针单次成本随宿主负载漂 100×+**（R96 轮 10-24 实测 ~3μs/次 vs 本轮 366μs/次，同宿主）
   ⇒ 907 形态把 REFRESH 墙钟**乘法式**暴露在宿主负载上。
3. **修法（迁移 910，零语义）**：父表腿 `LEFT JOIN LATERAL (... LIMIT 1)` 改
   **普通 `LEFT JOIN` + `dd.partition_date >= (now() - interval '8 days')::date` 日界下推**
   ⇒ details 侧执行期分区裁剪（躲开 0 行但堆 383MB 的 09 分区）+ Hash Join 可选
   ⇒ 整计划 buffers **146,423~147,079 → 51,425~52,664（−64%，多轮稳定）**；
   墙钟暖态低负载 8,531/7,354ms → 7,183/6,064ms、高负载态 27,397ms → 4,970ms
   （§143.28 口径：墙钟只作时点，**可承诺的是 buffers −64%**）。
   hot 腿原样保留 LATERAL LIMIT 1（details_hot 无唯一键证明）。
4. **口径等价（252 生产，同一事务同一快照）**：窗口内父表腿双向 EXCEPT ALL（66,838 行）
   **0/0**；等价性前提 = ukey 唯一键（catalog 实测 `session_turn_details_request_id_key`）
   + 日界谓词被 `t.ts >= now()-7d AND dd.partition_date = t.partition_date` 蕴含（错位行实测 = 0）。
5. **落地面**：迁移 910（up/down）+ `db.go` 唯一真源换形 + 门禁体系按 §302 全集同步
   （db 形态门改 910 形态 / 真库口径门夹具改 now() 相对日期 / 真库计划门改 910 判据 /
   **语义耦合门从 907 移交给 910**、907 的耦合门改判为**回退链门**（910.down ≡ 907.up）/
   migration_910 契约门 8 坏样本阳性对照）+ 7 点投递面 + 台账 910 行（pending）。
   本地一次性真库五段生命周期全绿（up / 幂等 / OID 保持 + REFRESH 联动 / down→907 形态 /
   down 幂等 / 复原）；installer 全模块 + db + startup 迁移包全绿。
6. **拍板材料（未拍板，本轮不动手）**：㉙(d) 刷新器分族抬 work_mem——对 routing_7d 族
   **不是杠杆**（temp 仅 0.46GB/451 次 ≈ 1.1MB/次，无外排），它救的是 matrix 三兄弟
   （131.6MB temp/次）；对 907 后 REFRESH 均值 40.6s 的**残余结构**（见 §8）。

## §2 读数（252 只读，pgss 1.11 / dbid=16384）

pgss 自 10-18 22:03 reset 起累计（本轮读取时点 2026-10-27）：

| stmt | calls | total | mean | max | read | temp | rows |
|---|---|---|---|---|---|---|---|
| REFRESH CONCURRENTLY public.routing_analytics_7d | 451 | 6.752h | 53.9s | 179.5s | 59.39GB | 0.46GB | 1,887,337 |
| REFRESH CONCURRENTLY public.routing_audit_summary_7d | 436 | 0.733h | 6.1s | 86.2s | 0.26GB | 0 | 819 |

- R147（10-24 读）基线：293 次 / 4.97h / 61.0s / max 179.5s。
- Δ = **158 次 / 1.782h ⇒ 907 后均值 ≈ 40.6s**；Δ窗口（10-24 23:07 → 本轮）× 30min 节奏
  ≈ 124~156 次预期，实测 158 ✓（30min RefreshInterval 对拍吻合；4095-4108 的
  stale-definition gate 快路径 + ensure 补账解释 ±10%）。
- max 179.5s 与 R147 读数**相同** ⇒ 907 后没有任何一次刷新超过 907 前的最坏值。
- 冷读分量：59.39GB/451 ≈ **132MB/次**（滚动 7 天窗的窗口旋转页）；temp 1.1MB/次 ⇒
  **work_mem 不是本族的杠杆**（对照 matrix 131.6MB/次）。

## §3 EXPLAIN 分解（907 现行形态，252 生产只读）

`EXPLAIN (ANALYZE, BUFFERS, TIMING OFF)`（热态，read=0 全 hit）：

| 组件 | 读数 | 说明 |
|---|---|---|
| 父表腿探针（Limit→Append→Index Scan × 27,486 loops） | hit 110,104（**4.006 页/探针**） | TIMING ON：**0.366ms/loop ≈ 10.1s（57%）** |
| hot 腿探针（× 3,208 loops，全部 0 命中） | hit 10,411 | 0.249ms/loop ≈ 0.8s；details_hot 有 3,237 行但与窗口 turns 0 匹配（R96 已登记的覆盖率行为，不动） |
| turns 侧（hot Seq Scan + 2026_10 ts_idx 扫） | hit 26,378 | 窗口 27.5K 行 + filter 剔 39.5K |
| Sort（GROUP BY 7 键） | quicksort 3,525kB **内存内** | 无外排（对照 matrix external merge） |
| GroupAggregate + percentile ×3 | 28,312 行 → 2,277 组 | 28K 行量级 ⇒ 聚合不是大头 |
| credentials SubPlan 1 | 2,824 loops / hit 6 | 可忽略 |
| **整计划** | **hit 147,079 / read 0** | 变异（重跑）17.7~27.4s ⇒ 墙钟被负载主导 |

★ 探针成本的可比锚点：**同一形态 R96 轮实测 ~3μs/探针（10-24）**，本轮 366μs/探针
（10-27，load 23/4 核）⇒ 逐行探针的墙钟贡献 = **探针次数 × 随负载漂移的单价**，
这是比「计划选择对不对」更本质的结构暴露。

## §4 候选修法与同缓存态 A/B

**候选（= 910 形态）**：父表腿普通 LEFT JOIN + 日界下推（其余逐字不动）。

| 臂 | buffers（整计划） | 墙钟 |
|---|---|---|
| 现行（907 形态） | 146,455 / 146,423 / 147,079（3 轮） | 8,531 / 7,354 / 27,397ms |
| 候选（910 形态） | 52,495 / 52,492 / 51,425（3 轮） | 7,183 / 6,064 / 4,970ms |
| **Δ** | **−64%（稳定）** | 暖态低负载 ~1.3×；高负载态 ~5.5× |

- 候选计划：父表腿 = Hash Right Join（Batches:1 / 3,847kB 内存），details 侧 =
  `session_turn_details_2026_10_pkey` **日界范围扫**（94,730 行 / 14,423 hit + 1,239 read）
  + 2026_11/default 空分区 Bitmap（Subplans Removed: 1 ⇒ **09 分区被裁掉**）。
- 墙钟两次高读数（27.4/17.7s）发生在宿主高负载时段，与 pgss 均值 40.6s 一致；
  交错轮（低负载段）差 1.3×。⇒ 按 §143.28/§143.29 纪律：**承诺 buffers −64%，
  不承诺秒数**；探针单价被负载放大的时段收益最大（27.4s → ~5s）。
- ✗ **不选**：再给 details 建覆盖索引（907 已实测白建）；抬 work_mem（无外排可消）；
  收窄窗口/增量刷新（语义变更，属主）。

## §5 口径等价（三前提 + 生产对拍）

1. **普通 JOIN ≡ LATERAL LIMIT 1**：`session_turn_details_request_id_key` =
   UNIQUE (tenant_id, request_id, partition_date)，建在**分区父表**并按分区传播
   （catalog 实查）⇒ 等值三键最多命中 1 行。生产窗口 66,838 行：join 后行数
   **66,838 = 窗口行数**（无增殖）。★ hot 腿不适用：details_hot 的索引是非唯一
   request 索引 ⇒ LATERAL LIMIT 1 保留。
2. **日界下推 ≡ 无日界**：`dd.partition_date >= (now()-8d)::date` 被
   `dd.partition_date = t.partition_date AND t.ts >= now()-7d` 蕴含
   （8 天 = 7 天窗 + 跨日边界余量）。蕴含唯一可破的情形 = 「ts 在窗内而
   partition_date 早于 8 天前」的错位行 ⇒ 生产实测 **count = 0**。
   （⚠ R96 夹具里 r8 是刻意错位行 ⇒ 910 夹具改为 partition_date = date(ts)，
   与生产不变量一致。）
3. **生产对拍**：窗口内父表腿双向 EXCEPT ALL（同一事务同一快照，66,838 行）：
   `only_current=0 / only_candidate=0`。

## §6 落地面（迁移 910 + 门禁 + 注册）

- **迁移**：`sql/migrations/startup/910_routing_analytics_source_hashjoin.sql`
  （CREATE OR REPLACE VIEW，lock_timeout=60s / statement_timeout=2min，903 系同款）
  + `.down.sql`（**逐字采用 907.up 的视图体**作回退形态）。
- **db.go**：`RoutingAnalyticsSourceViewDDL` 换 910 体（唯一真源）；两处
  stale-definition gate 特征串认 `session_turn_details` 关系名 ⇒ 910 形态天然成立
  （无启动期 CASCADE 重建），注释补 910 说明。
- **§302 形态全集同步**（动门前 grep 的全部落点）：
  - `db/routing_analytics_source_lateral_test.go`：形态门改 910 判据（父表腿普通
    JOIN + 日界在场、hot 腿 LATERAL LIMIT 1、canonical/v1 禁引、15 列契约、数字守卫）；
    **夹具日期全部改 now() 相对值**（写死日期会被日界谓词漂出窗外 ⇒ 假红）；
    真库计划门改 910 判据（**只允许 1 个 Limit 节点**——907 回归会出现第 2 个；
    Index Cond 逐节点归属核对：hot 探针三键 / 父表腿 pkey 范围扫只认 partition_date）；
    语义耦合门改钉 **910.up ≡ Go 常量** + **910.down ≡ 907.up（回退链）**。
  - `migration_907_..._test.go`：形状/冻结/down-canonical/embeddata/7 点投递面全部保留
    （907 文件已 applied 冻结、断言照旧成立）；**唯一失效的逐字耦合门改判**为
    `TestMigration907_RollbackChainBy910`（910.down ≡ 907.up）——
    ★ 这是「上一代迁移的耦合门在新代应用后如何处置」的第一个仓内先例。
  - `migration_910_..._test.go`（新，契约门）：检测器 + **8 坏样本阳性对照**
    （父表腿回退 LATERAL / 去日界 / 回 canonical / 去 lock_timeout / 加索引 /
    少 auto_profile / 去数字守卫 / 去 join 键）+ sha256 冻结 + embeddata 逐字节 + 7 点投递面。
- **7 点投递面**：embeddata ×2 / installer main.go（go:embed + map）/ dbinit runner.go
  StartupFiles / `apply-db-revision-sequence.sh` / `installed_startup_migrations.tsv`
  （row 312）/ `docs/db-changelog.md` 910 行（pending）。
  ★ **两处次序陷阱当场被抓**：runner.go 与 sequence.sh 都按**列表序**生效
  （runner 的 manifest 门按位置逐项对 TSV；sequence.sh 按序应用）——910 初版插在
  907 后 ⇒ manifest 门红（entry 310 错位）⇒ 移到 909 后全绿。**「登记了」≠「登记在正确的位置」**。

## §7 验证读数

| 层 | 结果 |
|---|---|
| `go build ./...`（主模块 + installer 模块） | ✓ |
| `go vet ./db/ ./sql/migrations/startup/` | ✓ |
| migration 契约门（907 全套 + 910 新门） | ✓ |
| db 形态门 / 真库口径等价门 / 真库计划门（本地一次性 scratch，`ROUTING_ANALYTICS_GATE_TEST_DATABASE_URL`） | ✓（口径等价 EXCEPT ALL 0/0 + 计划 1×Limit） |
| 本地一次性真库五段生命周期（itgate_910，退出即 DROP）：建族 → 907 up → 910 up → 幂等重跑 → OID 保持 + REFRESH 联动 → down（=907 形态 2×LATERAL 无日界）→ down 幂等 → 复原 up（1×LATERAL+日界）→ 15 列契约（auto_profile 末位）→ 行集非空 | 全绿 |
| installer 全模块（含 startup 并发门 / manifest 门） | ✓ |
| `go test ./db/ ./sql/migrations/startup/` | ✓ |
| 全模块回归 | 见 §7.1 |

### §7.1 全模块回归

`go test ./... -count=1`（342 包 ok / 2 红均为**已归类的负载型计时 flaky 族**，单跑复绿）：

- `domains/nodestatecache TestSelectP99Under1ms`：p99=1.187ms vs 门 1ms（余量毫秒级）⇒
  安静单跑 **ok（1.6s）**——与 handoff §19.28 登记的「计时断言余量 ~3ms 负载型判定」同族；
  本轮改动与该包零耦合（不 import、不共状态）。
- `scripts/ursmcheck`：全量负载下 10m 超时 panic（该包单独跑就要 ~648s）⇒
  安静单跑 **ok（648s）**——与 §143.21.9 已登记的「availability 用例只在全量负载下失败」
  同族（flaky 本身无法被门证明，只做文字登记）。

### §7.2 本轮自身踩坑（如实记）

1. **runner.go / sequence.sh 插入位置**：两处都是**列表序生效**（manifest 门按位对
   TSV / sequence 按序应用），910 初版插在 907 后被 manifest 门当场抓红 ⇒ 移到 909 后。
   「登记了 ≠ 登记在正确的位置」。
2. **真库计划门第一版把「Index Cond 三键」一刀切到所有 Cond 行** ⇒ 被候选计划自己的
   父表腿 pkey 范围扫（单键 partition_date）抓红 ⇒ 改为逐节点归属核对。
   ★ 量具的第一版把候选形态当成了坏形态——「门红了先读门在守什么，再判是它错还是它对」。
3. zsh 不对未加引号的变量做分词（`P="psql -h ..."` 后 `$P -f x` 报 command not found）
   ⇒ 本地生命周期脚本改用函数传 `"$@"`。

## §8 拍板材料（本轮只给材料，不替拍板）

1. **㉙(d) 刷新器分族抬 work_mem**（bg/materialized_view_refresher.go 的分族接线点，
   R75 分族 statement_timeout 同款零语义改动）：对 **matrix 三兄弟**是实杠杆
   （temp 131.6MB/次 → 0，est −34%，R147 §3.3 已测）；对 **routing_7d 族不是杠杆**
   （temp 1.1MB/次）。若属主拍板，建议只对 matrix 分族接线。
2. **残余结构（910 应用后的下一层）**：REFRESH ≈ SELECT（910 后 ~52K buffers）
   + REFRESH CONCURRENTLY 快照/diff + 冷读。若属主要继续压：
   - **增量刷新**（hourly bucket 只重算近窗）是唯一治本形态（matrix (c) 同款），
     结构变更（属主）；
   - **节奏**：RefreshInterval=30min × ~48 次/天 × ~52K buffers；降频/错峰是运维决策；
   - hot 腿 0 命中探针（3.2K 次/刷新全 0 匹配）属 R96 已登记的 details 覆盖率行为，
     修它 = 修覆盖语义（属主，另一条线）。

## §9 诚实边界

1. 全部生产读取只读；EXPLAIN ANALYZE（只读 SELECT）共 5 次（现行 3 + 候选 2），
   与 R147 §5.1 同款先例；每次 ~18-30s DB 时间。
2. 「907 后均值 40.6s」由 pgss 累计差推导（pgss 1.11 无时间窗），窗口两端读数取自
   R147 档与本轮；Δcalls 158 与 30min 节奏的预期吻合（±10% 内），无直接逐次证据。
3. 冷读分量的下降（132MB/次 → 预期 ~50MB/次量级）是 buffers −64% 的推论，
   **未被本轮单独量化**（无法在生产区分 cold read 归因）。
4. REFRESH CONCURRENTLY 的 diff/apply 开销未单独分解（pgss 只有整语句）；
   本轮 A/B 在 SELECT 层做，REFRESH 层的兑现依赖「SELECT 是 REFRESH 的主体」，
   与 40.6s（REFRESH）vs 17-27s（SELECT）的量级一致性自洽。
5. 迁移 910 未应用生产（pending）；本仓对生产的写入 = 0。台账 910 行按
   「registered（未应用，pending）」记账，应用状态由部署列车回填。
6. `admin/auto_route.go` 等 4 个 Go 消费方只读 source 的 15 列契约 ⇒ 契约未动
   （列名/类型/次序 15 列对拍 ✓），消费方零改动。
