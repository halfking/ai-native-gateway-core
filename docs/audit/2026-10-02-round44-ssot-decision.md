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
`*_DATABASE_URL`，没有一个拉过 `registry.internal.example.com`），所以定义了 3 个新 secret
并在 job 里对缺失做响亮失败。

---

## 9. 本记录没有回答的

- 族 3 / 族 4 何时治、用什么数据——需要业务输入，不在本轮。
- 5 张表中其余 4 张（`outbox_events` / `supplier_errors` 三件套）纯迁移路径下
  确实不存在，是否接受这个状态——决策已记录为接受，但**没有验证过生产上它们
  是否真的缺**（生产库可能由 DBA 脚本另行建过，本轮无证据）。
- `deploy/sql/migrations/V*.sql` 那 23 条里除建表外的其他 4 条「与安装器链
  不可组合」的失败，是否有生产仍在用——未查。
