# 工作包 4（SQL 域：供应商错误链路）修复报告 — E-#3 / E-#6 / D-2#2 核对 / V371 验证与本地重放

日期：2026-09-05。分支 main（未 commit，遵守禁令）。验证容器 `audit-pg-v371` 已停止删除。

---

## 1. E-#3：provider_error_details 聚合键去 message 碎片化（P2）

### 修法
**选型：error_message 移出唯一键，作样本列；UPSERT 更新为最新样本**（二选一中选"最新样本"）：
桶身份 = `(COALESCE(tenant_id,''), provider_id, COALESCE(credential_id,''), COALESCE(model_name,''), COALESCE(endpoint,''), error_type, COALESCE(error_code,''), COALESCE(aggregation_bucket,'epoch'))`。选最新而非首见的论证：`aggregated` CTE 的 `DISTINCT ON (... ts DESC)` 本来就以最新行携带 request_id/context（现有语义），message 与之保持同一"最新样本"基准最自洽；时间不丢——first_seen_at/last_seen_at 仍按整桶 MIN/MAX。

**Go 侧 `bg/provider_error_aggregator.go`**（staging 投影 :41 不变——message 仍作为样本列进 temp 表；改动在聚合管道）：
- `:225-227` affected_buckets 的 DISTINCT 去掉 error_message；
- `bucket_rows` join 条件去掉 `s.error_message IS NOT DISTINCT FROM b.error_message`（同措辞行重新并桶）；
- `aggregated` 的 DISTINCT ON / 三个 PARTITION BY / ORDER BY 全部去 message（:234-251 一带）；
- `:265-271` ON CONFLICT 冲突目标去掉 `(COALESCE(LEFT(error_message, 200), ''))`；
- `:273-277` DO UPDATE 新增 `error_message = EXCLUDED.error_message`（最新样本刷新），附论证注释；
- `:176` 管道注释记录 E-#3 修复语义。

**新迁移 `sql/migrations/startup/662_provider_error_details_agg_key_dedup.sql`**（要点）：
- **编号改 662 而非任务书上的 661**：`ls sql/migrations/startup/` 证实 661 已被 `661_session_summary_token_ratio_reassert.sql` 占用（apply-db-revision-sequence.sh 注释里的"evening renumber"条目），662 为当前最大号+1；
- 唯一索引来源核实：`01-schema.sql`（表定义，无索引）→ 616 首建 `idx_provider_error_details_fingerprint` → 620 换名 `idx_provider_error_details_tenant_fingerprint` → **639/V368 换名 `idx_provider_error_details_tenant_cred_fingerprint`（现网生效）**。662 同时 DROP 两个旧名（IF EXISTS）后以同名重建 8 列表达式索引；
- **与 620/639 的 fail-closed 不同，采用自动折叠**（616 先例，该表首次建指纹时即 merge）：碎片是常态——同桶不同措辞在真实数据上几乎必然存在，fail-closed 等于必现启动阻断。折叠语义：survivor 取 last_seen 最新行（message 最近），occurrences=SUM、first_seen=MIN、last_seen=MAX，其余行 DELETE；
- 幂等：折叠 CTE 在索引生效后必然 no-op；DROP INDEX IF EXISTS + CREATE UNIQUE INDEX IF NOT EXISTS 可重放（容器已实测重放）；
- 无 down（660 先例：纯索引重装类；文件头已论证——折叠删除的碎片行不可逆，提供 down 只会制造可回滚错觉）；
- 迁移内建验证块：索引存在 + 旧名索引不存在 + `pg_get_indexdef(...) NOT LIKE '%error_message%'`；
- 过 pre-commit 规则：唯一 NNN 编号、无 `$1`/SET LOCAL 陷阱、纯 SQL。

**docs/db-changelog.md**：追加 662 条目（SHA-256 在文件定稿后计算：`d0dbe5c44869c69633ac2eb2fa6a146c8055d663da4c1a1a773854507579cfde`；状态如实标 `file-ready（未应用）`，见遗留节）。

**测试**：`bg/provider_error_aggregator_contract_test.go` — 既有 `TestProviderErrorAggregatorSQLIsTenantScopedAndBucketIdempotent` 加正向（`error_message = EXCLUDED.error_message`）与负向断言（可执行 SQL 不得再出现 `COALESCE(LEFT(error_message`，该拼写与 staging 投影的 `LEFT(COALESCE(c.error_message…)` 精确区分，无误报）；新增 `TestProviderErrorAggregatorAggKeyMigrationDropsMessageFromFingerprint`（:113）钳制 662 的重装机制/折叠/验证块/索引键无 message/epoch 哨兵列。

---

## 2. E-#6：supplier_error_stats 分桶 + 凭据详情 summary 三维聚合（P2）

### 修法与列集设计（V371 原位编辑）
读现有 stats 表（UPSERT 键 `(stat_time, granularity, supplier, credential_id, error_type, model)`）后取**最小 additive 列集、UPSERT 键不变**：
- `retryable_count integer NOT NULL DEFAULT 0` — 桶内 `is_retryable=true` 行数；可重试率 = retryable_count/error_count；
- `stage_counts jsonb NOT NULL DEFAULT '{}'` — 桶内 stage→计数 map。

**为什么不把 is_retryable/stage 提为维度键**：行数会乘上 retryable×stage 组合数（N 倍存储/查询放大），并改变 hour/day 二次 rollup 的分组语义与趋势 API 的 SUM 语义；分桶计数随现有覆盖式 UPSERT 逐桶刷新，无重复累计风险。**为什么 stage 用 jsonb 而非固定列**：stage 是受控低基数词表（preflight/connect/upstream/stream，''=未知），但 jsonb 使未来新增 stage 零 DDL；hour/day rollup 用 `jsonb_each` 展开两级聚合合并。'' stage 在 stats 层保留为 `""` 键（无损），读端映射 `unknown`。

**V371 原位改动清单**（`deploy/sql/migrations/V371__supplier_errors_hot_and_stats.sql`）：
- `:26` 头部设计说明补 E-#6 条目；
- `:339-356` CREATE TABLE 增两列 + 论证注释；
- `:367-368` `ADD COLUMN IF NOT EXISTS` 自愈守卫（覆盖"某库已应用过增列前 V371、CREATE TABLE IF NOT EXISTS 整段跳过"的场景，NOT NULL DEFAULT 对既有行回填 0/'{}'）；
- `:370-373` COMMENT ON COLUMN；
- `:410,:428-431,:441-445` 验证块加 `has_cols`（两列必须在位）与 `buckets=%` 输出。
- down 文件无需改（整表 DROP 对称覆盖）。

**rollup SQL**（`bg/supplier_error_stats_aggregator.go`）：
- minute（:44-117）：改 `base/bucket_totals/bucket_stages` 三 CTE + LEFT JOIN；`SUM(CASE WHEN is_retryable…)` 与两层分组 `jsonb_object_agg`（jsonb_object_agg 不求和，需先按 stage COUNT 再聚合）；DO UPDATE 增两列；
- hour（:120-186）/ day（:188-243）：`SUM(retryable_count)`；stage_counts 经 `CROSS JOIN LATERAL jsonb_each` 按 (桶, stage) 求和后重新 `jsonb_object_agg`；
- 文件头注释同步（:9）。

**admin summary 聚合**（`admin/vendor_credential_error_handlers.go`）：
- `:189-196` SQL 改 `SELECT error_type, is_retryable, COALESCE(NULLIF(stage,''),'unknown') … GROUP BY 1,2,3`（即 (error_type, is_retryable, stage)）；
- `:198-241` Go 侧折叠回**每 error_kind 一行**——这是 API 向后兼容的关键：前端 `web/src/views/provider-detail/ErrorDetailTab.vue:145` 以 `:key="item.error_kind"` 渲染 error_summary，行基数变化即破坏 Vue 渲染；因此 additive 落地为每行新字段而不是增加行数；
- DTO `:44-56` 增 `retryable_count int`、`stage_counts map[string]int`（additive）；`distinct_status_codes` 取各子组 DISTINCT 的最大值（注释论证：子组求和会跨组重复计同一状态码，最大值为保守下界；旧"整组 DISTINCT"无法从分组行精确重建）；响应次序保持旧 `ORDER BY COUNT(*) DESC`（Go sort，同数按 error_kind 稳定排序）。`web/**` 未动（禁改）。
- `admin/errors_trend.go` 无需改动：其 SQL 为显式列 SUM，新增列对它不可见（读端已核对）。

**测试**：
- `bg/supplier_error_stats_aggregator_test.go` `TestSupplierErrorStatsRollupSQLContract`：三级 SQL 各加 RETRYABLE_COUNT/STAGE_COUNTS/JSONB_OBJECT_AGG/JSONB_EACH/SUM(CASE…) 令牌；断言 retryable_count 不得进入 UPSERT 键；
- `admin/vendor_credential_error_handlers_test.go`：summary mock 行改 6 列三维形状（2 个 rate_limit 子组 + 1 个 timeout 子组）；新增 `TestLoadVendorErrorSummaryFoldsRetryableStageBuckets`（:20）验证折叠（count=5、retryable=3、stage 桶 upstream:3/unknown:2、last_seen 取 max、行序不变）；scan 失败用例同步 6 列；
- `deploy/sql/verify/supplier_errors_pg_test.go`：矩阵说明加 H；新增 `TestSupplierErrorStatsRetryableStageBuckets`（:347）——5 行 hot 样本（3 retryable-upstream、1 connect、1 空stage）跑 minute rollup（逐字镜像聚合器 SQL，未导出常量的漂移由 bg 契约测试钳制）断言 `retryable_count=3`、`stage_counts={"upstream":3,"connect":1,"":1}`、重复聚合幂等不翻倍、hour rollup 经 jsonb_each 合并得同值。

---

## 3. D-2#2 关联：V371 promote 原子化 — **已由前序修复完成，本次核对无遗留**

任务启动时 V371 内 promote 已是 656 模板式单条 data-modifying CTE（`:278-307`）：参数守卫（:254-259）→ 月度预 ensure（:262-268，与 batch 同谓词）→ `WITH batch … FOR UPDATE SKIP LOCKED` → `DELETE … RETURNING` **显式列**（无 `SELECT *`）→ INSERT 显式列 → 单语句原子、无 EXCEPTION 吞错、错误上抛交 Go `recordPromoteFailure`；且带 E-#1 的 `app.bypass_rls` policy（:88-95、:207-214）。本次本地库 `pg_get_functiondef` 抽查证实与文件一致（见 §5）。父表 supplier_errors 无唯一约束，promote 按纯 INSERT 直插（659 对无唯一约束父表的先例一致）。

---

## 4. 容器验证矩阵（一次性容器，未触碰共享库）

`docker run -d --rm --name audit-pg-v371 -e POSTGRES_PASSWORD=audit -p 55432:5432 kx-citus-pg17:offline-arm64`

`LLM_GATEWAY_SUPPLIER_PG_DSN='postgres://postgres:audit@127.0.0.1:55432/postgres?sslmode=disable' go test ./deploy/sql/verify/ -run "TestSupplierError" -v -count=1` — **7/7 PASS**：

| 用例 | 结果 |
|---|---|
| A/MigrationIdempotent：V371（含本次增列+守卫+验证块）真实 PG 应用与重放 | PASS |
| B/HotHeapSemantics | PASS |
| C/ColumnarPartitionImmutable | PASS |
| D/PromoteRetentionAndBatch（原子 CTE 体） | PASS |
| E+F/UnifiedReadAndRLS | PASS |
| G/StatsUpsertIdempotent | PASS |
| **H/StatsRetryableStageBuckets（本次新增 E-#6）** | **PASS** |

**662 迁移容器验证**（脚本存 `.audit-workspace/2026-09-05-round2-followup/verify-662-{setup,assert}.sql`）：
1. 造生产形状表 + 639 时代 message 指纹 + 3 条同桶不同措辞碎片行（2/3/1 occurrences）+ 1 条他租户行；
2. 应用 662：`collapsed 2 message-fragmented duplicate rows` → 断言全过：行数 4→2、survivor occurrences=6（SUM）、message=最新措辞（"retry after 8s"行）、first/last_seen=MIN/MAX、他租户无损、索引定义不含 error_message；
3. 用聚合器新拼写 ON CONFLICT（8 列）对同桶换措辞 UPSERT → 1 行、message 刷新（42P10 不出现）；
4. **重放 662**：`no message-fragmented duplicates to collapse` + `662 VALIDATION OK`，行数仍 2（幂等）。

离线门禁：`go build ./...` OK；`go vet ./bg/... ./admin/... ./deploy/sql/verify/` OK；`go test ./bg/... ./admin/... ./sql/... -count=1` 全 ok（10 包，0 FAIL）；本包改动文件 gofmt clean。

---

## 5. V371 本地重放（共享库 llm-gateway-pg @127.0.0.1:5432）

**发现**：marker `session-summary-and-integrity-2026-09:V371__supplier_errors_hot_and_stats.sql` 已于 **2026-09-05 17:48:25** 落库（另一会话在本次任务启动前后已应用过 V371 的原子 promote + bypass_rls 版本）。按 apply-db-revision-sequence.sh 的 per-file marker 机制（`files[]` 逐个比对 `gateway_db_revision_sequences`，已应用即 skip），脚本重跑会跳过 V371——因此本次以**直接重放幂等迁移文件**的方式交付增量（V371 幂等性是其文件头契约 + TestSupplierErrorsMigrationIdempotent 验证项；新增的 `ADD COLUMN IF NOT EXISTS` 守卫正是为此场景设计）。marker 保持不动（未删除/未伪造）。

**重放证据**（`psql -f deploy/sql/migrations/V371__supplier_errors_hot_and_stats.sql`）：
- 首次重放：`ALTER TABLE`×2（新列守卫生效）+ `V371 VALIDATION OK: hot=t, partitioned=t, view=t, promote_fn=t, stats=t, uq=t, rls=t, buckets=t`；
- 对象抽查：`supplier_error_stats.retryable_count integer DEFAULT 0`、`stage_counts jsonb DEFAULT '{}'::jsonb` 在库；
- promote 函数体抽查：`pg_get_functiondef` 含 `WITH batch AS` 与 `FOR UPDATE SKIP LOCKED`、不含 `CREATE TEMP`/EXCEPTION 语句体 → **t**（原子体确认）；
- promote 冒烟：`SELECT promote_supplier_errors_hot_to_partition('8 hours', 10)` → 0（本地 hot 无超 8h 行，函数可正常执行）；
- 二次重放幂等：VALIDATION OK（buckets=t），无错误；
- RLS 三表 ENABLE+FORCE + 三 policy 在位（E-#1 版本）。

---

## 6. 遗留（需后续跟进，本包按权限未动）

1. **662 的部署接线（最重要）**：`scripts/apply-db-revision-sequence.sh` 的 `files[]` 未含 662（该文件不在本包可改清单）；且 662 与 `bg/provider_error_aggregator.go` 新 ON CONFLICT 目标是**同版本发布耦合**——旧二进制+新索引、或新二进制+旧索引，聚合 tick 均报 42P10。changelog 662 条目已标注。接线 owner 需：把 662 追加进 files[] 并与发版同窗口应用；本地共享库**刻意未应用 662**（运行中的网关二进制仍是旧冲突目标，先应用会把聚合器打断到下次重部署）。
2. **662 无 deploy 链 V 系列镜像**：V364/V368 是 620/639 的 deploy 链镜像，662 只在 startup 链（本包只允许新建 `sql/migrations/startup/661_*.sql`→实为 662；`deploy/sql/migrations/V372__*` 不在可改清单）。
3. **cmd/gateway/main.go 接线需求**（禁改未动）：无新增接线需求——E-#3/E-#6 均为既有 worker/读端的原位 SQL/列变更，ProviderErrorAggregator 与 SupplierErrorStatsAggregator 已在 PartitionManager 注册。唯一相关项仍是审计已列的 D-2#1（promoteSpecs 补 supplier_errors_hot）与 D-2#13（ensureSpecs 补 ensure_supplier_errors_partition），属 axis-D 修复包范围。
4. **252 环境部署仍未做**：V371（含本次 E-#6 列）与 662 均未触达 252；V371 在 252 首次应用时将直接带新列（无需守卫路径）。
5. **E-#6 读端可视化**：`retryable_count/stage_counts` 已在 API（additive），前端徽标/表格列展示未做（`web/**` 禁改）。
6. 前端 `VendorErrorKindStat`（`web/src/api/vendor-credential-error.ts`）未加新字段类型声明——TS 对多余 JSON 字段宽松，不影响现有编译；建议后续 web 包补类型。

## 7. 改动文件清单
- `bg/provider_error_aggregator.go`、`bg/provider_error_aggregator_contract_test.go`
- `bg/supplier_error_stats_aggregator.go`、`bg/supplier_error_stats_aggregator_test.go`
- `admin/vendor_credential_error_handlers.go`、`admin/vendor_credential_error_handlers_test.go`
- `deploy/sql/migrations/V371__supplier_errors_hot_and_stats.sql`（原位）
- `deploy/sql/verify/supplier_errors_pg_test.go`
- 新建 `sql/migrations/startup/662_provider_error_details_agg_key_dedup.sql`
- `docs/db-changelog.md`（追加 662 条目）
- 报告+验证脚本：`.audit-workspace/2026-09-05-round2-followup/`（本文件、verify-662-setup.sql、verify-662-assert.sql）
- 未动任何禁改文件；未 git add/commit。
