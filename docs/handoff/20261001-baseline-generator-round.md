# 接力 handoff — llm-gateway-go 全面审计 v3（基线生成器轮 / round 43）

**日期**：2026-10-01
**工作目录**：`/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go`
**分支**：main
**本轮 commit**：`65d8821c4`（**已提交、未推送**）。开工时本地领先 0 / 落后 origin 12；
收工时领先 1 / 落后 15。并发会话未提交的 WIP **原样保留在工作树，未纳入本次提交**。

---

## 一、⚠️ 并发警示（沿用上一轮，仍然有效）

开工第一件事仍是 `git fetch && git log --oneline origin/main -10 && git status --short`。
本轮开工时工作树里有 7 个并发会话的改动：`VERSION`、`version.json`、
`web/public/{menu-config,version}.json`、`docs/db-changelog.md`、
`{installer,sql}/…/761_stats_inbox_sync_status_backfill.sql`。本轮全程未触碰。

本轮提交用**显式路径** `git add`（12 个文件），未用 `git add -A`、未 stash。

---

## 二、本轮最重要的结论：路线 B 的「拷贝 538 个文件」被实测证伪

用户最初选择「把 538 个基线迁移并入 embeddata 并按依赖重排 StartupFiles」。
**决定性实验推翻了这条路线的形式**：

| 测量 | 结果 |
|---|---|
| installer 路径（prereqs + 01-schema + 171 个 StartupFiles） | **20 处失败**，最终库 421 relations / 544 functions |
| canonical `00-prereqs` + 全部 **465** 条 up 迁移按序 apply（该目录共 777 个 .sql，其中 312 个是 `.down.sql`） | **236 处失败**，最终库 **233 relations / 455 functions** |
| 迁移中因 `schema_migrations` 缺失而失败的 | **19 处** |
| `000_base_tables.sql` 的 CREATE TABLE 数 | **1**（只有 `request_logs`） |

**关键事实：`schema_migrations` 这个 19 个迁移依赖的元表，没有任何迁移创建它**，
它只存在于 pg_dump 基线（`sql/schema/01-schema.sql`、`sql/objects/tables/schema_migrations.sql`）。
⇒ **基线与迁移集互为前提，两者都不自足。** 拷贝文件只会把 538 个同样有缺口的迁移
搬进 embeddata，失败数从 20 变 236。

基线本身也是陈旧的：`01-schema.sql` 缺 `candidate_failure_logs_hot`、
`session_turns_hot`、`session_dim`、`model_offers`、`proxy_subscriptions`，
以及 `archived_at` / `context_window_source` 列。

**真正的路线 B = 重建可信基线**，而它的前置条件是那个缺失的生成器 —— 见第三节。

---

## 三、dump-schema.sh 的完整根因（比上一轮的「产出物不完整」更深）

上一轮记的是「两份派生副本各缺 2 个函数，根因是 dump-schema.sh 产出物不完整」。
**这个归因不完整。** 实际是四层：

1. **生成器从未在本仓存在**：`git log --all -- '*db-init-lib.sh'` 为空。
   `sql/scripts/dump-schema.sh` 第 10 行 source 的 `../../../../scripts/_lib/…`
   会**逃出本仓**、落到兄弟 workspace。该路径在 `official-deploy` monorepo 里有效
   （`services/llm-gateway-go/deploy/sql/scripts/` 向上 4 级 = monorepo 根），
   脚本被复制出来时没改路径 ⇒ **自 `28d4d8612`(2026-07-05) 起必然失败**。
2. **库在 monorepo 侧也已不对**：`db_init::service_dir` 输出到
   `$_MONOREPO_ROOT/services/$service/deploy/sql`，而该目录现在
   **连 00-prereqs.sql / 01-schema.sql 都没有**。
3. **库的扩展集是 TimescaleDB 世代**：`extname IN ('plpgsql','pgcrypto',
   'btree_gist','pg_trgm','uuid-ossp','timescaledb','timescaledb_toolkit')`，
   完全不知道 Citus。**照原样重新生成会删掉 `citus_columnar`**，回退上一轮 P0。
4. **库的 `dump_schema` 后处理不完整**：只搬 `recent_success_rate` 的 `CREATE`、
   留下它的 `COMMENT`，生成物仍失败于
   `function public.refresh_session_analytics_views() does not exist`。

### 本轮修掉的

- 库迁入 `scripts/_lib/db-init-lib.sh`（45KB）。
- `dump-schema.sh` 改仓内相对路径 + **强制 `DB_INIT_OUT_DIR`**（防止就地覆盖已提交基线）。
- 扩展集换成 Citus/列存世代；`citus`/`citus_columnar` 必须 `WITH SCHEMA pg_catalog`
  （写 `public` 实测失败于 `must be installed in schema "pg_catalog"`）。
- `COMMENT ON` 前向引用**普适化**：后置全部自包含单行 `COMMENT ON`（本次 774 条）。

### 生成基线已通过真库验证

| | 已提交 canonical | 新生成基线 |
|---|---|---|
| apply exit | 0 | **0** |
| errors | 0 | **0** |
| relations | 328 | **660** |
| indexes / policies | — | **2491 / 211** |
| 对象横幅数 | 2612 | 6354 |

新生成计数与源库（660 relations / 577 functions）**一致** —— 真 dump→apply 往返。

---

## 四、对账结论（`docs/audit/2026-10-01-baseline-reconcile.md`）

ADDED 3759 / **MISSING 17** / REORDERED 2595 / 同位置 0。

**17 个 MISSING 不能整体当「源库已删」**，逐条取证后分两类：

- **A 类 11 个 —— 预期消失**：7/8 月分区及其 TABLE ATTACH / CONSTRAINT / INDEX ATTACH。
  源库上这些月份分区已被保留策略清掉（源库仍有 119 个 `session_turns_2026_*` 分区，
  只是没有 07/08）。全新安装**不该**重建过期分区。
- **B 类 5 个 —— 出处已查明（⚠️ 上一版结论是错的，已更正）**：
  上一版称这 6 个对象「既不在源库、也无法由本仓任何迁移重建 —— 来源不可复现」。
  **该结论错误**：当时只 grep 了 `sql/migrations/`，漏掉 `sql/objects/`——
  本仓另有一个 **1910 个文件**的对象登记目录。逐条查证后：

  | 对象 | 真实出处 | 在 installer 启动集？ |
  |---|---|---|
  | `idx_stage_events_stage_status` | 迁移 `434:63` | **否**（低于 511 下限） |
  | `idx_stage_events_tenant_ts` | 迁移 `450:13` | **否**（低于 511 下限） |
  | `idx_stage_events_request_id` | 仅 `sql/objects/indexes/` | 无迁移 |
  | `handoff_logs_pkey` | 仅 `sql/objects/constraints/` | 无迁移 |
  | `route_incident_events_incident_id_fkey` | 仅 `sql/objects/other/` | 无迁移 |

  第 6 个 `instance_heartbeats_pkey` 在源库**仍然存在**（同名约束 2 个），
  是比对键构造差异，不是真丢失，已移出本类。

- **C 类（新发现，风险方向与上一版相反）—— local 开发库是「迁移到一半」的中间态**：
  迁移 434 一次性建 4 个 `idx_stage_events_*` 索引，源库上**只有 3 个**；
  源库另有非 434 所建的 `idx_stage_events_created_at`；
  源库 `handoff_logs` **完全没有主键**，而已提交基线有。
  ⇒ 源库既非「更新版」也非「干净版」。**用它重新生成会静默删掉上面 5 个合法对象**，
  其中 3 个在 `sql/migrations/` 里根本无创建者。

⇒ **本轮 dump 源（local 开发库）不可作为基线源，理由是 C 类而非「等价性存疑」。
所以本轮只出对账报告，不替换任何已提交副本。**

**正确顺序**：① 把源库补齐到与已提交基线对等（apply 迁移 434/450 + 那 3 份
`sql/objects/` 定义；`handoff_logs` 加主键前须先查 NULL/重复）→ ② 重新生成，
MISSING 应降到只剩 A 类 11 个分区 → ③ 才谈替换，且必须重跑
`TestBaselineDefinitionPrecedesEagerReference`（上一轮 6 处前向引用的人工排序
修复钉在**旧基线**上）。已加守卫
`TestEveryBaselineObjectHasRepoProvenance` 钉住这 5 个对象的出处。

---

## 五、本轮其余改动

- **applier 逐文件事务豁免**：`applySQL` 对带 `-- dbinit:no-transaction` 标记的文件
  不加 `--single-transaction`。718/719 打标记（canonical 与 embeddata 逐字节一致）。
  真库 A/B 已证：无标记失败于事务约束，有标记该错误消失并继续到另一处既有缺表错误。
- **改正了一条我自己造的不变式**：最初断言「三份基线副本对象集合必须一致」，
  报红后查证发现**规则本身错**——10 个差异全由 566/608/609 提供，派生副本只是
  566 之前的那一代 dump。`cp` canonical 过去会把 566 的工作重复进基线。
  已改为单向真实不变式 `TestDerivedBaselineLagIsSuppliedByMigrations`。
  该守卫还抓出我自己写错的供给声明：609 只提供 `tenant_model_policies_audit_pkey`，
  另一个 pkey 是 **608**。
- 修了自己守卫里的 `cleanJoin` bug：它会吃掉开头的 `..`，导致**原本的错误信息
  就是一条看似合理但错误的路径**（`scripts/_lib/…` 而非 `../../scripts/_lib/…`）。

---

## 五之二、本轮额外收口的两件事

### 既存红门 538：守卫只比名字不比对定义（已修，commit `4514d9247`）

`db/db.go` 的 `ensureNodeProbeTriggerKindSchema` 在 2026-09-23 加过一个性能守卫：
「`node_probe_runs` 大表上 ADD CONSTRAINT CHECK 要全表校验 + 持 ACCESS EXCLUSIVE，
必超 30s 被杀（245 seq 2201 boot 57014 实锤），所以约束已存在就整段跳过」。
写法是 `IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = '...')`。

**它只问名字。** 存量部署带的正是**旧值域**的同名约束 ⇒ 判断为真 ⇒
`DROP`+`ADD` 被跳过 ⇒ **恰好在需要升级的那批部署上不升级，且静默无日志**。
fresh install 反而正常（无约束 ⇒ 走 `ADD` 分支）。

红门 `TestEnsureNodeProbeTriggerKindSchemaUpgradesLegacyChecks` 正是为此而红，
且它是**正确的一方**，生产代码是错的。改法保留原性能意图：改为比较
`pg_get_constraintdef` 的**定义**是否已含全部预期字面量 —— 已正确则零 DDL
（纯 catalog 读取），不一致才 `DROP`+`ADD`。

真库双分支验证：已正确约束 → 走 skip-no-ddl，约束 OID `9286734` 前后不变；
旧值域 → 走 upgrade，测试由红转绿；变异（让升级分支不可达）→ 测试重新转红。

### harness 自身的两处缺陷（本轮新写代码，都是跑出来才发现）

- **DSN 不带密码** ⇒ 9 个真连库的 `./db` 用例报 `failed SASL auth`，
  看起来像产品缺陷。→ 加 `PG_PASSWORD`，并在跑测试前**先用生成的 DSN 连一次**，
  失败则一次性清楚报错，而不是散成 N 个无关用例失败。
- **只 apply prereqs、不 apply 基线** ⇒ 9 个 `db.ensure*` 用例报
  `relation "public.schema_migrations" does not exist`。
  **这正是本轮早先记录的引导洞**：该元表无任何迁移创建、只存在于 pg_dump 基线，
  而 `ensure*` 家族要往里盖迁移号。→ harness 改为 apply
  `00-prereqs.sql` + `01-schema.sql`，起始库 328 relations。

> 这条同时说明：本轮 §二 记录的「schema_migrations 无迁移创建」不只是
> fresh-install 的问题，**它同样让任何依赖 `ensure*` 的真库门禁无法在
> 一次性空库上运行**。

**已解（本轮末）**：harness 起始库口径已补齐——默认 `GATE_APPLY_STARTUP=1`，
在 `prereqs + 01-schema.sql` 之后按 `StartupFiles` 顺序应用启动迁移。

    仅基线          = 328 relations
    基线 + 启动迁移 = 421 relations   ← 与真实 installer 路径一致

`session_aggregate_outbox`（630）、`usage_facts`（537）因此到位，
`db_744` / `db_749` 等 8 个用例由红转绿。**`./db` 整包现为
68 PASS / 5 SKIP / 0 FAIL（exit=0）**，且 5 个 skip 按约定标为
「green with skips」而非全绿。

两处实现要点：
- 启动迁移里 18 条仍不应用（与本轮 §二 的 20 处缺口同族，差 2 条是因为
  本 harness **识别 `dbinit:no-transaction` 标记**，718/719 因此不再假失败）。
  这些按「已知 fresh-install 缺口」列出，**不计为门禁失败**。
- 守卫 `TestGateAppliesStartupMigrations` 同时钉住「默认值为 1」——
  默认关掉会静默退回陈旧基线，正���复现本次要消除的那批假失败。
  （该守卫第一版只查变量存在性，被 `:-0` 变异穿过；已加固。）

## 六、测试与验证

```bash
go test -count=1 ./sql/schema/              # ok（含 3 组新守卫）
(cd installer && go test -count=1 ./internal/dbinit/)   # ok
go build ./... && (cd installer && go build ./...)      # ok
gofmt -l sql/schema/ installer/internal/dbinit/         # clean
go test ./... -count=1                    # 唯一失败是本轮预期内的红门（已修复为绿）
```

**变异验证**（不绿即守卫不成立）：

| 变异 | 结果 |
|---|---|
| 给 622 打 no-transaction 标记（无不可事务化语句） | 红，指出豁免必须窄 |
| `dump-schema.sh` 指向不存在的库 | 红，并报**正确**解析后的路径 |
| 从生成器 IN 列表删 `citus_columnar` | 红（三份 prereqs 全部点名） |
| `citus_columnar` 改回 `WITH SCHEMA public` | 红（复现本轮真实踩到的 bug） |

**「integration 全绿」vs「integration 真的跑了」**：本轮真库 apply 全部**真跑了**
（一次性库 + 填充断言），不是 skip。

**自审更正（2026-10-01，见 `docs/audit/2026-10-01-round43-self-audit.md`）：**
上一版这里写「本轮唯一残留的红门 `TestBaselineDumpScriptIsRunnable` 已转绿」。
这句话有两处不实：

1. 该测试**从来不检查「能不能跑」**，只检查被 source 的库文件路径可解析。
   它叫 `Runnable` 是名不副实。已改名为
   `TestBaselineGeneratorLibraryResolves`，并在其注释里明确写出
   **未被任何测试覆盖**的那半边（生成产物 apply 到空库 exit=0 是 2026-10-01
   手工验证的，不是测试保证的）。
2. 「唯一残留的红门」这个说法当时是对的，但**同一天我加进去的 CI job 才是
   本轮最大的未验证项**，而它没有被算进「残留」——因为它一次都没跑过，
   所以没人知道它必败。见自审报告 P0。

---

## 七、本轮我犯的错误（已修正，供引为戒）

1. **在守卫里造了手写 SQL 词法分析器**：为跨行 `COMMENT ON` 写引号状态机，
   在含引号的注释上失步，**吞掉了紧随其后的 `CREATE FUNCTION ... AS $_$` 头**，
   产出裸 plpgsql 函数体（psql: `invalid command \_hot'`）。
   **代码生成器里的解析 bug 比未处理的 case 更糟。** 改为保守的单行规则。
   判据：能不动就不动；无法用可靠规则判定的，留给人工。
2. **「0 错误」不等于成功**：`citus_columnar` 写错 schema 时，prereqs 失败级联，
   若只看错误数会误判。**填充断言（relations/functions 非空）是本轮抓到该 bug 的唯一手段。**
3. **用「排错后感觉对」代替对照实验**：我一度怀疑 `ESCAPE '\'` 导致 psql 元命令误判，
   做了对照——已提交 canonical 同样含该内容且 exit=0 ⇒ 与我无关。
   **修 bug 前先确认该内容在正常路径上是否也出现。**
4. **`cleanJoin` 的 `..` 归一 bug**：让守卫报出错误路径，从而**看起来一直在正确工作**。

---

## 八、下一轮提示词

```
继续 llm-gateway-go 全面审计 v3。

工作目录：/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go
上一轮 handoff：docs/handoff/20261001-baseline-generator-round.md
上一轮 commit：65d8821c4（已提交未推送，领先 1 / 落后 origin 15）

【第一步必做】git fetch && git log --oneline origin/main -10 && git status --short
⚠️ 同一 worktree 内有并发会话在提交。工作树里常驻 7 个并发会话的未提交改动
   （VERSION / version.json / web/public/* / docs/db-changelog.md / 761_*.sql）。
   本轮全程未触碰，提交一律用显式路径 git add，不用 git add -A、不用 stash。
   ⇒ 不要沿用任何跨轮「修前错误数」；先核树再下结论。

【上一轮已完成，不要重做】
- 证伪「拷贝 538 个基线迁移」路线：canonical 全量 apply 236 失败，比 installer
  路径（20 失败）更差；schema_migrations 无任何迁移创建，基线与迁移集互为前提
- 迁入并修复基线生成器 scripts/_lib/db-init-lib.sh：扩展集改 Citus 世代、
  citus/citus_columnar 改 pg_catalog、COMMENT ON 前向引用普适化。
  生成基线 apply exit=0 / 660 relations / 2491 indexes，与源库一致
- applier 逐文件事务豁免（-- dbinit:no-transaction 标记），真库 A/B 验证
- 4 组守卫，全部经变异验证；删除了自己造的假不变式（三份副本对象集合必须一致）
- 出对账报告 docs/audit/2026-10-01-baseline-reconcile.md

【下一轮建议做（按优先级）】
1. 🔴 按「正确顺序」补齐 dump 源：apply 迁移 434/450 到源库 + apply 那 3 份
   `sql/objects/` 定义（`handoff_logs` 加主键前先查 NULL/重复），然后重新生成
   并重跑 `scripts/audit/baseline-reconcile.py`，确认 MISSING 降到只剩
   A 类 11 个分区。**这是替换基线的前置条件。**
2. ⚠️ 顺带查清 `sql/objects/`（1910 文件）到底是什么定位：它有出处价值，但
   没有任何脚本把它整体 apply（`scripts/apply-db-revision-sequence.sh` 只零星
   引用）。若它是 SSOT，则「迁移目录 + 基线 + objects 目录」三份表示的
   职责边界需要定清楚；若它只是历史归档，就不该被当成对象出处。
3. ⚠️ 合并 origin/main：落后 15 commit，重叠文件是 VERSION / version.json /
   web/public/* 四个（与上一轮同类冲突）。合并前结论全部作废，必须重跑全量。
4. ⚠️ fresh-install 剩余缺口（基于合并后重测，勿沿用本轮数字）：
   installer 路径 20 处失败里，路线 B 已判定不适用后，
   仍未处理的为 C 类（708 对 columnar 表 UPDATE，citus_columnar 无 CTID）
   与 D 类（冻结视图链未收敛 99 vs 114 列，733/734/738/740，
   含作者自写的自中止守卫）。
5. ⚠️ scripts/audit/fresh-schema-from-migrations.sh 仍是 511..635 硬编码范围、
   容器名 kx-citus/用户 kxuser 硬编码（本机是 llm-gateway-pg/llm_gateway），
   且无填充断言。
6. 未处理：给 66 个 integration 门建 CI 编排（build tag + 每测试独立一次性库
   + 环境变量注入；TEST_PG_URL 必须指向一次性库，共用库会产生假红）。

【纪律（两轮付出代价换来的）】
- 开工先 git fetch；并发 WIP 不是你的改动，别覆盖、别一把 stash
- apply 类测量必须断言「库确实被填充」（表数/函数数非空）。本轮唯一抓到
  citus_columnar schema 错误的手段就是这个断言
- 「环境相关」结论先反问：是否来自仓库内某个未提交的拷贝
- 一次「三份里两份通过」不能证明工具正确——要有一份会失败的副本才能证伪
- 宁可不测，不可测错规则：对象集合相等那条自造不变式已删
- 代码生成器里不要手写词法分析；保守规则优于脆弱的通用实现
- 修 bug 前先做对照：该内容在正常路径上是否也存在
- 合并 origin/main 后，合并前的绿结论全部作废，必须重跑全量
- 报告时区分「integration 全绿」与「integration 真的跑了」
```

---

## 附：「补齐 dump 源」步骤实测不成立（round 43 收尾更正）

上文 §四 给出的「正确顺序」是 **apply 434/450 + 3 份 `sql/objects/` 定义」。
本轮末在**克隆库**上真跑一遍（`pg_dump | psql` 克隆，不动用户本机库），
**5 步里 2 步失败**，故该步骤需更正：

```
✓ 450_request_stage_events_tenant
✓ sql/objects/indexes/idx_stage_events_request_id.sql
✓ sql/objects/other/route_incident_events_…_fkey.sql
✗ 434_request_stage_events_table      ERROR: cannot drop columns from view
✗ sql/objects/constraints/handoff_logs_handoff_logs_pkey.sql
      ERROR: unique constraint on partitioned table must include all
             partitioning columns
```

### 更正一：`handoff_logs_pkey` 的「出处」本身不可用于当前 schema

`sql/objects/constraints/handoff_logs_handoff_logs_pkey.sql` 写的是
`ADD CONSTRAINT handoff_logs_pkey PRIMARY KEY (id)`。而实测：

```
handoff_logs | relkind=p | RANGE (created_at)
```

即 `handoff_logs` **已是按 `created_at` RANGE 分区的表**。PostgreSQL 要求
分区表上的唯一约束**必须包含全部分区键列**，因此 `PRIMARY KEY (id)`
在当前 schema 上**根本无法建立**。

⇒ 这修正了本轮早先的说法：该文件是 `handoff_logs_pkey` 的**出处**没错，
但它是 **`handoff_logs` 还是普通表那个年代的陈旧对象定义**。

**再更正（本轮自审，2026-10-01）：** 上面那句「已提交基线里那个
`handoff_logs_pkey` 同样无法在分区形态下存在」写得有误导性，实测是错的。
已提交基线 `sql/schema/01-schema.sql:8388` 把 `handoff_logs` 建为**普通表**
（`CREATE TABLE public.handoff_logs (...)` 后面直接跟 `WITH (autovacuum_*)`，
**没有 `PARTITION BY`**），所以同一文件 `:21044` 的
`ADD CONSTRAINT handoff_logs_pkey PRIMARY KEY (id)` 在基线形态下是合法的，
基线 apply 到空库 exit=0 这一点也印证了它。

分区形态只存在于**本机开发库**（`relkind=p` RANGE `created_at`）——而那个库
正是上文认定「迁移到一半的中间态」。所以正确的说法是：

- `sql/objects/constraints/handoff_logs_handoff_logs_pkey.sql` 对**本机开发库**
  不可施加；
- 对**已提交基线的形态**可施加，两者并不矛盾。

**「sql/objects 提供权威出处」这个结论要打折扣：它对部分对象给的是相对
某个形态而言的、可能已过时的定义 —— 用之前必须先确认目标 schema 形态。**

### 更正二：434 对「已灌基线的库」不幂等

434 假定 `request_stage_events` 处于遗留形态并尝试改列；在基线已建好
`stage_performance_recent` / `upstream_5xx_distribution` 视图的库上直接
失败于 `cannot drop columns from view`。

⇒ 「把源库补到与基线对等」不是「顺序 apply 几个文件」，而需要先判定
每个迁移所假定的源形态。**这条不能照抄上文 §四 的步骤。**

### 净结论

- `sql/objects/` 的证据价值进一步下降：它不只是**漂移**，还包含
  **在当前 schema 上无法施加**的陈旧对象定义。
  这为下一轮「`sql/objects/` 继续做 SSOT 还是降为归档」的决策补了
  一条偏向**归档**的实证。
- dump 源的补齐需要按对象逐个判定，不能批量 apply。
- 本轮未修改用户本机 `llm_gateway` 库（全部实验在克隆库上做，
  克隆库已删除）。
