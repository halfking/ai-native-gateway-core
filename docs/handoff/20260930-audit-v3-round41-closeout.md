# 接力 handoff — llm-gateway-go 全面审计 v3（四十一轮收口 + 合并推送）

**日期**：2026-09-30
**分支 / HEAD**：main @ `9b0801f83`（merge commit）+ 其上 1 个自动 merge
**工作目录**：`/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go`
**状态**：已提交、已合并 origin/main、**待推送**（或已推送，视本文件生成时点）

---

## 一、结论与根因

### 本轮最重要的发现：提交前才暴露的 P0 —— 迁移 759 撞号

- **现象**：本地在制迁移 `759_session_turn_details_duplicate_drain.sql` 与 `origin/main` 已提交的 `759_report_snapshots_grain_dims.sql` **同号**。
- **它不是新问题**：`docs/12小时内修订审计-20260929-1635.md:81` 早已登记为 **R22-A「759 撞号仍活」**，规则是「**先提交方占号、后提交方让号**」。上游已提交 ⇒ 本地让号。
- **为何前 30+ 轮没发现**：本地 `TestNumericUpMigrationVersionsAreUnique` **当时是绿的** —— 该守卫只 `os.ReadDir(".")` 读本地目录，本地只有一个 759，**碰撞只在合并后才显现**。此前我反复验证「759 已注册进 installer」时，**从未核对 759 这个编号在远端是否已被占用**。
- **修法**：改号 **801**（760/800 已占），五点同步 + 位置修正（见下）。

### 第二个 P1 —— 是我自己引入的

改号后 `801_` 仍停在 759 的列表位置（758 与 760 之间），而它 `CREATE OR REPLACE` 的 `promote_session_turn_details_hot_to_partition` 函数体依赖 **733** 建的 `session_turn_details` 及其 hot 表。`dbinit.Runner.applySQL` 按切片顺序逐文件执行 ⇒ 会在依赖对象存在前就替换函数。**没有任何守卫校验 StartupFiles 的数值顺序**，所以「改名 + 跑绿测试」会完全漏掉。已把 801 移到列表末尾并补位置守卫。

### 合并后回归红 2 例（合并改 12 个文件 ⇒ 合并前绿结论全部作废）

1. **`chain.go`** —— 我在冲突裁决时「取上游 R24-C 修复」，但那个修复**只处理 `ModifiedChunk`、漏了 `InjectAfter`**；suppress 分支之后那处 `if len(result.InjectAfter) > 0` 无条件回填把刚清空的注入又填回去。更深的冲突是上下游对同一形状裁决相反。**用生产事实裁决**：`{SuppressChunk:true, ModifiedChunk:prior+terminal}` 的唯一生产者是 `OutputComplianceInterceptor(:127)`，而它实现了 `StreamPendingFlusher(:159)`，故该形状永远来自 holder。已让两条测试同时成立（实现加持有者判定 + `InjectAfter` 补同样的门；上游那条测试改用真实 holder 形状）。
2. **`sqlreadguard`** —— `admin/tenants.go:735` 是上游有意为之的双腿化内联 `UNION ALL`（R36-A1 漏热尾根修），非疏漏。**我第一版修法（加文件级白名单）是错的**：变异验证揭穿 —— 注入一处无 hot 腿的纯母表读，守卫仍绿。改为**行级 marker**。

---

## 二、改动文件与关键行为

### 生产代码（3 个文件）

| 文件 | 改动 |
|---|---|
| `installer/internal/dbinit/runner.go` | `801_` 条目**移到列表末尾**（800 之后）；注释写明占号原因（759 已被 upstream 占用，R22-A）与位置约束原因（依赖 733） |
| `domains/hooks/response/chain.go` | suppress 分支加 `StreamPendingFlusher` 持有者判定（注释早已写明该契约，实现未兑现）；`InjectAfter` 回填补 `!finalResult.SuppressChunk` 门 |
| `admin/tenants.go` | 合法双腿读那一行加 `-- sqlreadguard:allow <理由>`（**行级**，非文件级） |

### 测试 / 迁移

- `sql/migrations/startup/801_session_turn_details_duplicate_drain.sql` + `.down.sql`（原 759，改号）
- `installer/…/embeddata/startup/801_….sql`（与 canonical **字节一致**）
- `installer/internal/dbinit/runner_801_order_test.go`（**新增**：钉 `733<801`、`734<801`、旧名 759 不得复活）
- `domains/hooks/response/chain_test.go`（上游那条测试改用真实 holder 形状）
- `internal/sqlreadguard/guard_test.go`（撤销我加错的文件登记）
- `sql/migrations/startup/migration_759_behavior_integration_test.go`（ReadFile 路径改号）

### 此前会话（同一提交 `ec5edcfd7` 内）

`domains/stats` 6 处 fixture 修正（生产零改动）、1M handoff 契约 6 项新测试、F04 / IR / D12 / D07 / D06 / P1-4 等。

### 文档

`docs/全面审计v3/2026-09-29/03-执行方案与进度.md`（38 节）+ `docs/全面审计v3/README.md`（四十一轮条目）。

---

## 三、测试命令与结果

```bash
# 编译
go build ./...                              # root + installer 均干净
(cd installer && go build ./...)

# 全量回归
go test ./... -count=1                      # root  EXIT=0
(cd installer && go test ./... -count=1)    # installer 13 包 EXIT=0

# 关键门
go test -count=1 -run TestNumericUpMigrationVersionsAreUnique ./sql/migrations/startup/   # ok
(cd installer && go test -count=1 -run TestStartupFilesPlace801AfterItsDependencies ./internal/dbinit/)  # ok
go test -count=1 ./internal/sqlreadguard/  # ok
go test -count=1 ./domains/hooks/response/ # ok

# integration（需 Docker）
go test -tags=integration -count=1 ./domains/stats/   # ok 21.8s
```

**变异验证**（每项都按预期变红，否则门的守卫力不成立）：

| 变异 | 目标 | 结果 |
|---|---|---|
| 801 移到列表最前 | `runner.go` | 🔴 位置守卫红 |
| 重新注册旧名 759 | `runner.go` | 🔴 位置守卫红 |
| 同文件加无 marker 裸母表读 | `tenants.go` | 🔴 sqlreadguard 红 |
| 撤掉 marker | `tenants.go` | 🔴 sqlreadguard 红 |
| 删绝对阈值分支 | `trigger_hook.go:334` | 🔴 1M handoff 红 |
| `>=`→`>`（排他） | 同上 | 🔴 边界子用例红 |
| Default 300k→1M | `handoff_specs.go` | 🔴 spec 门红 |
| Max 2M→500k | 同上 | 🔴 spec 门红 |

**两次「变异仍绿」的教训**（都已写入文档）：
1. 我写的 801 位置守卫，第一次变异把 801 插到 734 **之后** —— 那没制造出倒挂（733 在 :212），是**变异设计错误**，不是测试缺口。
2. sqlreadguard 的文件级白名单登记通过了基线测试，但**注入新违规仍绿** —— 白名单类修改**必须变异验证守卫力未被削弱**。

---

## 四、遗留风险

### 🔴 未修（需产品/架构裁定）

1. **fresh-install 在 `01-schema.sql` 中止**（影响生产）。三份副本（canonical / installer embeddata / deploy baseline）各有前向引用，缺陷**分布在不同副本**；canonical 与 embeddata 相差 612 行 diff / 190 行行数，**不能盲目 `cp`**。installer 自身门 `TestFreshInstallerSessionTurnsHotBootstrap` 实测红（`:917`）。
   - **路线 A（有界，本轮结论）**：按 §31.2 工单重排 6 处前向引用 + 同步两份派生拷贝。
   - **路线 B（已证伪为「配置变更」）**：三组全新库实测 —— 511..635 建出 31 表、511..全量 68 表、**001..全量 248 表 / 464 函数**，而健康安装是 **648 表 / 577 函数**。机理是 382/430/451 三条**跨 511 下限**的前置声明 ⇒ 迁移目录是**增量不是基线**，无法自举。
2. **66 个 `//go:build integration` 文件对默认回归不可见**，部分还 env 门控（`TEST_PG_URL` / `TEST_INSTALLER_FRESH_DB_URL`）。`scripts/audit/fresh-schema-from-migrations.sh` 只覆盖 511..635，**落后约 130 个迁移**。
3. `TestMigration715FreshChainApplyMigrations` 缺 startup 迁移那一步（不保真）。补上会 FAIL→SKIP，**属信号减弱**，需明示取舍。
4. `ProviderExtensions` 持久化链是死路径（归 D17）/ D01 块级 identity / D10 费用置信度。

### ⚠️ 交接时须知

- **工作树里 31 个未跟踪文件属原作者 WIP**（`security/sanitize`、`streaming`、`outputcompliance` 等），本轮按你的指示一并提交了，但**我只验证过它们能编译、根测试绿，未逐个审计其正确性**。
- 本地 main 与 origin 是**两条独立时间轴**；本轮推送前远端又前进过 1 个 commit，已再次合并。**继续工作前先 `git fetch`。**
- `domains/hooks/response/chain.go` 的 holder 判定若将来放宽 `StreamPendingFlusher` 的实现范围，需同步复核 `CarriesNoReplacementWithoutHolder`。

---

## 五、下一轮提示词

```
继续 llm-gateway-go 全面审计 v3 接力。

工作目录：/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go
上一轮 handoff：docs/handoff/20260930-audit-v3-round41-closeout.md

【第一步必做】git fetch && git log --oneline origin/main -10 && git status
—— 本地与 origin 是两条独立时间轴，远端在上一轮推送期间又前进过；
上一轮的「全绿」结论只在当时的树上成立，不要沿用。

【已完成，不要重做】
- F02 / F04 / IR round-trip / 1M handoff 契约 / D01–D17 取证
- 迁移 759→801 改号 + 五点同步 + 位置守卫（已随 ec5edcfd7 / 9b0801f83 推送）
- domains/stats 6 处 fixture 修正（生产零改动）
- 合并 origin/main 52 commit + 7 处冲突裁决 + 合并后 2 例回归红修复

【下一轮建议做（按优先级）】
1. 路线 A：重排 01-schema 的 6 处前向引用。工单在
   docs/全面审计v3/2026-09-29/03-执行方案与进度.md §31.2，
   需同步 installer embeddata + deploy baseline 两份派生拷贝。
   ⚠️ 改完必须用真库 apply 实测（三份都要跑），不能只看 psql 退出码。
2. 给 66 个 integration 门建立 CI 编排：build tag + 每测试独立库 + 环境变量注入。
   注意既有约定：TEST_PG_URL 必须指向**一次性库**，多个测试共用一个库会互撞
   （本轮实测：4 个红里 3 个是共用库造成的假红，逐库隔离后只剩 1 个真红）。
3. 刷新 scripts/audit/fresh-schema-from-migrations.sh 的适用范围（511..635 → 全量）。

【纪律（本轮付出代价换来的，务必遵守】
- 改动迁移编号前，**先核 origin 是否已占用该号**（759 撞号沉了一个月，
  且本地唯一性守卫因只读本地目录而恒绿、无法证伪）。
- StartupFiles 是**顺序执行**的：注册位置必须晚于依赖对象。
- 合并 origin/main 后，**合并前的绿结论全部作废**，必须重跑全量。
- 白名单/豁免类修改**必须做变异验证**：证明「只有该放行的行没被检查」，
  而不是「已知违规仍被抓住」。
- 变异仍绿时，先分清是「测试缺口」「恒等死代码」还是「我的变异没制造出
  想制造的状态」——本轮两种都遇到过。
- 报告测试结果时区分「integration 全绿」与「integration 真的跑了」。
```
