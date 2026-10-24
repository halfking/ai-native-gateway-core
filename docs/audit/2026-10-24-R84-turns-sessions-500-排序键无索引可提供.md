# R84 · `GET /api/admin/turns/sessions` 生产恒 500@30s —— 归因、修复、门禁

> 2026-10-24 ｜ 会话 `mvs_bbd1346d211a4eb3ac41b552c82eaecf` ｜ 分支线 `P11-R84`
> 内仓 origin/main 基线 `589851004` ｜ 生产 154 active `2833-dd212c9b`（端口 8782）
> **本轮对生产的写入 = 0**（所有生产侧验证都在事务内建索引后 `ROLLBACK`）

---

## 0. 一句话结论

`turns/sessions` 的排序键 `(updated_at DESC, session_id DESC)` 在 `public.sessions` 上
**没有任何索引能提供该序** ⇒ 规划器只能 `Seq Scan + top-N Sort` ⇒ **Sort 必须把 439,106 行
全部物化才能吐出第一行** ⇒ 上层 `Limit` 无法提前停 ⇒ 整条 JOIN 链跑满 32s。
补一条 `public.sessions (updated_at DESC, session_id DESC)` 索引即可：
**32,089.911 ms → 3.639 ms**，且**不改任何一行查询**（口径零变化）。

---

## 1. 症状（生产基线）

2833 发版后 `REPEAT=5`：

| 端点 | 状态码 | min | med | max |
|---|---|---|---|---|
| `turns/sessions` | **500,500,500,500,200** | 30.003s | 30.013s | **54.510s** |

252 的 `pg_stat_statements` 抓到**真实生产 SQL**：

```
calls = 1     total = 24904.6 ms     rows = 21
```

⇒ 单次 24.9s 才返回 20 行。端点超时 ⇒ 500。

---

## 2. 事实底座（252 只读，2026-10-24）

### 2.1 表规模

| 表 | 行数 | 备注 |
|---|---|---|
| `public.sessions` | 439,564 | 49 个 `partition_date`；**只挂 6 个子分区**（2026_07/08/09/10/11 + default）≈ 134 MB |
| `session_dim` | 901,959 | `task_id` 为 NULL 的 322,014 行（35.7%） |
| `session_summaries` | 860,393 | |
| `session_title_states` | 175,975 | |
| `session_titles` | 71,077 | 69,309 个 distinct `scoped_session_id`；`task_id` 只有 3 个值（`default` 42,236 / `auto` 28,840 / 1 个 uuid） |
| `session_analysis_metadata` | **0** | |

### 2.2 索引事实（`pg_index` 逐条读出）

`public.sessions` 的既有索引：

```
idx_sessions_agent_role                    (tenant_id, agent_role, created_at DESC)
idx_sessions_parent_session                (tenant_id, parent_session_id)
idx_sessions_primary_request                (primary_request_id)
idx_sessions_status                        (status, updated_at DESC)          ← 需要 status 等值
idx_sessions_tenant                        (tenant_id, created_at DESC)       ← created_at，不是 updated_at
idx_sessions_tenant_session_partition_date (tenant_id, session_id, partition_date DESC)
sessions_pkey                              UNIQUE (id, partition_date)
sessions_session_id_partition_date_key      UNIQUE (session_id, partition_date)
```

★ **没有一条能按 `(updated_at DESC, session_id DESC)` 出序。**

`session_titles` 只有两条：

```
session_titles_pkey             UNIQUE (task_id, scoped_session_id)
idx_session_titles_generated_at (generated_at DESC)
```

⇒ ★ **`scoped_session_id` 没有最左前缀索引**（这曾是主假设，见 §3）。

---

## 3. ★ 主假设被实测推翻

**主假设**：「`session_titles` 缺 `scoped_session_id` 最左前缀索引 ⇒ 每次 LATERAL 全表扫
（71k 行 × 43.9 万外层行）⇒ 31 亿行 ⇒ 这是 24.9s 的主因 ⇒ 加索引即可。」

**事实成立**（索引确实缺），**但量级不够**。单变量 A/B（`EXPLAIN (ANALYZE, BUFFERS)`，252）：

| 变体 | Execution Time | shared hit |
|---|---|---|
| **A** 原查询（带 `st` LATERAL） | **32,089.911 ms** | 3,918,898 |
| **B** 只去掉 `session_titles` 的 LATERAL | **23,836.829 ms** | 9,876,509 |

⇒ 该 LATERAL 只占 **8.25s（26%）** ⇒ **只补 `session_titles` 索引，端点仍要 ~24s ⇒ 救不了。**

★ 通用式：**「缺失事实」成立 ≠ 「它是主因」** ⇒ 必须做**单变量 A/B** 定量，不能靠机制直觉得出结论。

---

## 4. 真正的成本结构（从 B 的计划逐节点读出）

```
Limit (actual time=23802.700..23802.716 rows=21)
  └─ Sort (top-N heapsort, Memory: 36kB)                 actual 21016.174..21016.188
     └─ Nested Loop Left Join                            actual ...20550.184 rows=439106
        ├─ Append（sessions 六个分区 Seq Scan）            actual ...10575.893
        │  └─ Filter: (partition_date = (SubPlan 2))
        │     └─ SubPlan 2 → Merge Append（6 个分区 Index Only Scan）
        │        loops=439570   Buffers: shared hit=6,363,231      ≈ 10.5s
        ├─ Index Scan session_dim_pkey         loops=439106  hit=1,754,606  ≈ 4.2s
        ├─ Index Scan session_summaries_pkey   loops=439106  hit=1,741,045  ≈ 1.6s
        └─ LATERAL sam（0 行表）                loops=439106
```

| 成本源 | 量 |
|---|---|
| 外层 Append 产出 **439,106 行** | 为拿 21 行把 43.9 万行全 JOIN 了一遍 |
| 去重 SubPlan（`MAX(partition_date)` 跨月去重） | 439,570 次循环 × 6 分区索引扫描 = 6,363,231 hits ≈ **10.5s** |
| `session_dim` JOIN | ≈ **4.2s** |
| `session_summaries` JOIN | ≈ **1.6s** |
| **top-N Sort** | **0.47s** ⇒ ★ **瓶颈不是排序** |
| JIT 编译 | 2.8s（估算成本 1.69M 触发 `jit_above_cost`） |

★ **根因句**：`ORDER BY s.updated_at DESC, s.session_id DESC` 无索引可提供该序 ⇒
`Seq Scan + top-N Sort` ⇒ ★ **Sort 是阻塞点**：它必须把所有行物化才能吐出第一行 ⇒
`Limit` 拿到第一行时，43.9 万行已经全 JOIN 完了。

---

## 5. 方案验证（生产零持久变更）

### 5.1 事务内建索引 → EXPLAIN ANALYZE → ROLLBACK

```sql
BEGIN;
CREATE INDEX idx_sessions_updated_at_session_id_try
  ON public.sessions (updated_at DESC, session_id DESC);
EXPLAIN (ANALYZE, BUFFERS) <生产查询原样>;
ROLLBACK;   -- ⇒ 生产零持久变更
```

结果：

```
Limit (actual time=0.881..3.639 rows=21)          ← 3.639 ms
  └─ Nested Loop Left Join (actual time=0.905..4.044 rows=21)
     └─ Merge Append  Sort Key: s.updated_at DESC, s.session_id DESC
        ├─ Index Scan using sessions_2026_07_updated_at_session_id_idx
        ├─ Index Scan using sessions_2026_08_updated_at_session_id_idx
        ├─ Index Scan using sessions_2026_09_updated_at_session_id_idx
        ├─ Index Scan using sessions_2026_10_updated_at_session_id_idx
        ├─ Index Scan using sessions_2026_11_updated_at_session_id_idx
        └─ Index Scan using sessions_default_updated_at_session_id_idx
```

| 指标 | 修复前 | 修复后 |
|---|---|---|
| Execution Time | 32,089.911 ms | **3.639 ms** |
| shared hit | 9,876,509 | **640**（≈ 15,400×） |
| 去重 SubPlan 循环 | 439,570 | **22** |

### 5.2 「Limit 在哪一层」曾经把我带偏

我一度认为「`Limit` 挂在 Nested Loop 之上 ⇒ 不会提前停 ⇒ 光加索引没用，必须把查询改成
sessions-only 子查询」。**实测否证了这个推理**：`Limit` 节点是**惰性拉取**的，它向子节点要一行、
拿够就停 ⇒ 子链随之停止 ⇒ **只要外层有可用序，`Limit` 在哪一层都不影响提前停**。
真正决定成败的是**外层能不能按序惰性产出**，而那正是索引提供的。

### 5.3 改写形态不带来额外收益 ⇒ **只加索引**

「sessions-only 子查询（内含 `ORDER BY` + `LIMIT`）+ 外层 JOIN」形态实测 **5.838 ms**
⇒ 与「只加索引不改查询」的 3.639 ms 同量级 ⇒ 改写**不带来额外收益** ⇒
**只加索引**：改动面最小、口径零风险。

### 5.4 generic plan 不是阻塞项

`LIMIT $1` 是参数 ⇒ 若 pgx 用命名预处理语句，PG 跑满 5 次后可能切 generic plan（`LIMIT` 值未知）。
实测（`PREPARE` + 6 次 `EXECUTE` 后第 7 次）：**仍是 `Index Scan`**，2.141 ms ⇒ 不是阻塞项。

### 5.5 口径等价性（同一快照内比对）

★ 第一次比对用默认 `READ COMMITTED` ⇒ 两次执行之间新产生了一个会话（`12:13:11` vs `12:22:02`）
⇒ 结果集差 1 行 ⇒ 那是**快照漂移**，不是语义差异 ⇒ 改用
`BEGIN ISOLATION LEVEL REPEATABLE READ` 重测：

```
生产查询行数（含表头）：22 ｜ 改写形态行数：22
✅ 同一快照下结果集逐字一致
```

★ 第一次比对还打出过一句「✅ 逐字一致（**0 行**）」的**假绿** —— 两个查询都语法报错、
落盘文件都是空的 ⇒ 必须加**行数守卫**（两侧都 ≥2 行）才允许下结论。

---

## 6. 落地

### 6.1 迁移 903

`sql/migrations/startup/903_sessions_updated_at_session_id_index.sql`（+ `.down.sql`）：

```sql
BEGIN;
SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '10min';
CREATE INDEX IF NOT EXISTS idx_sessions_updated_at_session_id
    ON public.sessions (updated_at DESC, session_id DESC);
COMMIT;
```

形态照抄 901（分区父表补索引的仓内样板）：普通 `CREATE INDEX`（**分区父表不支持
`CONCURRENTLY`**）+ `lock_timeout=60s` + `statement_timeout=10min`。

### 6.2 登记面（7 点，★ 不是 6 点 —— 模板里多了一个 TSV 镜像）

1. `sql/migrations/startup/903_*.sql`（+ `.down.sql`）
2. `installer/cmd/llm-gw-installer/embeddata/startup/903_*.sql`（**孪生副本，逐字节一致**）
3. `installer/cmd/llm-gw-installer/main.go`：`//go:embed` × 2 + embed map × 2
4. `installer/internal/dbinit/runner.go`：`StartupFiles`
5. `scripts/apply-db-revision-sequence.sh`：生产升级序列
6. `sql/schema/installed_startup_migrations.tsv`：机器可读镜像
   （**由 `cd installer && go test ./internal/dbinit/ -run TestStartupManifest -update` 重生成**）
7. `docs/db-changelog.md`：sha256 登记表

★ 第 6 点是我**按 902 的模板逐条抄下来才发现的** ⇒ 「记得 6 点」这种记忆不可靠，
**照抄同族最近一条迁移的登记表**才可靠。

### 6.3 门（8 条，`sql/migrations/startup/migration_903_sessions_updated_at_idx_test.go`）

| 门 | 守什么 |
|---|---|
| `TestMigration903_UpBodyShape` | 索引名/表/列序/方向逐字；幂等；无 `CONCURRENTLY`；`BEGIN`/`COMMIT` |
| `TestMigration903_DetectorRejectsBadShapes` | **阳性对照**：4 份坏样本必须被判红 |
| `TestMigration903_CarriesLockAndStatementTimeout` | `lock_timeout` / `statement_timeout` 且非 0 |
| `TestMigration903_DownDropsIndex` | down 会删索引 |
| `TestMigration903_EmbeddataCopiesAreByteIdentical` | 孪生逐字节一致 + sha256 与台账一致 |
| `TestMigration903_RegisteredAtAllDeliveryPoints` | 7 点登记面 |
| `TestMigration903_IndexColumnOrderMatchesQuerySortOrder` | ★ **语义耦合门**：索引列序 ↔ Go 侧 `ORDER BY` |
| `TestMigration903_NotInConcurrencyExceptionTable` | 903 不含 `CONCURRENTLY` ⇒ 不该出现在单事务豁免表里 |

★ 语义耦合门防的是**无声退化**：有人改了 `admin/turns_sessions.go` 的 `ORDER BY` 却没同步索引 ⇒
索引仍在位、迁移仍在台账里、端点悄悄回到 32s ⇒ **没有任何门会红**。

### 6.4 变异（8/8 全红，md5 还原）

| 变异 | 红在 |
|---|---|
| M1 删掉 `CREATE INDEX` | `UpBodyShape` |
| M2' 列序颠倒（**只打可执行体**） | `IndexColumnOrderMatchesQuerySortOrder` |
| M3 删 `lock_timeout` | `CarriesLockAndStatementTimeout` |
| M4 摘掉 runner 登记 | `RegisteredAtAllDeliveryPoints` |
| M5 破坏 embed 孪生（+1 字节） | `EmbeddataCopiesAreByteIdentical` |
| M6 改 Go 侧排序键 | `IndexColumnOrderMatchesQuerySortOrder` |
| M7 摘掉 tsv 行 | `RegisteredAtAllDeliveryPoints` |
| M8 `updated_at` 的 `DESC` 丢失 | `UpBodyShape` |

★ **M2 第一版打偏了**：`str.replace(..., 1)` 命中的是**文件头注释**里的同名串，
可执行体没变 ⇒ 形态门正确地忽略了注释（那正是「必须剥注释」的意义）⇒ 门没红。
⇒ 变异必须**精确打可执行体**，否则「门没红」会被误读成「门没牙」。

### 6.5 门自身的两处量具缺陷（★ 红的是门，不是迁移）

1. `TestMigration903_DownDropsIndex` 只认 `DROP INDEX IF EXISTS idx_…`，
   而真实 down 写的是 `DROP INDEX IF EXISTS **public.**idx_…` ⇒ 把**正确**的 down 判红。
2. `TestMigration903_NotInConcurrencyExceptionTable` 读原文，
   而文件头散文里就写着「分区父表不支持 `CREATE INDEX CONCURRENTLY`」⇒ 把正确的迁移判红。

⇒ 两处都是**判据没剥注释 / 形态写窄** ⇒ 修门（不是修迁移）。

---

## 7. 已知边界（如实登记）

1. ★ **带过滤条件的请求仍然慢**：过滤条件（`project_id` / `task_id` / `owner_user` /
   `client` / `tags` / `search`）依赖 `sd` / `ss` 的 JOIN ⇒ `Limit` 没法在 sessions 扫描上
   提前停 ⇒ 本轮只修**无过滤的默认列表路径**（生产上抓到的真实 SQL 正是这一条）。
2. ★ **索引构建期持 SHARE 锁，阻塞 `sessions` 写入**：`sessions` 是热写表
   （`domains/session/v2/session_aggregator.go:216` 的 `upsertSessionSnapshot` 每请求一次
   `INSERT … ON CONFLICT … DO UPDATE`）⇒ 该迁移应落在**低峰窗口**。
   实测构建时长：热缓存 **600ms**；一次冷缓存观测 **≈27s**（此前一条 42s 的全表扫查询
   把页缓存冲掉之后）⇒ ⇒ 两种量级都要有预案。
3. **down 是不可逆回退**：索引一删，慢查询形态立刻回来（32,089.911 ms）。
4. 其余慢端点未处理：`data-lifecycle/stats` 15.9s 中位、`filter-options` 9.1s、
   `session-overview` 3.5s、`inspector-stats` 2.3s。

---

## 8. 可复算清单

```bash
# 单变量 A/B（252，只读）
scp /tmp/kx-ab.sql 252:/tmp/ && ssh 252 'docker cp /tmp/kx-ab.sql pg-252-pg17:/tmp/ && \
  docker exec pg-252-pg17 psql -X -U llm_gateway -d llm_gateway -f /tmp/kx-ab.sql'

# 事务内建索引 + EXPLAIN ANALYZE + ROLLBACK（生产零持久变更）
scp /tmp/kx-t2b-252.sh 252:/tmp/ && ssh 252 'bash /tmp/kx-t2b-252.sh'

# 同快照口径等价
scp /tmp/kx-equiv2-252.sh 252:/tmp/ && ssh 252 'bash /tmp/kx-equiv2-252.sh'

# 索引/表规模事实底座
scp /tmp/kx-facts-252.sh 252:/tmp/ && ssh 252 'bash /tmp/kx-facts-252.sh'
```

---

# 附录 A · R85：主查询修好后，**第二条腿**又吃掉 21s（同一端点的 enrich 腿）

## A.1 症状

R84 发版 **2834**（迁移 903 已应用）后，端点从 `500@30s` 变成 **200 但仍要 17~24.7s**（中位 18.1s）。

★ 这正是 LESSONS **§337** 说的那件事：**一条端点有多条腿，修完一条不等于转绿**。

## A.2 归因：pg_stat_statements 增量差分（比「看累计 total」可靠）

★ 方法：252 上 `pg_stat_statements` 快照 → 打端点 3 次 → 再快照 → **按 queryid 求差**。
★ 为什么不用累计值：那张表是「上次 reset 以来」的累计量，混着别的工作负载，
★ 单次读数没有可比性（LESSONS §334/§340 同款纪律）。

| 腿 | 每次耗时 |
|---|---|
| **`SELECT DISTINCT ON (t.session_id)`（api_key enrich）** | **20,176.0 ms** ← 端点的全部 |
| `SELECT s.session_id, s.tenant_id, …`（主列表查询） | **8.3 ms** ✔ 索引生效 |
| `SELECT t.session_id, t.turn_no, …`（轮次加载） | **4.2 ms** ✔ |
| `SELECT DISTINCT ON (hl.new_session_id)`（handoff enrich） | 0.2 ms ✔ |

⇒ 主查询 32s → **8.3ms** ✔（迁移 903 生效）；**剩下的 ~20s 全在 enrich 腿**。

## A.3 根因

enrich 腿的 source 是 `db.SessionFamilyTurnsSourceSQL()`：

```
session_turns_hot  UNION ALL  session_turns（父表的**全部历史分区**，~1M 行）
```

而 **session 谓词在 source 外部**（`WHERE t.session_id = ANY($1)` 作用在外层 `t` 上）
⇒ 规划器只能把整条 UNION ALL **物化**后再 Hash Join。

## A.4 单变量 A/B（252 EXPLAIN ANALYZE，同 21 个**真实** session_id）

| 变体 | Execution Time | 计划 |
|---|---|---|
| **A** 谓词在 source 外部 | **11,478.486 ms** | Hash Join，估算 **1,587,817** 行 |
| **B** 谓词推进两条腿内部（`t.session_id = ANY($1)`） | **6.369 ms** | Hash Join，估算 **1,070** 行 ⇒ 走 `Index Scan using idx_session_turns_hot_tenant_session_turn` |

⇒ **~1,800×**。（A 用的是最小投影；真实生产 SQL 因为投影更宽，实测 20,176 ms。）

## A.5 落地

1. 新增 `db.SessionFamilyTurnsForSessionsSQL()`（**数组版**）：
   投影与 `SessionFamilyTurnsSourceSQL()` **逐字同源**（同一 `sessionFamilyProjection(true)`），
   唯一差别是两条腿各带一个 `WHERE t.session_id = ANY($1)`。
   ★ 既有 `SessionFamilyTurnsForSessionSQL()`（单 session 版，`= $1`）**不动** ⇒ 其它调用方零影响。
2. `admin/turns_sessions.go` 的 enrich 查询改用它。
   ★ **JOIN 条件（`rl ON rl.request_id = t.request_id`）逐字不变** ⇒ 入选行集合不变。

## A.6 门（3 条）+ 变异（4/4 全红）

| 门 | 守什么 |
|---|---|
| `TestR85_EnrichSourcePushesSessionPredicate` | 谓词**两条腿各一次**（少一次 = 只推一半 ⇒ 端点仍慢且不报错）／投影与全量 source 逐字一致／数据面未被换掉 |
| `TestR85_EnrichSourceDetectorRejectsBadShapes` | 阳性对照：3 份坏样本 |
| `TestR85_TurnsSessionsEnrichUsesPushedSource` | 调用点用新 helper；JOIN 条件不变；外层谓词仍在 |

变异：M1 调用点退回全量 source｜M2 只推进一条腿｜M3 投影被改｜M4 JOIN 条件被改 ⇒ **4/4 全红在目标门**。

★ ★ **M4 揭穿了门自己的一个洞**（本次最值钱的收获）：
★ 门最初用 `strings.Contains(enrich, "rl ON rl.request_id = t.request_id")` 判 JOIN 条件
★ ⇒ **对「后面追加 `AND rl.tenant_id = t.tenant_id`」的变体照样匹配** ⇒ **门假绿**
★ ⇒ 改成带终止符的正则 `rl ON rl\.request_id = t\.request_id\s*\n` 后 M4 才真正变红。
★ ⇒ ★ **「用 `Contains` 断言「某个片段没有被追加条件」」是一个通用陷阱**：
★　子串匹配对**追加**是宽容的 ⇒ ★ 断言「逐字不变」必须带**终止符**。

## A.7 已知边界（如实登记）

1. ★ **`?api_key_id=` 过滤路径仍用未推进的全量 source**（`buildTurnsSessionWhere` 里的 EXISTS 子句）。
   它是**带过滤**的路径（谓词按外层行相关），本次未改 ⇒ 属主/后续轮处理。
2. ★ **带过滤条件的请求仍然慢**（同 R84 §7.1）：过滤依赖 `sd`/`ss` 的 JOIN ⇒ `Limit` 无法提前停。
3. `cmd/tools/sql_source_indirection_audit` 的 `TestDocumentedSnapshotMatchesMeasurement` **在 origin/main 上就是红的**
   （文档 142 → 实测 145，+3）⇒ 用 origin/main 独立 worktree 复跑确认**不是本次改动造成**
   （把本次两个文件拷进该 worktree 后数字逐字不变）⇒ 按「同文件不要两个写者」纪律**未动它**。

---

# 附录 B · 生产读数对照（154，端口按探测取值；蓝绿在 8781/8782 间轮换）

| 时点 | release | 端点 | 状态码 | min | med | max |
|---|---|---|---|---|---|---|
| 发版前（2833） | 2833-dd212c9b | `turns/sessions` | **500,500,500** | 30.005s | 30.005s | 30.005s |
| 2834（R84：迁移 903） | 2834-f3b63e6e（8781） | `turns/sessions` | 200×5 | 17.039s | 18.112s | 24.728s |
| 2835（R85：enrich 腿） | 2835-51c27369（8782） | `turns/sessions` | **200×5** | **0.044s** | **0.049s** | 0.528s |

⇒ ★★ **完整演进：500@30.005s → 18.1s（只修主查询）→ 0.049s（中位）**
⇒ ★★ **相对「只修主查询」的那一步 ~370×；相对最差读数 ~600×**（30.005s / 0.049s）。

## B.1 其余慢端点回归对照（2835，同一次读数）

| 端点 | 状态码 | min | med | max | 与本轮前的基线 |
|---|---|---|---|---|---|
| `turns/sessions` | 200×3 | 0.048s | **0.051s** | 0.099s | ★ 本轮修复 |
| `turns/sessions/filter-options` | 200×3 | 3.544s | 4.264s | 6.947s | 独立根因，未变 |
| `dashboard/session-overview` | 200×3 | 2.752s | 3.097s | 3.625s | 无回退 |
| `sessions/inspector-stats` | 200×3 | 2.023s | 2.331s | 2.360s | 无回退 |
| `data-lifecycle/degradation/status` | 200×3 | 5.015s | 5.054s | 5.649s | ★ **过渡窗口**（见 B.2；稳态是 **3ms**） |

## B.2 ★ `degradation/status` 的 5s：**不是本轮回退**，是已登记待办项在显形

读数（2835）：`ttl_stats.sessions_count = 1,262,040`（完整值）、`session_keys_count = 374,480`、
`snapshot_at = 13:20:50`、`snapshot_age_seconds = 61`、`partial = None`、
**`degraded_reason` 不含 `"ttl stats unavailable"`** ⇒ ★ **R83 的修复仍然生效**。

`degraded = true` 的真正原因是**备份摘要腿**：`"backup summary incomplete: 备份摘要正在后台重算，请稍后重试"`。

读代码定位（`domains/dbdegradation/file_reader.go` `GetBackupSummary`）：
- ① 快路径：缓存命中且指纹一致且未过期 ⇒ 零 IO（**3ms 的那个读数就是这条**）；
- ② 过期但指纹一致 ⇒ 返回旧值 + 后台刷新（快）；
- ③ **指纹变了** ⇒ 旧值口径不可信 ⇒ **就地算**（受请求 ctx 限制 ⇒ 5s 预算被截断 ⇒ partial ⇒ 报错）。

⇒ ★ 备份集**指纹每天/每次写入都会变** ⇒ ⇒ 周期性落进 ③ ⇒ 端点周期性 5s + degraded
⇒ ⇒ ★★ **这正是已登记的待办项「per-file 增量缓存未做 ⇒ 每次指纹变都触发一轮 ~13min 全量重算」**
（属主待办清单第 5 条）⇒ ★ **不是 R84/R85 引入的回退**：本轮两个改动
（`sessions` 新索引 + enrich 腿谓词推进）**都不触及 dbdegradation 包**。

⇒ ★★ **稳态复测（同端点，间隔 25s 连打 4 次）**：

```
13:22:29  200  0.003
13:22:54  200  0.003
13:23:19  200  0.003
13:23:44  200  0.003
```

⇒ ★★ **3ms（与 R83 一致）** ⇒ ⇒ 上表那 5.015~5.649s 是**「指纹变化后的过渡窗口」**：
指纹一变就落进 ③ 号路径（就地算，被 5s 预算截断）⇒ 等后台刷新跑完（~13min）
⇒ 缓存写回 ⇒ 回到 3ms ⇒ ⇒ **周期性掉到 5s** ⇒ ⇒ **同一个待办项**
（「per-file 增量缓存未做」）⇒ ★ **不是回退**。

⇒ ★ 另注：`sessions` 新索引的**构建期 SHARE 锁**（热写表）实测未造成可见影响
（发版窗口 185s、切换 12s、healthz/readyz/凭据冒烟全部通过）。
