# 接力 handoff — llm-gateway-go 全面审计 v3（fresh-install 链复审轮）

**日期**：2026-09-30
**工作目录**：`__DEV_HOME__/workspace/ai-native-tools/llm-gateway/llm-gateway-go`
**分支**：main
**状态**：**已提交并推送到 `origin/main`**（本轮 commit `47c263c9b`；期间为追上并发会话的推送，先后合并 `origin/main` 三次）。并发会话未提交的 WIP **原样保留在工作树，未纳入本次提交**。

---

## 一、⚠️ 并发警示（下一轮第一件事）

**同一 worktree 内有并发会话在提交。** 本轮开工时：

- `HEAD` 从 `80a9a87ab` 变为 `2d750fb41`（本地一度领先 **37 commit**、落后 `origin/main` **18 commit**）；
- 工作树里出现**我从未修改过**的文件：`VERSION`、`version.json`、`web/public/*`、`web/src/composables/*`、`sql/migrations/startup/761_*.sql`、`docs/db-changelog.md`；
- 并发会话已提交 `9fb81a7c5 fix(schema): fresh install 列存分区 GIN 索引条件化`，**把我原本要做的 Cluster B 修掉了**。

**⇒ 任何跨轮沿用的「修前错误数」都可能已失效。开工必做：**

```bash
git fetch && git log --oneline origin/main -10 && git status --short
```

**本轮处置**：为追上并发会话的推送，先后合并 `origin/main` **三次**（120 / 2 / 39 文件），期间撞上两处需要人工裁决的冲突：

- `VERSION` / `version.json` / `web/public/version.json`：与并发会话的版本 bump 冲突，按 **build_seq 取高者（2356 > 2353）** 裁决；
- `web/src/composables/useLiveStreamUrl.test.ts`：仅注释冲突（vitest 5 Mock 泛型形态说明），取并发会话一侧，**仅注释差异、无功能回退**（已核对 staged diff）。

合并期间为放行 merge，**只对阻塞 merge 的单个文件做过 `git stash push`/`pop`**，并在 `/tmp/r42_insurance/` 留了全量备份；**并发会话未提交的其余 WIP 一律原样保留、未纳入本次提交**。`r42-concurrent-wip-sessionSummaryJump` 那次 stash 经核对与 HEAD **逐字节相同**（实为空变更），未丢失任何内容。

---

## 二、结论与根因

### 2.1 更正上一轮的错误归因（本轮最有价值的产出之一）

§31.2 记「13 个 `default_table_access_method: \"columnar\"` 错误属**环境相关**（本机未按预置 columnar 模板初始化）」——**不成立**。

- `columnar` 是**逐库访问方法**，由 `CREATE EXTENSION citus_columnar` 在该库内注册；
- 本镜像**没有 `columnar.control`**，所以 `pg_available_extensions` 查不到它，但 `pg_am` 内有、`citus_columnar.so` 可加载，**功能完全可用**；
- canonical 与 deploy 的 `00-prereqs.sql` **都写了**这一行 ⇒ 全新库 apply 后能拿到 columnar。
- **真凶**：`installer/cmd/llm-gw-installer/embeddata/00-prereqs.sql` 是 **2026-06-24 陈旧快照**，只有 4 条 `CREATE EXTENSION`，**缺 `pg_stat_statements` / `pgstattuple` / `citus` / `citus_columnar` / `vector`**。
- **实测（合并后 HEAD 树）**：用 HEAD 的 prereqs → **14 个 `invalid value for parameter \"default_table_access_method\": \"columnar\"`**；补齐后 **0 错误**。

**「本机环境问题」的归因会把一个仓库 P0 永远放过去。凡「环境相关」结论，先问：该环境差异是否其实来自仓库内某个未提交的拷贝？**

### 2.2 「6 处前向引用」实为 3 个根因 + 3 个级联

`LANGUAGE sql` 函数体在 **CREATE 时即校验**（plpgsql 惰性，不算）。逐条追根：

| 现象 | 真实根因 |
|---|---|
| `credential_most_used_model(bigint,…)` 失败 → 其 `COMMENT` 连带失败 | 缺 `request_logs_hot` **且** `provider_models`（同一函数体两个依赖，只补一个立刻暴露下一个） |
| `get_model_state_summary` 失败 | 缺 `model_probe_state` **且** `credentials` |
| `model_probe_credential_concurrency` 失败 → `v_suspicious_probe_targets` 连带失败 | 缺 `model_probe_state`（级联） |
| `COMMENT ON system_health_status` 失败 | **纯顺序倒挂**：COMMENT(:4474) 早于 CREATE(:28793)，函数体本身没问题 |

**这批 + Cluster B 已由并发会话 `9fb81a7c5` 修掉**；合并后三份副本真库 apply **均 0 错误**。本轮**不重复修**，只补守卫。

### 2.3 本轮新增 P0：迁移 541 从未注册进 installer

prereqs 修好后，fresh-install 端到端门**第一次跑过 `01-schema.sql`**，随即失败：

```
apply 568_credential_priority_flag.sql: ERROR: relation "public.candidate_binding_scope_revision" does not exist
```

`candidate_binding_scope_revision` 由 **`541_candidate_binding_scope_revision.sql`** 创建。该文件**在 canonical 目录存在，但在 installer 三处全缺**（embeddata 无、`//go:embed` 无、`dbinit.Runner.StartupFiles` 无），而**消费方 568 在 StartupFiles 里** ⇒ 全新安装必然失败。已补齐，失败点推进到 622。

### 2.4 合并 origin 带进来的红门：`sqlreadguard`（非本轮引入，但必须修）

合并 `origin/main` 后全量回归出现 `internal/sqlreadguard` 红：`admin/session_catalog_usage.go:44 裸 request_logs 读`。取证：**该文件在 `80a9a87ab` 不存在、由本次合并从 origin 带入**，故非本轮改动引入，但落在我的分支上就不能带着红门推送。

- 读法本身合法：`request_logs_hot` **UNION ALL** `request_logs`，正是仓库约定的**双腿化**读法（单读母表会漏掉仍在 hot 的近 8h 行）。
- 门只认**行级 marker**。按既有先例（`admin/tenants.go:744`、`bg/integrity_fingerprint_probe.go:51`）在该行加 `-- sqlreadguard:allow <理由>`。
- **未用文件级白名单**——四十一轮已证文件级豁免会让整个文件不再受检。

---

## 三、改动文件与关键行为

| 文件 | 改动 | 关键行为 |
|---|---|---|
| `installer/cmd/llm-gw-installer/embeddata/00-prereqs.sql` | 补 5 条 `CREATE EXTENSION` + 头部注释同步为 2026-08-04 口径 | 全新安装能注册 `citus_columnar`，`SET default_table_access_method = columnar` 不再失败；**14 错误 → 0** |
| `installer/cmd/llm-gw-installer/embeddata/startup/541_candidate_binding_scope_revision.{sql,down.sql}` | 新增，与 canonical **字节一致** | 提供 568 依赖的表 |
| `installer/cmd/llm-gw-installer/main.go` | `//go:embed` 声明 + `embeddedSQLFiles` map 项 | 541 随 installer 二进制分发 |
| `installer/internal/dbinit/runner.go` | StartupFiles 置于 540 与 544 之间（早于 568） | `applySQL` 按切片顺序执行，位置决定生效 |
| `admin/session_catalog_usage.go` | 第 44 行加**行级** `-- sqlreadguard:allow` marker | 合并带入的红门转绿；非文件级豁免 |
| `sql/schema/baseline_ordering_test.go`（新增） | 3 测试 | 三份 `01-schema` 的 10 组顺序；三份 prereqs 含 9 条扩展；三份 prereqs 字节一致 |
| `installer/internal/dbinit/runner_541_order_test.go`（新增） | 2 测试 | `541 < 568`；每个 StartupFiles 条目在 embeddata 有文件 |
| `docs/全面审计v3/…/03-执行方案与进度.md` §44 + `README.md` + 本 handoff | 本轮留档 | 含 4 条自我更正 |

**未改动**：三份 `01-schema.sql`（并发会话已修，本轮无必要改动）；`VERSION`/`version.json`/`web/public/version.json`（仅用于解冲突，内容为并发会话的 2356，**未纳入本次提交**）；并发会话其余 WIP（`web/src/composables/*`、`startup/761_*`、`docs/db-changelog.md`）**原样保留**。

---

## 四、测试命令与结果

```bash
# 守卫（默认回归内，纯静态）
go test -count=1 ./sql/schema/                    # ok
(cd installer && go test -count=1 ./internal/dbinit/)   # ok

# 真库逐份 apply（三份 01-schema + 各自 00-prereqs，均断言库被填充）
#   canonical / installer / deploy  →  errors=0  tables=271

# 关键反证：HEAD 的陈旧 prereqs + HEAD 的 schema
#   → errors=14（全部 default_table_access_method: "columnar"）

# 端到端 fresh install（需 -tags=integration + 一次性库）
(cd installer && TEST_INSTALLER_FRESH_DB_URL=... go test -tags=integration -count=1 \
   -run TestFreshInstaller ./cmd/llm-gw-installer/)
#   → 失败点：本轮由 :917 → :26350 → 568 → 622

# 回归
go test ./... -count=1                # root
(cd installer && go test ./... -count=1)  # installer
```

**变异验证**（不绿即守卫不成立）：把 541 移到 568 之后 → 顺序门红并指出索引位置；从 installer prereqs 删 `citus_columnar` → prereqs 两门同时红；还原后全绿。

**「integration 全绿」与「integration 真的跑了」**：本轮端到端门**真的跑了**（真库、非 skip、失败点可复现），但**仍是红的**（见遗留风险 1）。

---

## 五、遗留风险

1. 🔴 **fresh install 仍失败**：端到端门停在 `622_provider_error_aggregator_state.sql` → `relation "public.candidate_failure_logs_hot" does not exist`。该表仅由 **`392_candidate_failure_logs_monthly_partition.sql`** 创建（**低于 installer 的 511 下限**），而 `01-schema.sql` **只引用不创建**。与 §31.2 的 382/430/451 **同族**。**根因：迁移目录是叠加在基线之上的增量，不是自举基线**；逐条补迁移是治标，正解是把基线并入目录（§32 已判为独立中长期工程 = 路线 B）。**本轮未继续补**——每补一条都改变全新安装的迁移集合，需各自独立验证，且不解决根因。
2. 🔴 **并发会话仍在同一 worktree 写入**。推送前务必 `git fetch`；其未提交 WIP 仍留在工作树，**不要 `git checkout -- .` 或 `git stash` 一把梭**。
3. ⚠️ **两份派生副本各自缺 2 个函数**（`bump_credentials_governor_revision`、`notify_credentials_governor_revision`），installer 副本尾部还有一段**无头的 plpgsql 残片**（`cleanup_old_credential_probe_model_log` 的 `CREATE FUNCTION` 头丢失）。**根因是 `dump-schema.sh` 产出物不完整**，手补是治标。
4. ⚠️ **StartupFiles 两处真实倒挂**待专项复核：`560_session_summaries_tenant_uniqueness.sql` 在 `655_…` 之后；`704_plan_quota_probe_backoff.sql` 在 `713_…` 之后。**注意 apply 顺序跟随依赖而非编号**（801 被刻意置末），因此不能用「全局升序」当门。
5. ⚠️ `main.go` 在 HEAD 就**不是 gofmt 干净的**（本次未做全文件重排，避免淹没真实改动）；本次新增行已单独验证 gofmt 干净。
6. 未变：`TestMigration715FreshChainApplyMigrations` 缺 startup 迁移那一步（不保真）；`ProviderExtensions` 持久化链死路径；D01 块级 identity；D10 费用置信度。

---

## 六、本轮我犯的四个错误（已修正/登记，供下一轮引为戒）

1. **搬块脚本锚点位移 bug**：被搬块位于锚点**下游**时，删除它**不会**使锚点前移，我仍减了块长 ⇒ 插入点偏早 140 行，**落进 plpgsql 函数体中间把函数劈开**（deploy 一度 26→91）。canonical 与 installer **碰巧落在无害位置而显得全绿**。**一次「三份里两份通过」不能证明工具正确——要有一份会失败的副本才能证伪。**
2. **把函数搬到文件末尾、落到 `COMMIT;` 之后**，破坏 schema 文件事务结构。已从 HEAD 重建；**现三份副本均无对象位于 `COMMIT;` 之后**。
3. **测量自身骗人**：批量 helper 里 `CREATE DATABASE` 静默失败、后续查询返空，一度把 installer 报成「22→**0 错误**」（真值 14）。**apply 类测量必须附「库确实被填充」断言**——否则 0 错误既可能是修好了，也可能是压根没跑。
4. **自造假不变式**：断言 StartupFiles 按编号升序并报红。查证发现该不变式**本就不成立**（apply 顺序跟随依赖；`session_turns_hot_bootstrap.sql` 无编号前缀）。**宁可不测，不可测错规则**——已删除该断言。

---

## 七、下一轮提示词

```
继续 llm-gateway-go 全面审计 v3。

工作目录：__DEV_HOME__/workspace/ai-native-tools/llm-gateway/llm-gateway-go
上一轮 handoff：docs/handoff/20260930-fresh-install-chain-review.md

【第一步必做】git fetch && git log --oneline origin/main -10 && git status --short
⚠️ 同一 worktree 内有并发会话在提交。上一轮开工时 HEAD 在两小时内从 80a9a87ab
   变到 2d750fb41，本地一度领先 37 commit、落后 origin 18。工作树里出现过
   我从未改过的文件（VERSION/version.json/web/*/startup/761_*）。并发会话已提交
   9fb81a7c5（GIN 条件化）把我原定要做的 Cluster B 修掉了。
   ⇒ 不要沿用任何跨轮「修前错误数」；先核树再下结论。

【上一轮已完成，不要重做】
- 更正 §31.2 错误归因：13 个 columnar 错误不是环境问题，是
  installer/…/embeddata/00-prereqs.sql 陈旧（缺 citus_columnar 等 5 条）→ 已修
- 补迁移 541 进 installer 三处（embeddata/go:embed/StartupFiles）→ 端到端门
  失败点由 568 推进到 622
- 补 5 项守卫（01-schema 顺序 ×3 副本、prereqs 扩展与字节一致、StartupFiles
  541<568 与 embeddata 存在性），全部经变异验证

【下一轮建议做（按优先级）】
1. 🔴 决定 fresh-install 路线：622 缺 candidate_failure_logs_hot（仅 392 创建，
   低于 511 下限；01-schema 只引用不创建）。这是 382/430/451/541/622 同族。
   要么做路线 B（把基线并入迁移目录，独立中长期工程），要么明确「全新安装不是
   受支持路径」并从 main.go:1150 的 InitSchema 调用链上撤出。别再逐条补迁移。
2. 修 dump-schema.sh：两份派生副本各缺 2 个函数，installer 副本有孤儿 plpgsql
   残片。产出物不完整才是根因。
3. 复核 StartupFiles 两处倒挂（560 在 655 后、704 在 713 后）——但注意 apply
   顺序跟随依赖而非编号，801 刻意置末，别用「全局升序」当门。
4. 刷新 scripts/audit/fresh-schema-from-migrations.sh 的适用范围（511..635 → 全量）。
5. 给 66 个 integration 门建立 CI 编排（build tag + 每测试独立一次性库 +
   环境变量注入）。既有约定：TEST_PG_URL 必须指向一次性库，共用库会产生假红。

【纪律（上一轮付出代价换来的）】
- 开工先 git fetch；本仓存在并发写入的同 worktree，git status 里的陌生文件
  不是你的改动，别覆盖、别一把 stash。
- apply 类测量必须断言「库确实被填充」（表数/函数数非空）。否则 0 错误既可能
  是修好了，也可能是 CREATE DATABASE 静默失败、压根没跑。
- 「环境相关」结论先反问：是否其实来自仓库内某个未提交的拷贝（本轮 13 个
  columnar 错误即如此）。
- 一次「三份里两份通过」不能证明工具正确——要有一份会失败的副本才能证伪。
- 宁可不测，不可测错规则：本轮自造「StartupFiles 按编号升序」这条本就不成立
  的断言，报红后已删除。
- 合并 origin/main 后，合并前的绿结论全部作废，必须重跑全量。
- 报告测试结果时区分「integration 全绿」与「integration 真的跑了」。
```
