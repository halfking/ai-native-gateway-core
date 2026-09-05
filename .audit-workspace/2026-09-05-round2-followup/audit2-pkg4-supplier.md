# 审计报告 — pkg4 供应商错误链路（E-#3 / E-#6 / 662 / V371）只读复审

- 审计人：audit2 子代理（供应商错误链路）。日期：2026-09-05 深夜（跨 09-06）。
- 审计对象：工作区未提交改动（基线 HEAD 在审计期间被并行会话推进至 `b35cec661`；pkg4 的 Go/SQL 改动在审计结束时仍未提交，仅存在于 working tree）。
- 纪律：未修改任何业务文件；共享库 llm-gateway-pg 仅执行只读 SELECT；写路径验证全部在两个一次性容器（55433/55434，已销毁）完成。
- 总结论：**pkg4 修复本体（662 迁移逻辑、E-#3 冲突目标/样本列语义、E-#6 分桶数学、V371 增列守卫、测试设计）全部验证通过**；但审计发现 3 个 P1 级问题，全部源于「662 文件被并行会话移动 + 662 被并行会话提前应用 + Go 修复仍未提交」的组合状态，以及 1 个 pkg4 未覆盖的 P2 语义缺陷（occurrences/first_seen 在有界 staging 下 replace 失真，HEAD 已存在、本轮注释将其错误背书）。

---

## 一、逐项结论

### 1. 662 迁移正确性（最高优先） — 逻辑 PASS；当前工作区状态被并行会话破坏（F-1/F-2/F-3）

**列集逐列对照（psql 实查 + 文本比对）**：`pg_get_indexdef`（本地共享库实查 + 容器重建）：

```
CREATE UNIQUE INDEX idx_provider_error_details_tenant_cred_fingerprint ... USING btree
 (COALESCE(tenant_id,''::varchar), provider_id, COALESCE(credential_id,''::text),
  COALESCE(model_name,''::varchar), COALESCE(endpoint,''::varchar), error_type,
  COALESCE(error_code,''::varchar), COALESCE(aggregation_bucket,'1970-01-01...'::timestamptz))
```

与 `bg/provider_error_aggregator.go:265-270` 的 ON CONFLICT 8 列目标**逐列一致**（表达式、顺序、epoch 哨兵完全相同）。42P10 同版本约束的根基成立。

**容器实测（一次性容器，db1/db2 双库）**：
- db1 复现 pkg4 场景（3 条同桶 message 碎片 + 他租户行）：应用 662 → `collapsed 2 rows`、4→2 行、survivor occurrences=6（SUM）、message=最新措辞、first/last=MIN/MAX、他租户无损、聚合器新拼写 UPSERT 换措辞成功（无 42P10）→ pkg4 的 verify-662-assert.sql 全部 NOTICE OK。
- db2 扩展哨兵场景（pkg4 未覆盖）：`tenant NULL + ''`、`credential NULL + ''`、`bucket NULL + epoch 瞬时值` 三组同桶碎片**全部正确折叠**（索引 COALESCE 语义与折叠 JOIN 的 COALESCE 谓词一一对应，不会漏折产生唯一冲突）；不同 error_code/model 的非碰撞行**未误折**；新拼写 UPSERT 以 `tenant_id NULL` 命中 `''` survivor（哨兵等价在冲突推断下同样成立）；**旧 9 键拼写（含 LEFT(error_message,200)）对新索引实查报 42P10**（耦合两个方向均实证）；重放 662 → `no message-fragmented duplicates to collapse` + VALIDATION OK，行数不变（幂等）。

**折叠语义审查**（embeddata 662 副本 :41-105）：survivor 取 `last_seen_at DESC, id DESC`（message 随行保留=最新措辞）；occurrences=SUM、first_seen=MIN、last_seen=MAX；`upd`（data-modifying CTE 未被主查询引用也必然执行，:72-81）与 `del`（:82-95）触达行集不相交（del 恒排除本桶 survivor）；折叠先于建索引执行，首跑无唯一冲突窗口。**无误并 tenant/credential 维度**（JOIN 全键等值，8 列含 tenant/credential 的 COALESCE 形式与索引一致）。

**其余项**：`\set ON_ERROR_STOP on` + 单 BEGIN/COMMIT（:32-34,:149）；DROP IF EXISTS 两个旧名索引（:109-110）后 CREATE IF NOT EXISTS 同名重建（DROP 无条件先于 CREATE，故 IF NOT EXISTS 不会静默保留旧定义）；验证块（索引存在+旧名不在+`pg_get_indexdef NOT LIKE '%error_message%'`，:122-147）有效；无 down 的论证成立（660 先例、碎片行不可逆、回滚=恢复 639 定义有明示）；SHA-256 实算 `d0dbe5c4...9cfde` 与 changelog 两行一致（embeddata 副本即唯一现存副本）。

**但不通过的部分**：见发现清单 F-1（文件移动导致 2 个测试红 + 部署脚本必炸）、F-2（已提前应用 + Go 未提交）、F-3（changelog 自相矛盾）。

### 2. E-#3 聚合器语义 — 冲突目标/样本列改造 PASS；occurrences/first_seen 底层语义有缺陷（F-4）

- affected_buckets DISTINCT 去 message（:226-227）、bucket_rows join 去消息等值（:229-240）、aggregated DISTINCT ON/三个窗口 PARTITION BY/ORDER BY 全部去 message 且 `ts DESC` 选最新样本（:241-254）——三处键集与新唯一键逐列一致。
- DO UPDATE：`error_message = EXCLUDED.error_message`（:277）与 DISTINCT ON 的 ts-DESC 样本同基准，自洽；`resolved=FALSE`、context/request_id/tenant 刷新保持旧行为。
- 契约测试正负断言有效：正向 `error_message = EXCLUDED.error_message`；负向扫描 `COALESCE(LEFT(error_message` 与 staging 投影 `LEFT(COALESCE(c.error_message…`（:41）拼写精确区分，无误报。新增 `TestProviderErrorAggregatorAggKeyMigrationDropsMessageFromFingerprint`（:113-152）钳制重装机制/折叠聚合/验证块/索引键无 message/epoch 哨兵。
- **但**：`occurrences = EXCLUDED.occurrences`、`first_seen_at = EXCLUDED.first_seen_at` 是 replace 语义，其正确性依赖「bucket_rows 重读整桶全部源行」。该前提自 HEAD 已提交的 citus-columnar 修复（commit `3344f3f17`，staging CTAS 加 `WHERE c.aggregation_id > $1` 有界）起已不成立——跨 tick 的桶 occurrences 被最后一次 tick 的增量覆盖、first_seen 漂移。本轮新注释「first_seen_at retains the bucket's original onset」（:276）在该前提下不成立（容器实测：第二次 upsert 后 first_seen 10:00→10:30 被覆盖）。详见 F-4。

### 3. E-#6 分桶数学 — PASS

- minute（`bg/supplier_error_stats_aggregator.go:54-115`）：base/bucket_totals/bucket_stages 三 CTE；`SUM(CASE WHEN is_retryable THEN 1 ELSE 0 END)` 行数语义正确；stage_counts 用「先按 stage COUNT 再 jsonb_object_agg」两层分组正确规避 jsonb_object_agg 不求和；LEFT JOIN 补空桶 `'{}'`；UPSERT 键保持 6 列不变，两新列随覆盖写刷新——无重复累计。
- hour（:127-194）/day（:196-263）：`SUM(retryable_count)::int` 直接跨桶求和；stage_counts 经 `CROSS JOIN LATERAL jsonb_each` 展开、按（桶,stage）`SUM` 后重新 `jsonb_object_agg`——**同 key 数值相加而非覆盖**，两级聚合正确；GROUP BY 改为 CTE 具名列 1-5（旧 `GROUP BY 1,3,4,5,6` 的易错写法消除）。
- admin summary（`admin/vendor_credential_error_handlers.go:187-244`）：SQL `GROUP BY 1,2,3`（error_type,is_retryable,stage），Go 折叠 count/=Σ、retryable 仅累计 true 子组、last_seen=组间 max、stage_counts 每子组累加——数学正确；行基数回到每 error_kind 一行（前端 `:key` 契约保持）；DTO 仅 additive（`RetryableCount`/`StageCounts`，:54-55）；排序 Go sort 保持旧 COUNT DESC 语义（同数按 kind 稳定）。
- `stage` 列 `text DEFAULT '' NOT NULL`（V371:61）→ jsonb_object_agg NULL 键错误风险排除；`''` 键无损保留、读端映射 unknown 的约定与 errors_trend 一致。
- verify H 用例（`deploy/sql/verify/supplier_errors_pg_test.go:347-506`）逐字镜像聚合器 SQL，断言 5 行→retryable=3、stages `{"upstream":3,"connect":1,"":1}`、重复聚合不翻倍、hour 合并同值——**在独立一次性容器（citus_columnar）7/7 PASS 复跑确认**（含 V371 当前文件真实应用+重放的 Test A）。
- `admin/errors_trend.go` 读端为显式列清单（:164,:170-192），新增列不可见——「无需改动」论证核实成立。

### 4. V371 原位编辑安全性 — PASS

- `ADD COLUMN IF NOT EXISTS retryable_count integer NOT NULL DEFAULT 0 / stage_counts jsonb NOT NULL DEFAULT '{}'`（V371:364-369）：对「已应用增列前 V371、marker 在库」的库，文件重放时 CREATE TABLE 整段跳过、由此守卫补列并回填 0/'{}'——本地共享库即为该路径的活证据（17:48 marker 在库，两列已在位：`integer|0`、`jsonb|'{}'::jsonb`，information_schema 实查）。
- UPSERT 键不变的论证成立：is_retryable/stage 提为键会把行数乘以组合数并改变 hour/day 分组/趋势 SUM 语义；分桶计数走覆盖式 UPSERT 无重复累计。
- promote 原子体：V371 内 promote 为 656 模板单条 data-modifying CTE（参数守卫→预 ensure→`WITH batch … FOR UPDATE SKIP LOCKED`→DELETE RETURNING 显式列→INSERT 显式列），无 EXCEPTION 吞错；本地库 `promote_supplier_errors_hot_to_partition('8 hours',10)` 可执行（pkg4 报告 §5 证据与此相符）。
- 验证块 `has_cols`（:410,:424-431,:440-445）覆盖「表在列缺」的旧版重放场景。
- down 对称性：整表 DROP 覆盖新列，无需改。

### 5. 测试有效性 — pkg4 自身测试设计有效；当前因 F-1 两处红

- `go test ./bg/... ./admin/... ./sql/... -count=1`：**admin ok（66.8s）、sql/migrations/domain ok、sql/migrations/startup ok；bg FAIL** ——唯一失败即 `TestProviderErrorAggregatorAggKeyMigrationDropsMessageFromFingerprint`，原因是 662 canonical 文件被并行会话移走（F-1a），非断言逻辑缺陷。
- `go test -C installer ./... -count=1`：`TestStatsStartupMigrationsMatchCanonicalSources` FAIL（F-1b，同根因）。
- `LLM_GATEWAY_SUPPLIER_PG_DSN=<一次性容器> go test ./deploy/sql/verify/ -run TestSupplierError -v`：**7/7 PASS**（含新增 H）——独立复核，非仅采信 pkg4 报告。
- admin 折叠测试（`admin/vendor_credential_error_handlers_test.go:23-52`）：3 子组 mock（rate_limit×2 + timeout×1）验证 count=5/retryable=3/stage upstream:3+unknown:2/last_seen=max/distinct=max/行序——断言完整，能拦住折叠回归。

### 6. 同版本发布约束的标注 — 标注到位，但现实已部分突破标注前提

662 文件头（:14-18）、changelog pkg4 行（:299）、pkg4 报告遗留 #1/#2 三处均明确「662 与聚合器二进制必须同版本发布，误序任一侧聚合 tick 报 42P10」。但实际状态：662 已被并行会话应用（本地共享库 + changelog deploy-245 行 `applied+verified`），而 E-#3 Go 改动截至审计结束**仍未 commit**（HEAD b35cec661 不含）——一致性目前完全依赖「运行中的二进制恰好从 dirty working tree 构建」（本地实测 26h 内 0 次 42P10、聚合正常，佐证当前二进制是新拼写）。任何 clean checkout 重建即触发 F-2 的故障模式。另 apply-db-revision-sequence.sh files[] 已追加 662（:120，非 pkg4 所为）但指向已被移走的路径（F-1c）。

---

## 二、发现清单

### F-1（P1）662 canonical 文件被移走，三处引用断裂：2 个测试红 + 部署脚本必炸
- 证据：`sql/migrations/startup/662_provider_error_details_agg_key_dedup.sql` 已不存在（仅存 `installer/cmd/llm-gw-installer/embeddata/startup/662_...`，SHA 一致）；`bg/provider_error_aggregator_contract_test.go:114` 读 `../sql/migrations/startup/662_...` → FAIL（实跑复现）；`installer/cmd/llm-gw-installer/stats_migrations_test.go:75,79-88` 以 `sql/migrations/startup/` 为 canonical 目录 → FAIL（实跑复现）；`scripts/apply-db-revision-sequence.sh:120` files[] 指向不存在路径，:217 的 `[[ -f "$file" ]] || exit 4` 在 marker 检查**之前**生效 → 对包括已应用库在内的**所有**库直接 `missing migration` 退出。
- 判定：文件移动来自并行会话（pkg4 报告完成时文件仍在原位且其报告以该路径为准），但当前工作区整体处于不可发布状态。
- 建议修法：恢复 canonical 副本 `sql/migrations/startup/662_...`（与 embeddata 同 SHA，比照 659/660 的「canonical + embeddata 双份 + stats_migrations_test 等式钳制」既有模式）。不建议反向（改测试与脚本指向 embeddata）——那会破坏 canonical-source 契约的目录约定。

### F-2（P1）662 已应用而 E-#3 Go 修复未提交——clean 重建即 42P10
- 证据：本地共享库 marker `session-summary-and-integrity-2026-09:662_...` 存在、`pg_get_indexdef` 为新 8 键定义（实查）；`docs/db-changelog.md:306` deploy-245 行 `applied+verified`；`git log` HEAD=b35cec661 不含 `bg/provider_error_aggregator.go` 的新 ON CONFLICT（改动仅在 working tree）。运行容器 `llm-gateway-local-8782`（image 2.5.3.1953，dirty-tree 构建）聚合正常（`--since 26h` 内 `provider_error_aggregator: aggregation completed` 持续、42P10 计数 0）。
- 故障模式（容器已实证 42P10 方向）：任何从 git 干净状态重建的网关（CI、他机、image 重构建）携带 9 键 ON CONFLICT，对已应用 662 的库每个聚合 tick 报 42P10，provider_error_details 停更（优雅降级、watermark 不推进、诊断链路全盲）。
- 建议修法：立即 commit 工作区修复（含 662 canonical 恢复），使二进制与库状态可追溯；发布清单保留「662 与聚合器同版本」门禁。

### F-3（P1）changelog 对 662 存在两条状态矛盾行
- 证据：`docs/db-changelog.md:299`（pkg4 行）`file-ready（未应用…）` vs `:306`（deploy-245 行）`applied+verified`。数据库实查支持后者；前者的告警语义已失真，会误导运维对发布顺序的判断。
- 建议修法：删除 pkg4 行或改注「superseded by deploy-245 build_seq 1954」，保持 changelog 单一事实源。

### F-4（P2）有界 staging 下 occurrences/first_seen 的 replace 语义失真（HEAD 既有；本轮注释错误背书）
- 证据链：commit `3344f3f17` 将 `all_source_rows` 从无界视图改为 staging CTAS `WHERE c.aggregation_id > $1`（`bg/provider_error_aggregator.go:25-54`，`bucket_rows` 由此只含本 tick 新行，:229-240）；DO UPDATE 为 replace（:272,:278）。跨 tick 的桶（一个 10 分钟桶的行天然跨越 10 分钟 tick 边界）：occurrences 被最后 tick 增量覆盖（累计数丢失）、first_seen_at 被最后 tick 的窗口 min 覆盖（起始时刻漂移）。容器实证：对既有桶（first_seen=10:00）以新窗口（min=10:30）UPSERT 后 first_seen=10:30。本轮新增注释 `:276` 「first_seen_at retains the bucket's original onset」与 pkg4 报告「occurrences/first/last_seen 不再失真」均在该前提下不成立（仅 3344f3f17 之前的无界重读时代成立）。
- 建议修法（二选一）：① staging 恢复「affected buckets 全量重读」（在 CTAS 内按受影响桶过滤而非按 watermark 截断，保留 citus 安全的简单计划），replace 语义随之恢复正确——推荐；② DO UPDATE 改 `occurrences = provider_error_details.occurrences + EXCLUDED.occurrences`、`first_seen_at = LEAST(provider_error_details.first_seen_at, EXCLUDED.first_seen_at)`，并补重试不重复累计的论证（advisory lock + 事务原子性下 tick 内重试安全，但 tick 间失败重试会因 watermark 未推进而天然重算同桶，① 更稳）。

### F-5（P3）`distinct_status_codes` 语义由「整组 DISTINCT」弱化为「子组 DISTINCT 最大值」（可能低估）
- 证据：`admin/vendor_credential_error_handlers.go:183-184,224-226`（注释自认保守下界）。字段名与 JSON shape 不变、值可能低于旧行为。建议在 API/changelog 记录一句，避免前端读数对账困惑。

### F-6（P3）pkg4 的 662 验证脚本未覆盖 NULL vs '' 哨兵折叠与 UPSERT 后计数复核
- 证据：`.audit-workspace/.../verify-662-{setup,assert}.sql` 仅覆盖 message 措辞碎片；本次审计以 db2 补测（NULL/'' tenant、NULL/'' credential、NULL/epoch bucket 折叠 + 哨兵等价 UPSERT + 旧键 42P10 + 重放幂等）全部通过——属覆盖缺口而非缺陷。建议将哨兵场景并入正式迁移测试（或 supplier_errors_pg_test 的 662 用例，若后续补建）。

### F-7（P3）V371「原位编辑已应用迁移」依赖手动重放交付
- 证据：marker 机制（apply-db-revision-sequence.sh per-file skip）会跳过已应用文件，增列靠「直接 psql 重放幂等文件」送达（pkg4 报告 §5）；245 行称 applied+verified（当时文件路径未断，正常走 files[]）。流程可行但依赖操作者纪律，建议后续同类增量一律走新编号文件（本条仅为流程注记）。

---

## 三、验证命令记录（可复跑）

```
go test ./bg/... ./admin/... ./sql/... -count=1            # bg FAIL(F-1a) / admin ok / sql ok
go test -C installer ./... -count=1                        # cmd/llm-gw-installer FAIL(F-1b)
LLM_GATEWAY_SUPPLIER_PG_DSN='postgres://postgres:***@127.0.0.1:55434/...' \
  go test ./deploy/sql/verify/ -run TestSupplierError -v -count=1   # 7/7 PASS（一次性容器）
shasum -a 256 installer/cmd/llm-gw-installer/embeddata/startup/662_...sql
  # d0dbe5c44869c69633ac2eb2fa6a146c8055d663da4c1a1a773854507579cfde（=changelog）
# 共享库只读：marker/索引定义/统计列均在案；一次性容器 db1/db2 写路径验证后已销毁
```

审计期间并行会话持续改树（报告基于 HEAD=b35cec661 + 当时 working tree；662 文件位置与 changelog 状态以本报告记录时点为准）。
