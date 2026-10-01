# Round 44 收口报告

> **状态更新（本文写就之后的同一轮内）**
> 本文下文的 §5（§9-6 未决）、§6（三项待拍板）、§7（未验证）已被
> **`2026-10-02-round44-ssot-decision.md`** 取代或结论化。要读当前状态请看那篇。
> 变化摘要：
> - **红测试 22 → 21**：接上迁移 534 修掉了 `sql/migrations/startup` 最后一个红
>   （整包 173/0），顺带修掉一个真实生产阻断——全新安装的库此前网关拒绝启动。
> - **§9-5 已完成**：CI job 与状态行同批接回（`a172074ed`），job 本身本机未验证。
> - **§9-6/§9-7 已定论**：SSOT = 已注册启动链；另两套标记为部署惰性（**未删除任何文件**）。
> - 本文 §6.1 里「130 条未注册」这个数字**是错的**，任何口径下都复现不出来；实测 290 条。
> - 本文 §6.1 里 341/534 属「链缺失」的说法**已修正**：链本就不含任何 <388 的迁移，
>   341/534 属基线时代；534 是唯一例外，因为快照携带不了它。
> - 本文 §4 关于 `scripts/audit` 的结论不变，且已在新接回的 CI job 里显式排除。

范围：`docs/audit/2026-10-01-round44-self-audit.md` §9 的 7 项。
本报告只写被门禁或脚本实测过的事；没有证据的一律进「未验证」段。

---

## 1. 结论

- **§9-1 / §9-2 / §9-3 / §9-4：完成**，已合并进 `origin/main`。
- **§9-5：部分完成**。前置 1/2/3 已实测满足；前置 4（CI registry 凭据）只能 CI 侧验证，
  job 接回 workflow 留待同批提交。
- **§9-6：完成量化，结论是无害**。§9 原写「双份统一」，实测是**三份**；唯一对象级分歧
  已被已注册迁移覆盖，见 §5。
- **§9-7：量化到可决策粒度**，等拍板。
- **红测试 44 → 22**，两侧均已证明「真的跑了」，无新增红。
- 本轮新发现一处覆盖盲区（`scripts/audit` 6 个 integration 测试无人运行），见 §4。

---

## 2. 红测试：44 → 22

```
$ python3 docs/audit/verify/compare-sweeps.py \
      /tmp/r44-closure-sweep-after /tmp/r44-sweep-94

[基线] 尝试 27 包，产出结果 27 包，未产出 0 包
[当前] 尝试 28 包，产出结果 27 包，未产出 1 包
[当前]   未产出结果: scripts-audit  (verdict=INCOMPLETE, PASS=0)
```

| 包 | 基线红 | 当前红 | Δ |
|---|---:|---:|---:|
| admin | 11 | 9 | -2 |
| bg | 14 | 4 | -10 |
| sql-migrations-startup | 6 | 1 | -5 |
| domains-dispatch | 2 | 0 | -2（转绿） |
| taskprofile | 2 | 0 | -2（转绿） |
| domains-hooks-handoff | 1 | 0 | -1（转绿） |
| fault / internal-outbox / licensing / tests-integration / vibecoding | 各 1–2 | 持平 | 0 |
| internal-sessionv2mirror | 1 | 1 | 0 |
| **合计** | **44** | **22** | **-22** |

22 个仍红测试**全部「基线亦红」**，本轮无一新增。

### 2.1 口径：22 还是 27

两个数都对，回答的是不同问题，不要混用：

- **22 = 红测试数**（子测试折叠进父测试）。`--- FAIL:` 行含子测试，一个红了两个子用例的父
  测试是 3 行、1 个测试。
- **27 = FAIL 原始行数**（`totals.txt` 的 `FAIL=` 列）。

差额恒为 5：基线 49-44=5，当前 27-22=5。`compare-sweeps.py` 两行都打出来，
就是为了让这个差额不必手工对账。

### 2.2 「44 → 22」成立的前提

一个 sweep 里若有包压根没跑，`reds()` 只统计出现过 FAIL 的包，那个包贡献 0，
于是「没跑」会被读成「变绿」。所以基线和当前两侧都单独做了完整性证明：

- 基线 27 包，每包 `PASS` 计数均 > 0（最小 3，最大 2425）；
- 当前 28 个被尝试的包中 27 个产出结果，第 28 个是 `scripts-audit`（见 §4）。

**基线每一包都跑过**，所以 44 是实测值，不是下界猜测。

---

## 3. 修掉的一个工具缺陷（它一直在少报改善）

第一版对比脚本把 3 个已转绿的包标成「当前集合中无此包（测量集差异，非改善）」，
并让「本轮从红转绿的包」一节**整节为空**。

根因：`reds()` 只在见到 FAIL 时才建键，所以「跑过且全绿」和「没跑」在该字典里
**无法区分**——而这两个是相反的结论。脚本用「当前集合里没有这个包」来表达前者。

修法：拿 `totals.txt` 里 sweep 自己的 verdict 字段去消歧（`GREEN`/`RED` 才算
「产出了结果」；`VACUOUS` 是「跑了但 0 测试」，不算测量；`INCOMPLETE` 不算）。
`totals.txt` 缺失的旧 sweep 退化为按 log 的 PASS 计数判定。

固化版 `docs/audit/verify/compare-sweeps.py` 另修一处：完整性证明原先只枚举 `.log`
文件，于是 `scripts-audit` 因为没产出 log 而从证明里**静默消失**，证明会写成
「当前 27 包全部产出」而实际尝试了 28 个。改为 log 与 totals 取并集。

**变异验证**（合成用例：totals 声称转绿、但包没重跑，日志仍是基线那份）：

```
admin                  11        0   -11  本轮未产出结果，不可称转绿
sql-migrations-startup  6        0    -6  本轮未产出结果，不可称转绿
```

未重跑的包一律报「不可称转绿」，不会被计入改善。这是本工具最危险的分支，已验证承重。

---

## 4. 新发现：`scripts/audit` 的 6 个 integration 测试无人运行

`scripts/audit` 有 2 个 Go 文件、6 个测试函数，全部 `//go:build integration`。
它**不在 CI 包列表**里，而门禁也跑不了它：

```
$ bash scripts/audit/run-integration-gate.sh ./scripts/audit
package .../scripts/audit: build constraints exclude all Go files
ERROR: go list 无法加载包
```

根因是门禁的零覆盖守卫（`run-integration-gate.sh:185`）按设计用**不带 tag** 的
`go list` 与带 tag 的做集合差。对一个「所有文件都是 integration 标签」的包，
不带 tag 的 `go list` 加载失败，于是守卫在算差集之前就 `die` 了。

### 4.1 为什么不修它

修好 `go list` 会让这 6 个测试跑起来，但它们各自 `t.Skip` 于
`TEST_AUDIT_ISOLATED_DB_URL` 未设置——门禁用的是另一套 DSN 机制，从不设这个变量。
结果是 6 个 SKIP、0 个 PASS，门禁的真空检查照样判红，只是报错信息从「包加载不了」
退化成「没有 PASS」。**当前的 fail-closed 行为是更可诊断的那一个**，保留。

### 4.2 所以它是什么

一条真实的覆盖盲区：6 个测试需要 `:15433` 上的 kx-citus 容器，只有一条手工路径
（`start-isolated-pg.sh`）能触达，CI 与门禁都够不着。

**分类：既不是红，也不是绿，是「没被测量」。** 任何「28 包全测」的说法都要带上
这个例外。本轮没有为它造门禁形态——那要么改门禁 DSN 契约、要么给它单列一条 CI 通道，
两者都属于 §9-3 夹具形态机制的后续，不在本轮范围。

---

## 5. §9-6：基线不是双份，是三份；且分歧无害

§9-6 原文按两份写。实测三份，且两两不同：

| 文件 | 行数 | md5 |
|---|---:|---|
| `sql/schema/01-schema.sql` | 30393 | `f93e334b…` |
| `installer/cmd/llm-gw-installer/embeddata/01-schema.sql` | 30205 | `d1dc7199…` |
| `deploy/sql/schemas/baseline/01-schema.sql` | 30204 | `6eda0643…` |

忽略空白后的真实差异行数：

| 对比 | 原始差异 | 忽略空白 |
|---|---:|---:|
| deploy ↔ embeddata | 183 | **7** |
| sql/schema ↔ embeddata | — | **312** |
| sql/schema ↔ deploy | — | **319** |

deploy 与 embeddata 几乎同源（183 行差异里 176 行是行尾空白）。真正的对象级分歧
只有一处，在 sql/schema 这一侧：

- sql/schema **有**、embeddata 与 deploy **无**：`bump_credentials_governor_revision`、
  `notify_credentials_governor_revision`、`credentials_revision_idx`
- 两侧都有但定义不同：`columnar_heal`

### 5.1 这个分歧不产生坏安装

`db/db.go:5648` 确实建了触发器依赖 `public.bump_credentials_governor_revision()`，
所以不能简单说「embeddata 缺了也没事」。但这 3 个对象由
`sql/migrations/startup/566_credentials_governor_revision.sql` 创建，而 **566 已注册在
启动链第 42 位**（`installer/internal/dbinit/runner.go:120`，清单
`sql/schema/installed_startup_migrations.tsv` 第 60 行）。任何走安装器链的全新安装
都会拿到它们。

**结论：漂移是真的，当前无害。** §9-6 因此不是生产阻断项，是 SSOT 与生成器的卫生问题。

---

## 6. §9-7 与其余待决项

以下三项都需要拍板，**都不是本轮能自行决定的**。每项都附已量化的选项。

### 6.1 三套迁移来源谁是 SSOT（卡住 `TestMigration715FreshChainApplyMigrations` 的最后一层）

| 来源 | 规模 | 实测 |
|---|---|---|
| 已注册启动链（`dbinit.StartupFiles`） | 198 条 | 唯一被安装器真跑的 |
| 未注册的早期链 `V*.sql`（编号 0..387） | **130 条整条未注册** | 含 `534`（`handoff_logs` 分区）、`350`（唯一声明 `session_dim.status DEFAULT 'active'`） |
| `deploy/sql/migrations/V*.sql` | 23 条 | **与安装器链不可组合**：17 成功 / 4 失败 |

这条决策同时决定：`session_dim` 默认值怎么补、门禁库缺的 5 张表
（`outbox_events` / `supplier_error_stats` / `supplier_errors` / `supplier_errors_hot` /
`task_type_tier_config`）从哪来、`V355__backfill_session_task_id.sql:147` 的
`RAISE NOTICE 'Coverage: %%'` 真 bug 归谁修、以及 `db.Open` 在全新安装上跑不通
（`handoff_logs` 非 RANGE 分区）这一生产阻断。

### 6.2 族 3（6 个）与族 4（5 个）需要什么形态

- **族 3（6 个）**：要生产形态的**数据**。门禁库是空的，测试要的是有真实分布的
  `admin` / `session_dim` 行。
- **族 4（5 个，含 `TestHotTableOldestRowAge_RealDB`）**：要**绕过门禁库自起容器**。
  已归类为门禁形态不完整，未修。

### 6.3 §9-5 剩余（只能在 CI 侧验证）

- 前置 4：CI registry 凭据。
- `ci-gate-preconditions` 状态行维持 `NOT-SATISFIED`。**这是当前事实，不是遗漏**：
  守卫 `TestWorkflowDoesNotShipAnUnrunnableGateJob` 是双向的，把状态行翻成
  `SATISFIED` 而 job 未接回会导致门禁失败。job 接回必须与状态行同批提交。

---

## 7. 未验证 / 已知不可本地验证

- 前置 4（CI registry 凭据）与 job 接回后的实际 CI 运行结果——本机无法验证。
- 族 3 需要的生产数据形态——本机没有生产数据，未做任何模拟。
- `integration_fixture_shapes.tsv` 登记表**仍刻意留空**：现有两种形态都不是正确轴
  （见 `2026-10-02-round44-closure-fixtures.md` 的否证结论），在 SSOT 决策之前填它
  只会把错误的轴固化。
- 基线与当前 sweep 都在同一台 macOS/arm64 上跑；`bg` 的 listener flaky 实测约 33%，
  是既有问题，不影响本报告的红测试计数（同一份 log 内计数），但会让单次重跑结果波动。

---

## 8. 本轮提交

| 提交 | 内容 |
|---|---|
| `a61a74262` | R44 收口轮（809/808/19 缺口 / RSL 假红） |
| `fc9e055ef` | §9-3 `GATE_DB_SHAPE` 三形态 |
| `be79ef3d3` | per-test schema 隔离（`internal/testschema`） |
| `de6e6c65f` | `internal/testdb` + 迁移测试改用 per-test 独立库（startup 6→1 FAIL） |
| `7579825e0` | 715 executor 语义 + 隔离缺口修复 |
| `c5de82fc0` | §9-4 逐条定性 44 红 → 5 族 |
| `ce2f67992` | 族 2 夹具漂移 6 个 |
| `e8f384ca2` | 族 1 在 bg 收口（bg 10→6 FAIL）+ `DispatchPostgresDatabase` |
| `777ad2c4c` | 门禁形态与生产形态差异量化 |
| `e1ecbd04a` | 注册清单产物 + 防漂移守卫；715 按生产顺序重放 |
| `539a5823a` | §9-5 amd64 镜像实测完成 |
| 本次 | 收口报告 + `compare-sweeps.py` 固化 |

详细的每一轮证据见：
`2026-10-02-round44-red-triage.md`、`…-family2-fixture-fixes.md`、
`…-gate-shape-gap.md`、`…-closure-migration-fixtures.md`、
`2026-10-01-integration-gate-ci-preconditions.md`。
