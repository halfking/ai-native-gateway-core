# Round 44 续：门禁的「installer 形态」不等于生产形态（第三个系统性缺口）

日期：2026-10-02
基线：`e8f384ca2`（origin/main）
起因：核实上一轮自己标注为「未验证、不下结论」的那一条——
`TestHotTableOldestRowAge_RealDB` 的 `42P01 relation "supplier_errors_hot" does not exist`。

---

## 0. 结论先行

查那一条红的时候，撞见了一个比它大得多的事实：

> **门禁建出来的库（installer 形态，applied=198，435 relations）缺少 5 张真实部署才有的表。**
> 门禁的量具和生产形态不是同一个东西。

这与本系列已修的族 1、族 2 同源，但高一层：**那两族是测试夹具撞库，这一族是门禁自己没建出生产形态。**

实测缺失（在一份 installer 形态门禁库上逐表查 `pg_class`）：

```
outbox_events            supplier_error_stats
supplier_errors          supplier_errors_hot        task_type_tier_config
```

其中 `supplier_errors_hot` 正是那条红测试要的表，而生产代码 `bg/metrics.go:344`
对它做 oldest-age gauge。

**本轮没有修**，因为正确的修法就是 §9-6/§9-7 那个未决问题：哪一套迁移是 SSOT。
下面是把它量化后的证据，供拍板。

---

## 1. 为什么门禁库会缺表：两套互不相识的迁移系统

| 来源 | 规模 | 谁执行 | 门禁跑吗 |
|---|---|---|---|
| `sql/migrations/startup/**` | 458 个编号，**197 个**注册进 `dbinit.StartupFiles` | 安装器 | ✅ |
| `deploy/sql/migrations/V*.sql` | 23 条 Flyway 风格 | `scripts/apply-db-revision-sequence.sh`、几个 `deploy/sql/deploy-*.sh` | ❌ |

那 5 张表全部只由第二套创建（例如
`deploy/sql/migrations/V371__supplier_errors_hot_and_stats.sql:40` 的
`CREATE TABLE IF NOT EXISTS supplier_errors_hot`）。门禁不跑它，于是缺。

### 顺带修正我自己一个数错的结论

第一次统计时我用 `glob('sql/migrations/startup/*.sql')`，漏了 **`up/` 子目录**，
得出「注册了但目录里没有对应文件：600」。那是**我的口径错了，不是仓库错了**：
`600` 在 `sql/migrations/startup/up/600_outbound_body_to_bodies_hot.sql`，
同时也 `go:embed` 在 `installer/cmd/llm-gw-installer/embeddata/startup/`。

递归重算后的准确数字：

```
sql/migrations/startup/** 里的迁移编号: 458
  已注册: 197
  未注册: 261
  注册了但找不到文件: 无        <- 这一点是好的
  未注册且编号 < 388: 130 个    <- 0..387 整条早期链没接入安装器
```

**「注册了但找不到文件：无」** 是一条值得单独记的正面结论：
我前几轮反复强调「迁移登记是三处动作，缺第三处会静默跳过」，
这里实测**没有**出现那种半接线状态。

---

## 2. 那条红测试本身：不是限定名错配

先证伪我上一轮的猜测。我当时说「很可能是同一个限定名错配」——**不成立**：

- `bg/hot_ts_column_realdb_test.go` 里**没有** `supplier_errors_hot` 字样，
  它从一个共享的 hot 表清单里取（`bg/metrics.go:344` 的 `case`）。
- 该测试直接连门禁库（`TEST_DATABASE_URL` / `TEST_DB_URL`），不经 per-test schema，
  也不自建表。
- 报错是纯 `42P01`：表在门禁库里根本不存在。

**所以它是「门禁形态不完整」，不是「夹具错配」。** 归类改正。

---

## 3. 试过「让门禁也跑 V 系」，**不成立**——这是本节最有用的部分

在一份 installer 形态库上按 V 号顺序把 23 条 V 系灌进去（psql，`ON_ERROR_STOP=1`）：

```
成功 17 / 失败 4
relations 687 -> 700 (+13)
supplier_errors_hot、outbox_events、task_type_tier_config 补上了
```

4 条失败各自说明了一个不同的问题：

| 迁移 | 失败 | 说明 |
|---|---|---|
| `V353__session_summaries_project_task_tags` | `constraint "chk_session_status" … already exists` | **两套链重复创建同一约束**——安装器迁移 `655_session_summaries_schema_reconcile.sql` 已经建了 |
| `V355__backfill_session_task_id` | `too many parameters specified for RAISE` | **V355 自身的 bug**，见 §4 |
| `V359__candidate_failure_logs_hot_and_partition` | `candidate_failure_logs not found or relam is NULL` | 该表依赖一个本库没有的访问方法 |
| `V360__credential_probe_queue_automatic` | `relation "public.credential_probe_queue" does not exist` | 它依赖的表由 **未注册** 的安装器迁移 489 创建（见 §5） |

**结论：两套链不是可组合的。**「给门禁补跑 V 系」不是一个开关，而是会引入 4 个新失败、
并且大概率打破当前已绿的测试的改动。**因此本轮没有把它做成门禁行为。**

---

## 4. 附带查出 V355 一个真 bug

`deploy/sql/migrations/V355__backfill_session_task_id.sql:147`：

```sql
RAISE NOTICE 'Coverage: %%', report.coverage_percentage;
```

格式串里的 `%%` 在 PL/pgSQL 中是**转义的字面百分号**，并不占位。
于是 `%` 占位符不存在，却又传了一个参数 → `too many parameters specified for RAISE`。

这条语句在**每次执行到它的部署上都会失败**。它是 V 系里唯一一条在实测中因自身语法失败的迁移
（其余 3 条是环境/依赖问题）。

修法是删掉多余的 `%`（`RAISE NOTICE 'Coverage: %', report.coverage_percentage;`），
但这是部署脚本，**本轮未改**。

---

## 5. 与 §9-4 族 2 那条 `session_dim` 是同一个根

上一轮查到：全新安装的 `session_dim.status` / `created_at` 是 `NOT NULL` 无默认值，
因为唯一声明默认值的 `350_session_analytics_fix.sql` **从未注册**。

本轮量化了这件事的规模：**编号 0..387 的 130 条迁移整条没接入安装器链**。
`350` 就在其中。`489`（V360 依赖的 `credential_probe_queue`）也在其中。

所以这不是「漏了一条」，是**早期链整体未接入**，而代码和 deploy 脚本仍在引用它们创建的对象。

---

## 6. 这对 §9-6 / §9-7 意味着什么

§9-6 是「基线双份统一（`sql/schema/01-schema.sql` 与 `embeddata/01-schema.sql` 同源生成）」，
§9-7 是「`sql/objects/` 定位：承重 SSOT vs 部署惰性 vs 双向漂移」。

本轮的证据把这两个问题的**范围**说清楚了：问题不止是「基线有两份」，
而是**有三个迁移来源**（已注册 startup / 未注册 startup 早期链 / deploy V 系），
它们既不互相引用、也不互相排斥，且已经在生产形态上产生实际差异
（缺 5 张表、1 条约束重复、1 条迁移语法错误、2 处依赖未注册对象）。

**这三套里谁是 SSOT，是产品决策，不是本轮能替用户定的。**
本轮交付的是把差异量化到可决策的粒度。

---

## 7. 数据可信度

- 「缺 5 张表」是在真实的 installer 形态门禁库上逐表查 `pg_class` 得到的，不是从 SQL 推的。
- 「V 系 17 成功 / 4 失败」是在同一份库上实跑的，4 条错误原文都记在 §3 表格里。
- 迁移编号统计**重算过一次**：第一次漏了 `up/` 子目录，结论是错的，已在 §1 写明并给出正确值。
- 探针库已删除，`itgate%` 数据库残留 0、角色 0。
