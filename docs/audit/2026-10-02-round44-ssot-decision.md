# 迁移 SSOT 决策落地记录（Round 44 收口）

日期：2026-10-02
状态：已落地。**非破坏性**——本轮没有删除任何一份迁移文件。

---

## 1. 决策

**已注册的启动链（`dbinit.StartupFiles`，199 条）是唯一的 SSOT。**
`sql/migrations/startup/` 下未注册的文件、以及 `deploy/sql/migrations/V*.sql`，
一并标记为**部署惰性**（deployment-inert），不进入安装器。

「标记为惰性」的确切含义：写进文档 + 在本文件里列出清单与判据。
**没有删除、没有移动、没有改写任何一份 SQL。** 删它们是不可逆的，而「不进安装器」
这个决定本身不需要删任何东西就能生效——真正的约束是 `StartupFiles` 这个列表，
而它已经是唯一真源。

---

## 2. 修正了两个此前记错的事实

### 2.1 「130 条未注册」在任何口径下都复现不出来

| 口径 | 数量 |
|---|---:|
| `sql/migrations/startup/` 下 .sql 总数 | 806 |
| 其中 `.down.sql`（回滚，永不注册） | 331 |
| **未注册的正向迁移** | **290** |
| 已注册 | 199 |

此前记的「130 条」在 0..387 区间、含/不含 `.down` 等各种切法下都算不出来。
**290 是实测值**，分布：0xx 53、1xx 1、2xx 2、3xx 100、4xx 88、5xx 25、6xx 14、7xx 6。

### 2.2 架构不是「一条链」，是「基线 + 388 以上的链」

注册链里**没有任何编号低于 388 的迁移**（唯一的例外形态是 `session_turns_hot_bootstrap.sql`
等 18 个非数字开头文件）。这解释了此前一系列困惑：

- 为什么 341/534 这类编号的迁移不在链里——它们属于**基线时代**；
- 为什么 `deploy/sql/migrations/V*.sql` 与安装器链**不可组合**（17 成功 / 4 失败）——
  它们与基线做的是同一件事，叠上去自然冲突；
- 为什么「三套迁移来源」并不是三个对等的候选：**其中两套本质上是基线的前身**。

生产引导的真实形态是：

```
embeddata/01-schema.sql（基线快照） + 启动迁移 388..809 + db.Open 自愈
```

---

## 3. 决策直接带来的修复：534

这是本轮唯一的生产阻断修复，也是决策落地的主要产出。

`db/handoff_schema.go` 每次 `db.Open` 都跑一条 handoff 契约：要求 `handoff_logs`
是 RANGE 分区父表，并有 `handoff_logs_hot` 堆表孪生、`handoff_logs_with_current_month`
视图、两个分区函数。**534 是唯一产生该形态的迁移，而它从未注册。**

后果：每一个按「基线 + 注册链」装出来的全新库，网关都拒绝启动。实测复现，
且失败点**不在链上**——198 条迁移全部干净应用：

```
startup: applied=198 failed=0 missing=0
db.Open (full startup chain) failed: handoff hot+columnar schema contract:
ERROR: handoff_logs schema contract requires a RANGE partitioned parent;
run startup migration 534 (SQLSTATE P0001)
```

534 虽然编号 < 388，但**快照引导无法携带它**——所以它是唯一一条确实该进链的
子 388 迁移。534 自身按「legacy heap 形态可重放」来写，正是全新安装的形状。
Citus columnar 不是新依赖：同链的 392/532/535/562/627 已在用。

四处登记一次做齐（历史上漏过任何一处都会红）：

| 位置 | 内容 |
|---|---|
| `installer/internal/dbinit/runner.go` | `StartupFiles` 新增，位次 20（与 532/535 同类） |
| `installer/cmd/llm-gw-installer/embeddata/startup/` | 文件副本（字节一致，sha256 `1dbd3e70…`） |
| `installer/cmd/llm-gw-installer/main.go` | `go:embed` 变量 + `embeddedSQLFiles` 映射 |
| `sql/schema/installed_startup_migrations.tsv` | 清单产物（198 → 199） |

**验证**：

- `TestMigration715FreshChainApplyMigrations` 由红转绿；
- `sql/migrations/startup` **整包** `exit=0 PASS=173 SKIP=0 FAIL=0`（原 FAIL=1）；
- 门禁形态 relations 435 → 441。

**红测试 22 → 21。**

---

## 4. 顺带修掉的一个守卫缺陷

`installer/internal/dbinit/startup_manifest_test.go` 的漂移守卫在失败时打印

```
cd installer && go test ./internal/dbinit/ -run TestStartupManifest -update
```

而这条命令**执行不了**：`regenerateRequested()` 扫 `os.Args` 找 `-update`，
但 `-update` 从未注册为 flag，`testing` 的 `flag.Parse` 在任何测试体运行之前
就把它拒掉。实测两种写法都报 `flag provided but not defined: -update`。

守卫的恢复路径不通，比没有恢复路径更糟——它把读者送进一条死胡同。已改为注册
真 flag（保留 `os.Args` 语义不变）。改动后 `-update` 真的能重生成清单，
随后两个守卫（`TestStartupManifestFilesExist`、`TestStartupFilesAreAllEmbedded`）
各自立刻指出下一处缺失登记——它们在这次修复里各承重一次。

---

## 5. 「5 张表缺失」的真实归属

此前记为「门禁库缺 5 张生产表」。实测后这句话**不准确**，改成下面这版：

| 表 | 建表者 | 生产全新安装有吗 |
|---|---|---|
| `outbox_events` | 只有 `deploy/sql/migrations/V357` | 无迁移建；二进制无建表点 |
| `supplier_errors` / `_hot` / `supplier_error_stats` | 只有 `deploy/sql/migrations/V371` | 同上 |
| `task_type_tier_config` | 只有 `deploy/sql/migrations/V370` | **有**——`db.Open` 自愈建（`db/db.go`） |

即：这 5 张表在「基线 + 注册链」里都没有建表者，其中 `task_type_tier_config`
由二进制启动自愈补上，其余 4 张**在纯迁移路径上确实不存在**。

按第 1 节的决策，V357/V370/V371 是部署惰性、不进安装器。**这是一个已知的、
被明确接受的缺口，不是遗漏**——记在这里是为了让下一次有人撞上 `42P01
relation ... does not exist` 时不必重新调查一遍。

---

## 5b. 顺着 §9-7 挖出的两个表，暴露了同类问题的完整清单

为回答 §9-7（`sql/objects/` 定位，见第 5c 节）而做全量比对时，方法本身找出了
另外两张「只有未注册迁移创建、却被生产代码直接读写」的表。与 534 同类：

| 表 | 唯一建表者 | 是否注册 | 生产代码引用 | `db.Open` 自愈？ | 本轮处置 |
|---|---|---|---|---|---|
| `credential_model_capabilities` | 612 | 否 | `provider/client.go`、两个 streaming executor | **否** | **已注册** |
| `handoff_pending_confirmations` | 517 | 否 | `domains/hooks/handoff/confirmation_pg.go`、`bg/handoff_pending_trimmer.go` | **否** | **待决**，见下 |
| `session_title_states` | 550 | 否 | `db/db.go` 等 5 个 | **是**（`ensureSessionTitleStates` 用 `CREATE TABLE IF NOT EXISTS`） | 刻意不注册 |

`credential_model_capabilities` 已按 534 的同款四点登记接进链，实测
`applied=200 failed=0`、整包 `PASS=173 FAIL=0`，且全新安装库里
`to_regclass('public.credential_model_capabilities')` 由 `false` 变 `true`。

### 5b.1 517 为什么不注册：结构性冲突，不是顺序问题

试注册后门禁直接报：

```
517_handoff_pending_confirmations.sql :: ERROR: there is no unique constraint
matching given keys for referenced table "handoff_logs"
```

517 声明 `handoff_log_id INTEGER REFERENCES handoff_logs(id)`，在基线堆表上
合法（`id` 是主键）。而 534 把 `handoff_logs` 重建为 `PARTITION BY RANGE (created_at)`
的父表，**且没有给它任何唯一约束**——534 里唯一的 `PRIMARY KEY` 属于
`handoff_logs_hot`，是另一张表。分区表上的唯一约束必须包含分区键，所以 534 之后
`handoff_logs(id)` 不再唯一，517 的外键失去被引用目标。

**先跑 517 也不行（机制订正，2026-10-02 第二十七轮审计）**：原文写「534 会
RENAME 旧堆表、外键指向被改名的遗留表（静默错误）」——与 534 实际代码不符。
534 的 DO 块（`534_handoff_logs_hot_columnar.sql:289-307`）会**显式 DROP**
`handoff_pending_confirmations` 上所有指向 `handoff_logs` /
`handoff_logs_legacy_532` 的外键：517 先建的外键会在 534 执行时被删掉，链能跑通。
但这不等于「先跑 517 就安全」——「去掉外键」（下述解法 2）这个设计决定会被
534 的既有代码**隐式**替 Owner 做掉（存量库路径上 534 已这样处置过一次），
拍板权仍应留给 Owner，故本轮不注册 517 的结论不变。

两条路都需要改 schema 设计而非改接线：

1. 给分区父表加含分区键的唯一约束（如 `(id, created_at)`）并相应加宽 517 的外键；
2. 去掉 517 的外键。

**没有替用户选。** 门禁的失败信息里明确写着「不要为了让门禁变绿而放宽这里的判据」，
而 `sql/schema/startup_known_gaps.tsv` 虽然可以登记已知缺口，本轮**刻意没有登记**——
因为这不是「已知且接受的缺口」，是一个还没做的设计决定。登记它会让下一次有人
以为这件事已经有人判断过。

现状：`handoff_pending_confirmations` 在全新安装上不存在，handoff 确认流程会
在运行时 42P01。这是**接上 534 之前就存在**的状态（534 接线前的全新安装同样没有
这张表），所以本轮没有让任何东西变坏，但也没有把它修好。

---

## 5c. §9-7 结论：`sql/objects/` 的定位

§9-7 原文写「双向漂移 31/618」。**这个数字在两篇审计文档里都是裸数字，找不到任何
推导出处**，因此本轮重新实测，不继承它。

实测方法：解析 `sql/objects/tables/*.sql` 的建表语句取列集，与一个真实全新安装库
（基线 + 200 条注册链）的 `information_schema` 逐表比对。

| 量 | 值 |
|---|---:|
| `sql/objects/tables/` 声明的表 | 247（另有 30 个文件是月分区，不是独立建表） |
| 真库 `public` 表 | 439 |
| 只在 `sql/objects/`、真库没有 | 3 |
| 只在真库、`sql/objects/` 没有 | 195 |
| 两边都有但列集不同 | 18 |
| **列集完全一致** | **226 / 247（91.5%）** |

**漂移是单向的**：`sql/objects/` **落后于**现实，不是自相矛盾。
195 张表缺失、18 张少列（`request_logs_hot` 文件比真库多 3 列、少 19 列）。

那 3 张「文件有、真库无」的表就是 §5b 的 `credential_model_capabilities` /
`handoff_pending_confirmations` / `session_title_states`——现已查清归属，见上。

定位结论：

- **不是承重 SSOT。** 没有 `go:embed`，安装器不携带它；它不参与任何 CI workflow。
- **也还不算纯部署惰性。** `scripts/check-body-storage-schema.sh` 真的
  `grep` 解析 `sql/objects/tables/*.sql` 派生列清单，再拿去校验活库——
  对那一个脚本而言它确实是列定义来源。而**这个脚本今天是红的**（rc=1），
  因为 `sql/objects/tables/request_logs_hot.sql` 仍声明 573 已 DROP 的三个 body 列。
- `deploy/sql/sync-objects.sh`（`sql/objects/` → `deploy/sql/objects/`）的
  **目标目录不存在**，该脚本无人调用、不在任何 workflow 里——这条同步链是死的。

**因此 §9-7 的答案**：当前形态是「**陈旧且只被一个开发脚本单向信任**」——
既不是承重 SSOT，也不是干净的部署惰性。想把它变成部署惰性，只需处理
`check-body-storage-schema.sh` 一个脚本（改成以真库或注册链为准），
不必动 247 个文件。这是一个明确、低风险、但**本轮未做**的收口动作。

---

## 6. 又一个被推翻的判断：`session_dim` 缺默认值

此前记为「`session_dim.status` / `created_at` 在全新安装上 `NOT NULL` 无默认值，
需要新增迁移 + 三处登记」。

表定义确实如此（`805_session_dim_reconcile.sql` 建表：`status varchar NOT NULL`、
`created_at timestamptz NOT NULL`，均无 DEFAULT）。但**先查写方**的结果是：

```go
// internal/sessionv2mirror/session_dim.go:73 和 :106
INSERT INTO session_dim (..., status, ..., created_at, ...) VALUES (..., 'active', ..., NOW(), ...)
```

产品代码的**两条** INSERT 都显式给了 `status` 和 `created_at`。所以缺默认值
不是活缺陷，是健壮性问题。**不为此新增迁移**——为一个没有失败场景的假设动
schema，是拿不可逆的改动换一个不存在的问题。

（`350_session_analytics_fix.sql` 里那条 `DEFAULT 'active'` 确实存在，但 350
既未注册、又属于基线时代，与本问题无关。）

---

## 7. 已知缺口：族 3 与族 4（本轮决定不治）

决定：**本轮不治，登记为已知缺口**。理由是这两族不是代码 bug，而是
「门禁跑不出这个形态」，治它们等于扩门禁契约，是独立的一轮工作量。

| 族 | 数量 | 缺什么 | 怎么认出来的 |
|---|---:|---|---|
| 族 3 | 6 | 生产形态的**数据**：门禁库是空的，测试要的是有真实分布的 `admin` / `session_dim` 行 | 要造就得用生产数据或脱敏样本；用合成数据造出来仍是空跑变绿 |
| 族 4 | 5 | **绕过门禁库自起容器**。含 `TestHotTableOldestRowAge_RealDB`（`42P01 supplier_errors_hot`，与第 5 节同源） | 门禁形态不完整，未修 |

另有一处独立的覆盖盲区（不在 22→21 的红测试计数里，因为它是「没被测量」）：
`scripts/audit` 的 6 个 integration 测试无人运行，详见
`2026-10-02-round44-closure-final.md` §4。已在新接回的 CI job 里**显式排除**
并在日志中说明原因，不让它以「静默通过」的形式混进 CI。

---

## 8. CI 侧：job 已接回

`integration-gate` job 与状态行 `ci-gate-preconditions: SATISFIED` 在同一次提交
（`a172074ed`）里回来，由 `TestWorkflowDoesNotShipAnUnrunnableGateJob` 双向钉住。
变异验证：删掉 job 而保留 `SATISFIED` → 守卫变红；还原 → 绿。

包列表用 `scripts/audit/derive-gate-packages.sh` 推导，不用 grep——
grep 会命中 `//go:build !nintegration`（实测多出 `internal/collector`，
而门禁对它的判词正是「没有任何仅由 integration build tag 引入的测试文件」）。
`go list` 求值过约束，不会犯这个错。

**未验证**：这个 job 没有在 CI 上跑过一次。前置 1/2/3 是拿真实容器实测的
（镜像存在且是 amd64、tag 与 `PG_USER` 正确、导出 `PG_PASSWORD` 后门禁
`exit=0 PASS=112`），前置 4（registry 凭据）本机无法验证——实测结论是
**仓内根本不存在该约定**（所有 workflow 只用到 `GITHUB_TOKEN` 与 4 个
`*_DATABASE_URL`，没有一个拉过 `registry.kxpms.cn`），所以定义了 3 个新 secret
并在 job 里对缺失做响亮失败。

---

## 9. 本记录没有回答的

- **`handoff_pending_confirmations`（517）怎么处理**——见 §5b.1，需要 schema 设计决定，
  本轮没有替用户选，也没有登记进 `startup_known_gaps.tsv`。
- `sql/objects/` 怎么收口成真正的部署惰性——见 §5c，缺口很小（一个脚本）但本轮未做。
- 族 3 / 族 4 何时治、用什么数据——需要业务输入，不在本轮。
- `outbox_events` / `supplier_errors` 三件套在纯迁移路径下确实不存在，
  是否接受这个状态——决策已记录为接受，但**没有验证过生产上它们是否真的缺**
  （生产库可能由 DBA 脚本另行建过，本轮无证据）。
- `deploy/sql/migrations/V*.sql` 那 23 条里除建表外的其他 4 条「与安装器链
  不可组合」的失败，是否有生产仍在用——未查。
