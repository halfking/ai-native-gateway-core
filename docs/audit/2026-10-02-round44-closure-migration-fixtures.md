# Round 44 §9-3 收口（第四轮）：迁移测试的 per-test 数据库隔离

日期：2026-10-02
基线：`be79ef3d3`（`main`，已 push）
范围：`sql/migrations/startup` 的 11 个 integration 专用测试文件；`run-integration-gate.sh` 的包参数与覆盖守卫

---

## 0. 结论先行

| 指标 | 修复前 | 修复后 |
|---|---|---|
| `sql/migrations/startup`（installer 形态） | PASS=167 **FAIL=6** | PASS=**172** FAIL=**1** |
| 残留 scratch 数据库 | 10（且泄漏检查误报 0） | **0** |
| 门禁包参数打错时的诊断 | 「该包没有 integration 测试」（指错方向） | go list 原始错误 + 明确区分 |

唯一剩下的 1 个 FAIL 是 `TestMigration715FreshChainApplyMigrations`。**本轮没有把它变绿**，推翻了它原有的归因，并修掉了其中两层成因——见 §4 与 §4A。

两项变异验证均按预期转色，且 M1 精确复现了修复前的 6 FAIL（§5）。

---

## 1. 起手就撞上的一个假象

第一次跑门禁，脚本报的是：

```
ERROR: sql/migrations/startup 下没有任何仅由 integration build tag 引入的测试文件。
请把它从 CI 包列表里去掉，或给它补上真正的 integration 测试。
```

但该包实测有 **11 个** integration 专用测试文件，`go list -tags=integration` 与无 tag 的差集为 11。

真实原因有两个叠加：

1. **我的传参错了**：`sql/migrations/startup` 少了 `./`。`go list` 把裸相对路径当 std 包去找，
   `package sql/migrations/startup is not in std`，exit 1。
2. **守卫把证据吞了**：`gatelist_files()` 写着 `go list ... 2>/dev/null`。加载失败 → stdout 为空 →
   差集为 0 → 报「没有 integration 测试」。

第 2 点是守卫自身的缺陷，且方向是错的：它让「包路径打错 / 包加载失败 / 源码编译不过」三种完全不同
的问题，都显示成「你去补测试吧」。这正是这个守卫当初被造出来要消灭的那类**形状错误的绿/红**，
只是这次表现为红而非绿。

修法两处：

- 参数归一化：裸相对路径补 `./`（`./`、`../`、绝对路径、完整模块路径不动）。
- `go list` 的 stderr 不再丢弃；非零退出直接致命退出，并明确说明「这与该包没有 integration 测试是两回事」。

变异/对照片段（同一台机、同一时刻）：

| 场景 | 修复前 | 修复后 |
|---|---|---|
| `sql/no/such/pkg` | `stat ...: directory not found` 被吞 → 「没有 integration 测试」 | 打印 go list stderr → `ERROR: go list 无法加载包 './sql/no/such/pkg' …先修加载错误，再谈覆盖率` |
| `./internal/trace`（真的没有） | 「没有 integration 测试」 | 保持不变（诊断没有被新分支吞掉） |

---

## 2. 6 个 FAIL 的分类

| 测试 | 错误 | 归类 |
|---|---|---|
| `TestMigration541ScopeRevisionIntegration` | `2BP01` cannot drop table `credential_model_bindings` … | 自建夹具 + **破坏性** |
| `TestMigration541ScopeRevisionConcurrentBump` | 同上 | 同上 |
| `TestMigration705ReattachAndSelfHeal` | `42P07` `public.request_logs` already exists | 自建夹具撞满库 |
| `TestMigrationS1ASessionFamily` | `42P07` `public.session_turns_hot` already exists | 自建夹具撞满库 |
| `TestMigration717HotColumnAlignmentBothColumnStates` | `42P07` `public.request_logs_hot` already exists | 自建夹具撞满库 |
| `TestMigration715FreshChainApplyMigrations` | `42804` 分区列与父表不符 | **与上面五项不同类**，见 §4 |

541 那两条要单独说一句：它们的 `fixtureCleanup541` 会执行
`DROP TABLE public.credential_model_bindings` 和 `DROP TABLE public.provider_models`。
在满库上这不只是「多余」，2BP01 只是 PostgreSQL 拦住了它——**同一段语句指向真实部署就是数据丢失**。
测试自己的 skip 文案写的是「requires a disposable PostgreSQL database」，它从一开始就没打算跑在共享库上。

---

## 3. 为什么不是 per-test schema，而是 per-test database

上一轮（`be79ef3d3`）给 `bg` / `taskprofile` 落地的 `internal/testschema`（per-test **schema**）
在这里不适用，理由是硬的：

- 五个出问题的夹具全部写死 `public.`：`CREATE TABLE public.request_logs`、
  `CREATE TABLE public.session_turns_hot (LIKE public.session_turns …)`、
  `DROP TABLE public.credential_model_bindings`。
- 更关键的是，**它们执行的是生产迁移 SQL**（`sql/migrations/startup/705_*.sql` 等），
  那些文件里也全是 `public.`。那不是测试代码，不能为了测试改掉限定名。

per-test schema 靠 `search_path` 路由**未限定名**，而这里每一个名字都是限定名。
所以这个家族的正确隔离单位是整个数据库。

### 3.1 根因比「缺隔离」更具体

`partitionBehaviorContainer()`（`migration_694_behavior_integration_test.go:326`）本来有两条分支：

```go
if dsn := os.Getenv("TEST_PG_URL"); dsn != "" {
    return conn_to(dsn)        // 分支 1：连「那个」库
}
container, _ := postgres.Run(…)  // 分支 2：自起专用 testcontainer
```

**五个测试（694 / 705 / 706-708 / 717 / 759）都是照着分支 2 写的**——一个除了自己的夹具什么都没有的库。
门禁注入 `TEST_PG_URL` 静默选中了分支 1，把它们放到了 435 relations 的生产形态库上。

所以这不是「忘了做隔离」，是**注入 DSN 覆盖掉了夹具自己声明的前置条件**。

### 3.2 落地

新增 `internal/testdb`（与 `internal/testschema` 并列的姊妹包）：

- `testdb.Create(t, baseDSN) string` —— 从 base DSN 派生一个全新空库，返回指向它的 DSN，
  `t.Cleanup` 里 `DROP DATABASE … WITH (FORCE)`。
- 库名由 base 库名 + pid + 纳秒派生，校验为纯标识符，截断到 63 字节（截前缀、留后缀）。
- `mustLandInScratch` —— 连回去 `SELECT current_database()` 断言确实落在 scratch 库上。

接线：

- `partitionBehaviorContainer` 分支 1 改为 `dsn = testdb.Create(t, dsn)`（覆盖 694/705/706-708/717/759）。
- 541 两处入口同样接入。

改共享 helper 会同时影响 5 个测试，所以逐个核对了没回归：**694 与 759 在 installer 形态下改前改后都是 PASS**。

### 3.3 结果

```
修复前  exit=1  PASS=167  SKIP=0  FAIL=6
修复后  exit=1  PASS=172  SKIP=0  FAIL=1
```

`grep -c 'testdb: cleanup'` = 0，`SELECT count(*) … LIKE 'itgate%'` = 0。

---

## 4. 剩下的 1 个 FAIL：推翻原有归因

`TestMigration715FreshChainApplyMigrations` 报：

```
apply bootstrap ../../schema/01-schema.sql:
ERROR: table "request_logs_2026_07" contains column "application_id"
       not found in parent "request_logs"  (SQLSTATE 42804)
```

测试原本把它归因为「01-schema.sql 快照列漂移」（`request_logs_hot` 与父表列类型不一致），
并在 `dbpkg.Open` 失败处留了 SKIP 逃生口。**这个归因是错的**，三条实测：

1. 把 `00-prereqs.sql` + `01-schema.sql` 用 psql（`ON_ERROR_STOP=0`）灌进空库，**零错误**。
2. 之后 `public.request_logs` **有** `application_id`；`request_logs_2026_07` **已挂载**
   （`pg_inherits` = 1）；2026 的月分区共 74 张。
3. 父表 `CREATE TABLE`（01-schema.sql:7205）到 `ATTACH PARTITION`（:19528）之间，
   对 `request_logs` **没有任何** `ADD/DROP/RENAME COLUMN`。

再取一次证：让门禁 `KEEP_GATE_DB=1` 跑完这一条测试后直接查库，**终态是正确的**——
父表 154 列（含 `application_id`），`request_logs_2026_07` 已挂载。

结论：**42804 是测试自己的 `execTolerantSnapshot` 在分块容错重放过程中产生的瞬时错误**。
它把 64 KiB 语句块用 simple protocol 一次 Exec（隐式事务），块内任一失败即整块回滚、
再逐条重放并按 `isIgnorableSnapshotError` 容忍；重放到某条 `ATTACH PARTITION` 时父表恰好缺列。
`42804` 不在该容忍名单里，于是被当成致命错误抛出——**而它留下的库其实是完整的**。

顺带一个次生问题：这个 SKIP 逃生口锚在 `dbpkg.Open` 的错误上，但实际失败发生在更早的
`execTolerantSnapshot` 引导调用里，所以那段 SKIP 从来没机会生效。

### 4.1 本轮只更正归因，不改行为

代码里把注释与 SKIP 文案改成了上面的实测结论，并写明**故意不**把 `42804` 加进
`isIgnorableSnapshotError`：那份名单同时管着这个测试真正要抓的列类型漂移，
放宽它等于把真缺陷藏起来，而不是消掉测试harness 自己的噪声。要修得改重放器本身，
那是独立的一笔。**没有为了让门变绿而放宽容忍名单。**

---

## 4A. 追加：715 FreshChain 的第二层定位（本轮后续，仍未变绿）

§4 停在「成因是重放器」。本轮继续往下挖，机制闭合了，但**测试仍然 FAIL**。

### 4A.1 同一个输入，两次运行报不同的错误

把 `execTolerantSnapshot` 的致命错误改成报出出错语句后，它自报家门：

```
snapshot statement #2297 (chunk 2274, offset 23) failed:
ERROR: cannot attach index "credential_model_index_2026_0_bucket_credential_id_raw_mod_idx2"
       as a partition of index "credential_model_index_bucket_cred_model_key"  (SQLSTATE 55000)
```

而**上一次运行**报的是 `42804`。同一份 `01-schema.sql`、同一个空库起点，两次给出两个
完全不同的致命 SQLSTATE。这本身就排除了「某条 DDL 有确定性问题」这个解释。

### 4A.2 executor 没有复刻它声称要复刻的语义

`execTolerantSnapshot` 的注释写着「复刻 init-local-db 的 psql（无 ON_ERROR_STOP）语义」，
但实现是**遇到第一个不在容忍名单里的 SQLSTATE 就 return**。psql 报错后是继续往下走的。

这不只是提前退出：容错地跳过一条 `CREATE` 会留下半成品对象，使后面某条语句以一个
**不在名单里**的 SQLSTATE 失败——于是失败点取决于哪条语句先被跳过，也就解释了 4A.1 的不确定性。

**已修**：降级路径不再因任何服务端 SQL 错误中止；只有非服务端错误（连接/协议层）才中止。
被容忍的错误按 SQLSTATE 汇总 `t.Logf` 出来，不静默。
`isIgnorableSnapshotError` 整张名单随之删除——它编码的是「提前中止」这个错误前提。

### 4A.3 真正的隔离缺口：它在往装满的库上灌全新安装快照

改完 executor 后，引导跑完了，并报出它的真实状态：

```
execTolerantSnapshot: tolerated 1705 statement errors while replaying chunks:
  42P07 x1046, 42P16 x168, 42710 x141, 42723 x103, 42703 x98, 42P01 x60,
  55000 x59, 42809 x28, 42804 x2
```

**42P07「relation already exists」占 1046 条**。这不是引导噪声，这是快照被灌在了一个
**已经装满 435 relations 的门禁库**上。715 FreshChain 与本轮修掉的另外五个是同一个病：
它也假设自己有一个全新的空库，而门禁递给它的是 installer 形态库。

**已修**：同样接入 `testdb.Create`。改用空库后，引导的容忍统计**整行消失**——零错误通过。
42804、55000、42P07×1046 全部是这一个原因在不同语句上的表现。

### 4A.4 剩下的最后一层，以及为什么不补

现在它跑到 `db.Open` 才失败，错误干净且具体：

```
create idx_session_aggregate_outbox_done_completed_at:
ERROR: relation "public.session_aggregate_outbox" does not exist  (SQLSTATE 42P01)
```

原因清楚了：生产全新安装是三步，本测试只做了两步。

```
生产：  00-prereqs + 01-schema 快照
   →   安装器注册的 198 条启动迁移（session_aggregate_outbox 由 630 建）
   →   二进制启动链 db.Open（ensure* 链）
本测试：00-prereqs + 01-schema 快照 → db.Open          ← 少了中间那一步
```

门禁脚本自己的注释早就记过这件事（GATE_APPLY_STARTUP 段落：「baseline alone is missing
tables that only migrations create (session_aggregate_outbox via 630 …) 」）。

**本轮不补**，理由具体：注册清单在独立 Go module `installer/` 的 `dbinit.StartupFiles`，
根 module 的测试无法 import；而 `sql/migrations/startup/` 下有 **793 个** `.sql`
（含 `.down.sql` 与未注册的历史文件），按文件名排序全量灌是错的。
从测试里解析 `installer` 的 Go 源码来取清单属于脆弱做法，不做。

可行的补法（留给下一轮择一）：
1. 把注册清单抽成数据（如生成的 TSV），安装器与测试共用一份——顺带解决 §9-6 的双份问题；
2. 或者由门禁注入一个「已装到 714 态」的库，测试只验 389→715 这一段升级；
3. 或者在 installer module 内加这条测试（它能直接 import `dbinit`）。

### 4A.5 当前状态（合并后复跑，稳定可复现）

```
sql/migrations/startup   exit=1  PASS=172  SKIP=0  FAIL=1
唯一 FAIL: TestMigration715FreshChainApplyMigrations  (8.48s)
失败点:  db.Open -> 42P01 public.session_aggregate_outbox does not exist
itgate% 数据库 0，itgate% 角色 0
go test ./sql/schema/  ok
```


### M1：摘掉隔离 → 应回到修复前的 6 FAIL

变异：`Create` 不校验、直接 `return baseDSN`（等于退回「连共享库」）。

```
变异前  exit=1  PASS=172  SKIP=0  FAIL=1
M1      exit=1  PASS=167  SKIP=0  FAIL=6
SQLSTATE: 42P07 ×4, 42804 ×1
FAIL 名单与修复前逐字一致
```

**精确往返**：基线 6 → 修复 1 → 变异回 6。这同时证明了「隔离是承重的」，
而不只是「断言存在」。

### M2：破坏 DSN 改写 → `mustLandInScratch` 必须抓到

第一次做 M2 时**它绿了**（PASS=172/FAIL=1，与未变异完全相同）。查下来是**死变异**：
`dsnFor(&scratchCfg, scratch)` 当初同时收 config 和 `database` 两个参数，函数体只用后者，
`cfg.Database` 根本没被读——我改的是一个不承重的字段。diff 看着很像改了。

这暴露的是一个真实陷阱（同一个值两个来源、可静默不一致），已把冗余参数删掉，
让 `cfg.Database` 成为唯一来源。重跑 M2：

```
M2      exit=1  PASS=166  SKIP=0  FAIL=7
mustLandInScratch 触发 6 次（"DSN resolves to database …, want the scratch database …"）
```

第 7 个 FAIL 是本轮开始就存在的 715。

### control

全部还原后：`PASS=172 FAIL=1`，`itgate%` 数据库 0、角色 0。

---

## 6. 我自己引入又自己抓到的一个泄漏

`testdb.Create` 第一版在函数返回时 `defer admin.Close()`，而 `t.Cleanup` 在之后才跑，
于是每次 `DROP DATABASE` 都报 `conn closed`，**两个门禁跑次共泄漏 10 个库**。

它没被立刻发现，是因为我的泄漏检查 SQL 写的是：

```sql
… LIKE '%\_t%\_t%'
```

而生成的库名是 `itgate_63963_5970_t64746_25019000`——**只有一处 `_t`，模式匹配不上，检查恒返回 0**。
一个恒为 0 的检查和一个干净的检查长得一模一样。

已改用能匹配的正则 `^itgate_[0-9]+_[0-9]+_t[0-9]+_[0-9]+$` 复查（先捞出并清掉那 10 个），
并把 cleanup 改为自开连接。修完后：`itgate%` 库 0、角色 0，日志里 `testdb:` 报错 0 条。

---

## 7. 回归

- `go vet -tags=integration ./sql/migrations/startup/ ./internal/testdb/` — OK
- `go test ./sql/schema/`（门禁脚本的全部守卫单测）— ok, 8.185s
- `gofmt`：我新增/修改的行均干净。`migration_694/705/744` 在 HEAD 上本就有既存的
  gofmt 差异（694 在 238 行），**未顺手重排**，避免制造无关 diff。
- 门禁脚本是所有包共用的，故另跑 `bg`、`taskprofile` 两包确认无外溢（见 §8）。

---

## 8. 未完成 / 明确不做

- **`TestMigration715FreshChainApplyMigrations` 仍 FAIL**。已修两层（executor 语义 + 隔离缺口，
  见 §4A），剩最后一层：它漏了「安装器注册的 198 条启动迁移」这一步，`db.Open` 报
  42P01 `public.session_aggregate_outbox does not exist`。补法需要跨 Go module 拿注册清单，
  三个选项列在 §4A.4，本轮刻意未做。
- **§9-5** amd64 `kx-citus-pg17` 镜像：依赖外部镜像构建基础设施，本机 arm64，CI job 仍常年红（未假装变绿）。
- **§9-6** 基线双份统一：R43 已对账 2612 vs 6354 对象（ADDED 3759 / MISSING 17），本轮仍刻意延期。
  §4 顺带证明了一件事：`01-schema.sql` **本身能干净应用**，所以「快照列漂移」不在待办里，
  但这不等于 §9-6 的双份统一可以撤销——那是另一件事。
- **§9-7** `sql/objects/` SSOT 定位：未决。
- **bg 剩余的 10 个 FAIL**：本轮未逐个处理。
- **其余自建夹具未迁移**：本轮只处理了 `sql/migrations/startup` 这一个包。
  仓库里还有别的包共享数据库建夹具（`scripts/audit`、`tests/48h-audit/…` 已有各自的 scratch 库写法，
  可作为模板），未普查。
- `sql/schema/integration_fixture_shapes.tsv` 仍**刻意留空**。本包现在在 installer 形态下 172/1，
  但 1 个 FAIL 说明「整库形态」仍不是这个包的正确轴；登记一行「prereqs 更好」会是误导。
