# db.go ensure 索引所有权底账 子代理报告（R38，2026-09-17，窗口：全仓静态扫描）

> 主代理注：9 个影蔽判等逐条亲读复核成立；parent_ts 冲突对叶子拓扑经 pg_inherits 真库亲证（两父各 5 叶）；tool_hot 活表列集与索引形态经 pg_indexes 亲证（与报告一致，toolexecution 查询列不符为死路径）。tool_hot 三对 ASC/DESC 由主代理以 backward-scan 论证定夺（保 348 DESC 三件），入 719。

ROOT = `/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4`（下文以 `$ROOT` 缩写）
判等口径沿用 startup/718 注释（`$ROOT/sql/migrations/startup/718_drop_redundant_indexes_and_add_ttl_indexes.sql:11-15`）：b=同列约束影蔽；c=方向影蔽（backward scan）；d=全量索引影蔽窄谓词 partial。

## 1. 8 个约束影蔽冗余索引（全部由 Go ensure 链创建，drop 后每次启动复活）

| # | 表 / ensure 函数（链内调用点） | 约束（影蔽方） | ensure 索引（被影蔽方） | 判等 |
|---|---|---|---|---|
| 1 | applications / `ensureApplicationsTable` `$ROOT/db/db.go:3890`（调用 db.go:213） | `CONSTRAINT applications_tenant_id_code_key UNIQUE (tenant_id, code)` db.go:3908 | `CREATE INDEX IF NOT EXISTS idx_applications_tenant_code ON applications (tenant_id, code) WHERE enabled = TRUE;` db.go:3912-3914 | d（partial ⊂ 全量 UNIQUE 同列） |
| 2 | licenses / `ensureLicenseModulesSchema` db.go:4511（调用 db.go:355） | `license_key TEXT NOT NULL UNIQUE` db.go:4518 | `CREATE INDEX IF NOT EXISTS idx_licenses_key ON licenses (license_key);` db.go:4529 | b |
| 3 | offline_activation_requests / `ensureLicenseDevicesSchema` db.go:4677（调用 db.go:358） | `request_id TEXT NOT NULL UNIQUE` db.go:4706 | `CREATE INDEX IF NOT EXISTS idx_oar_request ON offline_activation_requests (request_id);` db.go:4711 | b |
| 4 | releases / `ensureAutoUpdateSchema` db.go:4794（调用 db.go:364） | `version TEXT NOT NULL UNIQUE` db.go:4801 | `CREATE INDEX IF NOT EXISTS idx_releases_version ON releases (version);` db.go:4816 | b |
| 5 | center_commands / `ensureCenterOpsSchema` db.go:4877（调用 db.go:367） | `command_id TEXT NOT NULL UNIQUE` db.go:4915 | `CREATE INDEX IF NOT EXISTS idx_cc_command_id ON center_commands (command_id);` db.go:4929 | b |
| 6 | instance_status_reports / 同上 ensureCenterOpsSchema | `PRIMARY KEY (instance_id, timestamp)` db.go:4942 | `CREATE INDEX IF NOT EXISTS idx_isr_instance ON instance_status_reports (instance_id, timestamp DESC);` db.go:4944 | c（同列反向，PK 影蔽） |
| 7 | keyless_providers / `ensureOmniFreeSchema` `$ROOT/db/db_omnifree.go:40`（调用 db.go:402） | `CONSTRAINT keyless_providers_provider_tenant_key UNIQUE (provider_code, tenant_id)` db_omnifree.go:176 | `CREATE INDEX IF NOT EXISTS idx_keyless_providers_enabled ON keyless_providers(provider_code) WHERE enabled = TRUE;` db_omnifree.go:384 | b+d（最左前缀 + partial ⊂ 全量）；同段 `idx_keyless_providers_auto_combo ON keyless_providers(provider_code) WHERE enabled = TRUE AND allowlist_in_auto_combo = TRUE` db_omnifree.go:385-386 亦为前缀 partial |
| 8 | free_resource_catalog / 同上 ensureOmniFreeSchema | `UNIQUE (provider_code, model_id, tenant_id)` db_omnifree.go:81 | `CREATE INDEX IF NOT EXISTS idx_free_resource_catalog_provider ON free_resource_catalog(provider_code);` db_omnifree.go:370 | b（最左前缀） |

718 迁移已将这 8 个登记为"遗留、本轮不动、须 ensure 根治"：`$ROOT/sql/migrations/startup/718_...sql:16-20`。baseline 存在情况见第 5 节。

## 2. request_logs 父表所有权冲突对（idx_request_logs_parent_ts ↔ idx_request_logs_parent_request_id）

四处定义：

1. **Go ensure（owner A）** `$ROOT/db/db.go:1220-1222`：
   `CREATE INDEX IF NOT EXISTS idx_request_logs_parent_ts ON request_logs (parent_request_id, ts DESC) WHERE parent_request_id IS NOT NULL;`
2. **startup 通道镜像（owner A'）** `$ROOT/sql/migrations/startup/013_compression_columns.sql:49-51`：同语句（`ON public.request_logs (parent_request_id, ts DESC) WHERE parent_request_id IS NOT NULL`）。
3. **deploy 通道（owner B）** `$ROOT/deploy/sql/migrations/V369__add_request_type_fields.sql:21-23`：
   `CREATE INDEX IF NOT EXISTS idx_request_logs_parent_request_id ON request_logs(parent_request_id, ts DESC) WHERE parent_request_id IS NOT NULL;` —— 与 #1/#2 同构不同名（两父索引各挂 5 个 attached 叶子，见 718:54-58 注）。
4. **v3 通道（owner C）** `$ROOT/sql/migrations/202609_01_add_request_type_fields.sql:49-51`：
   `CREATE INDEX IF NOT EXISTS idx_request_logs_parent_request_id ON request_logs(parent_request_id) WHERE parent_request_id IS NOT NULL;` —— **单列**版本，与 #3 同名但列序不同（潜在同名碰撞源）。

补充：canonical/baseline 里 parent_ts 是分区父索引 `ON ONLY` + ATTACH 叶子（`$ROOT/sql/objects/indexes/idx_request_logs_parent_ts.sql:5`；`$ROOT/sql/schema/01-schema.sql:25011`、27778、28016；`$ROOT/deploy/sql/schemas/baseline/01-schema.sql:24836`、27603、27841）。718:53-58 记录了原 B2 对 parent_ts 的 drop 已撤回（ensure 会复活 + 扰动叶子 ATTACH 拓扑）。`idx_request_logs_parent_request_id` 的 rollback：`$ROOT/sql/rollback/202609_01_rollback_request_type_fields.sql:13`、`$ROOT/deploy/sql/migrations/V369__add_request_type_fields.down.sql:8`。

## 3. tool_usage_stats_hot（表名实际为单数 tool）索引全集与 ASC/DESC 近重复

- 建表：`$ROOT/sql/migrations/startup/348_tool_usage_stats_hot_independence.sql:21-23` `CREATE TABLE IF NOT EXISTS tool_usage_stats_hot (LIKE tool_usage_stats INCLUDING ALL) WITH (fillfactor=90);`
- 348 显式创建 4 个（348:32-43）：
  - `CREATE UNIQUE INDEX IF NOT EXISTS tool_usage_stats_hot_tool_tenant_date_key ON tool_usage_stats_hot (tool_id, tenant_id, usage_date);`
  - `CREATE INDEX IF NOT EXISTS tool_usage_stats_hot_tool_date_idx ON tool_usage_stats_hot (tool_id, usage_date DESC);`
  - `CREATE INDEX IF NOT EXISTS tool_usage_stats_hot_date_idx ON tool_usage_stats_hot (usage_date DESC);`
  - `CREATE INDEX IF NOT EXISTS tool_usage_stats_hot_tenant_date_idx ON tool_usage_stats_hot (tenant_id, usage_date DESC);`
- 父表 `ON ONLY` 索引 4 个（**无 in-repo 迁移/ensure 创建者**，仅 dump 镜像：`$ROOT/deploy/sql/schemas/baseline/01-schema.sql:25979-26000`、`$ROOT/sql/objects/indexes/idx_tool_stats_part_*.sql`）：`idx_tool_stats_part_created (created_at)`、`idx_tool_stats_part_date (usage_date)`、`idx_tool_stats_part_tenant (tenant_id, usage_date)`、`idx_tool_stats_part_tool (tool_id, usage_date)`（全 ASC）。
- `LIKE ... INCLUDING ALL` 复制父索引 → hot 表上 4 个 PG 自动命名 ASC 索引（三份 baseline 一致，`deploy baseline:27169-27218` / `sql/schema:27344-27393` / installer embeddata 同）：
  - `tool_usage_stats_hot_created_at_idx (created_at)` / `tool_usage_stats_hot_usage_date_idx (usage_date)` / `tool_usage_stats_hot_tenant_id_usage_date_idx (tenant_id, usage_date)` / `tool_usage_stats_hot_tool_id_usage_date_idx (tool_id, usage_date)`
- **三对 ASC/DESC 近重复**（718:16 登记遗留，"混合排序语义需确认"）：
  1. `(tool_id, usage_date DESC)` [tool_date_idx, 348] ↔ `(tool_id, usage_date ASC)` [tool_id_usage_date_idx, LIKE 继承]
  2. `(tenant_id, usage_date DESC)` [tenant_date_idx, 348] ↔ `(tenant_id, usage_date ASC)` [tenant_id_usage_date_idx, LIKE 继承]
  3. `(usage_date DESC)` [date_idx, 348] ↔ `(usage_date ASC)` [usage_date_idx, LIKE 继承]
- db.go / db_omnifree.go 对该表**零索引创建**（grep 无 `tool_usage_stats_hot`），drop 后仅"重启 LIKE 重建表"场景才复活（表已存在则 IF NOT EXISTS 跳过）。

## 4. ensure 机制

- 幂等方式：主体是裸 `CREATE TABLE/INDEX IF NOT EXISTS`、`ADD COLUMN IF NOT EXISTS`（PG 目录名检查 → **drop 后下次执行必复活**）；少数 DO 块先查目录：pg_indexes（db.go:2079-2080）、pg_constraint（db.go:4111 注释所述模式）。
- 执行链：`db.Open` `$ROOT/db/db.go:35`（:78 调 ApplyMigrations）→ `ApplyMigrations` db.go:98（失败重试 2 次、5s 退避，db.go:100-116）→ `applyMigrationsOnce` db.go:119-5594，串行约 72 个 `db.ensure*` 调用，单次预算 3 分钟（db.go:122-124）。第 1 节 6 个 ensure 的调用点：db.go:213 / 355 / 358 / 364 / 367 / 402。
- 触发频率：**每进程启动一次**（`openDBWithBootRetry` `$ROOT/cmd/gateway/main_helpers.go:115-132`，20s 预算内失败会重跑整条链）；`gateway migrate` 子命令（`$ROOT/cmd/gateway/migrate.go:41-55`，经 db.Open 复跑）；CLI 工具 fetch-standard-iq / probe-cred / license-authority / verify-model-fetch / check-credentials 各自启动也触发。运行期无周期重跑。
- 平行 SQL 通道（不经 Go ensure）：`sql/migrations/startup/NNN_*.sql`（shell 应用：`$ROOT/scripts/init-local-db.sh`、`$ROOT/scripts/apply-hot-table-migrations.sh`、`$ROOT/tests/e2e/setup.sh`）；`deploy/sql/migrations/V###__*.sql`（deploy 通道）；`sql/migrations/*.sql`（v3/手工通道，如 202609_01）。Go ensure 与 startup 通道互为镜像（013 ↔ db.go:1192-1222；075-omnifree ↔ db_omnifree.go，见 db.go:399-401 注释）。

## 5. 三份 baseline 对齐

| 文件 | 行数 | 与其他差异 |
|---|---|---|
| `$ROOT/sql/schema/01-schema.sql`（canonical） | 30315 | 与另两份 diff 均不同 |
| `$ROOT/deploy/sql/schemas/baseline/01-schema.sql` | 30125 | 与 installer 186 行差异 |
| `$ROOT/installer/cmd/llm-gw-installer/embeddata/01-schema.sql` | 30125 | 同上 |

索引相关镜像：`$ROOT/sql/objects/indexes/*.sql`（pg_dump 对象级，如 idx_request_logs_parent_ts.sql、idx_tool_stats_part_*.sql）。
对齐事实：第 1 节 8 索引中 6 个在 baseline（idx_applications_tenant_code baseline:22854/schema:23023、idx_licenses_key 23835/24010、idx_oar_request 24115/24290、idx_releases_version 24605/24780、idx_cc_command_id 23163/23332、idx_isr_instance 23772/23947）；**keyless_providers / free_resource_catalog 两表在三份 baseline 中完全不存在**（仅 Go ensure 持有）。idx_request_logs_parent_ts 在三份 baseline 均为 ON ONLY 父索引 + 2026_07/2026_08 ATTACH；idx_request_logs_parent_request_id 三份 baseline 均无。tool_usage_stats_hot 的 8 个索引三份 baseline 一致。
