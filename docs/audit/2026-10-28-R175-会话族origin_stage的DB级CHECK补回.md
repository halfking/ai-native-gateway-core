# R175 · 会话族 `origin_stage` 的 DB 级 CHECK 补回（v1 族退役时丢掉的兜底，从未在会话族重建）

> **日期**：2026-10-28
> **分支 / worktree**：`r98/mv-refresh-load-attribution` @ `/tmp/kx-wt-r98`
> **基线**：`origin/main` = `5ef5c141a`（R174 全部提交已在 main）
> **性质**：**修一条真实缺陷**（不是测量轮、不是零 SQL 轮）—— 新增 startup 迁移 914 + 同名 `.down.sql` + 9 处登记面 + 新门 10 道断言 + **16 条变异臂**（★ 15 条期望红 ＋ **1 条【期望绿】反向对照**）+ 注释订正。
> **规模**：改动面 = 2 个新 SQL 文件（canonical + embeddata 副本 ×2）+ 3 处 Go 登记 + 1 处 TSV + 1 处 bash + 1 处台账 + 1 个新门文件（≈560 行）+ 6 个 Go 文件的**注释**订正。★ ★★ **【自审订正】★★ 本轮先前写成「零行生产代码改动」—— ★★ 【不准确】：★ 除登记行与注释外，★ 还有**一处非注释的字符串改动** = ★★ `metrics/prometheus.go` 的指标 `Help:` 文本（★ `_request_logs INSERT` → `session_turns INSERT`，★ 它是**暴露给监控的描述性文本**、不是行为逻辑，★ 但**确实**是产品可见的一行）⇒ ⇒ ★★ **准确表述：★ 零行【逻辑】改动；唯一一行【产品可见文本】改动是那条指标的 `Help`。★★ ★★**

---

## §0 一句话

★ **v1 族退役时，`origin_stage` 的 DB 级 CHECK 跟着一起没了，而会话族从未重建** ⇒
`session_turns` / `session_turns_hot` 上**一条 origin_stage 约束都没有**（PG 上 `pg_constraint` 实测 0 条）⇒
**DB 完全不拒** ⇒ 而消费方（★ **两种形态**，见 §1.3）分别靠
`COALESCE(origin_stage,'') NOT IN (8 个探针 stage)` **排除探针流量**
（⇒ 越界值 ⇒ **探针流量被算成业务流量**）
或靠 `COALESCE(origin_stage,'business') = 'business'` **只放行业务流量**
（⇒ 越界值 ⇒ **业务流量被静默丢弃**）
⇒ **两种形态下都没有任何一层会报错**。
⇒ 本轮用迁移 914 把这条兜底补回（允许集取自唯一真值源 `internal/probekind.ValidStages()`），
并用新门把「Go 侧真值源 ↔ DB 侧约束」**逐项对拍**钉住。

> ★★ **【本轮自审订正之二】★★ 上面那句「消费方**一律**用 `NOT IN` 排除集」是**事实错误**：
> ★★ 本轮初稿把它写进了 **5 个载体**（审计档 §0/§1.3 · 迁移 914 文件头 ·
> ★★ `docs/db-changelog.md` 的 914 登记行 · `AGENTS.md` ㊱ · `LESSONS.md` §496）⇒
> ★★ 独立复核阶段**逐处回源码核对** ⇒ ★★ 实测**有 3 处是等值形态**
> ★★（`admin/routing.go:2753` · `bg/probe_policy.go:275` · `bg/probe_policy.go:160`——
> ★★ 但那一条带的是 `_request_logs` 别名 ⇒ 属 **v1 族**，**不是**会话族消费方）
> ★★ **且清单漏了 `db/db.go` 的 3 处** ⇒ ⇒ **5 处载体已逐个订正**（本节 + §1.3 + §9 + §12）。
> ⇒ ⇒ ★★ **通式：★★ 「同一句机制描述」一旦被复制进【多个载体】（文档 ＋ 迁移文件头 ＋ 台账 ＋ 交接件），
> ★★ 它的错误就会被【复制成 N 份】⇒ ⇒ 单点订正不够 ⇒ 必须**逐载体**订正；
> ★★ 而且**判据要按【形态】分栏**，不能写成一句「一律用 X」—— ★★ 两种形态的**故障后果相反**，
> ★★ 混成一句就会讲错后果（污染 vs 少计）。★★** ★★★

---

## §1 缺口本体（★ 实测，非推断）

### 1.1 v1 族曾经有这条约束

`git show de1c13c38:sql/migrations/domain/341_probe_origin_and_node_probe.sql` 取回原文（`:45-53`）：

```sql
ALTER TABLE request_logs
    DROP CONSTRAINT IF EXISTS request_logs_origin_stage_check;
ALTER TABLE request_logs
    ADD CONSTRAINT request_logs_origin_stage_check CHECK (
        origin_stage IS NULL OR origin_stage IN (
            'self_check', 'node_probe', 'system_health', 'business',
            'probe_direct', 'probe_v2', 'model_probe', 'passive_probe', 'manual'
        )
    );
```

★ 注意 341 只加在**父表**上（`request_logs`），`request_logs_hot` **没有** ⇒ v1 族的语义是
「**越界值能先进 hot，在 promote 进月分区时才炸**」。

### 1.2 v1 族退役时它消失了，会话族没补

迁移 **890**（v1 族最终删除）把 `_request_logs*` 族整体 DROP ⇒ 那条 CHECK 随之消失。
而会话族建列时（**707**）只写了列、**没写约束**：

```sql
-- sql/migrations/startup/707_session_turns_s1a.sql:87（父表）与 :143（hot 表）
ADD COLUMN IF NOT EXISTS origin_stage TEXT,
```

★ **本地真库实测**（`pg_constraint`）：

```
 session_turns            | session_turns_quality_check            | t
 session_turns_2026_07    | session_turns_quality_check            | t
 ...（8 个兄弟 CHECK × 父表 + 4 个分区 + hot = 40 条）
```

⇒ **40 条 CHECK 里没有一条是 origin_stage 的**；`session_turns.origin_stage` 与
`session_turns_hot.origin_stage` 都是 `text`（v1 族是 `varchar(32)`）⇒ **DB 完全不拒**。

### 1.3 谁在依赖这条约束（★ 消费方实测：★★ **两种形态，故障后果不同**）

★★ **口径声明**：★ 本轮初稿把下面 6 处**一律**写成 `NOT IN` 形态 ⇒ **事实错误**（已订正，见 §0 的【自审订正之二】）。
★★ 逐处回源码核对后的真实形态如下 —— ★★ **两种形态的故障后果相反**，混成一句就会讲错后果。

**① 排除集形态**：`COALESCE(origin_stage,'') NOT IN (8 个探针 stage)`
⇒ ★ **越界值落进去 ⇒ 它不在排除集里、也不在允许集里 ⇒ 探针流量被算成业务流量**
（⇒ 统计污染 + 对账被污染）

| 位置 | 形态细节 |
|---|---|
| `admin/analytics.go:178` | 排除集 = `businessRequestFilterStages`（8 个探针 stage，定义在 `:167`） |
| `bg/mv_consistency.go:230` · `:285` | 排除集 = 8 个探针 stage 字面量 |
| `db/db.go:3959` · `:4007` | 排除集 = 8 个探针 stage；源 = `routing_analytics_source`（★ 该视图的源是**会话族**：`db/db.go:3902` 读 `session_turns_hot`、`:3928` 读 `session_turns`） |
| ★ `db/db.go:6917` | ★ **只排 3 个 stage**（`self_check` / `node_probe` / `system_health`）⇒ 另 5 个探针 stage 在**这一条 origin_stage 判据里**没被排除（★ 该函数体另有 `task_type <> 'probe_triggered'` 与 `request_id NOT LIKE 'probe-%'` 两道判据兜着 ⇒ ★ **不是「探针流量会混进业务统计」**，而是**这道判据比同族其它读点更窄**）；源 = `public.session_turns_hot` |

**② 白名单形态**：`COALESCE(origin_stage,'business') = 'business'`
⇒ ★ **越界值 ≠ 'business' ⇒ 被排除 ⇒ 业务流量被静默丢弃**（⇒ 少计 —— ★ **这是另一种故障，不是污染**）

| 位置 | 形态细节 |
|---|---|
| `admin/routing.go:2753` | 源 = `db.SessionFamilyTurnsSourceSQL()` ⇒ **会话族**（hot ∪ parent 两腿） |
| `bg/probe_policy.go:275` | `probeTrafficExclusionPredicateSession` ⇒ **会话族**（名字自带 `Session`） |

**③ ★ 不是会话族消费方的一处（★ 初稿把它算进来了）**

| 位置 | 说明 |
|---|---|
| `bg/probe_policy.go:160` | `probeTrafficExclusionPredicate` —— ★ 谓词带的是 **`_request_logs` 别名** ⇒ 属 **v1 族**（该族已随 890 退役）⇒ ★ **不是会话族的消费方**；但它的**形态**同样是等值白名单 |

⇒ ⇒ 两种形态下，**一个越界值都不会让任何一层报错** ⇒ ⇒ **DB 级兜底必须补**。
⇒ ⇒ 而**补上之后**（914 落地）：越界值在**落库那一刻**就被拒 ⇒ ⇒ 两种形态的读点都**不再可能**读到它。

### 1.4 唯一还活着的闸门：Go 侧

- `middleware/origin_mw.go` 的 `isValidOriginStage` → `probekind.IsValidStage`；
- `internal/probekind/probekind.go` 的 `IsValidStage` 注释**自己写着**
  「落库前的闸门与落库后的约束必须一致，否则会出现『中间件认、DB 拒』的行」
  —— ★ **设计意图明确，而现状恰好相反：DB 完全不拒**。

---

## §2 ★★ 允许集：唯一真值源与它的漂移风险

`internal/probekind.ValidStages()` = `ProbeStages`(8) + `BusinessStage`(`'business'`) = **9 项**。

★ 本轮补回约束后，**这条真值源就有两份物理形态**（Go 切片 + DB CHECK）⇒ 必须钉住它们的一致性。
⇒ 做法 = 新门的 `TestMigration914AllowedSetMatchesProbekindSSOT`：
**逐项相等**（不是包含、不是「大致相同」）：
- 少一项 ⇒ 该 stage 中间件认、DB 拒 ⇒ 正是上面那句注释要避免的状态；
- 多一项 ⇒ DB 认了一个 Go 侧不认的值 ⇒ 探针排除逻辑会漏掉它。

### 2.1 ★★ 写入面已核实：CHECK 不会误拒任何真会写的值

| 写入路径 | 闸门 | 结论 |
|---|---|---|
| HTTP 头声明 | `middleware/origin_mw.go:361` `isValidOriginStage(claimedStage)` | 未知值降级为 `business` ⇒ 恒合法 |
| DB 系统键兜底 | `ResolveOriginForSystemKey` → `systemOwnerFallbackStage`（从 `probekind.HTTPStageActors()` 反向派生） | 落在 `probekind.ProbeStages` 内 |
| 落库 | `domains/session/v2/turn_writer.go:633` `nilIfEmpty(rec.OriginStage)` | 空 ⇒ NULL ⇒ CHECK 放行 |
| 其他写 `session_turns_hot` 的生产代码 | **无**（全仓只 `turn_writer.go:531` 一处生产写入，其余都是测试） | — |

---

## §3 ★★ 两侧都要加（★ 会话族自己的约定，不是新政策）

`706b_session_turns_hot_prelude.sql` 把父表的 CHECK 克隆到 hot 并改名成 `session_turns_hot_*`；
hot 的 bootstrap（`embeddata/startup/session_turns_hot_bootstrap.sql`）也自带同样 8 个 `_hot_` 命名的 CHECK。

⇒ ⇒ 会话族的约定是「**父表一份 + hot 一份，同义不同名**」。
⇒ 914 照此办理 ⇒ 好处：**越界值在写入那一刻就被拒**，而不是等到 promote 阶段才炸成「promote 任务反复失败」。

---

## §4 ★★★ PG 17 实测（隔离库 `llm_gateway_r175probe`，★ 六条都是量出来的，不是查文档猜的）

| # | 实测项 | 结论 |
|---|---|---|
| 4.1 | `ADD CONSTRAINT … NOT VALID` 加在**分区父表**上 | **会克隆到所有分区**（含 DEFAULT 分区），`convalidated=false` |
| 4.2 | NOT VALID 的分区副本**拦不拦新行** | **拦**（父表路径 / 直插分区路径 / hot 路径三条都实测拒写） |
| 4.3 | `VALIDATE CONSTRAINT` 加在**父表**上 | **一条覆盖全族**（各分区 `convalidated` 同步变 `true`） |
| 4.4 | 分区上的继承约束能不能单独 DROP | **不能**（`cannot drop inherited constraint`）⇒ 必须在**父表**上 DROP，PG 连带清掉各分区副本 |
| 4.5 | 存量有越界行时 `VALIDATE` | **报错并中止**（`check constraint … is violated by some row`）⇒ 迁移失败 ⇒ 部署停下 ⇒ 人工裁决（**不静默放过**） |
| 4.6 | ★★ `SET LOCAL lock_timeout` 在 `psql -f -`（**无** `--single-transaction`）下 | **完全失效**：只打一条 `WARNING: SET LOCAL can only be used in transaction blocks`，之后 `current_setting('lock_timeout')` 仍是 `0` |

★ 4.6 是本轮**最值钱的一条**：它意味着 `scripts/apply-db-revision-sequence.sh` 这条**升级通道**上，
凡是用 `SET LOCAL` 写锁守卫的迁移，其守卫**都是装饰品**（详见 §6）。

---

## §5 ★★ 迁移 914 的形态

**文件**：`sql/migrations/startup/914_session_turns_origin_stage_check.sql`（+ 同名 `.down.sql`）

### 5.1 两步走（★ 为了不堵住写入）

1. `ADD CONSTRAINT … NOT VALID` —— 不扫存量、瞬时完成，**新行立刻受约束**（4.1/4.2）；
2. `VALIDATE CONSTRAINT` —— 只拿 `ShareUpdateExclusiveLock`（**不阻塞读写**），父表上一条覆盖全族（4.3）。

★ 对照：直接 `ADD CONSTRAINT`（要校验存量）在分区父表上会连带拿住**所有分区**的
`AccessExclusiveLock`，252 上是 114 万行 ⇒ 写入被堵数秒。⇒ 两步走把这个代价降到 0。

### 5.2 定义感知的幂等守卫（★ 三种幂等写法只有一种是对的）

| 写法 | 重放时的后果 |
|---|---|
| `ADD CONSTRAINT`（无守卫） | 报 `already exists` ⇒ **部署第二次就挂** |
| 先 `DROP` 再 `ADD` | 重放会**再扫一遍全表、再抢一次强锁** ⇒ 每次重放都付全款 |
| ★ **读现状定义 → 已是规范形态就跳过 → 否则才重建** | 已是规范形态时**零成本**；定义不对时才重建 |

★ 「规范形态」的判据 = **定义里点名 `origin_stage`** ∧ **定义里抽出的全部单引号字面量集合 == 规范允许集**
⇒ 名字在但定义不对（例如只剩 `'business'`）⇒ 判为非规范 ⇒ 重建（隔离库实测通过）。

### 5.3 ★ 会话级锁守卫（★ 因为 4.6）

```sql
SET lock_timeout = '2s';
SET statement_timeout = '120s';
```

★ 取 **2s**（仓内既有约定）而不是更长：PG 的锁队列里**等锁期间新事务会排在它后面**
⇒ 一条迟迟拿不到 `AccessExclusive` 的 ALTER 会把整张表的写入一起堵住
（§44 的生产事故：一条 `CREATE TABLE` 堵了 14 个网关会话 1.5 分钟）⇒ **必须快速失败而不是排队等锁**。

---

## §6 ★★★ 本轮的系统性发现：★ 锁守卫在**升级通道**上是装饰品

### 6.1 事实

- **installer 通道**：`installer/internal/dbinit/runner.go` 的 `applySQL` 默认给每个文件加
  `--single-transaction` ⇒ 文件里的 `SET LOCAL` **有效**。
- **升级通道**：`scripts/apply-db-revision-sequence.sh` 的 `psql_file` → `request_logs_sql "$1" | psql_stream`
  ⇒ `psql_stream` = `psql -X -v ON_ERROR_STOP=1 "$DATABASE_URL" -f -`
  ⇒ ★ **不加 `--single-transaction`**；且 `request_logs_sql` 在 `runtime_adaptation == none` 时只是 `cat "$file"`
  ⇒ **不注入 `BEGIN`** ⇒ 每个语句各自成事务 ⇒ `SET LOCAL` **失效**（4.6 实测）。

### 6.2 后果

★ `installer/cmd/llm-gw-installer/startup_ddl_lock_guard_gate_test.go` 这道门只检查
「文件里**含有** `lock_timeout` 这个字符串」⇒ `SET LOCAL lock_timeout` **满足**它
⇒ ★ **一道门绿着，而守卫实际不生效** ⇒ 这正是本仓反复记的那条形态
（「门测的那件事成立」≠「你要的那件事成立」）。

### 6.3 本轮的处理

- 914 改用**会话级** `SET lock_timeout` ⇒ 两条通道都真的生效；
- 新门 `TestMigration914CarriesEffectiveLockGuard` 钉住这个形态（含**阳性对照**：形态检测器必须能区分两种写法）；
- ★ **存量迁移的 `SET LOCAL` 形态仍属已知债务**（§7 明确登记，未在本轮批量改）——
  因为那是一次**部署行为变更**（原本能跑通的迁移可能变成 `55P03` 失败），需要人拍板。

---

## §7 ★★ 门

### 7.1 新门（`sql/migrations/startup/migration_914_session_turns_origin_stage_check_test.go`）

| # | 断言 | 钉住什么 |
|---|---|---|
| G1 | `TestMigration914HasDownCompanion` | down 脚本在场 |
| G2 | ★★ `TestMigration914AllowedSetMatchesProbekindSSOT` | **914 的允许集与 `probekind.ValidStages()` 逐项相等**（含阳性对照：提取器不许空转） |
| G3 | `TestMigration914CoversBothSessionFamilyTables` | 父表 + hot 表**两侧**都被覆盖（**结构化解析**目标清单，非子串包含） |
| G4 | `TestMigration914UsesDefinitionAwareIdempotentGuard` | 定义感知守卫的五个标记齐全（**词边界**匹配） |
| G5 | ★ `TestMigration914CarriesEffectiveLockGuard` | **会话级** `SET lock_timeout`（且**不是** `SET LOCAL`）＋ 形态检测器的阳性对照 |
| G6 | `TestMigration914EmbeddataCopiesAreByteIdentical` | canonical ≡ embeddata 逐字相同 |
| G7 | `TestMigration914IsDeliveredByEverySurface` | 升级通道 `files=()` / 台账登记行 / 启动清单 TSV **三处都精确命中** |
| G8 | ★ `TestNoGoSourceStillClaimsTheRetiredOriginStageCheck` | 退役形态（表限定名 + 退役约束名）不许出现在非测试 Go 文件里；豁免表**显式 + 每条带理由 + 非空**；豁免必须**同时**指出现役形态 |
| G9 | ★ `TestCorrectedCommentsPointAtTheLiveConstraint` | 订正后的注释必须**点名现役约束/现役迁移**（不许只是「已修正」三个字） |
| G10 | `TestSessionTurnsOriginStageCheckOnLiveDB` | 真库腿：`pg_constraint` 上两条约束存在 ∧ `convalidated=true` ∧ 允许集 == Go 侧（没配 DSN 时**显式 skip 并打印**） |

### 7.2 ★★ 门自己的两轮迭代（★ 本轮最值钱的一处）

★★★ 第 1 版的判据**有两处没有牙齿**，是变异臂抓出来的：

| 变异 | 第 1 版判据 | 为什么没红 | 修法 |
|---|---|---|---|
| 把 hot 目标的约束名改成 `session_turns_hot_origin_stage_check_disabled` | `strings.Contains(body, "session_turns_hot_origin_stage_check")` | ★ **`..._disabled` 仍然**含有** `..._check` ⇒ 子串包含判不出 | 改成**结构化解析** `c_targets` 里的 (表, 约束名) 对，**逐对精确比对** |
| 把 `VALIDATE CONSTRAINT` 改成 `VALIDATE CONSTRAINT_DISABLED` | `strings.Contains(body, "VALIDATE CONSTRAINT")` | ★ 同上，**裸子串骗得过** | 改成**词边界**正则 `\b` ⇒ `_DISABLED` 的 `_` 是单词字符 ⇒ 骗不过 |

★★★ 注释门第 1 版更糟：用「文件里提到 `origin_stage` + `CHECK` ⇒ 必须含 `session_turns`」
⇒ 实测 **5 条假阳性**（`bg/node_probe.go`、`bg/model_probe.go`、`bg/active_probe_emitter.go`、
`admin/routing.go` 里的 CHECK 全是**别的表**的：`node_probe_runs_attempt_check`、
`credential_probe_queue.source`、`providers.protocol`）
⇒ ⇒ 收窄到**两条精确判据**（表限定形态 + 退役约束名）＋ 显式豁免表。

★ 另有一处**自造的断言逻辑 bug**：豁免表的比对里 `delete(want, p)` 放错了分支
⇒ 把**已找到**的项报成「缺」⇒ 是变异臂 M3 先报出来的。

### 7.3 变异臂（★ **16 臂全达标**：★ 15 条期望红（全检出）＋ **1 条【期望绿】反向对照**（符合预期）；★ 每臂还原后 md5 逐字一致）

| 臂 | 注入 | 期望变红的门 | 结果 |
|---|---|---|---|
| M1 | 914 的允许集少一项（删 `manual`） | G2 | ✔ |
| M2 | `SET lock_timeout` → `SET LOCAL lock_timeout` | G5 | ✔ |
| M3 | hot 目标改名 `..._disabled` | G3 | ✔（**第 1 版没红**） |
| M4 | `VALIDATE CONSTRAINT` → `VALIDATE CONSTRAINT_DISABLED` | G4 | ✔（**第 1 版没红**） |
| M5 | 升级通道漏投递 914 | G7 | ✔ |
| M6 | embeddata 副本与 canonical 不一致 | G6 | ✔ |
| M7 | 整行删掉 hot 目标 | G3 | ✔ |
| M8 | `NOT VALID` → `NOT VALID_DISABLED` | G4 | ✔ |
| M9 | `VALIDATE CONSTRAINT` → `..._DISABLED` | G4 | ✔ |
| M10 | 台账登记行的 sha 被抹掉 | G7 | ✔（★ **本轮 sha 随文件头订正而变** ⇒ 这一臂的注入串**必须同步新 sha** ⇒ ★★ 否则 sed 匹配不到 ⇒ 门不会红 ⇒ **那一臂会误报成「没有牙齿」** ⇒ ★ 教训：**变异臂的注入串本身也是会被订正打断的依赖**） |
| M11 | 非豁免文件里出现退役限定名 | G8 | ✔ |
| M12 | 订正后的注释不再指现役约束 | G9 | ✔ |
| M13 | 豁免表被清空（阳性对照失效） | G8 | ✔ |
| ★★ **M14'** | ★★ **【期望绿】**：★ **删掉** `request_logs_hot.origin_stage` 那条标记 **+ 注入** hot 限定名 ⇒ ⇒ 门**不应当红** | G8 | ✔ **没红（符合预期）** ⇒ ★★ **证明「那条标记是唯一在拦这个洞的东西」** ⇒ ★ 与 M16 合成一对 |
| ★★ **M15** | ★★ 把标记写宽到 `"origin_stage"`（★ 能命中现役形态） | G8 | ✔ **红了** ⇒ ★★ **阴性对照有牙齿**（★ 门不许误伤 `session_turns*.origin_stage`） |
| ★★ **M16** | ★★ 非豁免文件里出现 `request_logs_hot.origin_stage` | G8 | ✔ **红了** ⇒ ★★ **端到端证明「那个洞被封上了」** |

★ ★★ **M14' 的由来**：★ 本轮补 G8 标记时**第 1 版把 M14 写成「删标记」（没有配套注入）**⇒
★★ **门当然不会红**（★ 没有违规可检出）⇒ ★★ **那一臂是无效臂** ⇒ ★★ 改成「删标记 **＋ 注入**」的
★★ **【期望绿】**形态（★ 本仓早有这条通式：★ **变异臂也要能表达「预期绿」** ⇒ ★ 而「预期绿」臂是用来
★★ **证明某条判据是承重的**的）⇒ ⇒ ★★ M14'（标记不在 ⇒ 抓不到）＋ M16（标记在 ⇒ 抓得到）
★★ **合起来**才是「洞被封上」的完整证据，★ 单给一条都只是半个证明。★★ ★★★

### 7.4 ★★★ 本门在 **CI 上跑得到吗**？—— ★★ 一条【自查坐实】的缺口（本轮最该记的一条）

★★★ **本门所在的包 `sql/migrations/startup` 此前【不在】任何 CI 强制路径上** ⇒
★★ **本轮新增的 10 道断言在 CI 上【一次都不会跑】** ⇒ ★★★ **一门没人跑的守卫是文档，不是门**
（★ 仓内 `Makefile` 自己写着这句话，★ 而本轮正好撞上它的同族形态）。

★ **实测依据**（不是推断）：

| 事实 | 出处 |
|---|---|
| CI 载体跑的是 **`make guards-fast`** | `.workflow/main-verify.yml:86`（job `audit-guards`） |
| `guards-fast` 只跑 `GUARD_PACKAGES` | `Makefile` 的 `guards-fast` target |
| `GUARD_PACKAGES` / `GUARD_PACKAGES_SLOW` **都不含** `./sql/migrations/startup` | `Makefile:119` / `Makefile:148` |
| ⇒ 本门只在**官方门** `verify.sh` 的 `go test ./...` 上跑 | `verify.sh:183` |

★ ★★ **仓内其实早就有一道门在管这件事**：`scripts/ursmcheck/audit_guard_registration_test.go`
（★ 「**门写了却没人跑** ⇒ 在 CI 上红」）⇒ ★★ 但它的判据是**显式标记**：
★ 门文件自己声明 `AUDIT-GUARD:` ⇒ **它所在的包必须被登记进 `GUARD_PACKAGES` / `GUARD_PACKAGES_SLOW`**。
★★ ⇒ ★★ **本轮之前这道门抓不到本轮的新门**（★ 因为新门**没有**声明那个标记 ⇒ ⇒ 它连「应该被登记」这件事都不知道）。
★★ ⇒ ⇒ ★★ **⇒ 本轮的处置（两处，缺一不可）**：★★
★★ ① 门文件头加 `// AUDIT-GUARD:` 声明（★ **显式承诺**：我保证这道门在 CI 上跑）；
★★ ② `Makefile` 的 `GUARD_PACKAGES` 补登 `./sql/migrations/startup`（★ 让这句承诺**可验证**）。
★★ ⇒ ★★ 补登后，本门在 **CI 的 `make guards-fast`** 上真跑；★★ **副作用（如实记录）**：
★★ 该包还有 **~160 个既有迁移契约测试文件**（★ `migration_NNN_test.go` 一族）⇒ ★★ 它们**同时**被带进 CI 快门
（★ 本地实测该包 **11.5s** 全绿 ⇒ ★★ 在 120s/包的快门预算内 ⇒ ⇒ **这是净收益，不是负担**）★★。

★ ★★★ **通式：★★★ 「新增一道门」这件事的验收清单里，必须有一条是【**它在 CI 上跑得到吗**】——
★ ★★ 只问「门写了吗」「门绿吗」是**不够的**：★★ ★★ **门绿**只说明「**在跑它的那台机器上**成立」，
★★ 而「那台机器」是不是 CI 的强制路径，**是另一个问题** ⇒ ★★ **两个问题都得问**。★★ ★★★
★ ★★ 顺带：★ 仓内 `Makefile` 里那三条注释（「补登 3 个从未被执行的守卫包」「补登记 `./admin`」）
★★ 说明**这条缺口在本仓反复出现过** ⇒ ★★ ⇒ **它不是本轮特有的毛病，而是本仓的复发型形态** ⇒ ★★ ⇒ 通用式：**当你在本仓新增门时，默认假设「它不会被执行」，并主动去登记。** ★★★

---

## §8 ★★ 登记面（9 处，一处不漏）

| # | 位置 | 内容 |
|---|---|---|
| 1 | `sql/migrations/startup/914_session_turns_origin_stage_check.sql` | canonical |
| 2 | `sql/migrations/startup/914_session_turns_origin_stage_check.down.sql` | 同名 down |
| 3 | `installer/cmd/llm-gw-installer/embeddata/startup/914_*.sql`（+ `.down.sql`） | embeddata 副本（**字节相同**） |
| 4 | `installer/cmd/llm-gw-installer/main.go` | `go:embed` × 2 + map × 2 |
| 5 | `installer/internal/dbinit/runner.go` | `StartupFiles` 末位 |
| 6 | `sql/schema/installed_startup_migrations.tsv` | `316	914_session_turns_origin_stage_check.sql`（★ 该 TSV 是 **0 基**序号） |
| 7 | `scripts/apply-db-revision-sequence.sh` | `files=()` 末位（升级通道） |
| 8 | `docs/db-changelog.md` | sha 登记行 `12e185f1…`（★ **本轮订正过文件头 ⇒ sha 随之变** ⇒ ★ 登记行、变异臂 M10 的注入串、台账里的 `content_sha256` 三处**必须同步**） |
| 9 | `installer/internal/dbinit/runner.go` 的登记注释 | 指向现役约束名 |

★ **不需要**登记的两处：`scripts/audit/asset/db-object-expect.tsv` 与
`scripts/audit/asset/installer-reachability.tsv` —— 实测二者**只收索引**，不收 CHECK 约束；
本迁移**不建任何索引** ⇒ 无需加行。

---

## §9 ★★ 注释订正（★ 注释也是事实源）

本轮把**「断言了不存在的对象」**的那几处注释改了（★ **本节只改注释**；★ 唯一一处非注释的产品可见改动是 `metrics/prometheus.go` 的指标 `Help:` 文本，★ 见文首【自审订正】）：

> ★★ **口径声明（★★ 本轮自审订正之四）★★**：★ 下表按**行**计（⇒ 合计 **15 行** = 14 行注释 + 1 行 `Help:`）；
> ★★ 而「本轮改了 **13** 处」这句话是按 **hunk** 计的 ⇒ ★★ **两个数都是真的，但必须连口径一起写** ——
> ★★ 否则**下一次订正**就会把「13 处」当成「13 行」⇒ ★★ 那时它就变成了一个新的错误。
> ★★ 精确口径：**13 个 hunk**，分布 **7 个非测试 Go 文件**；★ 其中 **12 个 hunk 是纯注释**、
> ★ **1 个 hunk**（`metrics/prometheus.go`）含那行产品可见文本。★★

| 文件 | 旧文案 | 改后 |
|---|---|---|
| `middleware/origin_mw.go` | 「matches the additive CHECK on `_request_logs.origin_stage` (see migration 341)」 | 指向现役约束 `session_turns.origin_stage`（迁移 914）＋ 写明 890~914 之间「DB 完全不拒」这段真空 |
| `internal/probekind/probekind.go` | 「能落进 `_request_logs.origin_stage`」「DB CHECK（migration 341）用的就是这份集合」 | 同上 |
| `internal/probekind/probekind.go`（`ProbeStages` 注释） | 「migration 341 已落库的 CHECK 合法值」 | ★ 改为「**domain 链的** migration 341」+ 全路径（★ 今天的 startup 链里 `341_` 是 `341_hot_table_independence.sql`） |
| `admin/probe_request_info.go` | 「DB CHECK（migration 341）也不认」 | 迁移 914 + 两侧表名 |
| `middleware/requestid_mw.go`(×4) · `middleware/auth_mw.go`(×1) · `middleware/origin_mw.go`(×2) · `metrics/prometheus.go`(×2) · `metrics/interface.go`(×1) | 提到 `_request_logs` / `request_logs` 这张**已真删除**的表 | 改成会话族表名 |

★ 为什么必须改：本仓自己记过「注释说一套、代码做另一套」是第 6 条已知形态 ——
这类注释**把后来人引向错误的修改方向**（去 v1 族里找约束 / 去 startup 链里找 341）。

---
## §10 ★★ 诚实边界（本轮明确**没做**的）

1. ★★ **存量迁移的 `SET LOCAL` 形态仍属已知债务**（§6）—— 批量改是一次**部署行为变更**，需人拍板。
2. ★★ **211 / 252 尚未执行 914** —— 252 只读、本轮未连 211；两侧的**越界行数已实测为 0**
   （252 用过滤计数形态；`GROUP BY` 聚合形态在 252 上超时）。
3. ★★ **共享库 `pg_container_usable` 的探针缺陷未修**（§10.1）—— 那是**仓外共享资产**，属主裁决。
4. ★ **移动端 / 后端下发的中文 / 页面级硬编码中文** —— 与本轮无关，仍在 backlog。
5. ★ **`request_context_attrs.origin_stage`（telemetry 侧表）也没有 CHECK** —— v1 族当年也只加在
   `request_logs` 上 ⇒ 不是回归；本轮未动（属另一族）。
6. ★ **canonical 视图退役 / 35 读点逐个实测分区下界 / U+FFFD 专项 / 属主裁决清单** —— 承 R174 未完项，本轮未做。

---

## §10.1 ★★★★ 本轮最值钱的一处结构发现：★★ **部署的【活代码】不在本仓的版本控制里**

### 10.1.1 事实

```bash
$ ls -la scripts/deploy-lib
lrwxr-xr-x  scripts/deploy-lib -> ../../../deploy-lib      # ★ 相对软链 ⇒ 指向仓外

$ git -C ~/workspace/ai-native-tools/deploy-lib rev-parse --show-toplevel
/Users/niuer/workspace/ai-native-tools                      # ★ 属于【另一个】 git 仓
```

★ `scripts/deploy-local.sh:17-24` 的注释自己写明：
「共享部署库 SSOT（P1.1）：导出 `AIAN_DEPLOY_LIB` … 历史副本留档于 `deploy-lib.legacy/`；
`scripts/deploy-lib` 为共享 SSOT 的相对软链。」

⇒ ⇒ **决定「部署能不能成功」的代码不在本仓的版本控制里** ⇒ ⇒
**本仓无法仅凭本仓复现一次部署** ⇒ ⇒ ★★ **这是「事实源分裂」的典型形态。**

### 10.1.2 ★★ 本轮因此连打两次空（★ 两次都「看起来对」但运行时完全不执行）

| 次第 | 我改在哪 | 为什么没用上 |
|---|---|---|
| ① | `scripts/deploy-local.sh` 的 `detect_existing_containers`（★ 仓内、★ 语法正确、★ 助手单测全绿） | 那段**不是运行的那条路径** —— 真正跑的是共享库的 `pg_container_usable` |
| ② | `scripts/deploy-lib.legacy/smart-discovery.sh`（★ 仓内、★ 与共享库同名同形） | 它是**历史留档** —— 运行时 source 的是 `$AIAN_DEPLOY_LIB/smart-discovery.sh`（仓外） |

⇒ ⇒ ★★ **两次失败的共同原因：★ 我没有先确认「我改的这份是运行的那份」。★★**

★ ⇒ ★★★ **定位手段（本轮实测有效）**：

```bash
PS4='+${BASH_SOURCE[0]}:${LINENO}: ' bash -x scripts/deploy-local.sh status 2>&1 | grep -n 'POSTGRES_PASSWORD'
# ⇒ ++/Users/niuer/workspace/ai-native-tools/deploy-lib/smart-discovery.sh:70: dl_container_env …
# ⇒ ★★ 这一行就把【真正执行的副本】指出来了。
```

⇒ ⇒ ★★★ **通式：★★ 【改任何东西之前，先确认「我改的这份是【运行的那份】」】——
★★ 「语法正确 ＋ 单测通过 ＋ 看起来对」**三者都不构成**「它会生效」的证据。★★ ★★**

### 10.1.3 ★★ 共享库里的真缺陷（★ 属主裁决项，本轮**未**改）

`$AIAN_DEPLOY_LIB/smart-discovery.sh` 的 `pg_container_usable()`：

```bash
exists=$(docker exec -e PGPASSWORD="$db_pass" "$container" psql -U "$db_user" -d postgres -Atqc \
  "SELECT 1 FROM pg_database WHERE datname='$db_name'" 2>/dev/null) \
  || { warn "PostgreSQL: container $container not usable (cannot authenticate as $db_user; check POSTGRES_PASSWORD drift)"; return 1; }
...
export DL_DISCOVERED_PG_PASS="$db_pass"
```

★★ **这条探针走的是【容器内的本地 socket】⇒ pg_hba 是 `trust` ⇒ 口令根本不参与认证**
（本轮实测：错口令 ＋ 容器内 loopback ⇒ **仍然成功**）⇒ ⇒
**它永远检出口令漂移** ⇒ ⇒ 而它的失败信息却写着「check POSTGRES_PASSWORD drift」
⇒ ⇒ ★★ **报错信息说的是真因，探针却检不出真因** ⇒ ⇒
⇒ 容器被判为「usable」⇒ ★★ **那个已经漂移的口令被导出成 `DL_DISCOVERED_PG_PASS`** ⇒ ⇒
⇒ 下游据此拼出的 DSN **必然** SASL 认证失败 ⇒ ⇒ 部署**必挂在迁移步**。

★ ⇒ ★★ **修法（三态探针，本轮已在本仓侧写好并实测）**：
① 探针必须走**会命中 scram** 的路径（本机实测：容器内经 `host.docker.internal:<宿主发布端口>` 回头连宿主
⇒ 错口令 ⇒ `FATAL: password authentication failed`；真口令 ⇒ `1`）；
② 返回值必须区分 **0=正确 / 1=被拒 / 2=探不通** ⇒ ★ **否则「探不通」会被误判成「口令过期」**；
③ 探不通时**保持原行为**（不把部署改死），只 `warn`。

---

## §10.2 ★★ 另一处订正：★★ 「本地库缺 900~913」这个前提【读错了账本】

R174 的交接里写：「本地库 `schema_migrations.max = 899` 而仓内 startup 已到 913 ⇒ 缺 900~913」。

★★★ **实测（本轮）**：本仓有**两本账**：

| 账本 | 行数 | 记什么 |
|---|---|---|
| `public.schema_migrations` | **74**（max **899**） | 只收**显式自注册**的迁移（★ 900~913 都不自注册 ⇒ 它**不反映** startup 通道的进度） |
| `public.gateway_db_revision_sequences` | **282** | ★★ **startup 通道的交付账本** ⇒ 记录通道**投递过的每一个文件** |

⇒ ⇒ ★★★ 逐条核实（`900`~`914` 在**交付账本**里）：

```
900 已应用 · 901 已应用 · 902 已应用 · 903 已应用 · 904 已应用
905 已应用 · 906 已应用 · 907 已应用 · 908 已应用 · 909 已应用
910 已应用 · 911 已应用 · 912 已应用 · 913 已应用 · 914 已应用
```

⇒ ⇒ ★★★ ⇒ **★ 部署前真正缺的只有 912 / 913 两条（上游新加的），
而不是 900~913 十四条 ⇒ ⇒ ⇒ ⇒ **R174 的那条前提【读错了账本】。★★ ★★**
⇒ ⇒ ★★ 通用式：**两本账都存在 ⇒ 「已应用」这个问题没有单一答案 ⇒ 判据必须先声明【读的是哪本】。★★ ★★**

---

## §11 本轮交付读数

| 项 | 读数 |
|---|---|
| 迁移全生命周期实测 | **7 臂全通过**（建约束 / 幂等跳过 / 拒写三条路径 / 非规范定义被重建 / 存量越界 ⇒ `VALIDATE` 失败且 **DO 块原子回滚、零残留** / 清理后收敛 / down 后列与行不动且 DB 不再拒） |
| ★★ **真实通道上的 content replay** | ★★ **本轮订正过 914 的文件头 ⇒ 文件 sha 变了 ⇒ 台账指纹失配 ⇒ 升级通道必然走「content replay」重放** ⇒ ★★ **实测（在真库 + 真通道上）**：★★ `content replay (ledger 961ff47607e0 -> file 12e185f15f9e): applying 914` ⇒ ★★ **914 的定义感知守卫判定「已是规范形态，跳过重建」⇒ no-op**；★ 706b 同款 ⇒ 「already exists; nothing to do」⇒ no-op；★ **通道 rc=0** ⇒ ★★ **幂等性在真实通道上得证**（★ 不只是在隔离库上）；★★ 跑后终态：台账指纹已同步新 sha · 约束 **7 处**（父表 + **5 个分区含 DEFAULT** + hot）全部 `validated=true` · 越界行数 **parent=0 / hot=0** ★ |
| PG 17 形态实测 | **6 条**（§4 表） |
| 越界行数 | 本地 **0**（`business` 2,290 行 ＋ NULL 468 行）· **252 0**（★ 只读，★ 本轮**重采**过：父表 **0** / hot **0**） |
| 252 的现状 | ★★ **会话族 origin_stage 约束 = 0 条**（★ 缺口在 252 同样存在）· 分区形态 = **5 个月分区**（2026_07~2026_11，★ **无 DEFAULT 分区**，★ 与本地不同）⇒ ⇒ ★★ **914 在 252 上无行冲突、且父表一条 `VALIDATE` 覆盖 5 个分区** |
| 新门 | **10 道断言**（静态 9 ＋ 真库腿 1） |
| 变异臂 | **16 臂全达标**（★ 15 条期望红 **全检出** ＋ **1 条【期望绿】反向对照**符合预期）；每臂还原后 **md5 逐字一致** |
| 登记面 | **9 处**（两个审计 TSV 只收索引 ⇒ 本迁移不建索引 ⇒ 无需加行） |
| 部署 | release **2.5.8.2890** · **`VERIFY_PASS=1`** · 凭据解密冒烟 `providers=16,2452,2453 creds=3 failed=0`；★ ★★ **部署读数以【运行中网关的版本接口】为准**（`GET /api/system/version` ⇒ `build_seq=2890`）★★ ★★ —— ★★ 而**仓内版本产物**（`VERSION` / `version.json` / `web/public/version.json`）当前是 **2893** ⇒ ★★ 原因是：**2891 / 2892 / 2893 三次都是失败的部署尝试**（★ 失败前仍会先 bump 版本文件）⇒ ★★ **2890 是本轮唯一成功的那次** ⇒ ★★ ⇒ ★★ **同一件事在两处的读数不同，是因为它们量的不是同一件事**（★ 运行中的二进制 vs 仓内待提交的构建产物）⇒ ★★ **部署事实读运行中网关，版本产物读仓内文件**，★ 不许互相代入 ★★ |
| 落地读数 | `gateway_db_revision_sequences` 记 **914 @ 2026-10-28 11:56:18**；★ 部署前真正缺的只有 **912 / 913** |
| 库上形态 | **6 处**约束（父表 ＋ 4 分区 ＋ hot）· 全部 `convalidated=true` · 允许集 **9 项逐字相符** |
| 上游 r1043 真库写入形状门 | **PASS**（「真库形状满足生产写入要求：2 张表 ✓」） |
| 生产代码改动 | ★★ **零行【逻辑】改动**；★ 唯一一行**产品可见文本**改动 = `metrics/prometheus.go` 的指标 `Help:`（★ `_request_logs INSERT` → `session_turns INSERT`；★ 描述性文本、非行为；★ 仓内无测试断言该字符串）★★ |
| 注释订正面 | ★★ **13 个 hunk**（★ **口径**：hunk 口径 = 13 ⇒ 分布 **7 个非测试 Go 文件**；★ 其中 **12 个 hunk 是纯注释**、**1 个 hunk 含那行产品可见文本** ⇒ ★★ **按行计则是 15 行**（14 注释 + 1 Help）⇒ ★★ **两个数都是真的，但必须连口径一起写**，否则下一次订正就会变成新的错误）★★ |
| 独立复核 | ★★★ **verifier 给出 `PASS-WITH-ISSUES`** ⇒ ★★ **本轮逐条自查后确认 3 处是真错**（★ ① 消费方形态写成一类、且漏列 `db/db.go` 3 处 ⇒ ★ 已被**独立复核**抓出 ⇒ **5 处载体已逐个订正**；★ ② 慢门 timeout 前提过期（900s ⇒ 实际 1800s）；★ ③ 计数口径混用）⇒ ★★ **3 处已全部订正并重新过门**（见 §12 与 §13）★★ |
| 慢门读数 | 见 §12（★ **PASS 2760 / FAIL 0 / SKIP 197**） |

---

## §12 ★★ 慢门权威读数

★ 权威入口：`go test ./scripts/ursmcheck ./admin -count=1 -timeout=3600s -v`

★★★ **【本轮自审订正之三】★★★ 初稿写「`Makefile:213` 的默认是 **900s**，而 `Makefile:168` 自记实测 **767s** ⇒ 余量仅 17%」⇒ ★★★ **该前提是错的** ⇒ ★★★ 逐条回仓内核对：

| 初稿前提 | 实测 | 结论 |
|---|---|---|
| `Makefile:213` 的默认是 900s | ★ `Makefile:211` 的注释明写「2026-10-28 r1044 轮：慢门预算由 **900s 提到 1800s**」；★ `Makefile:220` 的实参是 `$(GO) test $(GUARD_PACKAGES_SLOW) -count=1 -timeout=1800s` | ★★ **默认预算现在就是 1800s**；★ `Makefile:213` 只是那段注释里的一行 ⇒ ★★ **900s 是 r1044 之前的历史值** |
| `Makefile:168` 的 767s 可用来算本机余量 | ★ 那行在 `guards-fast` target 的注释里，描述的是「**4 核 Linux** 实测 767s（13 分钟）」 | ★★ **那是另一台机器的历史读数** ⇒ ★★ **跨机器的历史读数不能拿来算本机的余量** |
| （未提） | ★ `verify.sh:183` 同样是 `-timeout=1800s`（r1044 同批提的） | ★ 两处预算现在同步在 1800s |

⇒ ⇒ ★★★ **所以本轮显式放宽到 3600s 的理由不是「默认预算快不够」，而是：★★**
★★ **慢门一旦被 SIGKILL，`testing` 的 panic 发生在 alarm goroutine 里 ⇒ 其它 goroutine 的 `defer` 不执行 ⇒ ★★ 被跟踪的台账会【静默留空】（★ 这正是 r1044 轮提预算的理由）⇒ ★★ 在【并行会话抢同一台机器】的环境下，★ 宁可宽松也不要被腰斩。★★**

### 12.1 ★ 本轮读数（★ `-v` 重跑取三计数）

★ ★★ **本轮共跑了 3 次慢门**，**三次都绿**，★ **三计数完全一致**（★ ⇒ ⇒ 说明本轮的订正**没有引入任何门红**）：

| 次 | 树的状态 | `ursmcheck` | `admin` | 顶层 PASS / FAIL / SKIP |
|---|---|---|---|---|
| v1 | 订正**前**（初稿树） | **651.741s** | **82.347s** | **2760 / 0 / 197** |
| v2 | 订正**后**（消费方形态 / timeout 前提 / 计数口径 / G8 补洞 / 706b 订正已落） | **658.220s** | **85.025s** | **2760 / 0 / 197** |
| ★ **v3** | ★ **最终树**（★ 再加 `AUDIT-GUARD:` 标记 ＋ `Makefile` 补登） | ★ **645.602s** | ★ **81.463s** | ★ **2760 / 0 / 197** |

★ ★★ **含子测试的全量计数**（v3）：PASS **4283** / FAIL **0** / SKIP **197** ⇒ ★★ 与 v1/v2 **逐项相同**。
★ ★★ 顺带：★ **CI 快门本体**（★ `make guards-fast` ⇒ ★ CI 的 audit-guards job 跑的就是它）
★★ 也在最终树上跑过：**rc=0 / 墙钟 35s / FAIL 0**（★ 新登记的 `./sql/migrations/startup` 占 **13.704s**）。

★ ★★ **197 个 SKIP 的成因（逐条读原因行统计，不是猜的）**：★★ **157 个 = 未设 DB 类环境变量**
（★ `TEST_DATABASE_URL` / `TEST_PG_URL` / `TEST_PG_DSN` / `TEST_ADMIN_500_DSN` / `LLM_GATEWAY_PG_URL` / `database not available`）
⇒ ★★ **本轮故意不设 `TEST_DATABASE_URL`**（★ 避免在主库上留 `dlperf_*` 残留）⇒ ★★ **40 个 = 其它条件性 skip**
（★ 缺 `VAPEUR_API_KEY` / 浏览器夹具关闭 / 特性未实现 / `USAGE_OBSERVATION_LOCAL_PG` 未设 等）。
⇒ ⇒ ★★ **「SKIP」在本轮的含义是【显式 skip 并打印原因】，不是【没跑】** ⇒ ★★ 而**真库相关的两道门单独跑过并 PASS**：
★★ ① 本轮新门的真库腿（`TestSessionTurnsOriginStageCheckOnLiveDB`，★ 配 `TEST_DATABASE_URL` 后 **PASS**）；
★★ ② 上游 r1043 的写入形状门（`TestLiveDbHasReconciliationWriteShape`，★ **PASS**）。

### 12.2 ★★ 本轮的两条偏离，都写明

| 偏离 | 理由 |
|---|---|
| `-timeout=3600s`（默认 1800s） | ★ 见上：★ 慢门被 SIGKILL ⇒ **台账静默留空** ⇒ 在抢机器的环境里宁可宽松 |
| **不设** `TEST_DATABASE_URL` | ★ 慢门里有 **157** 个真库腿 ⇒ 在**主库**上跑会留下 `dlperf_*` 残留 ⇒ ⇒ 改用**隔离跑真库腿**（★ 单独跑、单独记读数） |

---

## §13 ★★★ 独立复核：本轮**自己写下的**事实错误，与逐载体的订正

★★★ **本节记的不是【新缺陷】，而是【本轮刚写完、就被自己复核抓出来的错误陈述】。★★★

★ 本轮收尾前起了一个**独立复核子代理**（只读），把审计档的断言**逐条回源码核对** ⇒
★★ 结论 `PASS-WITH-ISSUES` ⇒ ★★ **本轮逐条自查后确认 3 处是真错**（★ 不是复核方误报）：

| # | 错误陈述 | 真相 | 波及的载体 | 处置 |
|---|---|---|---|---|
| ① | 「消费方**一律**用 `COALESCE(origin_stage,'') NOT IN (8 个探针 stage)` 排除探针流量」 | ★ **两种形态**：★ 排除集形态（`admin/analytics.go` · `bg/mv_consistency.go` · `db/db.go`）＋ **白名单形态** `COALESCE(...,'business')='business'`（★ `admin/routing.go:2753` · `bg/probe_policy.go:275`）；★ 且 `bg/probe_policy.go:160` 属 **v1 族**、不是会话族消费方；★ **清单还漏了 `db/db.go` 的 3 处** | ★ **5 处**：审计档 §0/§1.3 · **914 迁移文件头**（⇒ **sha 随之变**）· `docs/db-changelog.md` 登记行 · `AGENTS.md` ㊱ · `LESSONS.md` §496 | ★★ **逐载体订正**（§0/§1.3 已重写为**按形态分栏**）★★ |
| ② | 「`Makefile:213` 的默认是 **900s**，`Makefile:168` 自记实测 **767s** ⇒ 余量仅 17%」 | ★ `Makefile:211` 的注释明写 **r1044 轮已把预算提到 1800s**；★ `Makefile:220` 实参 = `-timeout=1800s`；★ 那个 **767s** 是「**4 核 Linux**」机器的历史读数 | 审计档 §12 | ★★ 改写为**逐条对照表** ＋ 重述放宽到 3600s 的真实理由（★ SIGKILL ⇒ 台账静默留空）★★ |
| ③ | 「13 处注释订正」（且 `LESSONS` 写「`middleware/*` **4 个文件**」） | ★ **13 个 hunk** / **7 个文件** / 按行计 **15 行**；★ `middleware/*` 是 **3 个文件** | 审计档 §9/§11 · `LESSONS.md` §496 | ★★ **统一口径**（★ 数字必须连口径一起出现）★★ |

### 13.1 ★★ 为什么第 ① 条最严重（★ 不只是「写错了」）

★★ **两个形态的故障后果是相反的**：★★
★ 排除集形态下，越界值 ⇒ **探针流量被算成业务流量**（⇒ 统计污染）；
★ 白名单形态下，越界值 ⇒ **业务流量被静默丢弃**（⇒ 少计）。
⇒ ⇒ ★★ **把两种形态写成一句「一律用 X」⇒ 读者会照着错的那一句去设计修法。★★**

### 13.2 ★★ 复核还暴露了本门自己的一个**真洞**（★ 并已封上）

★ G8（退役形态注释门）旧版只有两条标记 ⇒ ★★ **`request_logs_hot.origin_stage`**
（★ `_hot.` 插在中间 ⇒ **不是** `request_logs.origin_stage` 的子串）⇒ ★★ **旧版门管不到它**。
⇒ ★ 实测全仓该形态命中 **0** ⇒ ★★ 补这条标记的**误报面为 0** ⇒ ★★ 已补 ＋ 加**阴性对照**
（★ 现役形态 `session_turns*.origin_stage` **不许**被标记命中）⇒ ★★ 变异臂 **M14'（期望绿）/ M15 / M16** 三臂坐实。

★ ★★ 另注：★ `_request_logs.origin_stage` 旧版是被**子串偶然**覆盖的（★ 它含有 `request_logs.origin_stage`）
⇒ ★★ 那不是判据，是巧合 ⇒ ★★ 这也正是本轮通式「**形态判据必须带词边界或改成结构化解析**」的又一次命中。★★

### 13.3 ★★ 三条沉淀（已写进 `LESSONS.md` §496）

1. ★★ **「引用机制」的每一句描述，都必须能指回源码的一行**；★ 一旦发现一处错 ⇒ **逐载体**订正（★ 复制会放大）。
2. ★★ **自己复述自己的话，永远查不出自己写错** ⇒ ★ 「刚写完的事实陈述」要过一遍**第二遍逐字回源码核对**；★ **独立复核比自查更有效**。
3. ★★ **数字在文档里出现时，必须连它的【口径】一起出现**（★ 「13 处 / 15 行 / 13 个 hunk」是三个不同的数）。

---

## §14 ★★ 下一轮的接手提示

1. ★ **211 / 252 执行 914**：迁移是幂等的、定义感知的 ⇒ 可直接重放；
   ★ 执行前**重采一次越界行数**（数据会变），并注意 252 的分区数多于本地。
2. ★★ **共享库 `pg_container_usable` 的探针缺陷**（§10.1.3）：★ 属主裁决项；
   修法与三态语义已在 §10.1.3 写好。
3. ★★ **「改之前先确认是运行的那份」**（§10.1.2）：★ 本轮连打两次空的教训。
4. ★ **存量迁移的 `SET LOCAL` → 会话级 `SET`**：需要一次「哪些迁移真的会因此失败」的评估，
   而不是一个批量替换（§6.3）。
5. ★ **`probekind.ProbeStages` 增删时**：必须同步改 914 的 `c_canon`，否则 G2 会红（这是**故意**的）。
6. ★ **canonical 视图退役 / 35 读点逐个实测分区下界 / U+FFFD 专项 / 属主裁决全清单** —— 承 R174。
