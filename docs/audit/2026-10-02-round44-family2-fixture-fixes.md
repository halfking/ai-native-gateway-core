# Round 44 §9-4 续：族 2 的 6 个真缺陷，以及一条新发现的生产 schema 分歧

日期：2026-10-02
基线：`06948fac9`（origin/main），本轮改动在其上
对应：§9-4 族 2「夹具与生产 schema 漂移」

---

## 0. 结论先行

| 包 | 修复前 | 修复后 | 状态 |
|---|---|---|---|
| `domains/hooks/handoff` | 1 FAIL | **0 FAIL**（119 PASS / 1 SKIP） | 已验证 |
| `domains/dispatch` | 2 FAIL | **0 FAIL**（377 PASS / 2 SKIP） | 已验证 |
| `admin` | 11 FAIL | **9 FAIL**（2432 PASS） | 已验证，0 新增破坏 |
| `bg` | 10 FAIL | **10 FAIL**（1044 PASS） | `TestMigration762ProjectBackfillChain_RealDB` 修好 |

**新发现（未修，需拍板）**：全新安装出来的 `public.session_dim` 有两列是 `NOT NULL` 且
**无默认值**，而仓库里声明过默认值的迁移**从未被安装器注册**。详见 §6。

---

## 1. 族 2 不是一个原因，是三个

`§9-4` 把这 6 个归为一族「夹具与生产 schema 漂移」。逐条查下来，三个不同成因，
其中两个的修法和归属都不一样。

### 1.1 `23503 fk_session_tenant`（3 个）—— 夹具没种 FK 的父行

`session_summaries.tenant_id` 引用 `public.tenants(code)`（`01-schema.sql:29231`），
而门禁库是**空的**：`SELECT count(*) FROM tenants` = **0**（实测）。

于是只种子、不种父行，必然 23503。这不是 schema 漂移，是**夹具不完整**。

有意思的是 `admin/session_task_project_p1_test.go:35` 早就写着
`tenant = "default" // fk_session_tenant 外键要求已存在租户`——**作者知道这个前提**，
只是假设了一个 `default` 租户存在，而这个假设在一次性库上不成立。

修法：种父行，并且**只删自己建的那一行**（`ON CONFLICT DO NOTHING RETURNING true`
拿到 created 标志）。删别人的租户就是本轮在 `sql/migrations/startup` 拆掉的那类破坏性夹具。

### 1.2 `23502` NOT NULL（2 个）—— 夹具没跟上「无默认值」这件事

见 §6：`session_dim` 的 `status` 和 `created_at` 在真实库里都是 `NOT NULL` **且无默认值**。
产品代码两处都显式写字面量（`internal/sessionv2mirror/session_dim.go:80` 和 `:113`），
夹具没有，于是 23502。

修法：夹具照产品代码那样显式传值。

### 1.3 `42703 max_queue_depth`（2 个）—— 自建表落后于产品查询

`domains/dispatch` 那两个测试**自起 testcontainer 并自建 `credentials` 表**，
而 `domains/dispatch/policy_publisher.go:220-223` 的 `publishCatchUp` 已经 SELECT 9 列，
其中 `max_queue_depth` / `max_queue_wait_ms` 是 `566_credentials_governor_revision` 之后加的，
夹具没跟。

**先证伪再定性**：我原本把它标为「需确认是夹具缺列还是产品引用了从未创建的列」。
实测 `information_schema.columns`：`credentials.max_queue_depth integer, nullable, no default`
—— **列是存在的**。所以是夹具落后，不是产品引用了不存在的列。

修法：按产品 SELECT 的 9 列把夹具建全，类型/可空性取自实测而非猜测。

### 1.4 `23502 handoff_logs.session_id`（1 个）—— 少给了三个必填列里的一个

`handoff_logs` 的 NOT NULL 列是 `id, session_id, tenant_id, trigger_reason, tokens_at_handoff`，
而测试只 INSERT 了 `tenant_id`。报错只提 `session_id` 是因为它是第一个缺的，
另外两个本来也会接着炸。**一次性补齐三个**，而不是等下一轮再报一个。

---

## 2. 我自己犯了两次错，都记在这

### 2.1 修错了对象

第一版改的是 `admin/session_analytics_realdb_test.go` 里的 `seedSessionRow`——
改得没错，但**那两个红测试根本不用它**（它只被另外几个已经绿着的测试调用）。
admin 复跑：11 → 11，**一个没动**。

教训和 §5 的 M2 是同一族：我验证了「改动进了文件、门是绿的」，但没验证「我改的是不是那一条」。
正确做法是**先从错误行号反查是哪个文件哪一行**（`session_task_project_p1_test.go:66`、
`session_task_project_p5_test.go:70`），再动手。

### 2.2 修法自我拆台

`bg` 的第一版把种租户放在了 `cleanup := func(){...}` **之前**，而原代码里
`cleanup()` 是**紧跟定义后立即调用**的（为了清上一轮残留）。于是第一次 `cleanup()`
看到 `createdTenant == true`，**把刚种进去的租户删了**，测试照旧 23503。

闭包按引用捕获变量，所以正确顺序是：定义 cleanup → 立即调用 → **再**种租户。
代码注释里把这个顺序理由写下来了，免得下一个人「顺手整理」回去。

---

## 3. 已验证的修复

```
domains/hooks/handoff   exit=0  PASS=119  SKIP=1  FAIL=0    (原 1 FAIL)
domains/dispatch        exit=0  PASS=377  SKIP=2  FAIL=0    (原 2 FAIL)
admin                   exit=1  PASS=2432 SKIP=30 FAIL=9    (原 11 FAIL)
admin 修好: TestProjectTasksSkipsNullTaskID
           TestTaskSummaryAggregatesModelsTagsAndWallClockDuration
admin 新坏: 无
```

---

## 4. admin 剩下的 9 个不在这族里

它们分属 §9-4 的族 3（要生产形态数据）与族 4（绕过门禁自起容器），**需要裁决**，
本轮不动。清单见 `2026-10-02-round44-red-triage.md`。

---

## 5. bg：一个真实修复 + 一个被误当成回归的 flaky

`TestMigration762ProjectBackfillChain_RealDB` 的修复过程是逐层剥洋葱，
每一层都验证过才继续（不是猜）：

```
23503 fk_session_tenant   -> 种 tenants 父行
23502 session_dim.status  -> 显式传 'active'
23502 session_dim.created_at (第 137 行)  -> 显式传 NOW()
23502 session_dim.created_at (第 211 行)  -> 显式传 NOW()
```

第 2、3 层说明**打地鼠式逐条修是低效的**：修完一处才暴露下一处。
第三次改成「把这个文件里所有 `session_dim` 写入一次性对齐」才收敛
（该文件 INSERT 总数 2，含 `created_at` 的也是 2）。

### 一个我没有归因的 flaky

`TestAutoRouteRealtimeListener_IntegrationStopReturnsPromptlyAfterRealListen` 在某一轮
**新变红**（`real NOTIFY never reached refresher; calls=0`，耗时 120.04s），
而我在上一轮只改了另一个文件。下一轮它又变绿了，反而是它的兄弟
`...ContextCancelStopsPendingRefresh` 变红。

三次运行（f2b / f2c / f2d）红的那一个在三个 listener 测试之间**轮换**：

| 运行 | 红的那一个 |
|---|---|
| f2b | `...StopReturnsPromptlyAfterRealListen`（120.04s） |
| f2c | `...ContextCancelStopsPendingRefresh` |
| f2d | `...TriggerFiresRefreshOnce`（2.05s） |

错误一致：`real NOTIFY never reached refresher; calls=0`。

**三个不同的测试轮流红，这比任何单次观察都更能说明它是 flaky，而不是某次改动的后果。**
我**没有**把它算作我的修复引入的回归，也没有算作修好的东西。

顺带厘清一件容易记错的事：这 4 个 listener 测试在 §9-4 的原始基线里是 **42P07**
（`exec schema: relation "credentials" already exists`），现在 3 个通过、1 个间歇红。
那个 42P07 的改善来自**第三轮的 per-test schema 隔离**（`be79ef3d3`），
不是本轮。本轮在这个族里没有功劳，也没有责任。

附带观察：`--- FAIL` 的耗时包含 `t.Cleanup`，而 `waitFor(t, 2*time.Second, …)` 本身
只等 2s（`bg/auto_route_realtime_listener_test.go:38` 实现正确）。那 120s 里约 118s
花在 cleanup 的 `l.Stop()` 上——和该文件顶部注释描述的 Stop 行为一致，
值得单独看，但不是本轮范围。

---

## 6. 新发现：`session_dim` 在全新安装上是无默认值的（**未修**）

### 实测

在一份 installer 形态的门禁库上（`applied=198`，435 relations）：

```
information_schema.columns where table_name='session_dim':
  status      character varying  notnull=YES  default=<NONE>
  created_at  ...                notnull=YES  default=<NONE>
```

### 成因（两条都查证过）

| 迁移 | 是否注册到 `dbinit.StartupFiles` | 建表时 `status` 的定义 |
|---|---|---|
| `350_session_analytics_fix.sql` | **否** | `VARCHAR(20) NOT NULL DEFAULT 'active'` |
| `805_session_dim_reconcile.sql` | 是 | `character varying NOT NULL`（**无默认值**） |

已注册迁移的编号区间实测是 **388 … 809**——`350` 从来没进过安装器链。
两条都是 `CREATE TABLE IF NOT EXISTS`，先落地的赢，所以全新安装拿到的是 805 的形态。

### 影响

- **今天没有生产故障**：产品代码两处 INSERT 都显式传 `'active'`（`session_dim.go:80`、`:113`）。
- **但这是一个潜伏陷阱**：任何省略 `status` / `created_at` 的写入（第三方 SQL、临时运维、
  以及本轮修掉的这些夹具）都会 23502。
- **一个库两种形态（待验证的假设）**：升级过的部署可能保留着 350 时代的形态（**有**默认值），
  全新安装才是 805 的形态（**无**默认值）。这条假设需要在一台真实部署上核对
  `information_schema`，本轮没有这样的环境，**不下结论**。

### 为什么不修

修法是一行：

```sql
ALTER TABLE public.session_dim
    ALTER COLUMN status      SET DEFAULT 'active',
    ALTER COLUMN created_at  SET DEFAULT NOW();
```

但它需要**新增一条迁移并完成三处登记**（embeddata 文件 + `StartupFiles` +
`go:embed` 变量与 `embeddedSQLFiles` 映射），而这一片区域正是 §9-6（基线双份统一）
和 §9-7（`sql/objects/` SSOT）尚未定论的地方。本轮不替用户动生产 schema，
把迁移体和登记点写清楚，等拍板。

---

## 7. 数据可信度

- 每一处修复都用**真实门禁跑**验证过，不是靠读代码推断。
- §1.1 的 `tenants` 为空、§1.3 的 `max_queue_depth` 存在、§6 的两个列无默认值，
  都是直接查 `information_schema` / `pg_database` 得到的，不是从迁移文件推的。
- admin 保留了修复前后的 FAIL 名单对比（`comm`），明确区分「修好的」和「新坏的」，
  后者为空。
- bg 已复跑三次确认。`TestMigration762ProjectBackfillChain_RealDB` 从 23503 一路修到通过，
  最终 `exit=1 PASS=1044 SKIP=14 FAIL=10`，修好 1 个、新增 1 个（listener flaky，已单列）。
- 环境已清干净：`itgate%` 数据库 0、`itgate%` 角色 0（探针库与租户角色均已删）。
