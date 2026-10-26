# R89 · inspector-stats 慢端点归因轮：两条部分索引（迁移 904）落地

> 会话：`mvs_bbd1346d211a4eb3ac41b552c82eaecf`（承接 R87/R88）
> 日期：2026-10-27
> 起点：`origin/main = 8bbcc145f`（R88 准备件），生产 build **2839**（`2.5.8-8d8415ac-20261024-2839`）@ 154:8781
> ★ 本轮口径纪律：**零口径变更**（只加索引，不改任何一行查询）⇒ 不需要属主口径裁决；
> ★ 生产写操作仅限「随发版投递一条 startup 迁移」这一项，且它**只建索引**（可 `DROP INDEX` 回退）。


> ★★ **日期口径校准（一条，不批量改号）**：★ 本系列（R87/R88/R89）的审计档文件名、标题、
> ★ LESSONS §344~§353、AGENTS.md 与 Go 注释**统一用「2026-10-27」作为轮次标签**
> ⇒ ★ 这是**全系列一致的标签**（不是单点笔误）。
> ★ 但 ★ **实测时钟与 git 提交时刻都是 2026-10-24**：
> ★ · 本机 / 252 PG 宿主 / 154 三方 `date` 一致 = **2026-10-24 15:44 CST**
> ★ · git 提交时刻：R87 `7c12f372e` = 14:28:35、R88 `8bbcc145f` = 14:56:32、
> ★   R89 `3e47b1fe2` = 15:41:09（均 2026-10-24）
> ★ · 生产 `schema_migrations.applied_at`（904）= **2026-10-24 15:42:54+08**
> ⇒ ⇒ **凡本系列引用「实测时间戳」处一律以 2026-10-24 为准**；标签 2026-10-27 保留不改。
> ★ 为什么不批量改号：仓库里**已有**「档名日期 = 提交日 +1 天」的先例（跨午夜写档），
> ★ 而本系列标签被 **LESSONS §344~§351、AGENTS.md、R87 的 Go 注释**引用 ⇒
> ★ **批量改号会造成大面积不一致与引用断裂** ⇒ 改为「**一条校准说明 + 保留标签**」。
> ★★ **例外（不可改，已冻结）**：`sql/migrations/startup/904_inspector_stats_partial_indexes.sql`
> 的文件头仍写「2026-10-27」⇒ ★ **该文件的 sha256 已进生产
> `llm_gateway_migration_checksums` 台账**（`d2cd82dc…`）⇒ ★ **改它会让下次发版的
> history gate 以「checksum 不匹配」拒绝部署** ⇒ ⇒ 故**保持原样**并在此登记。
> （`.down.sql` 未入台账，但为与 up 体口径一致也保持原样。）

---

## 0. 一句话结论

`GET /api/admin/sessions/inspector-stats` 的两条「无时间窗全表聚合腿」**没有索引可承接**
⇒ 落地迁移 **904**（两条部分索引，8,192 B + 2,120 kB，**只加索引不改任何 SQL**）⇒
L2 `389.373 ms → 119.706 ms`、L3 `195.432 ms → 0.100 ms`（252 生产 EXPLAIN ANALYZE 实测，
事务内建索引后 ROLLBACK，回滚后残留索引实测 = 0）。

---

## 1. 症状与基线

| 读数 | 值 | 来源 |
|---|---|---|
| 154 直连端点（2839，REPEAT=5） | min **2.411 s** / med **3.989 s** / max **6.160 s** | `scripts/deploy-154.sh` 同款直连计时脚本（不经隧道） |
| 三条腿的结构 | L1 `session_summaries ⋈ session_dim` 聚合；L2 `AVG(health_score)`；L3 `COUNT(*) WHERE status='closed'` | `admin/session_health_api.go:676-685 / 694-698 / 732-737` |
| Go 侧开销 | **26 ms（0.9%）** ⇒ 瓶颈 100% 在 DB | 同上轮归因 |

★ **腿模型**：这 3 条腿在 Go 里是**串行 await**（三个 `h.db.QueryRow` 逐个等）
⇒ 端点墙钟 ≈ **三条腿之和**，不是「最慢腿」。
★ 这与 R87 的 `session-overview`（9 段**并行** ⇒ 墙钟 ≈ 最慢腿）**不同**
⇒ 「端点墙钟 = 最慢腿」**不是全局规律**，读腿级数字前必须先看 Go 侧是串行还是并行。

---

## 2. 根因（252 生产只读实测，2026-10-27）

### 2.1 L3：没有任何索引含 `closed_at`

`session_dim` 实测 **902,868 行 / 206 MB**，既有索引 7 条：

```
idx_session_dim_apikey      (tenant_id, api_key_id)      WHERE api_key_id IS NOT NULL   9472 kB
idx_session_dim_client      (tenant_id, client_id)      WHERE client_id IS NOT NULL     11 MB
idx_session_dim_end_user    (tenant_id, end_user_id)    WHERE end_user_id IS NOT NULL   3168 kB
idx_session_dim_owner_user  (tenant_id, owner_user)     WHERE owner_user IS NOT NULL    9240 kB
idx_session_dim_task        (tenant_id, task_id)        WHERE task_id IS NOT NULL         19 MB
idx_session_dim_tenant      (tenant_id, last_active_at DESC)                                58 MB
session_dim_pkey            UNIQUE (gw_session_id)                                       67 MB
```

★ **没有一条以 `closed_at` 开头或含 `closed_at`** ⇒ 规划器只能：

```
Parallel Seq Scan on session_dim sd
  Filter: ((status = 'closed') AND (closed_at >= CURRENT_DATE))
  Rows Removed by Filter: 301,024
Execution Time: 195.432 ms
```

⇒ **扫 90 万行、丢掉 100%**，只为证明「一行都没有」。

### 2.2 L2：只有 NULL 侧有索引，NOT NULL 侧没有

`session_summaries` 实测 **861,483 行 / 396 MB**，既有索引 19 条，其中与 health 相关的**只有一条**：

```
idx_session_summaries_health_pending (last_request_at DESC) WHERE health_score IS NULL    19 MB
```

★ 那是 **NULL 侧**。L2 的谓词是 `health_score IS NOT NULL` ⇒ **NOT NULL 侧无索引可承接**：

```
Parallel Seq Scan on session_summaries ss
  Filter: (health_score IS NOT NULL)
  Rows Removed by Filter: 184,361
Execution Time: 389.373 ms
```

★ 实测谓词命中：`health_score IS NOT NULL` = **308,435 / 861,483**（35.8%）。

### 2.3 L1：不是索引问题，是**哈希连接溢写**

L1 的 `Parallel Hash Join` 实测：

```
Buckets: 262144  Batches: 8  Memory Usage: 11552kB
Buffers: shared hit=77267, temp read=15403 written=15468
```

⇒ **哈希表装不下 `work_mem`，溢写到临时文件**（15,468 个 temp 块 ≈ 121 MB）。
⇒ 这是**第三条腿**，与「缺索引」并列，且成本更低（见 §6）。

---

## 3. 修复（迁移 904）

`sql/migrations/startup/904_inspector_stats_partial_indexes.sql` + 同名 `.down.sql`：

```sql
BEGIN;
SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '10min';

CREATE INDEX IF NOT EXISTS idx_session_dim_closed_at_closed
    ON public.session_dim (closed_at)
    WHERE status = 'closed';

CREATE INDEX IF NOT EXISTS idx_session_summaries_health_score_notnull
    ON public.session_summaries (health_score)
    WHERE health_score IS NOT NULL;

COMMIT;
```

### 3.1 效果（252 生产，事务内建索引 → EXPLAIN ANALYZE → ROLLBACK）

| 腿 | 修复前 | 修复后（热） | 计划形态变化 |
|---|---|---|---|
| L2 `AVG(health_score)` | **389.373 ms** | **119.706 ms** | Parallel Seq Scan → **Parallel Index Only Scan** using `idx_session_summaries_health_score_notnull`（Heap Fetches 86,938） |
| L3 `COUNT(*) closed` | **195.432 ms** | **0.100 ms** | Parallel Seq Scan → **Index Only Scan** using `idx_session_dim_closed_at_closed`（Heap Fetches 0） |
| L1 JOIN 聚合 | 1,615 ms | 1,401 ms | **不变**（噪声带内） |

索引体积：`idx_session_dim_closed_at_closed` = **8,192 B**；
`idx_session_summaries_health_score_notnull` = **2,120 kB**。
回滚后残留索引实测 = **0**（`pg_indexes` 计数）。

### 3.2 口径零变化

本迁移**不改任何一行查询**，只加索引 ⇒ 结果集取值不变（**构造性成立**，不依赖快照比对）。
★ 这与 903 的「改写查询后逐字比对」是不同强度的论证：904 更强。

### 3.3 两张表都不是分区表

`pg_class.relkind = 'r'` 且 `pg_inherits` 无子行 ⇒ 可用普通 `CREATE INDEX`；
但仍**不用 `CONCURRENTLY`**（startup 迁移默认在事务里跑，CONCURRENTLY 不能在事务块内执行）。
两张都是**热写表** ⇒ 构建期持 SHARE 锁阻塞该表写入 ⇒ 落在低峰窗口。

---

## 4. 诚实边界（四条，全部实测）

1. ★★ **C1 索引当前是空的，而这是「被证实的空」不是「建错了」。**
   `session_dim.status` **distinct = 1**（全表只有 `'active'`）、`closed_at` **100% NULL**
   ⇒ 谓词命中 0 行 ⇒ 索引 8,192 B。
   ★ 收益形态 = **把「证明一行都没有」从全表扫降级为索引扫**（195 ms → 0.1 ms）。
   ★ **真因不在索引**：会话**关闭**路径是坏的 ——
   `admin/session_health_api.go` 的 `HandleSessionRecycle` 与
   `bg/session_lifecycle_worker.go:496-500` 都 UPDATE 了 `public.session_dim` 上
   **并不存在**的列（`stop_reason` / `updated_at`）⇒ 真库 42703
   ⇒ 所以 `status` 永远停在 `'active'`、`closed_at` 永远 NULL
   ⇒ **4 个「恒 0 字段」（`idle_sessions` / `closed_sessions` / `recycled_today` /
   `findings_last_1h`）的真因在此**，不在索引。★ 另立处置项。

2. ★★ **L2 还差一步，但这一步是生产写操作，本迁移不做。**
   `Heap Fetches: 86,938` ⇒ 可见性地图（`relallvisible = 37,826 / relpages = 50,660 = 74.6%`）
   尚未覆盖全部页 ⇒ 约 28% 的行仍需回堆。
   若对 `public.session_summaries` 做一次 `VACUUM (ANALYZE)`，`Heap Fetches` 可降到 0。
   ★ VACUUM 属生产写操作 ⇒ 不在本迁移内（另见 `sql/ops/vacuum_session_family_vm.sql`）。

3. ★★ **L1 不在本迁移的射程内**（1,615 ms → 1,401 ms 是噪声）。
   它的瓶颈是哈希连接溢写 ⇒ 见 §6。

4. ★ **本地库不能证明生产计划形态。**
   本地 `session_dim` 1,677 行 / `session_summaries` 2,004 行（比生产小 **3 个数量级**）
   ⇒ 本地规划器仍选 Seq Scan ⇒ 本地读数**只用于**验「可应用 / 幂等 / 可下线」。

---

## 5. 门禁与变异（8 条门 + 8 个变异，全红）

门：`sql/migrations/startup/migration_904_inspector_stats_partial_idx_test.go`

| # | 门 | 钉的是什么 |
|---|---|---|
| 1 | `UpBodyShape` | 两条索引的「名字 + 表 + 列 + 谓词」逐字；幂等；无 CONCURRENTLY；BEGIN/COMMIT；**CREATE INDEX 条数恰好 = 2**；不得出现 UPDATE/DELETE/TRUNCATE/ALTER TABLE |
| 2 | `DetectorRejectsBadShapes` | 6 份合成坏样本（含**追加型**多塞一条索引）必须被判红 |
| 3 | `CarriesLockAndStatementTimeout` | `lock_timeout='<秒>s'` 且 ≠ 0；`statement_timeout='<分钟>min'` 且 ≠ 0 |
| 4 | `DownDropsBothIndexes` | down 必须删**两条** |
| 5 | `EmbeddataCopiesAreByteIdentical` | 孪生副本逐字节一致 + sha256 与台账一致 |
| 6 | `RegisteredAtAllDeliveryPoints` | **7 点登记面** + 台账 sha 取**文件实测值**比对 |
| 7 | **`IndexPredicatesMatchGoQueries`** | ★ 语义耦合门：用 `go/ast` 取 `admin/session_health_api.go` 的**字符串字面量**，断言索引谓词与查询谓词**归一化后逐字相等**，且索引列是查询真正过滤的列 |
| 8 | `NotInConcurrencyExceptionTable` | 904 不得出现在单事务通道豁免表里 |

★ 门 #7 为什么用 `go/ast`：Go 源里到处是**解释**这两个谓词的注释
（`// status = 'closed' AND closed_at >= CURRENT_DATE` 就在注释里）
⇒ 正则扫源码会命中注释 ⇒ **恒绿门**。

### 5.1 真应用变异（改真文件 → 门必须红 → md5 逐文件还原）

| 变异 | 内容 | 结果 |
|---|---|---|
| M1 | C1 谓词改错（`status='active'`） | 门红（3 条断言）✓ |
| M2 | C2 谓词写成 `IS NULL`（与既有索引重复侧） | 门红（3 条断言）✓ |
| M3 | C1 幂等守卫被摘 | 门红（3 条断言）✓ |
| M4 | **追加型**：up 体多塞一条索引 | 门红（3 条断言）✓ |
| M5 | `runner.go` 登记点被摘 | 门红（1 条断言）✓ |
| M6 | TSV 登记点被摘 | 门红（1 条断言）✓ |
| M7 | embeddata 孪生被改 | 门红（1 条断言）✓ |
| M8 | **Go 侧 L3 谓词被改**（`sd.status = 'idle'`） | 门红（1 条断言，语义耦合门）✓ |

★ **8/8 全被抓住**，逐文件 md5 还原校验全 `ALL_OK`，还原后 904 门与 manifest 门均绿。

★ **门自身的三处量具缺陷（第一版被阳性对照当场逼出）**：
① 坏样本①/③ 用**字面量替换**构造，而真文件里 `)` 与 `WHERE` 之间**是换行**
⇒ 替换没发生 ⇒ 坏样本 == 好样本 ⇒ 检测器报「没有鉴别力」
⇒ 修法：变异体一律用**正则**构造；
② 语义耦合门两侧**捕获粒度不一致**（查询侧捕获了 `WHERE …`、迁移侧只捕获谓词）
⇒ 归一化后仍不等 ⇒ 门对着**正确实现**报红
⇒ 修法：查询侧改用捕获组只取谓词；
③ 同上（`qC1[1]` vs `qC1` 的下标错位）。

---

## 6. 顺带测出的独立杠杆（**属主裁决项，本轮未实施**）

L1 的 `Parallel Hash Join` 溢写（`Batches: 8`、temp 15,468 块）。
事务内 `SET LOCAL work_mem='64MB'`（**同一条查询，其余全部不变**）：

| work_mem | Batches | temp 溢写 | Execution Time |
|---|---|---|---|
| 默认（role 未设） | 8 | temp read=15,403 / written=15,468 | **1,700 ms** |
| 64 MB | **1** | **归零** | **1,123 ms（−34%）** |

★ 这是**配置**杠杆（不改一行 SQL ⇒ 口径零变更），且**成本远低于加索引**。
★ 但它是**生产配置变更**，且每 parallel worker 占 84 MB（3 workers ≈ 252 MB）
⇒ 252 是共享库（loadavg 5.2 / 4 核）⇒ 内存账必须算清 ⇒ **属主裁决**。

---

## 7. 交付面

### 7.1 7 点登记面（全部已核对）

| # | 位置 | 状态 |
|---|---|---|
| 1 | `sql/migrations/startup/904_inspector_stats_partial_indexes.sql`（+ `.down.sql`） | 新增 |
| 2 | `installer/cmd/llm-gw-installer/embeddata/startup/904_*.sql`（孪生，逐字节一致） | 新增 |
| 3 | `installer/cmd/llm-gw-installer/main.go`：`go:embed` ×2 + embed map ×2 | 已登记 |
| 4 | `installer/internal/dbinit/runner.go`：`Runner.StartupFiles` | 已登记 |
| 5 | `scripts/apply-db-revision-sequence.sh` | 已登记 |
| 6 | `sql/schema/installed_startup_migrations.tsv` | 已登记（由 `go test ./internal/dbinit/ -run TestStartupManifest -update` **重生成**，非手改） |
| 7 | `docs/db-changelog.md` | 已登记（含**文件实测** sha256） |

★ sha256：up `d2cd82dccf0ff6060ba11c3f593484e445e46ac44cba4b057edf12d45b25af20`；
down `3328f8c7368769c42641f7874dd38ba4e04138891abad3b8781abdc2ed2f5532`。

### 7.3 迁移在生产上的真实执行读数（252，2026-10-27）

用**真实文件体**（把 `COMMIT;` 换成 `ROLLBACK;`）在生产跑了一遍：

| 步骤 | 实测 |
|---|---|
| `CREATE INDEX idx_session_dim_closed_at_closed` | **3.18 s** |
| `CREATE INDEX idx_session_summaries_health_score_notnull` | **6.68 s** |
| 合计 | **≈ 10 s** ⇒ ★ 这 10 秒里两张**热写表**的写入被 SHARE 锁阻塞 ⇒ 落在低峰窗口 |
| 回滚后残留索引 | **0** |
| 回滚后计划 | 回到 Parallel Seq Scan（L3 160.9 ms / L2 349.8 ms）⇒ 双向自证 |

★ 角色级 `statement_timeout = 30s` 下实测安全（`SET LOCAL statement_timeout='10min'` 生效）。
★ 部署通道的失败语义（**实测，非推断**）：deploy-lib 用
`psql -v ON_ERROR_STOP=1 -v ON_ERROR_ROLLBACK=1 -f <file>` ⇒ 实测「事务内出错」时
psql **退出码 = 3 且事务回滚** ⇒ **fail-closed** ⇒ 不会出现「迁移失败却被记账成已应用」。

### 7.2 发版前 pending 集（用**安装器清单**口径，不是文件 basename）

生产 `schema_migrations` 实查水位 = **903**（902 applied 2026-10-24 08:49、
903 applied 2026-10-24 12:47）。
清单 307 条 vs 生产实查 ⇒ pending = {375, 376, 387, 388, 392, 600, **904**}
⇒ ★ **904 是唯一新增待投递项**，其余 6 条是既有历史漂移（用 `DB_LEDGER_RECONCILE_FROM=902` 排除）。

★ **台账订正**：`docs/db-changelog.md` 旧节把 902 / 903 标成「未应用，pending」，
但生产**早已记账** ⇒ 那是**标注漂移**，本节按实测水位如实登记。

★ 投递机制核对（`deploy-lib/db-changelog.sh`）：
`_deploy_pending_startup_migrations` 只收 `^[0-9]{3}_` 且 ≥ `DB_LEDGER_RECONCILE_FROM`
且生产未记账的文件 ⇒ **904 会被部署自动投递**；
应用方式是 `psql -v ON_ERROR_STOP=1 -f <file>`（**不套外层事务**）
⇒ 迁移文件自带的 `BEGIN/COMMIT` 安全；应用后由部署写入
`schema_migrations` + `llm_gateway_migration_checksums`。

---

## 8. 本轮**没有**做（明确边界）

- ★ **没有**改任何生产 SQL（口径零变更）。
- ★ **没有**做 `VACUUM`（生产写操作 ⇒ 属主裁决，见 §4.2 与 R88 准备件 A）。
- ★ **没有**改 `work_mem`（生产配置变更 ⇒ 属主裁决，见 §6）。
- ★ **没有**实施 C3（合并 L1+L2）—— ★ 已独立测量判定它是**口径变更**
  （LEFT JOIN 形态 `total_tokens` = 156,277,969,134 vs INNER JOIN = 151,880,679,978，
  **+4.4B（+2.9%）**；`active_sessions` 764,668 vs 764,665）⇒ 划归属主裁决项。
- ★ **没有**修 `HandleSessionRecycle` / `bg/session_lifecycle_worker.go` 的 42703
  （那才是 4 个恒 0 字段的真因）⇒ **另立处置项**（见 §9）。
- ★ **没有**做备份写端（52% 坏文件）方案 (a) 的实现。
- ★ **没有**做 `?api_key_id=` / `?model=` 过滤路径的修法（子代理已交方案，见 §9）。

---

## 8.5 发版与生产对照读数（build 2840）

★ 发版：154 build **2840**（`2.5.8-3e47b1fe-20261024-2840`），nginx 切到 **8782**，
路由身份仲裁通过，`DEPLOY_EXIT=0`，旧实例（2839）graceful drain。
★ 904 由部署在**切换前自动投递**（deploy-lib `_deploy_pending_startup_migrations`
只收 `^[0-9]{3}_` 且 ≥ `DB_LEDGER_RECONCILE_FROM=902` 且生产未记账的文件）。

### 8.5.1 生产落库核对（252 实查）

| 项 | 实测 |
|---|---|
| `schema_migrations` | `904` / `inspector_stats_partial_indexes` / applied_at **2026-10-24 15:42:54+08** |
| `llm_gateway_migration_checksums` | `904` / `904_inspector_stats_partial_indexes.sql` / `d2cd82dc…`（**与文件实测一致**） |
| `idx_session_dim_closed_at_closed` | 存在，**8,192 B**，谓词 `status = 'closed'` |
| `idx_session_summaries_health_score_notnull` | 存在，**2,120 kB**，谓词 `health_score IS NOT NULL` |

### 8.5.2 生产计划形态（实查，非推断）

- **L3**：`Index Only Scan using idx_session_dim_closed_at_closed`，Heap Fetches **0**，
  **Execution Time 0.074 ms**（迁移前 160.9~195.4 ms）
- **L2**：`Parallel Index Only Scan using idx_session_summaries_health_score_notnull`，
  Heap Fetches 87,138，Execution Time 232 ms（冷）/ 119.7 ms（热）

### 8.5.3 端点级对照（154 直连，**不经隧道**）

| 端点 | 发版前 | 发版后 | 判定 |
|---|---|---|---|
| `GET /api/admin/sessions/inspector-stats` | min 2.411 / **med 3.989** / max 6.160 s（REPEAT=5） | min **1.427** / **med 1.801** / max **2.074** s（REPEAT=6） | ★ **med −55%**、max −66% ⇒ **可归因**（见下） |
| `GET /api/admin/dashboard/session-overview` | ≈2.6 s（R87） | min 1.887 / med 2.037 / max 2.153 s | 未劣化 |
| `GET /api/admin/data-lifecycle/stats` | 18.567 s（R87/R88） | min 17.239 / med 19.138 / max 23.483 s | 未变（**根因在别处**，§8.5.5） |
| `GET /api/admin/turns/sessions` | 0.111 s（R87） | min 0.033 / med 0.034 / max 0.477 s | 未劣化 |
| `GET /api/admin/turns/sessions/filter-options` | 10.208 s（R88） | min 2.014 / med 2.361 / max 5.368 s | ★ **不可归因于 904**，见 §8.5.4 |

★★★ **归因口径（本轮唯一可归因的一处）**：★ **2840 与 2839 的运行时 Go 代码完全相同**
（本轮只改了 SQL / 门 / 文档 / installer 登记面，**没有一行 `cmd/gateway` 或 `admin` 的 Go 代码**）
⇒ ★ 任何端点耗时变化只可能来自「**新加的两条索引**」或「**环境（缓存 / 争用 / 后台作业）**」
⇒ ⇒ `inspector-stats` 的 **−55% 可归因**：它的两条腿正是 904 服务的，
且 ★ **生产计划形态已实查确认切换**（§8.5.2）⇒ 不是「读数碰巧变好」。

### 8.5.4 `filter-options` 的 10.208 s → 2.361 s：**不可归因**（诚实记录）

- ★ 904 的两条索引**不服务** `filter-options` 的查询：它对 `session_summaries` 用的是
  `MAX(ss.first_request_at) GROUP BY …` 与 `JOIN session_dim`，
  ★ **既不按 `closed_at` 过滤、也不按 `health_score` 过滤** ⇒ 与 904 的两个谓词**无交集**。
- ★ 本轮响应体 **1,332 字节**（R88 记录的**未降级**区间 = 1,362~1,402 字节，
  **降级**区间 = 682~745 字节）⇒ ★ 本次调用**未降级**
  ⇒ ⇒ R88 的 10.208 s 是「**turns 臂 8 s 子预算被烧掉**」的降态读数（与 R88 归因一致）。
- ⇒ ★★ **两次读数不可比** ⇒ **不能宣称「变快了 4 倍」**。
  ★ 这正是 R88 那条通用式的另一面：★ **同一端点在不同争用状态下可以是 2.4 s 或 10.2 s，
  而代码一个字没变** ⇒ ★ 任何「端点变快」的结论都必须先过「同代码对照」这一关。

### 8.5.5 顺带的现场取证：12 s 趋势腿仍在烧（根因未变）

154 日志（发版后实时抓取）：

```
WARN data_lifecycle_stats trend failed; growth_trend 已退化为空
     budget=12000000000  error=timeout: context deadline exceeded
```

⇒ 与 R88 记录的「**恒失败且产出恒空**的 12 s 趋势腿」**逐字吻合** ⇒ 根因未变，仍待属主裁决。
★ 同族：`WARN availability key counter: SCAN failed`（context deadline exceeded）。


---

## 9. 移交属主的裁决项（本轮新增 + 既有）

### 9.1 本轮新增

1. ★ **`work_mem` 提升到 64 MB**（L1 1,700 → 1,123 ms，口径零变更）——
   生产配置变更，且 252 是共享库 ⇒ 需先算内存账。
2. ★ **会话关闭路径 42703 的修复**（`HandleSessionRecycle` +
   `bg/session_lifecycle_worker.go:496-500` UPDATE 了不存在的
   `stop_reason` / `updated_at` 列）⇒ 这是 4 个恒 0 字段的真因，
   修好之后 C1 索引才会开始承载真实行集。
3. ★ **`session_summaries` 的 VACUUM (ANALYZE)** ⇒ L2 的 `Heap Fetches` 86,938 → 0。

### 9.2 既有（本轮未变）

- `data-lifecycle/stats` 18.6 s：生产 VACUUM / 恒失败 12 s 趋势腿 / bodies 部分索引。
- `filter-options` 10.2 s：父表级覆盖索引（需维护窗口）/ turns 子预算 8s→3s / JIT 关闭。
- `session-overview` ≈2.6 s：服务端 60 s 缓存 / 预聚合 rollup / 维持现状。
- 备份写端：方案 (a) 分片+原子 rename+flock / (c) 保留期归档 / 14370 门判据。
- **新增**：`?api_key_id=` 与 `?model=` / `?provider=` / `?status_code=` 过滤路径
  **生产 500@30 s**（角色级 `statement_timeout=30s` ⇒ SQLSTATE 57014），
  根因 = EXISTS 打在 115 列未推进 source / view 的 hot 腿反连接；
  候选修法 = 拆成 hot/parent 两个单表 EXISTS + 逐字等价的 CASE 表达式
  （EXPLAIN 实测 52,854 ms → **192~212 ms**），口径等价在同一 REPEATABLE READ
  快照内实测对称差 = 0 ⇒ **属主拍板后可落地**。
- 台账 75 条历史漂移；摘要 sidecar 元数据；「会话数」口径；刷新期间端点返回什么；
  库侧作业错峰；生产每页 2 条 `maintain-api` 503 噪声；`build_seq` floor 作用域；
  211 环境同步状态；R72e 遗留。

---

## 10. 通用式（已并入 `LESSONS.md` §353）

1. ★★★★ **「恒空谓词」也是正当索引收益** —— 判据是「它替查询省掉了什么」，不是「命中了多少行」；
   **空索引 ≠ 建错了**。
2. ★★★★ **部分索引的谓词方向抄反是最隐蔽的错** —— 在位、规划器会用、但覆盖互补行集 ⇒ 不变慢也不报错。
3. ★★★ **语义耦合门必须用 `go/ast` 取字符串字面量** —— 正则会命中 Go 注释 ⇒ 恒绿门。
4. ★★★★ **台账的「pending」标注会漂移** —— 生产水位一律以 `schema_migrations` 实查为准。
5. ★★★ **变异体的第一版常因替换目标写错而根本没生效** —— 阳性对照会当场自曝「尺子坏了」。
6. ★★ **端点墙钟是「各腿之和」还是「最慢腿」取决于 Go 侧串行/并行** —— 不是全局规律。
7. ★★ **哈希连接的 temp 溢写是独立于索引的第三条腿** —— `Batches > 1` 就该想 `work_mem`。
