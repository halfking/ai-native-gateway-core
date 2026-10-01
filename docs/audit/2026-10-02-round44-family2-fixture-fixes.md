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

---

# 追加：族 1 在 bg 的收口（`public.` 限定名错配）

同一份报告的后半段。族 1 是 §9-4 里最大的一族（16 个），本节收掉 bg 的最后 5 个。

## 8. 一个不查产品代码就会造成假绿的陷阱

bg 剩下的 3 个 autoroute 测试和 `TestPolicyPublisherE2E…` 报的错误**形态和 §9-4 原始基线不同**
（不再是 42P07），因为第三轮的 per-test schema 隔离已经让它们走到了私有 schema。真实机制：

- 夹具 DDL 是**未限定名** → 落在 per-test schema；
- 测试自己的 DML 写死 `public.` → 打到**生产表**。

于是 INSERT 撞上生产表的约束：
`null value in column "tenant_id" of relation "request_logs_hot" violates not-null constraint`（23502），
而夹具那张表根本没这个约束。

**直接把测试的 DML 去限定名，对其中一部分是对的，对另一部分是假绿。** 判据只能是产品代码自己怎么写：

| 被测代码 | 引用方式 | 去限定名是否安全 |
|---|---|---|
| `bg/auto_route_settle_worker.go` | `FROM request_logs_hot`、`JOIN request_logs_hot`、`FROM/UPDATE auto_route_selections_hot`（**全部未限定**） | 安全 |
| `bg/auto_route_affinity_worker.go` | `FROM request_logs_hot`、`JOIN request_logs_hot` | 安全 |
| `domains/dispatch/policy_publisher.go:224` | **`FROM public.credentials`**（写死） | **危险** |

对 `policy_publisher_e2e_test.go` 去限定名，会变成：产品读 `public.credentials`（生产），
测试种进私有 schema —— **一个什么都不证明的绿**。这正是「门测的不是它声称测的那个东西，而且它绿着」。

统计口径也踩过一次：先统计时把 `_test.go` 一起数了，得出「产品两种写法都有（126 未限定 / 6 写死）」，
差点因此判定整体不可改。**只统计产品代码**之后结论才干净。数字不是装饰，口径错了结论就反。

## 9. 两种修法，按产品代码的写法分流

### 9.1 产品未限定 → 测试 DML 去限定

`auto_route_settle_worker_integration_test.go`、`auto_route_affinity_worker_integration_test.go`：
把 `INSERT INTO/FROM/UPDATE public.<三张表>` 全部去限定。这**不是**让测试变空跑——产品查询
经 `search_path` 解析到同一个 per-test schema，夹具就建在那里。

文件里写了反向警告，明确指出 `policy_publisher_e2e_test.go` 不可照抄。

### 9.2 产品写死 `public.` → 夹具必须**就是** `public.`

`DispatchPostgresContainer` 原本只有两态：空 schema → 共享库；非空 schema → per-test schema。
**两态都服务不了这一类**，于是新增第三态：

```go
DispatchPostgresContainer(t, ctx, "")          // 真实、已迁移的 public
DispatchPostgresContainer(t, ctx, "CREATE …")  // 私有 schema（仅当产品未限定时可用）
DispatchPostgresDatabase(t, ctx, "CREATE …")   // 私有【数据库】，其 public schema 就是夹具
```

第三态用 `internal/testdb` 派生一个空库、注册 DROP、并断言 DSN 真的落在那个库上。
`policy_publisher_e2e_test.go` 改调它。独立数据库让 `public.` 这两个名字重合，
测试于是无论产品怎么写都是诚实的。

## 10. 结果

```
bg  exit=1  PASS=1048  SKIP=14  FAIL=6     (本轮起点：PASS=1044 FAIL=10)
修好: TestAutoRouteAffinity_AggregateExcludesSyntheticActors_RealDB
      TestAutoRouteSettleBaselinesExcludeSyntheticActors
      TestAutoRouteSettleBatchMrLateralExcludesSyntheticActors
      TestAutoRouteSettleBatchSkipsSyntheticActorSelections
      TestPolicyPublisherE2EAppliesRevisionToLiveCredForwarder
新增: 无（listener 的轮换另计，见 §11）
itgate% 数据库残留: 0
```

族 1 在 bg 已清空。剩下 5 个非 flaky 的红分属：
`TestHotTableOldestRowAge_RealDB`（42P01 `supplier_errors_hot` 缺对象）、
`TestLedgerReconciler_RunOnce_RealDB`（族 3，要数据）、
`TestNonfeaturedWatchdogIntegration` 与 `TestStrictCanaryProbeQueueIntegration`（族 4，自起容器）。

## 11. flaky 终于被量化，而不是被解释

`TestAutoRouteRealtimeListener_IntegrationStopReturnsPromptlyAfterRealListen`
在整包运行的 5 次里轮换出现（有时 1 个红，有时 2 个）。整包数据无法区分
「我新增的 CREATE/DROP DATABASE 负载影响了时序」和「它本来就抖」。

于是**隔离复跑**：`-run TestAutoRouteRealtimeListener_Integration`，只跑这 4 个，
我新增的 `DispatchPostgresDatabase` 一次都不会被调用。

```
第1次 FAIL=1  第2次 FAIL=0  第3次 FAIL=0
第4次 FAIL=1  第5次 FAIL=0  第6次 FAIL=0
隔离 6 次：全绿 4 / 有红 2，且红的始终是同一个测试
```

**结论：约 33% 失败率，且与本轮改动无关**（隔离运行里我的代码路径不参与）。
错误恒为 `real NOTIFY never reached refresher; calls=0`。

顺带记一次我自己把量具弄坏：`grep -cE '…--- FAIL'` 在零匹配时**退出码为 1**，
我写的 `|| echo 0` 于是又追加了一个 0，`$f` 变成 `0\n0`，字符串比较失败走进错误分支，
第一次隔离复跑的结果是假的。改成 `wc -l` 分离计数与退出码后重跑才有上面这组数。

## 12. 本轮没有解决的

- `TestHotTableOldestRowAge_RealDB`：`42P01 relation "supplier_errors_hot" does not exist`。
  未查（它可能同样是限定名错配，也可能是真缺对象）。
- 族 3 / 族 4 共 11 个：需用户裁决，见 `2026-10-02-round44-red-triage.md`。
- `session_dim` 默认值：需新增迁移 + 三处登记，需用户拍板。
