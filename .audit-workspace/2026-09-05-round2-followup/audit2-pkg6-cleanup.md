# 审计报告：pkg6（installer 注册 + P3 死代码清理）— 只读复审

审计时间：2026-09-05 深夜 ~ 2026-09-06 00:45（本地）。
审计对象：main 分支工作区未提交改动（`git diff HEAD` + untracked）。
审计方式：只读（未修改任何被审文件；本报告文件为唯一产出）。

**重要环境说明（影响证据解读）**：审计期间工作区被并行会话持续改动——
HEAD 在审计中途前进两次（`b3413208f` docs、`c1fd9f4c9` feat(p2.3)），
canonical 的 `662_provider_error_details_agg_key_dedup.sql` 在审计进行中从工作区消失，
且新 HEAD 提交引入了**另一个同号 662**（`662_feature_distribution_stats.sql`）。
下文 P0-1 记录了完整证据链。pkg6 本身的改动集（installer/ir/domains 等 23 文件，
+80/−426）在审计全程保持稳定。

---

## 一、installer 注册一致性核对表

### 1.1 四处置清单 × 迁移矩阵（"✓"=存在且指向正确变量）

| 迁移 | embeddata 文件 | go:embed 变量 (main.go) | copySQLBackup map | setupSQLDir map | dbinit runner.StartupFiles | stats_migrations_test expected | embeddata ≡ canonical sha256 |
|---|---|---|---|---|---|---|---|
| 658_auto_route_structured_features | ✓ | ✓ (:256) | ✓ | ✓ | ✓（本轮补，657 与 659 之间） | ✓（本轮补） | **MATCH** `0f263e57…` |
| 659_legacy_promote_atomic_cte | ✓ (untracked) | ✓ (:259) | ✓ | ✓ | ✓（658 后） | ✓ | **MATCH** `ac7794c4…` |
| 660_credential_model_weekly_peak_unique | ✓ (untracked) | ✓ (:262) | ✓ | ✓ | ✓（659 后） | ✓ | **MATCH** `ba4b3bb8…` |
| 662_provider_error_details_agg_key_dedup | ✓ (untracked) | ✓ (:265) | ✓ | ✓ | ✓（660 后、bootstrap 前） | ✓ | **canonical 文件缺失 → P0-1**（installer 副本 `d0dbe5c4…` 与 pkg4 报告记录的定稿 SHA 一致） |
| 563_session_summary_trigger_on_hot | ✓ (M) | ✓ (:118) | ✓ | ✓ | ✓（562 后） | ✓ | **MATCH** `a22740a1…`（22003 修复已同步） |

全量交叉核对（脚本化，非抽查）：embeddata/startup 共 98 文件；73 个 go:embed
startup 变量；copySQLBackup 与 setupSQLDir 的 startup 键集**完全相等（各 73）且
逐条 var 名与 go:embed 变量一致**；runner.StartupFiles（73 条）与 setupSQLDir 键集
**完全相等**；`TestStartupFilesAreAllEmbedded`、`TestStatsStartupMigrationsAreWritten
ToInstallerDirectories` 均通过。

### 1.2 顺序与依赖核验

- runner 顺序：…620 → 639 → … → 656 → 657 → 658 → 659 → 660 → **662** → session_turns_hot_bootstrap。
  662 只依赖 `provider_error_details`（620 建表、639 装旧指纹索引），二者均在 662 之前 → 满足。
- runner 中**无 V371、无 661**：fresh-install 路径 572（runner :63）在 563（runner :54）
  **之后**应用，固定触发器体不会被 563 旧体回铲，故 661 仅属升级轨道（script）——设计自洽。
- script files[] 顺序：… 654 → 660 → 661 → **662（新增 :120）** → V371（:121）。
  662 不引用任何 V371 对象（supplier_errors 族），先于 V371 执行无依赖问题。位置正确。
- 662 幂等性（脚本 per-file marker 语义下的重放安全）：折叠 CTE 在新索引下 no-op、
  DROP INDEX IF EXISTS + CREATE UNIQUE INDEX IF NOT EXISTS、BEGIN/COMMIT 自带事务，
  与 runner 的 `--single-transaction` 兼容（嵌套 BEGIN 仅 WARNING）。✓
- 01-schema.sql 对 provider_error_details **未定义指纹索引**（仅 idx_ped_* 辅助索引），
  fresh install 走 639 → 662 重装路径不会撞名。✓

### 1.3 相关测试运行结果（本机实跑）

- `installer` 模块 `go build ./...`：OK；`go vet ./...`（含 `-tags integration`）：OK。
- `go test ./... -count=1`：**仅 `TestStatsStartupMigrationsMatchCanonicalSources` FAIL**
  —— `read canonical migration 662_provider_error_details_agg_key_dedup.sql: no such
  file or directory`（P0-1）。其余全部 ok（含 dbinit、TestStartupFilesAreAllEmbedded）。
- `fresh_installer_integration_test.go`：`//go:build integration` + `TEST_INSTALLER_FRESH_DB_URL`
  门控，默认 `go test` 不编译不运行（本审计环境无一次性空库，仅验证可编译）。其内部按
  `runner.StartupFiles` 泛化遍历，无硬编码清单，补录后天然覆盖 658/659/660/662。

### 1.4 预存在（非本 diff 引入）的注册侧观察

- canonical `sql/migrations/startup/` 无 `600_outbound_body_to_bodies_hot.sql`
  （在 `up/` 子目录，测试已特判）；installer embeddata 的 600 副本无顶层 canonical 孪生。
- installer embeddata 内 24 个 `.down.sql` + `632_audit_attachments_filesystem_cleanup.sql`
  （up+down）**无任何 go:embed 引用**（惰性资产；632 亦不在 runner）。
- canonical `649_routing_analytics_probe_filter.down.sql` 与 installer 副本 sha256
  **不一致**（`e627eda2…` vs `dfcf0d19…`，pre-existing；up 文件一致）。
- 除 662 外，embeddata 全部 98 文件与 canonical 逐字节比对：仅上述 649 down 一处 DIFFER。

---

## 二、scripts/apply-db-revision-sequence.sh

- `bash -n`：OK。
- 662 条目位置：:120（661 之后、V371 之前）✓；仅 +1 行，其余语义未动。
- marker 幂等语义：per-file marker（`${sequence_name}:$(basename)`）与序列级旧 marker
  清理逻辑原样保留；对已应用库追加 662 只会增量执行 662。✓
- 守卫脚本 `scripts/apply-db-revision-sequence_test.sh` **实跑 PASS**
  （`apply-db-revision-sequence contract passed`），intentional_function_chains
  （update_session_summary / archive_credential_model_index 两条链）完好；662 不定义
  函数，无需入链。✓
- **运行期阻塞**：因 canonical 662 文件缺失，脚本首循环 `[[ -f $file ]]` 即 exit 4，
  **整个修复序列（含 655/560/572/563/656/V371 等既有条目）在升级库上一并无法应用** —— 见 P0-1。

---

## 三、死代码删除逐项判定（grep 全仓 + HEAD 对照 + 实跑）

| # | 项 | 判定 | 证据要点 |
|---|---|---|---|
| B1a | serialize_anthropic `mapEffortToBudget` | ✅ 正确删除 | 全仓仅剩 ：108 历史注释（工单明示保留）；`reasonnormEffortToBudget` 仍被使用，无孤儿 |
| B1b | parse_responses 第二个 `function_call_output` 分支 | ✅ 正确删除 | HEAD `:323` 早退分支对同一 `item["type"]` 无条件先返回（HEAD 版已核实存在）；被删分支还含 `if _, ok := item["output"]` no-op；产物结构逐字段等价 |
| B1c | parse_anthropic `msg["source"]` 特例 | ✅ 正确删除 | 纯 `_ = source`，行为零变化 |
| B1d | `requestIDFromIR`（~40 调用点） | ✅ 等价改写 | HEAD 定义核实为 `func requestIDFromIR(_ *InternalRequest) string { return "unknown" }` 恒常量；4 个 serialize_* 全部调用点改 `"unknown"` 字面量；审计论断成立，零悬空引用 |
| B1e | serializeAnthropicMessages 文档注释重复块 | ✅ 正确删除 | 二选一保留，余文一致 |
| B2 | domains/routing/candidate_failure_logger.go(+test) 整删 | ✅ 正确删除 | 生产 wiring 在 cmd/gateway/main.go:1933 用 executors 版 `NewCandidateFailureWriter`；helper（unwrapErr/recoveryContext/marshalContext）在 executors 副本中存活且仍被 supplier_error_logger 使用；`go test ./domains/routing/` ok |
| B3 | dispatch `sortPriorityClusters` + `CredentialRef.PriorityCluster` | ✅ 正确删除 | 全仓（go/sql/json/ts/vue/md）零引用；对恒零值做 `SliceStable` 排序为恒等操作 → 删除行为等价；无 json tag/DB 列依赖 |
| B4 | executors/router.go `planLegacy` | ✅ 正确删除 | 全仓零调用；留删除记录注释 |
| B5 | bg/auto_route_affinity_worker `prevAvgReward` | ✅ 正确裁剪 | SELECT 裁列 + Scan 单目标同步；`prevEMA` 仍被下方 UpdateEMA 消费；零残留引用 |
| B6 | handler_autocombo `emptySet` | ✅ 正确删除 | map/写入/`_ =` 全删；`continue` 语义保留并留注释；`mu` 仍在 ：225/:228 使用（编译通过佐证） |
| B7 | autoroute/classifier `buildReason` | ✅ 正确删除，无递归孤儿 | 零引用；真实实现 `buildReasonEx` 在 classifier.go:609 仍被 rankPrimary 调用 |
| B8 | samples.seed.jsonl 删 5 行 planning | ✅ 一致 | 45→40 行、8 标签 ×5、零 planning 残留；README 同步（40 条/8 类+移除原因）；report_test.go 不依赖样本数；main.go deriveLabels 派生无行数断言 |

**误删检查（反射/字符串拼接/sqlc/模板）**：全部被删标识符的字符串形态在全仓
（md/json/sql/ts/vue/yml/sh，除 vendor 与 .audit-workspace）仅出现于历史审计/CHANGELOG/
docs-archive 文档，无代码级反射或模板依赖；仓库无 sqlc。零误删。

---

## 四、全量验证（审计者本机实跑）

```
主模块
  go build ./...                                   → OK
  go vet ./internal/ir/... ./domains/... ./bg/... ./autoroute/... ./cmd/autoclass-bench/...
                                                   → OK
  go test -count=1 internal/ir domains/dispatch domains/routing
          domains/streaming/... autoroute/... cmd/autoclass-bench/...
                                                   → 全部 ok（streaming 72s）
  go test -count=1 ./bg/...                        → 仅 1 FAIL：
    TestProviderErrorAggregatorAggKeyMigrationDropsMessageFromFingerprint
    provider_error_aggregator_contract_test.go:116:
    open ../sql/migrations/startup/662_provider_error_details_agg_key_dedup.sql:
    no such file or directory                      → 与 P0-1 同根因（该测试文件属 pkg4 在途改动）
installer 模块
  go build ./...                                   → OK
  go vet ./...（默认 + -tags integration）          → OK
  go test ./... -count=1                           → 仅 TestStatsStartupMigrationsMatchCanonicalSources
                                                     FAIL（P0-1），其余全 ok
gofmt：触及文件中仅 domains/dispatch/queued_request.go 不洁 —— HEAD 版本同样不洁
（pre-existing，见 P3-4）；其余触及文件全部干净。
```

---

## 五、发现清单

### P0-1｜662 编号冲突 + canonical 文件失踪：升级轨道脚本必然中断、两处测试红
- **现象**（审计期间实时捕获的时间线）：
  1. 审计开始时 `sql/migrations/startup/662_provider_error_details_agg_key_dedup.sql`
     存在，与 installer embeddata 副本 sha256 一致（逐字节 MATCH 实测）；
  2. 审计中途该 canonical 文件从工作区消失（`find` 全仓仅剩 installer 副本；
     installer 副本 sha `d0dbe5c44869c696…` 与 pkg4 报告记录的定稿 SHA 一致，副本完好）；
  3. 与此同时 HEAD 前进到 `c1fd9f4c9 feat(p2.3)`，该提交带入**另一个 662**：
     `sql/migrations/startup/662_feature_distribution_stats.sql`。
  ⇒ 形成同号双迁移动态冲突；当前工作区里 pkg6 注册的 662 名字在 canonical 侧已无对应物。
- **直接后果**（均已实测复现）：
  1. installer `TestStatsStartupMigrationsMatchCanonicalSources` FAIL（读不到 canonical 662）；
  2. bg `TestProviderErrorAggregatorAggKeyMigrationDropsMessageFromFingerprint` FAIL（同因）；
  3. `scripts/apply-db-revision-sequence.sh` 运行期在 :217 `[[ -f $file ]]` 即
     **exit 4**，整个升级修复序列（655/560/572/563/656/V371 全部条目）被阻塞。
- **建议修法**：两包（pkg4 与 p2.3 提交方）协商编号——将 agg_key_dedup 迁移重编号为
  **663**（canonical 新文件 + installer embeddata 改名 + main.go embed 变量与两 map 键 +
  stats_migrations_test expected + runner.go StartupFiles + script files[] 共 6 处同步，
  db-changelog 的 SHA 同步重算），canonical 文件恢复入库；feature_distribution_stats 维持
  662。重编号后重跑 installer 与 bg 两测试 + `bash scripts/apply-db-revision-sequence_test.sh`。
- **责任归属**：冲突由并行的 c1fd9f4c9 提交与 pkg4 未提交迁移相撞造成，非 pkg6 改动集
  自身缺陷（pkg6 对 662 的五处注册彼此一致且副本字节正确）；但**合入前必须解决**。

### P1-1｜已提交的 662_feature_distribution_stats.sql 未注册进任何部署轨道（c1fd9f4c9 引入，越界提醒）
- `bg/feature_stats_worker.go`（同提交新增）直接 INSERT `feature_distribution_stats`；
  该表仅由该迁移创建。但：installer runner 无此文件（fresh install 缺表→42P01）、
  apply-db-revision-sequence.sh 无此文件（升级库缺表→42P01）、db/db.go 无 ensure。
- 建议：作为独立跟进项把该迁移接入 fresh-install（runner 或 01-schema）与升级 script
  轨道；与本 P0 的重编号一并在下一轮接线。

### P2-1｜659（promote 函数原子化）缺升级轨道注册（pre-existing，越同族缺口）
- 659 修复 7 个旧式 promote 函数的"DELETE 已提交、INSERT 失败被吞 → 静默丢批"数据丢失
  缺陷；升级库存量函数体仍为坏体，且运行时确实调用（bg/partition_manager.go:875-880、
  admin/data_lifecycle_hot_partition.go:33-38）。script files[] 无 659（本 diff 只加了 662）。
- 659 自带幂等声明（7× CREATE OR REPLACE、确定性函数体），可仿 651-654 先例安全追加。
- 建议：给 apply-db-revision-sequence.sh 追加 659（独立小改，可并入 P0 重编号一起做）。

### P3-1｜守卫脚本未纳入新增条目
- `scripts/apply-db-revision-sequence_test.sh` 的 `for required in 655 560 … 660 661 V371`
  未加 662（重编号后应为 663）——守卫无法防住本次这类"条目被移除/文件失踪"。
  建议随 P0 修法一并补。

### P3-2｜残留注释引用已删除文件
- `domains/analysis/bus/publisher.go:81` 注释仍提 `candidate_failure_logger.go`
  （现歧义指向 executors 同名文件，风险低）；建议顺手改为 `executors/candidate_failure_logger.go`。

### P3-3｜embeddata 惰性资产与 649 down 漂移（pre-existing）
- 24 个 `.down.sql` 与 `632_…cleanup.sql`（up+down）无 go:embed（632 亦不在 runner）；
  canonical `649_routing_analytics_probe_filter.down.sql` 与 installer 副本 sha 不一致。
  不影响运行，建议后续清理/对齐。

### P3-4｜domains/dispatch/queued_request.go gofmt 不洁（pre-existing）
- HEAD 版本同样不洁（实测 `git show HEAD:… | gofmt -l` 列出）；本 diff 触及相邻行，
  顺手 `gofmt -w` 该文件可消除（非本 diff 引入，不计违规）。

### 信息项｜工作区易变性
- 审计期间 HEAD 两度前进（b3413208f、c1fd9f4c9）、存在 stash `临时保存本地修改`、
  审计早期读到过一过性的 137 行版 apply-db-revision-sequence.sh（当前为 229 行 =
  HEAD + 662 条目，守卫实测通过）。pkg6 改动集（23 文件 +80/−426）全程稳定。

---

## 六、结论

- **pkg6 自身的 21 项改动（installer 五处注册 × 658/659/660/563、script 662 条目、
  12 项死代码删除）全部判定正确**：注册四处置齐备、字节级一致、行为等价、零悬空引用、
  触及包 build/vet/test 全绿。
- **唯一放行阻断项是 P0-1**（662 编号冲突/canonical 文件失踪），属并行会话相撞而非
  pkg6 缺陷，但当前工作区不可合入：两测试红 + 升级脚本运行期 exit 4。按建议重编号 663
  并恢复 canonical 后即可解除。
